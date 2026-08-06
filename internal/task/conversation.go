package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/timmersuk/llm-workbench/internal/yamlutil"
)

// ErrInvalidStage is returned when a stage name isn't one of the stages
// that has a Conversation ("requirements", "planning", "review") —
// implementation/complete don't have an interview mechanism (see CONTEXT.md's
// "Conversation" entry). Review's conversation (Milestone 6) persists the
// same way as the others, in conversation-review.yaml.
var ErrInvalidStage = errors.New("invalid conversation stage")

// ConversationToolCall records a Draft-proposing tool call an assistant
// message made, so revisiting a stage lets the model see its own prior
// proposal in history. Reasoning content is deliberately never persisted
// here (see docs/architectural invariants.md: "Store durable semantics...
// not transient internal reasoning") — only the final content/tool call.
type ConversationToolCall struct {
	ID        string `yaml:"id" json:"id"`
	Name      string `yaml:"name" json:"name"`
	Arguments string `yaml:"arguments" json:"arguments"` // raw JSON string of the proposed Draft's fields
}

// MarshalYAML forces Arguments through yamlutil.Quoted — Arguments is a
// raw JSON string of the proposed Draft's fields (e.g. a long "detail"
// field), which can contain characters goccy's own default plain-style
// heuristic doesn't safely handle (see yamlutil.Quoted's doc comment).
// ID/Name are short, controlled-vocabulary values with no such risk, left
// as plain strings.
func (c ConversationToolCall) MarshalYAML() (interface{}, error) {
	return struct {
		ID        string          `yaml:"id"`
		Name      string          `yaml:"name"`
		Arguments yamlutil.Quoted `yaml:"arguments"`
	}{c.ID, c.Name, yamlutil.Quoted(c.Arguments)}, nil
}

// ConversationToolActivity records one intermediate tool call and its
// result an agent made while producing a Conversation turn (CONTEXT.md's
// "Tool Activity") — distinct from ConversationToolCall, which is the
// single proposal-ending Draft call a turn may end with, never itself
// activity. Arguments/Result are truncated at maxPersistedToolActivityBytes
// (docs/adr/0018) — smaller than the live/model-facing cap
// (internal/toolloop/tool.go's maxToolResultBytes), since this is for a
// human glancing at "what did it do" in a reopened conversation, not for
// feeding a model's context window.
type ConversationToolActivity struct {
	Name      string `yaml:"name" json:"name"`
	Arguments string `yaml:"arguments,omitempty" json:"arguments,omitempty"`
	Result    string `yaml:"result,omitempty" json:"result,omitempty"`
	IsError   bool   `yaml:"is_error,omitempty" json:"is_error,omitempty"`
}

// MarshalYAML forces Arguments/Result through yamlutil.Quoted — raw tool
// output (git diff --stat, test runner summaries, ls -l, ...) can contain
// characters goccy's own default plain-style heuristic doesn't safely
// handle (see yamlutil.Quoted's doc comment; a literal tab, exactly what
// `go test`'s own summary lines contain, silently corrupts on round-trip
// otherwise). Name is left as a plain string since it's always a short,
// controlled tool identifier.
func (a ConversationToolActivity) MarshalYAML() (interface{}, error) {
	return struct {
		Name      string          `yaml:"name"`
		Arguments yamlutil.Quoted `yaml:"arguments,omitempty"`
		Result    yamlutil.Quoted `yaml:"result,omitempty"`
		IsError   bool            `yaml:"is_error,omitempty"`
	}{a.Name, yamlutil.Quoted(a.Arguments), yamlutil.Quoted(a.Result), a.IsError}, nil
}

// ConversationSegmentKind distinguishes ConversationSegment's two shapes.
type ConversationSegmentKind string

const (
	SegmentKindText  ConversationSegmentKind = "text"
	SegmentKindTools ConversationSegmentKind = "tools"
)

