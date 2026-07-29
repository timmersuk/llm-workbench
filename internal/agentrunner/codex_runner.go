package agentrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	codex "github.com/hishamkaram/codex-agent-sdk-go"
	"github.com/hishamkaram/codex-agent-sdk-go/types"

	"github.com/timmersuk/llm-workbench/internal/chat"
	"github.com/timmersuk/llm-workbench/internal/drafttool"
	"github.com/timmersuk/llm-workbench/internal/gitutil"
	"github.com/timmersuk/llm-workbench/internal/knowledgetool"
)

// codexDraftServerName is the MCP server name CodexRunner registers
// cmd/draftmcp's binary under (config key: mcp_servers.<name>). Chosen to
// be distinctive enough it won't collide with a user's own MCP servers.
const codexDraftServerName = "llm-workbench-draft"

// codexKickoffMessage is Execute's fixed opening turn — mirrors
// claude_runner.go's executionKickoffMessage; all real instructions live
// in ExecuteInput.SystemPrompt (sent via developerInstructionsArgs).
const codexKickoffMessage = "Begin executing the plan."

// CodexRunner implements AgentRunner backed by
// github.com/hishamkaram/codex-agent-sdk-go, which drives `codex
// app-server` as a subprocess over JSON-RPC. Unlike ClaudeRunner, no
// per-session client is cached: codex has no "system prompt fixed at
// connect time" concept to make caching worthwhile the way severity1's SDK
// does, so every Run/Execute call connects a fresh client and closes it
// when done — the same pattern ClaudeRunner.Execute already uses for its
// one-shot autonomous runs.
//
// codex-agent-sdk-go has no in-process "SDK MCP server" helper (unlike
// severity1's WithSdkMcpServer) and its per-thread WithMCPServers config
// does not reliably reach the underlying `codex` CLI (verified against
// codex-cli 0.143.0 — the registered tool never appeared in the model's
// tool list). The only mechanism that actually works is a *statically*
// registered MCP server (persisted to the user's codex config, the same
// as running `codex mcp add`) plus a persisted per-tool
// `approval_mode: "approve"` — both confirmed to let a brand-new,
// never-before-seen tool succeed on its very first non-interactive call,
// with no human approval step. ensureRegistered does this once, lazily,
// via Client.WriteConfigBatch.
type CodexRunner struct {
	mu       sync.Mutex
	inFlight map[string]bool

	timeout        time.Duration
	executeTimeout time.Duration
	reposRoot      string
	draftMCPPath   string
	knowledgeRoot  string

	registerOnce sync.Once
	registerErr  error
}

// NewCodexRunner returns a CodexRunner whose Run calls are each bounded by
// timeout and whose Execute calls are separately bounded by executeTimeout
// — split the same way and for the same reason as NewClaudeRunner's
// timeout/executeTimeout. reposRoot is the configured REPOS_ROOT
// value (same role as NewClaudeRunner's). draftMCPPath is the absolute path
// to the compiled cmd/draftmcp binary; CodexRunner registers it as an MCP
// server the first time Run or Execute is actually called (see
// ensureRegistered), not at construction time. knowledgeRoot, if non-empty,
// is passed to that same draftmcp process as its --knowledge-root flag
// (docs/milestones/done/milestone9.md), so codex threads get the same real
// list_knowledge_concepts/get_knowledge_concept tools ClaudeRunner and
// ChatClientRunner do — an empty knowledgeRoot just omits the flag, the
// same as running draftmcp directly with no --knowledge-root.
func NewCodexRunner(timeout, executeTimeout time.Duration, reposRoot string, draftMCPPath string, knowledgeRoot string) *CodexRunner {
	return &CodexRunner{
		inFlight:       make(map[string]bool),
		timeout:        timeout,
		executeTimeout: executeTimeout,
		reposRoot:      reposRoot,
		draftMCPPath:   draftMCPPath,
		knowledgeRoot:  knowledgeRoot,
	}
}

