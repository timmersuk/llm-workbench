package agentrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	claudecode "github.com/severity1/claude-agent-sdk-go"

	"github.com/timmersuk/llm-workbench/internal/chat"
)

// lookPath is exec.LookPath, indirected so CheckHealth is deterministically
// testable without depending on whether `claude` is actually on the test
// machine's PATH.
var lookPath = exec.LookPath

// readOnlyTools is the fixed allow-list for Requirements/Planning stage
// agent runs — no Write/Edit/Bash, so the agent can read the reference
// repository but never modify it. This is the guardrail described in
// docs/architectural invariants.md's "the new trust boundary is scoped to
// can read files in the reference repo, not can modify" framing.
var readOnlyTools = []string{"Read", "Grep", "Glob"}

// executionTools is the allow-list for Execute — the Implementation stage
// is the one place this trust boundary deliberately widens beyond
// readOnlyTools, because Execute always runs against an isolated git
// worktree (see ResolveExecutionWorkspace), never the shared checkout Run
// uses, so Write/Edit/Bash here can't touch anything a human or another
// stage's read-only agent has open.
var executionTools = append(append([]string{}, readOnlyTools...), "Write", "Edit", "Bash")

// claudeRunnerMaxTurns bounds how many internal tool-call round-trips a
// single Run call may make, as a defense-in-depth cap independent of the
// Write/Edit/Bash denial above.
const claudeRunnerMaxTurns = 30

// claudeExecutionMaxTurns bounds Execute's round-trips — higher than
// claudeRunnerMaxTurns because an actual implementation run (write code,
// run tests, fix failures, commit) legitimately needs far more turns than
// an interview question does.
const claudeExecutionMaxTurns = 100

// executionKickoffMessage is the fixed user turn Execute sends to start an
// autonomous run — all real instructions live in the system prompt
// (agentrunner.ExecuteInput.SystemPrompt, built by the caller), the same
// split stage conversations already use for their own kickoffUserMessage.
const executionKickoffMessage = "Begin executing the plan."

// mcpServerName is the in-process MCP server name Draft tools are
// registered under (mcp__<mcpServerName>__<tool name> in WithAllowedTools).
const mcpServerName = "draft"

// ClaudeRunner implements AgentRunner backed by
// github.com/severity1/claude-agent-sdk-go, which drives the `claude` CLI
// as a subprocess. One claudecode.Client is created and connected lazily
// per RunInput.SessionKey and kept alive until CloseSession is called for
// that key — cwd, system prompt, and allowed tools are all
// claudecode.Client-scoped (fixed at connect time, not per-query), so a
// client cannot be shared across keys with different workspaces/prompts.
type ClaudeRunner struct {
	mu        sync.Mutex
	clients   map[string]claudecode.Client
	inFlight  map[string]bool
	timeout   time.Duration
	reposRoot string
	// newClient constructs a claudecode.Client from the given options —
	// indirected (defaulting to claudecode.NewClient) purely so tests can
	// substitute a fake client without spawning a real `claude` subprocess,
	// the same seam lookPath provides for CheckHealth.
	newClient func(opts ...claudecode.Option) claudecode.Client
}

// NewClaudeRunner returns a ClaudeRunner whose Run calls are each bounded
// by timeout (covering client connection, the query, and draining the
// response stream). reposRoot is the configured AGENT_REPOS_ROOT value,
// held so CheckHealth can report unavailable when it's unset.
func NewClaudeRunner(timeout time.Duration, reposRoot string) *ClaudeRunner {
	return &ClaudeRunner{
		clients:   make(map[string]claudecode.Client),
		inFlight:  make(map[string]bool),
		timeout:   timeout,
		reposRoot: reposRoot,
		newClient: claudecode.NewClient,
	}
}

// CheckHealth implements AgentRunner. reposRoot must be configured — without
// one, ResolveWorkspace can never succeed regardless of CLI presence — and
// the `claude` CLI must be discoverable on PATH (the cheapest real signal
// available; the SDK has no standalone ping, and a full Connect+Disconnect
// would spawn a real subprocess per check).
func (r *ClaudeRunner) CheckHealth(_ context.Context) error {
	if r.reposRoot == "" {
		return errors.New("AGENT_REPOS_ROOT is not configured")
	}
	if _, err := lookPath("claude"); err != nil {
		return fmt.Errorf("claude CLI not found on PATH: %w", err)
	}
	return nil
}