// ConversationSegment is one piece of an assistant turn's real
// chronological sequence: either a run of narration text or one run of
// consecutive tool calls (a "sequence" in CONTEXT.md's Tool Activity
// sense) — never both. A turn that narrates between tool calls (Review's
// automated-checks pass narrating "build passes, now testing" between
// `go build` and `go test`, say) produces several of these in the real
// order they happened; Content/ToolActivity on ConversationMessage flatten
// that same turn into one string plus one list, which cannot represent
// which narration came before which call (see docs/adr/0023). Segments is
// the one place real order is recorded; Content/ToolActivity remain
// derived from it (or, for a message persisted before this field existed,
// synthesized in the opposite direction — see ConversationMessage.EffectiveSegments).
type ConversationSegment struct {
	Kind ConversationSegmentKind `yaml:"kind" json:"kind"`
	// Text holds this segment's narration when Kind == SegmentKindText;
	// empty otherwise.
	Text string `yaml:"text,omitempty" json:"text,omitempty"`
	// ToolActivity holds this segment's maximal run of consecutive calls
	// when Kind == SegmentKindTools; empty otherwise. Each entry already
	// carries its own MarshalYAML (Arguments/Result quoting), applied
	// automatically since it's passed through unchanged here.
	ToolActivity []ConversationToolActivity `yaml:"tool_activity,omitempty" json:"tool_activity,omitempty"`
}

// MarshalYAML forces Text through yamlutil.Quoted, the same treatment
// ConversationMessage.Content gets and for the same reason: it's ordinary
// LLM prose, and goccy's default plain-style heuristic doesn't safely
// handle every character that can appear in it.
func (s ConversationSegment) MarshalYAML() (interface{}, error) {
	return struct {
		Kind         ConversationSegmentKind    `yaml:"kind"`
		Text         yamlutil.Quoted            `yaml:"text,omitempty"`
		ToolActivity []ConversationToolActivity `yaml:"tool_activity,omitempty"`
	}{s.Kind, yamlutil.Quoted(s.Text), s.ToolActivity}, nil
}

// maxPersistedToolActivityBytes caps each persisted
// ConversationToolActivity.Arguments/Result string. Deliberately smaller
// than the 16KB a tool result is capped at before a model ever sees it
// (internal/toolloop/tool.go's maxToolResultBytes, sized for a small local
// model's context window) — 16KB times many calls times many turns is
// exactly the unbounded conversation-{stage}.yaml rewrite cost this ADR
// avoids; 2KB is plenty for a human recognizing what a past turn did.
const maxPersistedToolActivityBytes = 2 * 1024

// TruncateForPersistence caps s at maxPersistedToolActivityBytes, using the
// same "[truncated: ...]" marker convention internal/toolloop/tool.go's
// truncateResult uses for its own (larger, model-facing) cap. Exported so
// internal/api/stage_conversation.go can apply it when building a turn's
// ConversationToolActivity list.
func TruncateForPersistence(s string) string {
	if len(s) > maxPersistedToolActivityBytes {
		return s[:maxPersistedToolActivityBytes] + "\n[truncated: exceeded the persisted size limit]"
	}
	return s
}

// ConversationMessage is one message in a stage's persisted, append-only
// history.
type ConversationMessage struct {
	Role     string `yaml:"role" json:"role"` // "user" | "assistant" | "tool"
	Content  string `yaml:"content" json:"content"`
	// ToolCall is ToolCalls[0] when the turn proposed at least one Draft,
	// nil otherwise — kept for every reader that only ever expects one
	// Draft-proposing tool per turn (Requirements/Planning/TaskDraft each
	// offer exactly one, plus ask_question). A turn that proposes more
	// than one Draft in the same turn (Review offers propose_review and
	// propose_knowledge at once, stageTool in
	// internal/api/stage_conversation.go) used to lose every call after
	// this one — see ToolCalls.
	ToolCall *ConversationToolCall `yaml:"tool_call,omitempty" json:"tool_call,omitempty"`
	// ToolCalls carries every Draft-proposing tool call this turn made, in
	// the order they were resolved — ToolCall above stays just the first,
	// unchanged, so this is purely additive. Excluded from the default
	// JSON marshaling (like Segments) since a message persisted before
	// this field existed has it empty even though ToolCall is set;
	// EffectiveToolCalls is how a caller gets an always-populated view
	// regardless of which case this is. Marshaled to YAML as a plain list
	// (its own MarshalYAML on ConversationToolCall applies automatically).
	ToolCalls  []ConversationToolCall `yaml:"tool_calls,omitempty" json:"-"`
	ToolCallID string                 `yaml:"tool_call_id,omitempty" json:"tool_call_id,omitempty"`
	// ToolActivity is the ordered list of intermediate tool calls/results
	// (CONTEXT.md's "Tool Activity") the agent made while producing this
	// turn, bundled onto the assistant message that closes it out rather
	// than as separate Conversation entries (docs/adr/0018's rejected
	// alternatives). Never set on a "user" message.
	ToolActivity []ConversationToolActivity `yaml:"tool_activity,omitempty" json:"tool_activity,omitempty"`
	// Segments is this turn's real chronological sequence of narration and
	// tool-call runs — see ConversationSegment's doc comment. Nil for a
	// "user" message, and nil for any assistant message persisted before
	// this field existed; EffectiveSegments is how a caller gets a always-
	// populated view regardless of which case this is.
	Segments []ConversationSegment `yaml:"segments,omitempty" json:"-"`
	// Error records why this turn failed, if it did — an assistant message
	// with empty Content and no Error means the agent genuinely said
	// nothing; empty Content with Error set means the turn errored out
	// before (or without) producing a reply. Never set on a "user" message.
	Error     string    `yaml:"error,omitempty" json:"error,omitempty"`
	Executor  string    `yaml:"executor,omitempty" json:"executor,omitempty"`
	Model     string    `yaml:"model,omitempty" json:"model,omitempty"`
	Effort    string    `yaml:"effort,omitempty" json:"effort,omitempty"`
	CreatedAt time.Time `yaml:"created_at" json:"created_at"`
}

