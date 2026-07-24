package task

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
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

// MarshalYAML forces Arguments/Result to the double-quoted scalar style,
// overriding yaml.v3's own style choice for this type. Raw tool output
// (git diff --stat, test runner summaries, ls -l, ...) commonly has lines
// that themselves start with a leading space, which forces yaml.v3's
// block-literal encoder to add an explicit indentation indicator (e.g.
// "|4-") to disambiguate structural indent from the content's own leading
// space — and a yaml.v3 encoder bug (still v3.0.1, no fix available)
// writes the body one space short of what that indicator requires,
// producing a self-inconsistent file that fails to parse back ("did not
// find expected key"), corrupting the whole Conversation on the next read.
// Double-quoted style has no such indentation ambiguity — newlines are
// escaped explicitly — so it round-trips safely regardless of the
// content's own leading whitespace. Name is left in yaml.v3's default
// style since it's always a short, plain tool identifier.
func (a ConversationToolActivity) MarshalYAML() (interface{}, error) {
	node := &yaml.Node{Kind: yaml.MappingNode}
	appendField := func(key, value string, forceDoubleQuoted, omitEmpty bool) {
		if omitEmpty && value == "" {
			return
		}
		valueNode := &yaml.Node{Kind: yaml.ScalarNode, Value: value}
		if forceDoubleQuoted {
			valueNode.Style = yaml.DoubleQuotedStyle
		}
		node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, valueNode)
	}
	appendField("name", a.Name, false, false)
	appendField("arguments", a.Arguments, true, true)
	appendField("result", a.Result, true, true)
	if a.IsError {
		isErrorNode := &yaml.Node{}
		if err := isErrorNode.Encode(a.IsError); err != nil {
			return nil, err
		}
		node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "is_error"}, isErrorNode)
	}
	return node, nil
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
	Role       string                `yaml:"role" json:"role"` // "user" | "assistant" | "tool"
	Content    string                `yaml:"content" json:"content"`
	ToolCall   *ConversationToolCall `yaml:"tool_call,omitempty" json:"tool_call,omitempty"`
	ToolCallID string                `yaml:"tool_call_id,omitempty" json:"tool_call_id,omitempty"`
	// ToolActivity is the ordered list of intermediate tool calls/results
	// (CONTEXT.md's "Tool Activity") the agent made while producing this
	// turn, bundled onto the assistant message that closes it out rather
	// than as separate Conversation entries (docs/adr/0018's rejected
	// alternatives). Never set on a "user" message.
	ToolActivity []ConversationToolActivity `yaml:"tool_activity,omitempty" json:"tool_activity,omitempty"`
	// Error records why this turn failed, if it did — an assistant message
	// with empty Content and no Error means the agent genuinely said
	// nothing; empty Content with Error set means the turn errored out
	// before (or without) producing a reply. Never set on a "user" message.
	Error     string    `yaml:"error,omitempty" json:"error,omitempty"`
	CreatedAt time.Time `yaml:"created_at" json:"created_at"`
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

func conversationPath(root, id, stage string) string {
	return filepath.Join(root, id, "conversation-"+stage+".yaml")
}

// GetConversation returns the stage's message history for id. Unlike
// GetContext/GetPlan (where a missing file means "not finalized yet" and
// is a meaningful 404), a missing conversation file just means "no
// messages yet" — a normal starting state for any task — so this returns
// an empty Conversation rather than an error when the file doesn't exist.
func (s *FileStore) GetConversation(id, stage string) (Conversation, error) {
	if err := validateID(id); err != nil {
		return Conversation{}, err
	}
	if err := validateConversationStage(stage); err != nil {
		return Conversation{}, err
	}

	path := conversationPath(s.Root, id, stage)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Conversation{Stage: stage}, nil
	}
	if err != nil {
		return Conversation{}, fmt.Errorf("reading %s: %w", path, err)
	}

	var c Conversation
	if err := yaml.Unmarshal(data, &c); err != nil {
		return Conversation{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return c, nil
}

// AppendConversationMessages appends msgs to the stage's Conversation for
// id and rewrites the file, stamping each message's CreatedAt server-side
// (ignoring any client-supplied value, same "server always wins" treatment
// Task.CreatedAt/UpdatedAt get). "Append-only" here is an enforced policy —
// this is the only method that adds messages, and it never edits or
// removes prior ones — rather than a mechanical single-writer-append file
// format; these are small conversational logs, not execution.yaml's
// cross-process ledger, so read-modify-rewrite is the right complexity
// level.
func (s *FileStore) AppendConversationMessages(id, stage string, msgs ...ConversationMessage) (Conversation, error) {
	if err := validateID(id); err != nil {
		return Conversation{}, err
	}
	if err := validateConversationStage(stage); err != nil {
		return Conversation{}, err
	}

	existing, err := s.GetConversation(id, stage)
	if err != nil {
		return Conversation{}, err
	}

	now := time.Now().UTC()
	for _, m := range msgs {
		m.CreatedAt = now
		// gopkg.in/yaml.v3 (still v3.0.1 as of writing, no fix available)
		// fails to round-trip a block-scalar string whose first character
		// is a newline: it marshals fine but errors on Unmarshal ("did not
		// find expected key"). Raw LLM output commonly starts with a blank
		// line, so trim before persisting rather than writing a file this
		// same package can't read back.
		m.Content = strings.TrimSpace(m.Content)
		existing.Messages = append(existing.Messages, m)
	}
	existing.Stage = stage

	return existing, writeConversation(s.Root, id, stage, existing)
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
func (s *FileStore) ReplaceConversationMessages(id, stage string, msgs []ConversationMessage) (Conversation, error) {
	if err := validateID(id); err != nil {
		return Conversation{}, err
	}
	if err := validateConversationStage(stage); err != nil {
		return Conversation{}, err
	}

	replaced := Conversation{Stage: stage, Messages: msgs}
	return replaced, writeConversation(s.Root, id, stage, replaced)
}

// writeConversation marshals and writes conv to id/stage's conversation
// file, creating the task directory if needed — the shared tail of both
// AppendConversationMessages and ReplaceConversationMessages.
func writeConversation(root, id, stage string, conv Conversation) error {
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating task directory %s: %w", dir, err)
	}

	data, err := yaml.Marshal(conv)
	if err != nil {
		return fmt.Errorf("encoding conversation for %s/%s: %w", id, stage, err)
	}

	path := conversationPath(root, id, stage)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