// ListModels implements AgentRunner. The claude CLI has no per-request
// selectable model list through this integration (its model is configured
// via the CLI/its own settings, not per Run call) — it always reports no
// models, which callers should treat as "model selection isn't offered by
// this executor," not an error.
func (r *ClaudeRunner) ListModels(_ context.Context) ([]string, error) {
	return nil, nil
}

// CloseSession implements AgentRunner: disconnects and forgets the cached
// client for sessionKey, if one exists.
func (r *ClaudeRunner) CloseSession(sessionKey string) {
	r.mu.Lock()
	client, ok := r.clients[sessionKey]
	if ok {
		delete(r.clients, sessionKey)
	}
	r.mu.Unlock()
	if ok {
		_ = client.Disconnect()
	}
}

// Run implements AgentRunner.
func (r *ClaudeRunner) Run(ctx context.Context, in RunInput, onDelta func(chat.Delta) error) (RunOutput, error) {
	// The claude CLI path drives its own subprocess rather than the shared
	// toolloop engine, so it does not surface per-call tool activity here:
	// in.OnToolCall/OnToolResult are intentionally ignored (only the
	// engine-backed ChatClientRunner honors them).
	key := in.SessionKey
	if !r.tryLock(key) {
		return RunOutput{}, ErrRunInProgress
	}
	defer r.unlock(key)

	runCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	client, err := r.clientFor(runCtx, key, in)
	if err != nil {
		return RunOutput{}, err
	}

	if err := client.Query(runCtx, in.UserMessage); err != nil {
		if !isStaleClaudeConnectionError(err) {
			return RunOutput{}, fmt.Errorf("querying claude code agent: %w", err)
		}
		// A long idle gap between conversation turns (e.g. the human took
		// a while to reply) can outlive the cached `claude` CLI
		// subprocess — its stdin pipe is already gone, so the write fails
		// regardless of what's sent, on every future turn, until the
		// stale client is discarded. Evict it and reconnect exactly once
		// rather than surfacing a raw pipe error for something the human
		// did nothing to cause.
		r.CloseSession(key)
		client, err = r.clientFor(runCtx, key, in)
		if err != nil {
			return RunOutput{}, err
		}
		if err := client.Query(runCtx, in.UserMessage); err != nil {
			return RunOutput{}, fmt.Errorf("querying claude code agent: %w", err)
		}
	}

	var out RunOutput
	var content strings.Builder
	for msg := range client.ReceiveMessages(runCtx) {
		done, err := processMessage(msg, toolNames(in.Tools), &content, &out, onDelta)
		if err != nil {
			return out, err
		}
		if done {
			return out, nil
		}
	}
	return out, runCtx.Err()
}

// Execute implements AgentRunner. Unlike Run, it never reuses or caches a
// claudecode.Client across calls — Run's client cache exists to resume a
// multi-turn human conversation across separate HTTP requests, but an
// execution is one autonomous run to completion with no further turns, so
// a fresh client connected and disconnected within this call is simpler
// and can't accidentally leak a write-enabled session into some other
// SessionKey. Still guarded by the same tryLock/unlock(key) pair Run uses,
// so two overlapping Execute (or Execute+Run) calls for the same
// SessionKey still can't race.
func (r *ClaudeRunner) Execute(ctx context.Context, in ExecuteInput, onEvent func(ExecuteEvent) error) (ExecuteOutput, error) {
	key := in.SessionKey
	if !r.tryLock(key) {
		return ExecuteOutput{}, ErrRunInProgress
	}
	defer r.unlock(key)

	if in.Workspace == "" {
		return ExecuteOutput{}, errors.New("claude-code requires a resolved execution workspace")
	}

	runCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	client := r.newClient(
		claudecode.WithCwd(in.Workspace),
		claudecode.WithSystemPrompt(in.SystemPrompt),
		claudecode.WithPartialStreaming(),
		claudecode.WithMaxTurns(claudeExecutionMaxTurns),
		claudecode.WithAllowedTools(executionTools...),
	)
	if err := client.Connect(runCtx); err != nil {
		return ExecuteOutput{}, fmt.Errorf("connecting claude code agent for execution %s: %w", key, err)
	}
	defer func() { _ = client.Disconnect() }()

	if err := client.Query(runCtx, executionKickoffMessage); err != nil {
		return ExecuteOutput{}, fmt.Errorf("starting claude code execution: %w", err)
	}

	var out ExecuteOutput
	var content strings.Builder
	for msg := range client.ReceiveMessages(runCtx) {
		done, err := processExecuteMessage(msg, &content, &out, onEvent)
		if err != nil {
			return out, err
		}
		if done {
			return out, nil
		}
	}
	return out, runCtx.Err()
}