// MarshalYAML forces Content/Error through yamlutil.Quoted. Unlike
// ConversationToolActivity's tool output, Content isn't a rare edge case:
// it's ordinary LLM prose, and goccy's default plain-style heuristic
// doesn't safely handle every character that can appear in it (see
// yamlutil.Quoted's doc comment). Role/ToolCallID/CreatedAt are short,
// controlled-vocabulary values with no such risk, left as plain
// types; ToolCall/ToolActivity/Segments carry their own MarshalYAML and
// are applied automatically since they're passed through unchanged.
// Segments is persisted exactly as recorded (nil stays nil) — never
// synthesized here; EffectiveSegments' synthesis is wire-only (see
// MarshalJSON), so a legacy file on disk is never rewritten to pretend it
// always had real ordering.
func (m ConversationMessage) MarshalYAML() (interface{}, error) {
	return struct {
		Role         string                     `yaml:"role"`
		Content      yamlutil.Quoted            `yaml:"content"`
		ToolCall     *ConversationToolCall      `yaml:"tool_call,omitempty"`
		ToolCalls    []ConversationToolCall     `yaml:"tool_calls,omitempty"`
		ToolCallID   string                     `yaml:"tool_call_id,omitempty"`
		ToolActivity []ConversationToolActivity `yaml:"tool_activity,omitempty"`
		Segments     []ConversationSegment      `yaml:"segments,omitempty"`
		Error        yamlutil.Quoted            `yaml:"error,omitempty"`
		Executor     string                     `yaml:"executor,omitempty"`
		Model        string                     `yaml:"model,omitempty"`
		Effort       string                     `yaml:"effort,omitempty"`
		CreatedAt    time.Time                  `yaml:"created_at"`
	}{m.Role, yamlutil.Quoted(m.Content), m.ToolCall, m.ToolCalls, m.ToolCallID, m.ToolActivity, m.Segments, yamlutil.Quoted(m.Error), m.Executor, m.Model, m.Effort, m.CreatedAt}, nil
}

// EffectiveToolCalls returns m.ToolCalls when the turn was recorded with
// the full list, or — for a message persisted before ToolCalls existed, or
// built by an older code path that only ever set the singular ToolCall —
// wraps m.ToolCall as a one-element slice. Returns nil when neither is set.
// This is the one place a caller should look to see every Draft a turn
// proposed; m.ToolCall/m.ToolCalls themselves stay as recorded, exactly
// like Segments/EffectiveSegments above.
func (m ConversationMessage) EffectiveToolCalls() []ConversationToolCall {
	if len(m.ToolCalls) > 0 {
		return m.ToolCalls
	}
	if m.ToolCall != nil {
		return []ConversationToolCall{*m.ToolCall}
	}
	return nil
}

// EffectiveSegments returns m.Segments when the turn was recorded with
// real ordering, or — for a message persisted before Segments existed —
// synthesizes a best-effort [tools, text] pair from the legacy flattened
// Content/ToolActivity fields, omitting either piece when empty. This
// reproduces today's bundled rendering (every tool call, then all the
// text) for old data; real order is not recoverable retroactively, since
// it was never captured for a message recorded before this field existed.
func (m ConversationMessage) EffectiveSegments() []ConversationSegment {
	if len(m.Segments) > 0 {
		return m.Segments
	}
	var segments []ConversationSegment
	if len(m.ToolActivity) > 0 {
		segments = append(segments, ConversationSegment{Kind: SegmentKindTools, ToolActivity: m.ToolActivity})
	}
	if m.Content != "" {
		segments = append(segments, ConversationSegment{Kind: SegmentKindText, Text: m.Content})
	}
	return segments
}