// CheckHealth implements AgentRunner. reposRoot and draftMCPPath must both
// be configured, and the `codex` CLI must be discoverable on PATH — the
// cheapest real signal available without spawning a subprocess per check
// (mirrors ClaudeRunner.CheckHealth). This deliberately does not attempt
// ensureRegistered: that spawns a real `codex app-server` subprocess, too
// expensive to run on every health poll.
func (r *CodexRunner) CheckHealth(_ context.Context) error {
	if r.reposRoot == "" {
		return errors.New("REPOS_ROOT is not configured")
	}
	if r.draftMCPPath == "" {
		return errors.New("codex draft MCP server binary is not configured")
	}
	if _, err := lookPath("codex"); err != nil {
		return fmt.Errorf("codex CLI not found on PATH: %w", err)
	}
	return nil
}

// ListModels implements AgentRunner. codex's model is configured via
// -c/--model per call (RunInput.Model/ExecuteInput.Model, honored directly
// by Run/Execute), not discovered through a listing call here — mirrors
// ClaudeRunner.ListModels' "no models" convention.
func (r *CodexRunner) ListModels(_ context.Context) ([]string, error) {
	return nil, nil
}

// CloseSession implements AgentRunner. CodexRunner never caches a session
// across calls (see the type doc comment), so there is nothing to
// discard — safe to call for any key.
func (r *CodexRunner) CloseSession(_ string) {}

// Run implements AgentRunner.
func (r *CodexRunner) Run(ctx context.Context, in RunInput, onDelta func(chat.Delta) error) (RunOutput, error) {
	if err := r.ensureRegistered(ctx); err != nil {
		return RunOutput{}, fmt.Errorf("registering codex draft MCP server: %w", err)
	}
	if in.Workspace == "" {
		return RunOutput{}, errors.New("codex requires a project repository checked out under REPOS_ROOT")
	}

	key := in.SessionKey
	if !r.tryLock(key) {
		return RunOutput{}, ErrRunInProgress
	}
	defer r.unlock(key)

	runCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	opts := types.NewCodexOptions().
		WithSandbox(types.SandboxReadOnly).
		WithApprovalPolicy(types.ApprovalNever).
		WithExtraArgs(developerInstructionsArgs(systemPromptWithHistory(in.SystemPrompt, in.History))...)
	if in.Model != "" {
		opts = opts.WithModel(in.Model)
	}

	client, err := codex.NewClient(runCtx, opts)
	if err != nil {
		return RunOutput{}, fmt.Errorf("creating codex client for %s: %w", key, err)
	}
	if err := client.Connect(runCtx); err != nil {
		return RunOutput{}, fmt.Errorf("connecting codex agent for %s: %w", key, err)
	}
	defer func() { _ = client.Close(context.Background()) }()

	thread, err := client.StartThread(runCtx, &types.ThreadOptions{Cwd: in.Workspace})
	if err != nil {
		return RunOutput{}, fmt.Errorf("starting codex thread for %s: %w", key, err)
	}

	names := toolNames(in.Tools)
	prompt := in.UserMessage
	if len(names) > 0 {
		prompt = prompt + "\n\n" + draftToolInstruction(names)
	}

	events, err := thread.RunStreamed(runCtx, prompt, nil)
	if err != nil {
		return RunOutput{}, fmt.Errorf("running codex turn for %s: %w", key, err)
	}

	var out RunOutput
	var content strings.Builder
	for ev := range events {
		done, err := processCodexRunEvent(ev, names, &content, &out, onDelta, in.OnToolCall, in.OnToolResult)
		if err != nil {
			return out, err
		}
		if done {
			return out, nil
		}
	}
	return out, runCtx.Err()
}

