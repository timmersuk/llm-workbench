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

// claudeRunnerMaxTurns bounds how many internal tool-call round-trips a
// single Run call may make, as a defense-in-depth cap independent of the
// Write/Edit/Bash denial above.
const claudeRunnerMaxTurns = 30

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
		done, err := processMessage(msg, in.Tool.Function.Name, &content, &out, onDelta)
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
	// in.Tool is optional — free-chat callers (no Draft concept) leave it
	// as the zero value, in which case no MCP tool/server is registered at
	// all rather than trying to build one from an empty name/schema.
	if in.Tool.Function.Name != "" {
		schema, err := decodeToolSchema(in.Tool.Function.Parameters)
		if err != nil {
			return nil, err
		}
		tool := claudecode.NewTool(in.Tool.Function.Name, in.Tool.Function.Description, schema, draftToolHandler)
		server := claudecode.CreateSDKMcpServer(mcpServerName, "1.0.0", tool)
		allowedTools = append(allowedTools, mcpQualifiedName(in.Tool.Function.Name))
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
// form, never the bare tool name a caller passes in RunInput.Tool.
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

// processMessage folds one message from a claudecode.Client's message
// stream into content/out, and reports whether the turn is complete
// (msg was a ResultMessage). Split out from Run's loop so it's testable
// against hand-built claudecode.Message values without a live subprocess.
func processMessage(msg claudecode.Message, toolName string, content *strings.Builder, out *RunOutput, onDelta func(chat.Delta) error) (done bool, err error) {
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
				if out.ToolCall == nil && b.Name == mcpQualifiedName(toolName) {
					args, err := json.Marshal(b.Input)
					if err != nil {
						return true, fmt.Errorf("encoding tool call arguments: %w", err)
					}
					out.ToolCall = &chat.ToolCall{
						ID:   b.ToolUseID,
						Type: "function",
						Function: chat.ToolCallFunction{
							Name:      toolName,
							Arguments: string(args),
						},
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