func (r *ClaudeRunner) tryLock(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inFlight[key] {
		return false
	}
	r.inFlight[key] = true
	return true
}

func (r *ClaudeRunner) unlock(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.inFlight, key)
}

// clientFor returns the cached client for key, or creates, connects, and
// caches one from in's workspace/system prompt/tool. Callers must hold
// key's in-flight lock (via tryLock) so two calls for the same key never
// race to create a client.
func (r *ClaudeRunner) clientFor(ctx context.Context, key string, in RunInput) (claudecode.Client, error) {
	r.mu.Lock()
	client, ok := r.clients[key]
	r.mu.Unlock()
	if ok {
		return client, nil
	}

	if in.Workspace == "" {
		return nil, errors.New("claude-code requires a project repository checked out under AGENT_REPOS_ROOT")
	}

	opts := []claudecode.Option{
		claudecode.WithCwd(in.Workspace),
		claudecode.WithSystemPrompt(systemPromptWithHistory(in.SystemPrompt, in.History)),
		claudecode.WithPartialStreaming(),
		claudecode.WithMaxTurns(claudeRunnerMaxTurns),
	}

	allowedTools := append([]string{}, readOnlyTools...)
	// The Review stage (Milestone 6) widens the read-only boundary with Bash
	// so the reviewing agent can run the project's tests over the executed
	// change — confined to the execution worktree (in.Workspace), never the
	// shared checkout. Requirements/Planning leave EnableBashTool false and
	// stay strictly read-only.
	if in.EnableBashTool {
		allowedTools = append(allowedTools, "Bash")
	}
	// in.Tools is optional — free-chat callers (no Draft concept) leave it
	// empty, in which case no MCP tool/server is registered at all rather
	// than trying to build one from zero tools.
	if len(in.Tools) > 0 {
		tools := make([]*claudecode.McpTool, 0, len(in.Tools))
		for _, t := range in.Tools {
			schema, err := decodeToolSchema(t.Function.Parameters)
			if err != nil {
				return nil, err
			}
			tools = append(tools, claudecode.NewTool(t.Function.Name, t.Function.Description, schema, draftToolHandler))
			allowedTools = append(allowedTools, mcpQualifiedName(t.Function.Name))
		}
		server := claudecode.CreateSDKMcpServer(mcpServerName, "1.0.0", tools...)
		opts = append(opts, claudecode.WithSdkMcpServer(mcpServerName, server))
	}
	opts = append(opts, claudecode.WithAllowedTools(allowedTools...))

	client = r.newClient(opts...)
	if err := client.Connect(ctx); err != nil {
		return nil, fmt.Errorf("connecting claude code agent for %s: %w", key, err)
	}

	r.mu.Lock()
	r.clients[key] = client
	r.mu.Unlock()
	return client, nil
}

// systemPromptWithHistory returns systemPrompt unchanged when history is
// empty (the common case: an already-live session, or a brand-new
// conversation with nothing to replay), or with a rendered transcript of
// history appended when a fresh client is being created to replace one
// this process lost (e.g. a server restart wiped ClaudeRunner's cached
// clients even though the conversation's messages survived in
// conversation-{stage}.yaml). The CLI has no "resume with this history"
// primitive — only a system prompt fixed at connect time and a fresh
// Query — so a rendered transcript block is the only way a new session's
// agent learns what was already discussed.
func systemPromptWithHistory(systemPrompt string, history []chat.Message) string {
	if len(history) == 0 {
		return systemPrompt
	}
	var b strings.Builder
	b.WriteString(systemPrompt)
	b.WriteString("\n\n## Prior conversation (restored after restart)\n")
	for _, m := range history {
		fmt.Fprintf(&b, "%s: %s\n", m.Role, m.Content)
	}
	return b.String()
}