// Execute implements AgentRunner. Like ClaudeRunner.Execute, never reuses
// a client across calls — an execution is one autonomous run to
// completion with no further turns.
func (r *CodexRunner) Execute(ctx context.Context, in ExecuteInput, onEvent func(ExecuteEvent) error) (ExecuteOutput, error) {
	if err := r.ensureRegistered(ctx); err != nil {
		return ExecuteOutput{}, fmt.Errorf("registering codex draft MCP server: %w", err)
	}

	key := in.SessionKey
	if !r.tryLock(key) {
		return ExecuteOutput{}, ErrRunInProgress
	}
	defer r.unlock(key)

	if in.Workspace == "" {
		return ExecuteOutput{}, errors.New("codex requires a resolved execution workspace")
	}

	runCtx, cancel := context.WithTimeout(ctx, r.executeTimeout)
	defer cancel()

	start := time.Now()

	// A git worktree's own admin metadata (HEAD, index, refs — what `git
	// commit` actually writes to) always lives under the *original* repo's
	// .git/worktrees/<id>/, never inside the worktree's own working
	// directory (verified live: codex's workspace-write sandbox denied
	// writing there with "Permission denied" on index.lock, since it's
	// outside in.Workspace). --add-dir is CLI-flag syntax for `codex exec`
	// only — `codex app-server` (what this SDK actually drives) rejects it
	// outright ("unexpected argument '--add-dir' found"), which silently
	// hung our Connect() rather than failing fast (verified live: the
	// subprocess exits immediately on the bad flag, but the SDK's
	// initialize call has no liveness check and just waits for a response
	// that will never arrive, until our own context timeout fires).
	// sandbox_workspace_write.writable_roots is the config-level
	// equivalent app-server actually accepts.
	extraArgs := developerInstructionsArgs(in.SystemPrompt)
	if gitDir, err := worktreeGitDir(runCtx, in.Workspace); err == nil {
		extraArgs = append(extraArgs, "-c", "sandbox_workspace_write.writable_roots=["+tomlQuote(gitDir)+"]")
	} else {
		return ExecuteOutput{}, fmt.Errorf("resolving worktree git-dir for execution %s: %w", key, err)
	}

	opts := types.NewCodexOptions().
		WithSandbox(types.SandboxWorkspaceWrite).
		WithApprovalPolicy(types.ApprovalNever).
		WithExtraArgs(extraArgs...)
	if in.Model != "" {
		opts = opts.WithModel(in.Model)
	}

	client, err := codex.NewClient(runCtx, opts)
	if err != nil {
		return ExecuteOutput{}, fmt.Errorf("creating codex client for execution %s: %w", key, err)
	}
	if err := client.Connect(runCtx); err != nil {
		return ExecuteOutput{}, fmt.Errorf("connecting codex agent for execution %s: %w", key, err)
	}
	defer func() { _ = client.Close(context.Background()) }()

	thread, err := client.StartThread(runCtx, &types.ThreadOptions{Cwd: in.Workspace})
	if err != nil {
		return ExecuteOutput{}, fmt.Errorf("starting codex execution thread %s: %w", key, err)
	}

	events, err := thread.RunStreamed(runCtx, codexKickoffMessage, nil)
	if err != nil {
		return ExecuteOutput{}, fmt.Errorf("starting codex execution: %w", err)
	}

	var out ExecuteOutput
	var content strings.Builder
	for ev := range events {
		done, err := processCodexExecuteEvent(ev, &content, &out, onEvent)
		if err != nil {
			out.DurationSeconds = time.Since(start).Seconds()
			return out, err
		}
		if done {
			out.DurationSeconds = time.Since(start).Seconds()
			return out, nil
		}
	}
	out.DurationSeconds = time.Since(start).Seconds()
	return out, runCtx.Err()
}

func (r *CodexRunner) tryLock(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.inFlight[key] {
		return false
	}
	r.inFlight[key] = true
	return true
}

func (r *CodexRunner) unlock(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.inFlight, key)
}

