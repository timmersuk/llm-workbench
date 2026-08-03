package agentrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hishamkaram/codex-agent-sdk-go/types"
	codex "github.com/pmenglund/codex-sdk-go"
	"github.com/pmenglund/codex-sdk-go/protocol"
	"github.com/pmenglund/codex-sdk-go/rpc"

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
// in ExecuteInput.SystemPrompt (sent as a thread's developer instructions).
const codexKickoffMessage = "Begin executing the plan."

// CodexRunner implements AgentRunner backed by codex-sdk-go, which drives
// `codex app-server` as a subprocess over JSON-RPC. Unlike ClaudeRunner, no
// per-session client is cached: codex has no "system prompt fixed at
// connect time" concept to make caching worthwhile the way severity1's SDK
// does, so every Run/Execute call connects a fresh client and closes it
// when done — the same pattern ClaudeRunner.Execute already uses for its
// one-shot autonomous runs.
//
// The only mechanism that actually works is a *statically*
// registered MCP server (persisted to the user's codex config, the same
// as running `codex mcp add`) plus a persisted per-tool
// `approval_mode: "approve"` — both confirmed to let a brand-new,
// never-before-seen tool succeed on its very first non-interactive call,
// with no human approval step. ensureRegistered does this once, lazily,
// via app-server's config/batchWrite method.
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
// timeout/executeTimeout, including runTimeout's EnableBashTool exception
// (see NewClaudeRunner's doc comment). reposRoot is the configured REPOS_ROOT
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