// isStaleClaudeConnectionError reports whether err indicates the cached
// `claude` CLI subprocess is already gone — its stdin pipe closed, or the
// SDK's own transport already noticed the process died — as opposed to a
// genuine query failure that reconnecting wouldn't fix. This is the
// specific failure a long idle gap between conversation turns produces:
// ClaudeRunner keeps a claudecode.Client cached indefinitely per
// SessionKey (see the type doc), but nothing keeps the underlying `claude`
// subprocess itself alive forever, so writing to a turn after it has
// exited fails with a low-level pipe error. Detected by message content
// since neither the OS-level error (its concrete type differs by
// platform) nor the SDK (github.com/severity1/claude-agent-sdk-go)
// exposes a stable sentinel for this.
func isStaleClaudeConnectionError(err error) bool {
	msg := err.Error()
	for _, substr := range []string{
		"pipe is being closed", // Windows: write to a pipe the peer closed
		"broken pipe",          // Unix: write to a pipe the peer closed
		"closed pipe",          // io.ErrClosedPipe's message
		"stdin closed",         // the SDK's own "transport not connected or stdin closed"
		"file already closed",  // os.ErrClosed's message
	} {
		if strings.Contains(msg, substr) {
			return true
		}
	}
	return false
}

// mcpQualifiedName returns the fully-qualified tool name the `claude` CLI
// reports in a ToolUseBlock for an in-process MCP tool (mcp__<server>__
// <tool>) — WithAllowedTools and ToolUseBlock.Name both use this qualified
// form, never a bare tool name a caller passes in RunInput.Tools.
func mcpQualifiedName(toolName string) string {
	return "mcp__" + mcpServerName + "__" + toolName
}

func decodeToolSchema(parameters json.RawMessage) (map[string]any, error) {
	var schema map[string]any
	if err := json.Unmarshal(parameters, &schema); err != nil {
		return nil, fmt.Errorf("decoding tool schema: %w", err)
	}
	return schema, nil
}

// draftToolHandler acknowledges a Draft-proposing tool call so the CLI's
// turn can complete. The actual proposal is extracted from the
// ToolUseBlock in the message stream (processMessage) — matching how the
// local-LLM chat path never applies a Draft itself (Finalize does) — this
// handler has no side effects on task state.
func draftToolHandler(_ context.Context, _ map[string]any) (*claudecode.McpToolResult, error) {
	return &claudecode.McpToolResult{
		Content: []claudecode.McpContent{{Type: "text", Text: "Draft proposed to user for review."}},
	}, nil
}

// toolNames returns the bare (non-MCP-qualified) names of tools.
func toolNames(tools []chat.Tool) []string {
	if len(tools) == 0 {
		return nil
	}
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Function.Name
	}
	return names
}

// processMessage folds one message from a claudecode.Client's message
// stream into content/out, and reports whether the turn is complete
// (msg was a ResultMessage). toolNames are the bare names of every Draft
// tool this turn offered (RunInput.Tools) — a ToolUseBlock is only ever
// treated as the turn's Draft proposal if its MCP-qualified name matches
// one of them; a session with several offered tools (e.g. Review's
// propose_review and propose_knowledge) surfaces whichever one the model
// actually called. Split out from Run's loop so it's testable against
// hand-built claudecode.Message values without a live subprocess.
func processMessage(msg claudecode.Message, toolNames []string, content *strings.Builder, out *RunOutput, onDelta func(chat.Delta) error) (done bool, err error) {
	switch m := msg.(type) {
	case *claudecode.StreamEvent:
		if text, ok := streamDeltaText(m); ok && onDelta != nil {
			if err := onDelta(chat.Delta{Content: text}); err != nil {
				return true, err
			}
		}
	case *claudecode.AssistantMessage:
		for _, block := range m.Content {
			switch b := block.(type) {
			case *claudecode.TextBlock:
				content.WriteString(b.Text)
			case *claudecode.ToolUseBlock:
				if out.ToolCall == nil {
					if name, ok := matchQualifiedToolName(b.Name, toolNames); ok {
						args, err := json.Marshal(b.Input)
						if err != nil {
							return true, fmt.Errorf("encoding tool call arguments: %w", err)
						}
						out.ToolCall = &chat.ToolCall{
							ID:   b.ToolUseID,
							Type: "function",
							Function: chat.ToolCallFunction{
								Name:      name,
								Arguments: string(args),
							},
						}
					}
				}
			}
		}
	case *claudecode.ResultMessage:
		out.Content = content.String()
		if m.IsError {
			return true, fmt.Errorf("claude code agent run failed: %s", strings.Join(m.Errors, "; "))
		}
		return true, nil
	}
	return false, nil
}

// matchQualifiedToolName reports whether qualifiedName (a ToolUseBlock's
// mcp__<server>__<tool> name) matches any of names' MCP-qualified form,
// returning that bare name if so.
func matchQualifiedToolName(qualifiedName string, names []string) (string, bool) {
	for _, name := range names {
		if qualifiedName == mcpQualifiedName(name) {
			return name, true
		}
	}
	return "", false
}

