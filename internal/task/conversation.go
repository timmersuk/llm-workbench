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
// that has a Conversation ("requirements", "planning") — implementation/
// review/complete don't have an interview mechanism (see CONTEXT.md's
// "Conversation" entry).
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

// ConversationMessage is one message in a stage's persisted, append-only
// history.
type ConversationMessage struct {
	Role       string                `yaml:"role" json:"role"` // "user" | "assistant" | "tool"
	Content    string                `yaml:"content" json:"content"`
	ToolCall   *ConversationToolCall `yaml:"tool_call,omitempty" json:"tool_call,omitempty"`
	ToolCallID string                `yaml:"tool_call_id,omitempty" json:"tool_call_id,omitempty"`
	// Error records why this turn failed, if it did — an assistant message
	// with empty Content and no Error means the agent genuinely said
	// nothing; empty Content with Error set means the turn errored out
	// before (or without) producing a reply. Never set on a "user" message.
	Error     string    `yaml:"error,omitempty" json:"error,omitempty"`
	CreatedAt time.Time `yaml:"created_at" json:"created_at"`
}

// Conversation is one stage's full, append-only message history, stored as
// <task root>/<id>/conversation-<stage>.yaml — one file per stage
// (requirements, planning), not one file for the task's whole life, and
// not a fresh file per visit: revisiting a stage (via Revise, lifecycle.go)
// resumes appending to the same Conversation.
type Conversation struct {
	Stage    string                `yaml:"stage" json:"stage"`
	Messages []ConversationMessage `yaml:"messages" json:"messages"`
}

func validateConversationStage(stage string) error {
	if stage != StageRequirements && stage != StagePlanning {
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
