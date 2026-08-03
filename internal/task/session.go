package task

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/timmersuk/llm-workbench/internal/yamlutil"
)

// sessionRecord is the on-disk shape of a *.session.yaml sibling file: one
// entry per executor that has ever produced a real session/thread id for
// this conversation (map key "claude-code" | "codex" — the same
// AgentRunners map key resolveStageStreamTarget/resolveTaskDraftStreamTarget
// already use to pick a runner). Keyed per-executor rather than a single
// scalar id because a conversation can switch executors mid-conversation
// (stageMessageRequest.Executor) — each runner only ever reads/writes its
// own key, so switching executors just falls back to systemPromptWithHistory
// for the other runner's turn rather than misreading a foreign session id as
// its own.
type sessionRecord struct {
	Sessions map[string]string `yaml:"sessions"`
}

// sessionPath returns the sibling file a stage Conversation's session/thread
// ids are persisted to — conversation-<stage>.session.yaml, next to
// conversation-<stage>.yaml itself, so both live and are committed together.
func sessionPath(dir, stage string) string {
	return filepath.Join(dir, "conversation-"+stage+".session.yaml")
}

// readSessionRecord reads and parses path, tolerating a missing file (no
// session id has ever been recorded yet — a normal starting state, not an
// error, mirroring GetConversation's own "missing file just means nothing
// yet" treatment).
func readSessionRecord(path string) (sessionRecord, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return sessionRecord{}, nil
	}
	if err != nil {
		return sessionRecord{}, fmt.Errorf("reading %s: %w", path, err)
	}
	var rec sessionRecord
	if err := yamlutil.Unmarshal(data, &rec); err != nil {
		return sessionRecord{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return rec, nil
}

// writeSessionRecord persists rec to path as a whole-file rewrite — unlike
// AppendConversationMessages' append-only transcript, a session id is a
// single mutable value per executor (superseded on every turn's resume
// attempt or fallback), so there is no append-only invariant to preserve
// here, and the file is tiny enough that a full rewrite is not a performance
// concern the way it would be for the message transcript.
func writeSessionRecord(dir, path string, rec sessionRecord) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating task directory %s: %w", dir, err)
	}
	data, err := yamlutil.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encoding session record for %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// GetSessionID returns the durable session/thread id previously recorded
// for executor on this stage Conversation, or "" if none has ever been
// recorded (a brand-new conversation, an executor that has never produced
// one, or one that was cleared after a "not found" resume failure). Callers
// consult this only when they have no live in-memory session for the
// runner already (mirroring RunInput.History's own "only when no live
// session" contract) — see agentrunner.RunInput.ResumeSessionID.
func (s *FileStore) GetSessionID(projectID, id, stage, executor string) (string, error) {
	if err := validateID(projectID); err != nil {
		return "", err
	}
	if err := validateID(id); err != nil {
		return "", err
	}
	if err := validateConversationStage(stage); err != nil {
		return "", err
	}

	rec, err := readSessionRecord(sessionPath(s.taskDir(projectID, id), stage))
	if err != nil {
		return "", err
	}
	return rec.Sessions[executor], nil
}

// SetSessionID records the session/thread id a turn's AgentRunner.Run
// returned (agentrunner.RunOutput.SessionID) for executor on this stage
// Conversation, overwriting whatever was previously recorded for that
// executor (including clearing it back to "" after a "not found" resume
// failure). Only executor's own key is touched; another executor's
// previously-recorded id is left untouched, so switching executors
// mid-conversation can never misattribute the wrong session/thread id to
// the wrong runner.
func (s *FileStore) SetSessionID(projectID, id, stage, executor, sessionID string) error {
	if err := validateID(projectID); err != nil {
		return err
	}
	if err := validateID(id); err != nil {
		return err
	}
	if err := validateConversationStage(stage); err != nil {
		return err
	}

	dir := s.taskDir(projectID, id)
	path := sessionPath(dir, stage)
	rec, err := readSessionRecord(path)
	if err != nil {
		return err
	}
	if rec.Sessions == nil {
		rec.Sessions = make(map[string]string)
	}
	rec.Sessions[executor] = sessionID
	return writeSessionRecord(dir, path, rec)
}

// taskDraftSessionPath returns the sibling file a task-drafts session's
// session/thread ids are persisted to — conversation.session.yaml, next to
// a task-drafts session's own conversation.yaml (taskDraftConversationPath),
// the task-drafts analog of sessionPath.
func taskDraftSessionPath(dir string) string {
	return filepath.Join(dir, "conversation.session.yaml")
}

// GetTaskDraftSessionID mirrors GetSessionID for a task-drafts session
// (keyed by project+sessionID rather than task+stage — there is no task yet;
// see taskDraftDir).
func (s *FileStore) GetTaskDraftSessionID(projectID, sessionID, executor string) (string, error) {
	if err := validateID(projectID); err != nil {
		return "", err
	}
	if err := validateID(sessionID); err != nil {
		return "", err
	}

	rec, err := readSessionRecord(taskDraftSessionPath(s.taskDraftDir(projectID, sessionID)))
	if err != nil {
		return "", err
	}
	return rec.Sessions[executor], nil
}

// SetTaskDraftSessionID mirrors SetSessionID for a task-drafts session.
func (s *FileStore) SetTaskDraftSessionID(projectID, sessionID, executor, value string) error {
	if err := validateID(projectID); err != nil {
		return err
	}
	if err := validateID(sessionID); err != nil {
		return err
	}

	dir := s.taskDraftDir(projectID, sessionID)
	path := taskDraftSessionPath(dir)
	rec, err := readSessionRecord(path)
	if err != nil {
		return err
	}
	if rec.Sessions == nil {
		rec.Sessions = make(map[string]string)
	}
	rec.Sessions[executor] = value
	return writeSessionRecord(dir, path, rec)
}