// ensureRegistered registers cmd/draftmcp as a persistent MCP server
// (config key mcp_servers.<codexDraftServerName>) and grants every known
// Draft tool a persisted approval_mode="approve", exactly once per
// process lifetime. Both edits are equivalent to what a human would get
// running `codex mcp add` plus interactively choosing "Always allow" once
// per tool — done here so no human step is required on a fresh machine.
// A failed attempt is not retried (sync.Once) — restart the process to
// retry after fixing the underlying problem (e.g. installing codex).
func (r *CodexRunner) ensureRegistered(ctx context.Context) error {
	r.registerOnce.Do(func() {
		r.registerErr = r.registerDraftServer(ctx)
	})
	return r.registerErr
}

func (r *CodexRunner) registerDraftServer(ctx context.Context) error {
	if r.draftMCPPath == "" {
		return errors.New("codex draft MCP server binary path is not configured")
	}

	client, err := codex.NewClient(ctx, types.NewCodexOptions().WithSandbox(types.SandboxReadOnly))
	if err != nil {
		return fmt.Errorf("creating codex client: %w", err)
	}
	if err := client.Connect(ctx); err != nil {
		return fmt.Errorf("connecting codex client: %w", err)
	}
	defer func() { _ = client.Close(context.Background()) }()

	serverConfig := map[string]any{"command": r.draftMCPPath}
	if r.knowledgeRoot != "" {
		serverConfig["args"] = []string{"--knowledge-root", r.knowledgeRoot}
	}
	edits := []types.ConfigEntry{
		{
			KeyPath: "mcp_servers." + codexDraftServerName,
			Value:   serverConfig,
		},
	}
	// Every tool this same draftmcp process might serve — both the Draft
	// proposal tools and (when knowledgeRoot is configured) the knowledge
	// query tools — gets the same persisted "always allow" approval, so a
	// codex thread never blocks on an interactive approval prompt for
	// either kind, the first time it calls any of them.
	approvedNames := make([]string, 0, len(drafttool.All())+len(knowledgetool.All()))
	for _, def := range drafttool.All() {
		approvedNames = append(approvedNames, def.Name)
	}
	if r.knowledgeRoot != "" {
		for _, def := range knowledgetool.All() {
			approvedNames = append(approvedNames, def.Name)
		}
	}
	for _, name := range approvedNames {
		edits = append(edits, types.ConfigEntry{
			KeyPath: fmt.Sprintf("mcp_servers.%s.tools.%s.approval_mode", codexDraftServerName, name),
			Value:   "approve",
		})
	}
	if _, err := client.WriteConfigBatch(ctx, edits); err != nil {
		return fmt.Errorf("writing draft MCP server config: %w", err)
	}
	return nil
}

// worktreeGitDir resolves the real git directory backing an isolated
// execution worktree — for a linked worktree this is the original repo's
// .git/worktrees/<id>/, not a path under workspace itself, since that's
// where git actually writes commit-time state (index.lock, HEAD, refs).
func worktreeGitDir(ctx context.Context, workspace string) (string, error) {
	out, err := gitutil.RunGit(ctx, workspace, "rev-parse", "--git-dir")
	if err != nil {
		return "", fmt.Errorf("resolving git-dir: %w", err)
	}
	gitDir := strings.TrimSpace(out)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(workspace, gitDir)
	}
	return gitDir, nil
}

// draftToolInstruction tells the model which MCP tool(s) it may call once a
// proposal is ready — codex has no equivalent of severity1's
// WithAllowedTools scoping the conversation to a single expected tool, so
// the model needs to be told explicitly which of the statically registered
// Draft tools (propose_context, propose_plan, ...) applies here. Usually
// one name; Review offers two (propose_review, propose_knowledge).
func draftToolInstruction(toolNames []string) string {
	quoted := make([]string, len(toolNames))
	for i, name := range toolNames {
		quoted[i] = fmt.Sprintf("%q", name)
	}
	return fmt.Sprintf(
		"When (and only when) you are ready to submit a proposal, call the appropriate MCP tool named %s "+
			"(registered under the %q server) with your proposal as its arguments, matching its declared schema exactly.",
		strings.Join(quoted, " or "), codexDraftServerName,
	)
}

