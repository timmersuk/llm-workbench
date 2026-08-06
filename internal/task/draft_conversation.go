package task

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/timmersuk/llm-workbench/internal/yamlutil"
)

// taskDraftDir returns the directory holding a task-drafts session's
// conversation.yaml — <projects root>/<projectID>/task-drafts/<sessionID>/,
// a sibling of tasks/ within the project rather than nested inside it,
// since the session exists before any task does (docs/task schema v0.md
// §1's "task-drafts/<sessionId>/" entry). sessionID is client-minted (the
// frontend mints one the moment "New Task" is clicked, before any message
// is sent — see NewTaskPanel.tsx) rather than server-generated, the same
// trust model AppendConversationMessages already gives task/stage ids.
func (s *FileStore) taskDraftDir(projectID, sessionID string) string {
	return filepath.Join(s.Root, projectID, "task-drafts", sessionID)
}

func taskDraftConversationPath(dir string) string {
	return filepath.Join(dir, "conversation.yaml")
}

// GetTaskDraftConversation returns a task-drafts session's message
// history — mirroring GetConversation's "a missing file just means no
// messages yet" treatment: a session nobody has chatted in yet (or one
// that's never existed at all — a freshly-minted sessionID) is a normal
// starting state, not an error.
func (s *FileStore) GetTaskDraftConversation(projectID, sessionID string) (Conversation, error) {
	if err := validateID(projectID); err != nil {
		return Conversation{}, err
	}
	if err := validateID(sessionID); err != nil {
		return Conversation{}, err
	}

	path := taskDraftConversationPath(s.taskDraftDir(projectID, sessionID))
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Conversation{}, nil
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

// AppendTaskDraftConversationMessages appends msgs to a task-drafts
// session's Conversation, stamping each message's CreatedAt server-side —
// the same append-only, crash-safe raw-O_APPEND write as
// AppendConversationMessages (see that method's doc comment for the full
// rationale: a crash mid-write can only ever tear the newest message being
// appended, never touch an earlier one). Creates the session's directory
// and conversation.yaml on its very first message, same as
// AppendConversationMessages does for a task/stage's first message.
func (s *FileStore) AppendTaskDraftConversationMessages(projectID, sessionID string, msgs ...ConversationMessage) (Conversation, error) {
	if err := validateID(projectID); err != nil {
		return Conversation{}, err
	}
	if err := validateID(sessionID); err != nil {
		return Conversation{}, err
	}
	if len(msgs) == 0 {
		return s.GetTaskDraftConversation(projectID, sessionID)
	}

	dir := s.taskDraftDir(projectID, sessionID)
	path := taskDraftConversationPath(dir)

	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Conversation{}, fmt.Errorf("creating task draft directory %s: %w", dir, err)
		}
		if err := os.WriteFile(path, []byte("messages:\n"), 0o644); err != nil {
			return Conversation{}, fmt.Errorf("writing %s: %w", path, err)
		}
	} else if statErr != nil {
		return Conversation{}, fmt.Errorf("checking existing task draft conversation %s: %w", path, statErr)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return Conversation{}, fmt.Errorf("opening task draft conversation %s: %w", path, err)
	}
	defer f.Close()

	now := time.Now().UTC()
	for _, m := range msgs {
		m.CreatedAt = now
		// Same defensive trim AppendConversationMessages applies — see its
		// doc comment (a block-scalar string starting with a blank line is
		// common in raw LLM output and is kept trimmed as deliberate
		// hygiene here too).
		m.Content = strings.TrimSpace(m.Content)

		// No indentBlock shift here — see AppendConversationMessages'
		// identical comment in conversation.go: yamlutil.Marshal's
		// yaml.IndentSequence(true) already puts this bare
		// []ConversationMessage{m}'s list marker at the correct 4-space
		// depth on its own.
		data, err := yamlutil.Marshal([]ConversationMessage{m})
		if err != nil {
			return Conversation{}, fmt.Errorf("encoding task draft conversation message for %s/%s: %w", projectID, sessionID, err)
		}
		if _, err := f.Write(data); err != nil {
			return Conversation{}, fmt.Errorf("appending to task draft conversation %s: %w", path, err)
		}
	}

	return s.GetTaskDraftConversation(projectID, sessionID)
}

// ReplaceTaskDraftConversationMessages overwrites a task-drafts session's
// Conversation with exactly msgs, rewriting the file — mirroring
// ReplaceConversationMessages' "plain set the list to this" contract for
// message delete/edit/regenerate (no stamping or trimming: callers here are
// already working from a mix of kept messages and at most one
// freshly-produced one they finish themselves).
func (s *FileStore) ReplaceTaskDraftConversationMessages(projectID, sessionID string, msgs []ConversationMessage) (Conversation, error) {
	if err := validateID(projectID); err != nil {
		return Conversation{}, err
	}
	if err := validateID(sessionID); err != nil {
		return Conversation{}, err
	}

	dir := s.taskDraftDir(projectID, sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Conversation{}, fmt.Errorf("creating task draft directory %s: %w", dir, err)
	}

	replaced := Conversation{Messages: msgs}
	data, err := yamlutil.Marshal(replaced)
	if err != nil {
		return Conversation{}, fmt.Errorf("encoding task draft conversation for %s/%s: %w", projectID, sessionID, err)
	}

	path := taskDraftConversationPath(dir)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return Conversation{}, fmt.Errorf("writing %s: %w", path, err)
	}
	return replaced, nil
}