// runTimeout picks Run's per-call budget — see ClaudeRunner.runTimeout,
// which this mirrors exactly.
func (r *CodexRunner) runTimeout(in RunInput) time.Duration {
	if in.EnableBashTool {
		return r.executeTimeout
	}
	return r.timeout
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

	runCtx, cancel := context.WithTimeout(ctx, r.runTimeout(in))
	defer cancel()

	client, err := newCodexClient(runCtx, nil)
	if err != nil {
		return RunOutput{}, fmt.Errorf("creating codex client for %s: %w", key, err)
	}
	defer func() { _ = client.Close() }()

	thread, err := client.StartThread(runCtx, codex.ThreadStartOptions{
		Cwd:                   in.Workspace,
		Model:                 in.Model,
		SandboxPolicy:         codex.SandboxModeReadOnly,
		ApprovalPolicy:        codex.ApprovalPolicyNever,
		DeveloperInstructions: systemPromptWithHistory(in.SystemPrompt, in.History),
	})
	if err != nil {
		return RunOutput{}, fmt.Errorf("starting codex thread for %s: %w", key, err)
	}

	names := toolNames(in.Tools)
	prompt := in.UserMessage
	if len(names) > 0 {
		prompt = prompt + "\n\n" + draftToolInstruction(names)
	}

	events, err := thread.RunStreamed(runCtx, []codex.Input{codex.TextInput(prompt)}, nil)
	if err != nil {
		return RunOutput{}, fmt.Errorf("running codex turn for %s: %w", key, err)
	}

	var out RunOutput
	var content assistantText
	defer events.Close()
	for {
		ev, err := events.Next(runCtx)
		if err != nil {
			return out, fmt.Errorf("reading codex event for %s: %w", key, err)
		}
		done, err := processCodexSDKRunEvent(ev, names, &content, &out, onDelta, in.OnToolCall, in.OnToolResult)
		if err != nil {
			return out, err
		}
		if done {
			return out, nil
		}
	}
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
	var configOverrides []string
	if gitDir, err := worktreeGitDir(runCtx, in.Workspace); err == nil {
		configOverrides = append(configOverrides, "sandbox_workspace_write.writable_roots=["+tomlQuote(gitDir)+"]")
	} else {
		return ExecuteOutput{}, fmt.Errorf("resolving worktree git-dir for execution %s: %w", key, err)
	}

	client, err := newCodexClient(runCtx, configOverrides)
	if err != nil {
		return ExecuteOutput{}, fmt.Errorf("creating codex client for execution %s: %w", key, err)
	}
	defer func() { _ = client.Close() }()

	thread, err := client.StartThread(runCtx, codex.ThreadStartOptions{
		Cwd:                   in.Workspace,
		Model:                 in.Model,
		SandboxPolicy:         codex.SandboxModeWorkspaceWrite,
		ApprovalPolicy:        codex.ApprovalPolicyNever,
		DeveloperInstructions: in.SystemPrompt,
	})
	if err != nil {
		return ExecuteOutput{}, fmt.Errorf("starting codex execution thread %s: %w", key, err)
	}

	events, err := thread.RunStreamed(runCtx, []codex.Input{codex.TextInput(codexKickoffMessage)}, nil)
	if err != nil {
		return ExecuteOutput{}, fmt.Errorf("starting codex execution: %w", err)
	}

	var out ExecuteOutput
	var content assistantText
	defer events.Close()
	for {
		ev, err := events.Next(runCtx)
		if err != nil {
			out.DurationSeconds = time.Since(start).Seconds()
			return out, fmt.Errorf("reading codex execution event: %w", err)
		}
		done, err := processCodexSDKExecuteEvent(ev, &content, &out, onEvent)
		if err != nil {
			out.DurationSeconds = time.Since(start).Seconds()
			return out, err
		}
		if done {
			out.DurationSeconds = time.Since(start).Seconds()
			return out, nil
		}
	}
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

	client, err := newCodexClient(ctx, nil)
	if err != nil {
		return fmt.Errorf("creating codex client: %w", err)
	}
	defer func() { _ = client.Close() }()

	serverConfig := map[string]any{"command": r.draftMCPPath}
	if r.knowledgeRoot != "" {
		serverConfig["args"] = []string{"--knowledge-root", r.knowledgeRoot}
	}
	edits := []protocol.ConfigEdit{
		{
			KeyPath:       "mcp_servers." + codexDraftServerName,
			MergeStrategy: protocol.MergeStrategyUpsert,
			Value:         serverConfig,
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
		edits = append(edits, protocol.ConfigEdit{
			KeyPath:       fmt.Sprintf("mcp_servers.%s.tools.%s.approval_mode", codexDraftServerName, name),
			MergeStrategy: protocol.MergeStrategyUpsert,
			Value:         "approve",
		})
	}
	if _, err := client.Client().ConfigBatchWrite(ctx, protocol.ConfigBatchWriteParams{Edits: edits}); err != nil {
		return fmt.Errorf("writing draft MCP server config: %w", err)
	}
	return nil
}

// newCodexClient starts the current Codex app-server protocol client. The
// generated SDK deliberately validates the CLI version; the SDK is currently
// one minor behind the installed CLI, so warn rather than rejecting a known
// newer compatible binary. Use the default logger so this compatibility
// warning remains observable in the server log.
func newCodexClient(ctx context.Context, configOverrides []string) (*codex.Codex, error) {
	return codex.New(ctx, codex.Options{
		CompatibilityPolicy: codex.Warn,
		Logger:              slog.Default(),
		Spawn:               codex.SpawnOptions{ConfigOverrides: configOverrides},
	})
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

// processCodexSDKRunEvent adapts an app-server notification into the stable
// AgentRunner contract. App-server carries completed items as JSON unions, so
// this deliberately reads only the fields shared by the current item shapes.
func processCodexSDKRunEvent(ev rpc.Notification, toolNames []string, content *assistantText, out *RunOutput, onDelta func(chat.Delta) error, onToolCall func(id, name, argsJSON string), onToolResult func(id, name, result string, isError bool)) (bool, error) {
	if ev.Method == "item/agentMessage/delta" {
		var payload protocol.AgentMessageDeltaNotification
		if err := ev.UnmarshalParams(&payload); err != nil {
			return true, fmt.Errorf("decoding codex message delta: %w", err)
		}
		return false, content.appendDelta(payload.Delta, onDelta)
	}
	if text, ok, err := completedAgentMessageText(ev); err != nil {
		return true, err
	} else if ok && content.String() == "" {
		return false, content.appendDelta(text, onDelta)
	}
	return processCodexSDKEvent(ev, content, &out.Content, func(item codexSDKItem) error {
		if item.Type == "mcpToolCall" && slices.Contains(toolNames, item.Tool) {
			if out.ToolCall == nil && item.Status != "failed" {
				out.ToolCall = &chat.ToolCall{ID: item.ID, Type: "function", Function: chat.ToolCallFunction{Name: item.Tool, Arguments: jsonValue(item.Arguments)}}
			}
			return nil
		}
		return forwardCodexSDKTool(item, onToolCall, onToolResult)
	}, "codex agent run")
}

// processCodexSDKExecuteEvent adapts app-server notifications for an
// autonomous execution, including every completed tool action.
func processCodexSDKExecuteEvent(ev rpc.Notification, content *assistantText, out *ExecuteOutput, onEvent func(ExecuteEvent) error) (bool, error) {
	if ev.Method == "item/agentMessage/delta" {
		var payload protocol.AgentMessageDeltaNotification
		if err := ev.UnmarshalParams(&payload); err != nil {
			return true, fmt.Errorf("decoding codex execution delta: %w", err)
		}
		return false, content.appendExecuteText(payload.Delta, onEvent)
	}
	if text, ok, err := completedAgentMessageText(ev); err != nil {
		return true, err
	} else if ok && content.String() == "" {
		return false, content.appendExecuteText(text, onEvent)
	}
	if ev.Method == "thread/tokenUsage/updated" {
		var payload protocol.ThreadTokenUsageUpdatedNotification
		if err := ev.UnmarshalParams(&payload); err != nil {
			return true, fmt.Errorf("decoding codex token usage: %w", err)
		}
		out.TokensUsed = payload.TokenUsage.Total.TotalTokens
		return false, nil
	}
	done, err := processCodexSDKEvent(ev, content, &out.Content, func(item codexSDKItem) error {
		if onEvent == nil {
			return nil
		}
		var eventErr error
		if err := forwardCodexSDKTool(item,
			func(id, name, input string) {
				eventErr = onEvent(ExecuteEvent{Kind: "tool_call", ID: id, ToolName: name, ToolInput: input})
			},
			func(id, _ string, result string, isError bool) {
				if eventErr == nil {
					eventErr = onEvent(ExecuteEvent{Kind: "tool_result", ID: id, ToolResult: result, IsError: isError})
				}
			},
		); err != nil {
			return err
		}
		return eventErr
	}, "codex execution")
	if done && err == nil {
		out.NumTurns = 1
	}
	return done, err
}

type codexSDKItem struct {
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	Text             string          `json:"text"`
	Command          string          `json:"command"`
	AggregatedOutput string          `json:"aggregatedOutput"`
	Output           string          `json:"output"`
	Status           string          `json:"status"`
	Changes          json.RawMessage `json:"changes"`
	Tool             string          `json:"tool"`
	Arguments        json.RawMessage `json:"arguments"`
	Result           json.RawMessage `json:"result"`
	Error            json.RawMessage `json:"error"`
}

func processCodexSDKEvent(ev rpc.Notification, content *assistantText, output *string, onItem func(codexSDKItem) error, failurePrefix string) (bool, error) {
	switch ev.Method {
	case "item/started":
		var payload protocol.ItemCompletedNotification
		if err := ev.UnmarshalParams(&payload); err != nil {
			return true, fmt.Errorf("decoding codex item start: %w", err)
		}
		var item codexSDKItem
		if err := json.Unmarshal(payload.Item, &item); err != nil {
			return true, fmt.Errorf("decoding codex item: %w", err)
		}
		if item.Type == "agentMessage" {
			content.startNewRound()
		}
	case "item/completed":
		var payload protocol.ItemCompletedNotification
		if err := ev.UnmarshalParams(&payload); err != nil {
			return true, fmt.Errorf("decoding completed codex item: %w", err)
		}
		var item codexSDKItem
		if err := json.Unmarshal(payload.Item, &item); err != nil {
			return true, fmt.Errorf("decoding completed codex item: %w", err)
		}
		if err := onItem(item); err != nil {
			return true, err
		}
	case "turn/failed":
		return true, turnNotificationError(ev, failurePrefix)
	case "error":
		var payload protocol.ErrorNotification
		if err := ev.UnmarshalParams(&payload); err != nil {
			return true, fmt.Errorf("decoding codex error notification: %w", err)
		}
		if payload.WillRetry != nil && *payload.WillRetry {
			return false, nil
		}
		if payload.Error != nil && payload.Error.Message != "" {
			return true, fmt.Errorf("%s failed: %s", failurePrefix, payload.Error.Message)
		}
		return true, fmt.Errorf("%s failed", failurePrefix)
	case "turn/completed":
		var payload protocol.TurnCompletedNotification
		if err := ev.UnmarshalParams(&payload); err != nil {
			return true, fmt.Errorf("decoding codex turn completion: %w", err)
		}
		*output = content.String()
		if payload.Turn != nil && payload.Turn.Status == "failed" {
			if payload.Turn.Error != nil && payload.Turn.Error.Message != "" {
				return true, fmt.Errorf("%s failed: %s", failurePrefix, payload.Turn.Error.Message)
			}
			return true, fmt.Errorf("%s failed", failurePrefix)
		}
		return true, nil
	}
	return false, nil
}

func completedAgentMessageText(ev rpc.Notification) (string, bool, error) {
	if ev.Method != "item/completed" {
		return "", false, nil
	}
	var payload protocol.ItemCompletedNotification
	if err := ev.UnmarshalParams(&payload); err != nil {
		return "", false, fmt.Errorf("decoding completed codex item: %w", err)
	}
	var item codexSDKItem
	if err := json.Unmarshal(payload.Item, &item); err != nil {
		return "", false, fmt.Errorf("decoding completed codex item: %w", err)
	}
	return item.Text, item.Type == "agentMessage" && item.Text != "", nil
}

func turnNotificationError(ev rpc.Notification, failurePrefix string) error {
	var payload protocol.TurnCompletedNotification
	if err := ev.UnmarshalParams(&payload); err != nil {
		return fmt.Errorf("decoding failed codex turn: %w", err)
	}
	if payload.Turn != nil && payload.Turn.Error != nil && payload.Turn.Error.Message != "" {
		return fmt.Errorf("%s failed: %s", failurePrefix, payload.Turn.Error.Message)
	}
	return fmt.Errorf("%s failed", failurePrefix)
}

func forwardCodexSDKTool(item codexSDKItem, onToolCall func(id, name, argsJSON string), onToolResult func(id, name, result string, isError bool)) error {
	var name, input, result string
	switch item.Type {
	case "commandExecution":
		name, input, result = "Bash", item.Command, item.AggregatedOutput
		if result == "" {
			result = item.Output
		}
	case "fileChange":
		name, input, result = "FileChange", jsonValue(item.Changes), item.Status
	case "mcpToolCall":
		name, input, result = item.Tool, jsonValue(item.Arguments), jsonValue(item.Result)
	default:
		return nil
	}
	if onToolCall != nil {
		onToolCall(item.ID, name, input)
	}
	if onToolResult != nil {
		isError := item.Status == "failed" || item.Status == "denied"
		if isError && len(item.Error) > 0 {
			result = jsonValue(item.Error)
		}
		onToolResult(item.ID, name, result, isError)
	}
	return nil
}

func jsonValue(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	return string(raw)
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
// codexItemToolCallID returns item's own id — a real, stable per-item id on
// every ThreadItem variant this codebase reads (*types.CommandExecution,
// *types.FileChange, *types.MCPToolCall), used identically by
// processCodexRunEvent and processCodexExecuteEvent so both derive a tool
// call's correlation id the same way rather than two independently-written
// copies. Codex resolves a call and its result together, synchronously, for
// the same completed item (both call sites below), so correctness never
// actually depends on this id — but every RunInput.OnToolCall/OnToolResult
// caller shares one signature across all three runners, so it still needs
// one to pass.
func codexItemToolCallID(item types.ThreadItem) string {
	switch it := item.(type) {
	case *types.CommandExecution:
		return it.ID
	case *types.FileChange:
		return it.ID
	case *types.MCPToolCall:
		return it.ID
	default:
		return ""
	}
}

func processCodexRunEvent(ev types.ThreadEvent, toolNames []string, content *assistantText, out *RunOutput, onDelta func(chat.Delta) error, onToolCall func(id, name, argsJSON string), onToolResult func(id, name, result string, isError bool)) (done bool, err error) {
	switch e := ev.(type) {
	case *types.ItemStarted:
		// ItemStarted for a new AgentMessage item is codex's equivalent of
		// claude_runner.go's message_start StreamEvent — the live-stream
		// signal that a new round's remarks are about to begin, arriving
		// before that item's own AgentMessageDelta chunks (see
		// assistantText.startNewRound).
		if _, ok := e.Item.(*types.AgentMessage); ok {
			content.startNewRound()
		}
	case *types.ItemUpdated:
		if delta, ok := e.Delta.(*types.AgentMessageDelta); ok {
			if err := content.appendDelta(delta.TextChunk, onDelta); err != nil {
				return true, err
			}
		}
	case *types.ItemCompleted:
		// AgentMessage's text is not handled here — it already accumulated
		// live via ItemUpdated/AgentMessageDelta (assistantText.appendDelta
		// above), which is both the complete and authoritative source (see
		// assistantText's doc comment).
		switch item := e.Item.(type) {
		case *types.CommandExecution:
			id := codexItemToolCallID(item)
			if onToolCall != nil {
				onToolCall(id, "Bash", item.Command)
			}
			if onToolResult != nil {
				isErr := item.Status == "failed" || item.Status == "denied"
				onToolResult(id, "Bash", item.AggregatedOutput, isErr)
			}
		case *types.FileChange:
			id := codexItemToolCallID(item)
			if onToolCall != nil {
				input, marshalErr := json.Marshal(item.Changes)
				if marshalErr != nil {
					return true, fmt.Errorf("encoding file change input: %w", marshalErr)
				}
				onToolCall(id, "FileChange", string(input))
			}
			if onToolResult != nil {
				onToolResult(id, "FileChange", item.Status, item.Status == "failed")
			}
		case *types.MCPToolCall:
			isDraft := slices.Contains(toolNames, item.ToolName)
			if isDraft {
				// handleDraftToolCall (draftmcp) may have rejected this
				// proposal (isError: true, surfaced here as item.Status ==
				// "failed") — a rejected call is neither trusted as
				// out.ToolCall nor genuine Tool Activity; drop it so a
				// later, valid retry can still be captured (mirrors
				// claude_runner.go's processMessage).
				if out.ToolCall == nil && item.Status != "failed" {
					out.ToolCall = &chat.ToolCall{
						ID:   item.ID,
						Type: "function",
						Function: chat.ToolCallFunction{
							Name:      item.ToolName,
							Arguments: string(item.Input),
						},
					}
				}
				break
			}
			// Not the Draft — genuine intermediate tool activity (e.g. a
			// knowledge-query call).
			id := codexItemToolCallID(item)
			if onToolCall != nil {
				onToolCall(id, item.ToolName, string(item.Input))
			}
			if onToolResult != nil {
				isErr := item.Status == "failed"
				resultText := string(item.Result)
				if isErr {
					resultText = item.ErrorText()
				}
				onToolResult(id, item.ToolName, resultText, isErr)
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
func processCodexExecuteEvent(ev types.ThreadEvent, content *assistantText, out *ExecuteOutput, onEvent func(ExecuteEvent) error) (done bool, err error) {
	switch e := ev.(type) {
	case *types.ItemStarted:
		// See processCodexRunEvent's identical comment: this is codex's
		// message_start equivalent, arriving before the item's own
		// AgentMessageDelta chunks.
		if _, ok := e.Item.(*types.AgentMessage); ok {
			content.startNewRound()
		}
	case *types.ItemUpdated:
		if delta, ok := e.Delta.(*types.AgentMessageDelta); ok {
			if err := content.appendExecuteText(delta.TextChunk, onEvent); err != nil {
				return true, err
			}
		}
	case *types.ItemCompleted:
		// AgentMessage's text is not handled here — see
		// processCodexRunEvent's identical comment; it already accumulated
		// live via ItemUpdated/AgentMessageDelta above.
		if onEvent == nil {
			break
		}
		switch item := e.Item.(type) {
		case *types.CommandExecution:
			id := codexItemToolCallID(item)
			if err := onEvent(ExecuteEvent{Kind: "tool_call", ID: id, ToolName: "Bash", ToolInput: item.Command}); err != nil {
				return true, err
			}
			isErr := item.Status == "failed" || item.Status == "denied"
			if err := onEvent(ExecuteEvent{Kind: "tool_result", ID: id, ToolResult: item.AggregatedOutput, IsError: isErr}); err != nil {
				return true, err
			}
		case *types.FileChange:
			id := codexItemToolCallID(item)
			input, marshalErr := json.Marshal(item.Changes)
			if marshalErr != nil {
				return true, fmt.Errorf("encoding file change input: %w", marshalErr)
			}
			if err := onEvent(ExecuteEvent{Kind: "tool_call", ID: id, ToolName: "FileChange", ToolInput: string(input)}); err != nil {
				return true, err
			}
			isErr := item.Status == "failed"
			if err := onEvent(ExecuteEvent{Kind: "tool_result", ID: id, ToolResult: item.Status, IsError: isErr}); err != nil {
				return true, err
			}
		case *types.MCPToolCall:
			id := codexItemToolCallID(item)
			if err := onEvent(ExecuteEvent{Kind: "tool_call", ID: id, ToolName: item.ToolName, ToolInput: string(item.Input)}); err != nil {
				return true, err
			}
			isErr := item.Status == "failed"
			resultText := string(item.Result)
			if isErr {
				resultText = item.ErrorText()
			}
			if err := onEvent(ExecuteEvent{Kind: "tool_result", ID: id, ToolResult: resultText, IsError: isErr}); err != nil {
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