// developerInstructionsArgs renders systemPrompt as codex's
// `-c developer_instructions=<toml string>` override — codex has no
// dedicated --system-prompt flag/option (verified: neither ThreadOptions
// nor RunOptions expose one), but developer_instructions is honored the
// same way, confirmed live. Returns nil for an empty prompt so callers
// don't pass a meaningless "-c developer_instructions=\"\"" arg.
func developerInstructionsArgs(systemPrompt string) []string {
	if systemPrompt == "" {
		return nil
	}
	return []string{"-c", "developer_instructions=" + tomlQuote(systemPrompt)}
}

// tomlQuote renders s as a TOML basic string (RFC-ish: quoted, with
// backslashes, quotes, and control characters escaped) suitable for a
// `-c key=value` override, where value is parsed as TOML.
func tomlQuote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// appendAgentMessageText appends one completed AgentMessage item's text to
// content, which accumulates across an entire turn (Run's/Execute's loop).
// Codex's thread stream emits a separate ItemCompleted{Item: *AgentMessage}
// event for each complete remark the agent makes between tool calls, so
// without a paragraph break here two consecutive AgentMessages would
// concatenate directly into one run-on line once rendered as markdown (see
// claude_runner.go's identical fix for processMessage/processExecuteMessage,
// where the same content builder is shared across separate AssistantMessages).
func appendAgentMessageText(content *strings.Builder, text string) {
	if content.Len() > 0 {
		content.WriteString("\n\n")
	}
	content.WriteString(text)
}

// processCodexRunEvent folds one types.ThreadEvent from a codex thread
// into content/out for Run, mirroring claude_runner.go's processMessage.
// toolNames are the Draft tool(s) this turn offered (empty for free-chat
// callers, in which case no MCPToolCall is ever treated as the Draft
// proposal); a session offering more than one (Review's propose_review and
// propose_knowledge) surfaces whichever one the model actually called.
// onToolCall/onToolResult (docs/adr/0018) surface every other completed
// item — a shell command, a file change, or a non-Draft MCP call — as
// intermediate Tool Activity (CONTEXT.md), mirroring
// processCodexExecuteEvent's existing handling for Execute. Unlike
// claude_runner.go's processMessage (which needs a toolUseID->name
// correlation across two separate messages), a codex ItemCompleted event
// already carries the whole call-and-result together, so onToolCall/
// onToolResult are simply invoked back to back, no correlation state
// needed.
func processCodexRunEvent(ev types.ThreadEvent, toolNames []string, content *strings.Builder, out *RunOutput, onDelta func(chat.Delta) error, onToolCall func(name, argsJSON string), onToolResult func(name, result string, isError bool)) (done bool, err error) {
	switch e := ev.(type) {
	case *types.ItemUpdated:
		if delta, ok := e.Delta.(*types.AgentMessageDelta); ok && onDelta != nil {
			if err := onDelta(chat.Delta{Content: delta.TextChunk}); err != nil {
				return true, err
			}
		}
	case *types.ItemCompleted:
		switch item := e.Item.(type) {
		case *types.AgentMessage:
			appendAgentMessageText(content, item.Text)
		case *types.CommandExecution:
			if onToolCall != nil {
				onToolCall("Bash", item.Command)
			}
			if onToolResult != nil {
				isErr := item.Status == "failed" || item.Status == "denied"
				onToolResult("Bash", item.AggregatedOutput, isErr)
			}
		case *types.FileChange:
			if onToolCall != nil {
				input, marshalErr := json.Marshal(item.Changes)
				if marshalErr != nil {
					return true, fmt.Errorf("encoding file change input: %w", marshalErr)
				}
				onToolCall("FileChange", string(input))
			}
			if onToolResult != nil {
				onToolResult("FileChange", item.Status, item.Status == "failed")
			}
		case *types.MCPToolCall:
			if out.ToolCall == nil && slices.Contains(toolNames, item.ToolName) {
				out.ToolCall = &chat.ToolCall{
					ID:   item.ID,
					Type: "function",
					Function: chat.ToolCallFunction{
						Name:      item.ToolName,
						Arguments: string(item.Input),
					},
				}
				break
			}
			// Not the Draft — genuine intermediate tool activity (e.g. a
			// knowledge-query call).
			if onToolCall != nil {
				onToolCall(item.ToolName, string(item.Input))
			}
			if onToolResult != nil {
				isErr := item.Status == "failed"
				resultText := string(item.Result)
				if isErr {
					resultText = item.ErrorText()
				}
				onToolResult(item.ToolName, resultText, isErr)
			}
		}
	case *types.TurnFailed:
		out.Content = content.String()
		return true, fmt.Errorf("codex agent run failed: %s", e.Message)
	case *types.TurnCompleted:
		out.Content = content.String()
		if e.Status == "failed" {
			return true, errors.New("codex agent run failed")
		}
		return true, nil
	}
	return false, nil
}