// processExecuteMessage folds one message from a claudecode.Client's
// message stream into content/out for an Execute call, emitting an
// ExecuteEvent for every meaningful thing that happens along the way, and
// reporting whether the run is complete (msg was a ResultMessage). Unlike
// processMessage (which only surfaces one Draft-matching ToolUseBlock and
// silently drops everything else), this surfaces every tool call and its
// result — an Execute run's real actions (files written, commands run) are
// the point, not incidental.
func processExecuteMessage(msg claudecode.Message, content *strings.Builder, out *ExecuteOutput, onEvent func(ExecuteEvent) error) (done bool, err error) {
	switch m := msg.(type) {
	case *claudecode.StreamEvent:
		if text, ok := streamDeltaText(m); ok && onEvent != nil {
			if err := onEvent(ExecuteEvent{Kind: "text", Text: text}); err != nil {
				return true, err
			}
		}
	case *claudecode.AssistantMessage:
		for _, block := range m.Content {
			switch b := block.(type) {
			case *claudecode.TextBlock:
				content.WriteString(b.Text)
			case *claudecode.ToolUseBlock:
				if onEvent == nil {
					continue
				}
				input, err := json.Marshal(b.Input)
				if err != nil {
					return true, fmt.Errorf("encoding tool call input: %w", err)
				}
				if err := onEvent(ExecuteEvent{Kind: "tool_call", ToolName: b.Name, ToolInput: string(input)}); err != nil {
					return true, err
				}
			}
		}
	case *claudecode.UserMessage:
		blocks, ok := m.Content.([]claudecode.ContentBlock)
		if !ok || onEvent == nil {
			break
		}
		for _, block := range blocks {
			b, ok := block.(*claudecode.ToolResultBlock)
			if !ok {
				continue
			}
			isError := b.IsError != nil && *b.IsError
			if err := onEvent(ExecuteEvent{Kind: "tool_result", ToolResult: toolResultText(b.Content), IsError: isError}); err != nil {
				return true, err
			}
		}
	case *claudecode.ResultMessage:
		out.Content = content.String()
		out.DurationSeconds = float64(m.DurationMs) / 1000
		out.NumTurns = m.NumTurns
		if m.TotalCostUSD != nil {
			out.CostEstimate = *m.TotalCostUSD
		}
		out.TokensUsed = sumUsageTokens(m.Usage)
		if m.IsError {
			return true, fmt.Errorf("claude code execution failed: %s", strings.Join(m.Errors, "; "))
		}
		return true, nil
	}
	return false, nil
}

// toolResultText renders a ToolResultBlock's Content (documented as
// "string or structured data") as a string for ExecuteEvent.ToolResult —
// the common case is already a string; anything else is JSON-encoded
// best-effort rather than dropped.
func toolResultText(content any) string {
	if s, ok := content.(string); ok {
		return s
	}
	data, err := json.Marshal(content)
	if err != nil {
		return fmt.Sprintf("%v", content)
	}
	return string(data)
}

// sumUsageTokens adds up every numeric token-count field in a
// ResultMessage's Usage map (e.g. "input_tokens", "output_tokens",
// "cache_read_input_tokens") — the SDK models Usage as an untyped
// map[string]any (it mirrors the CLI's own JSON verbatim rather than
// defining a fixed struct), so this sums whatever numeric fields are
// actually present rather than assuming specific key names. Returns 0 for
// a nil map rather than erroring, per ExecuteOutput's "leave unreported
// metrics at zero" convention.
func sumUsageTokens(usage *map[string]any) int {
	if usage == nil {
		return 0
	}
	total := 0
	for _, v := range *usage {
		switch n := v.(type) {
		case float64:
			total += int(n)
		case int:
			total += n
		}
	}
	return total
}

// streamDeltaText extracts incremental assistant text from a partial
// streaming event (emitted because clientFor sets WithPartialStreaming),
// or reports ok=false for any event that isn't a text content delta.
func streamDeltaText(ev *claudecode.StreamEvent) (text string, ok bool) {
	if evType, _ := ev.Event["type"].(string); evType != claudecode.StreamEventTypeContentBlockDelta {
		return "", false
	}
	delta, ok := ev.Event["delta"].(map[string]any)
	if !ok {
		return "", false
	}
	text, ok = delta["text"].(string)
	return text, ok
}
