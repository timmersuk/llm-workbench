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

// MarshalYAML forces Arguments to double-quoted style — same workaround
// and rationale as ConversationToolActivity.MarshalYAML below: Arguments
// is a raw JSON string of the proposed Draft's fields (e.g. a long
// "detail" field), which can contain embedded newlines or indented
// content that would otherwise risk yaml.v3's block-literal corruption
// bug. ID/Name are short, controlled-vocabulary values with no such risk,
// left in yaml.v3's default style.
func (c ConversationToolCall) MarshalYAML() (interface{}, error) {
	node := &yaml.Node{Kind: yaml.MappingNode}
	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "id"}, &yaml.Node{Kind: yaml.ScalarNode, Value: c.ID},
		&yaml.Node{Kind: yaml.ScalarNode, Value: "name"}, &yaml.Node{Kind: yaml.ScalarNode, Value: c.Name},
		&yaml.Node{Kind: yaml.ScalarNode, Value: "arguments"}, &yaml.Node{Kind: yaml.ScalarNode, Value: c.Arguments, Style: yaml.DoubleQuotedStyle},
	)
	return node, nil
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

// MarshalYAML forces Content/Error to double-quoted style — same
// workaround and rationale as ConversationToolActivity.MarshalYAML.
// Unlike that type's tool output, Content isn't a rare edge case: it's
// ordinary LLM prose, and any reply that quotes something already-indented
// (a git diff --stat, an ls -l, a fenced code block) has lines starting
// with a leading space, which is exactly the shape that trips yaml.v3's
// block-literal encoding bug and corrupts the whole Conversation on the
// next read. Role/ToolCallID/CreatedAt are short, controlled-vocabulary
// values with no such risk, left in yaml.v3's default style; ToolCall and
// ToolActivity carry their own MarshalYAML and are encoded through it
// unchanged by nesting them via yaml.Node.Encode.
func (m ConversationMessage) MarshalYAML() (interface{}, error) {
	node := &yaml.Node{Kind: yaml.MappingNode}
	appendScalar := func(key, value string, forceDoubleQuoted, omitEmpty bool) {
		if omitEmpty && value == "" {
			return
		}
		valueNode := &yaml.Node{Kind: yaml.ScalarNode, Value: value}
		if forceDoubleQuoted {
			valueNode.Style = yaml.DoubleQuotedStyle
		}
		node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, valueNode)
	}
	appendNode := func(key string, v interface{}) error {
		n := &yaml.Node{}
		if err := n.Encode(v); err != nil {
			return err
		}
		node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, n)
		return nil
	}

	appendScalar("role", m.Role, false, false)
	appendScalar("content", m.Content, true, false)
	if m.ToolCall != nil {
		if err := appendNode("tool_call", m.ToolCall); err != nil {
			return nil, err
		}
	}
	appendScalar("tool_call_id", m.ToolCallID, false, true)
	if len(m.ToolActivity) > 0 {
		if err := appendNode("tool_activity", m.ToolActivity); err != nil {
			return nil, err
		}
	}
	appendScalar("error", m.Error, true, true)
	if err := appendNode("created_at", m.CreatedAt); err != nil {
		return nil, err
	}
	return node, nil
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
	if err := yaml.Unmarshal(data, &c); err != nil {
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
		header, err := yaml.Marshal(struct {
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
		// gopkg.in/yaml.v3 (still v3.0.1 as of writing, no fix available)
		// fails to round-trip a block-scalar string whose first character
		// is a newline: it marshals fine but errors on Unmarshal ("did not
		// find expected key"). Raw LLM output commonly starts with a blank
		// line, so trim before persisting rather than writing a file this
		// same package can't read back.
		m.Content = strings.TrimSpace(m.Content)

		data, err := yaml.Marshal([]ConversationMessage{m})
		if err != nil {
			return Conversation{}, fmt.Errorf("encoding conversation message for %s/%s: %w", id, stage, err)
		}
		if _, err := f.Write(indentBlock(data, "    ")); err != nil {
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

	data, err := yaml.Marshal(conv)
	if err != nil {
		return fmt.Errorf("encoding conversation for %s/%s: %w", dir, stage, err)
	}

	path := conversationPath(dir, stage)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