// processCodexExecuteEvent folds one types.ThreadEvent into content/out
// for Execute, mirroring claude_runner.go's processExecuteMessage: every
// tool action (shell command, file change, MCP tool call) is surfaced as
// a tool_call/tool_result ExecuteEvent pair, not just prose.
func processCodexExecuteEvent(ev types.ThreadEvent, content *strings.Builder, out *ExecuteOutput, onEvent func(ExecuteEvent) error) (done bool, err error) {
	switch e := ev.(type) {
	case *types.ItemUpdated:
		if delta, ok := e.Delta.(*types.AgentMessageDelta); ok && onEvent != nil {
			if err := onEvent(ExecuteEvent{Kind: "text", Text: delta.TextChunk}); err != nil {
				return true, err
			}
		}
	case *types.ItemCompleted:
		if onEvent == nil {
			if msg, ok := e.Item.(*types.AgentMessage); ok {
				appendAgentMessageText(content, msg.Text)
			}
			break
		}
		switch item := e.Item.(type) {
		case *types.AgentMessage:
			appendAgentMessageText(content, item.Text)
		case *types.CommandExecution:
			if err := onEvent(ExecuteEvent{Kind: "tool_call", ToolName: "Bash", ToolInput: item.Command}); err != nil {
				return true, err
			}
			isErr := item.Status == "failed" || item.Status == "denied"
			if err := onEvent(ExecuteEvent{Kind: "tool_result", ToolResult: item.AggregatedOutput, IsError: isErr}); err != nil {
				return true, err
			}
		case *types.FileChange:
			input, marshalErr := json.Marshal(item.Changes)
			if marshalErr != nil {
				return true, fmt.Errorf("encoding file change input: %w", marshalErr)
			}
			if err := onEvent(ExecuteEvent{Kind: "tool_call", ToolName: "FileChange", ToolInput: string(input)}); err != nil {
				return true, err
			}
			isErr := item.Status == "failed"
			if err := onEvent(ExecuteEvent{Kind: "tool_result", ToolResult: item.Status, IsError: isErr}); err != nil {
				return true, err
			}
		case *types.MCPToolCall:
			if err := onEvent(ExecuteEvent{Kind: "tool_call", ToolName: item.ToolName, ToolInput: string(item.Input)}); err != nil {
				return true, err
			}
			isErr := item.Status == "failed"
			resultText := string(item.Result)
			if isErr {
				resultText = item.ErrorText()
			}
			if err := onEvent(ExecuteEvent{Kind: "tool_result", ToolResult: resultText, IsError: isErr}); err != nil {
				return true, err
			}
		}
	case *types.TurnFailed:
		out.Content = content.String()
		return true, fmt.Errorf("codex execution failed: %s", e.Message)
	case *types.TurnCompleted:
		out.Content = content.String()
		out.TokensUsed = int(e.Usage.TotalTokens)
		out.NumTurns = 1
		if e.Status == "failed" {
			return true, errors.New("codex execution failed")
		}
		return true, nil
	}
	return false, nil
}