// conversationMessageAlias exists only so ConversationMessage.MarshalJSON
// can re-marshal every field through encoding/json's normal struct-tag
// behavior without recursing back into itself.
type conversationMessageAlias ConversationMessage

// MarshalJSON overrides Segments (tagged json:"-" on the real field, since
// the wire value isn't always the persisted value) with EffectiveSegments()
// — so every API response carries a populated segments array regardless of
// whether the underlying message predates this field, without ever
// rewriting the actual persisted YAML (see MarshalYAML's doc comment).
func (m ConversationMessage) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		conversationMessageAlias
		Segments  []ConversationSegment  `json:"segments,omitempty"`
		ToolCalls []ConversationToolCall `json:"tool_calls,omitempty"`
	}{conversationMessageAlias(m), m.EffectiveSegments(), m.EffectiveToolCalls()})
}

// Conversation is one stage's full, append-only message history, stored as
// <task root>/<id>/conversation-<stage>.yaml — one file per stage
// (requirements, planning, review), not one file for the task's whole life,
// and not a fresh file per visit: revisiting a stage (via Revise,
// lifecycle.go) resumes appending to the same Conversation.
type Conversation struct {
	Stage    string                `yaml:"stage" json:"stage"`
	Messages []ConversationMessage `yaml:"messages" json:"messages"`
}

func validateConversationStage(stage string) error {
	if stage != StageRequirements && stage != StagePlanning && stage != StageReview {
		return fmt.Errorf("%w: %q", ErrInvalidStage, stage)
	}
	return nil
}

func conversationPath(dir, stage string) string {
	return filepath.Join(dir, "conversation-"+stage+".yaml")
}

// GetConversation returns the stage's message history for id. Unlike
// GetContext/GetPlan (where a missing file means "not finalized yet" and
// is a meaningful 404), a missing conversation file just means "no
// messages yet" — a normal starting state for any task — so this returns
// an empty Conversation rather than an error when the file doesn't exist.
func (s *FileStore) GetConversation(projectID, id, stage string) (Conversation, error) {
	if err := validateID(projectID); err != nil {
		return Conversation{}, err
	}
	if err := validateID(id); err != nil {
		return Conversation{}, err
	}
	if err := validateConversationStage(stage); err != nil {
		return Conversation{}, err
	}

	path := conversationPath(s.taskDir(projectID, id), stage)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Conversation{Stage: stage}, nil
	}
	if err != nil {
		return Conversation{}, fmt.Errorf("reading %s: %w", path, err)
	}

	var c Conversation
	if err := yamlutil.Unmarshal(data, &c); err != nil {
		return Conversation{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return c, nil
}

// AppendConversationMessages appends msgs to the stage's Conversation for
// id, stamping each message's CreatedAt server-side (ignoring any
// client-supplied value, same "server always wins" treatment
// Task.CreatedAt/UpdatedAt get). "Append-only" here is an enforced policy —
// this is the only method that adds messages, and it never edits or
// removes prior ones.
//
// Writes via a raw O_APPEND, one message at a time, the same
// crash-safety shape as AppendExecutionLogEvent — never a read-modify-
// rewrite of the whole file: a crash mid-write can only ever tear the
// newest message being appended, never touch anything written by an
// earlier call, and gitstore's commit-time validation (repairTornYAML)
// repairs exactly that shape. Creating the file on its very first message
// (CreateExecutionLog's equivalent step) is folded in here rather than a
// separate call, since — unlike an execution, which always has an
// explicit start — a Conversation's first message *is* its start.
//
// Returns the full up-to-date Conversation by reading it back after
// writing, matching GetConversation's shape; the append itself never
// reads anything first.
func (s *FileStore) AppendConversationMessages(projectID, id, stage string, msgs ...ConversationMessage) (Conversation, error) {
	if err := validateID(projectID); err != nil {
		return Conversation{}, err
	}
	if err := validateID(id); err != nil {
		return Conversation{}, err
	}
	if err := validateConversationStage(stage); err != nil {
		return Conversation{}, err
	}
	if len(msgs) == 0 {
		return s.GetConversation(projectID, id, stage)
	}

	dir := s.taskDir(projectID, id)
	path := conversationPath(dir, stage)

	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Conversation{}, fmt.Errorf("creating task directory %s: %w", dir, err)
		}
		header, err := yamlutil.Marshal(struct {
			Stage string `yaml:"stage"`
		}{stage})
		if err != nil {
			return Conversation{}, fmt.Errorf("encoding conversation header for %s/%s: %w", id, stage, err)
		}
		if err := os.WriteFile(path, append(header, []byte("messages:\n")...), 0o644); err != nil {
			return Conversation{}, fmt.Errorf("writing %s: %w", path, err)
		}
	} else if statErr != nil {
		return Conversation{}, fmt.Errorf("checking existing conversation %s: %w", path, statErr)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return Conversation{}, fmt.Errorf("opening conversation %s: %w", path, err)
	}
	defer f.Close()

	now := time.Now().UTC()
	for _, m := range msgs {
		m.CreatedAt = now
		// Originally required to work around a gopkg.in/yaml.v3 bug (fixed
		// by the migration to yamlutil/goccy): a block-scalar string whose
		// first character was a newline marshaled fine but errored on
		// Unmarshal ("did not find expected key"), and raw LLM output
		// commonly starts with a blank line. Kept now as deliberate
		// hygiene, not correctness (task/sanitize.go's trimmedList etc.
		// carry the same story for Draft fields).
		m.Content = strings.TrimSpace(m.Content)

		// yamlutil.Marshal's yaml.IndentSequence(true) option already
		// indents this bare top-level []ConversationMessage{m}'s "- role:"
		// list marker by one level (4 spaces) on its own — the exact depth
		// a sibling of this file's `messages:` key needs. No further
		// indentBlock shift is applied: doing so on top would double it to
		// 8 spaces, which (once entries at both depths exist in the same
		// file, e.g. across a process restart with a different marshal
		// baseline) breaks YAML's sibling-indentation requirement — a
		// deeper-indented entry parses as nested content of the previous
		// one instead of a new messages: list item, and GetConversation
		// silently stops returning anything past that point. See
		// TestFileStore_AppendConversationMessages_ListItemsIndentFourSpaces.
		data, err := yamlutil.Marshal([]ConversationMessage{m})
		if err != nil {
			return Conversation{}, fmt.Errorf("encoding conversation message for %s/%s: %w", id, stage, err)
		}
		if _, err := f.Write(data); err != nil {
			return Conversation{}, fmt.Errorf("appending to conversation %s: %w", path, err)
		}
	}

	return s.GetConversation(projectID, id, stage)
}

// ReplaceConversationMessages overwrites the stage's Conversation for id
// with exactly msgs, rewriting the file. Unlike AppendConversationMessages,
// this is a plain "set the list to this" — no stamping or trimming — since
// callers here (message delete/edit/regenerate) are already working from a
// mix of kept messages (their real CreatedAt/Content untouched) and at most
// one freshly-produced message they're responsible for finishing
// themselves. This is how a human-directed correction (not a new exchange)
// gets to rewrite already-persisted messages, a deliberate departure from
// AppendConversationMessages' append-only policy.
func (s *FileStore) ReplaceConversationMessages(projectID, id, stage string, msgs []ConversationMessage) (Conversation, error) {
	if err := validateID(projectID); err != nil {
		return Conversation{}, err
	}
	if err := validateID(id); err != nil {
		return Conversation{}, err
	}
	if err := validateConversationStage(stage); err != nil {
		return Conversation{}, err
	}

	replaced := Conversation{Stage: stage, Messages: msgs}
	return replaced, writeConversation(s.taskDir(projectID, id), stage, replaced)
}

// writeConversation marshals and writes conv to dir/stage's conversation
// file, creating the task directory if needed — the shared tail of both
// AppendConversationMessages and ReplaceConversationMessages. dir is the
// specific task's directory (FileStore.taskDir(projectID, id)).
func writeConversation(dir, stage string, conv Conversation) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating task directory %s: %w", dir, err)
	}

	data, err := yamlutil.Marshal(conv)
	if err != nil {
		return fmt.Errorf("encoding conversation for %s/%s: %w", dir, stage, err)
	}

	path := conversationPath(dir, stage)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
