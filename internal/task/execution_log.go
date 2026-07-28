package task

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/timmersuk/llm-workbench/internal/yamlutil"
)

// Execution log event kinds, mirroring agentrunner.ExecuteEvent's Kind
// values. "reasoning" is deliberately not one of them: reasoning content is
// excluded from every persisted record in this codebase (see
// ConversationToolCall's doc comment, "Store durable semantics... not
// transient internal reasoning"), and an execution log is no exception.
const (
	ExecutionLogEventText       = "text"
	ExecutionLogEventToolCall   = "tool_call"
	ExecutionLogEventToolResult = "tool_result"
)

// ErrExecutionLogAlreadyExists is returned by CreateExecutionLog when
// executionID's log file already exists — like execution.yaml itself, a
// log is append-only for the life of one attempt, never restarted empty.
var ErrExecutionLogAlreadyExists = errors.New("execution log already exists")

// ExecutionLogEvent is one raw event from an execution attempt's
// agentrunner.Execute call, persisted verbatim. Unlike
// ConversationToolActivity, this is never truncated: an execution log is
// the only durable evidence of what an unattended, potentially very long
// run actually did (build output, test output, every command run), and a
// fixed byte cap would risk cutting exactly the informative part — a
// failing test's assertion is usually at the *end* of its output, not the
// start.
type ExecutionLogEvent struct {
	Kind       string    `yaml:"kind"`
	Text       string    `yaml:"text,omitempty"`
	ToolName   string    `yaml:"tool_name,omitempty"`
	ToolInput  string    `yaml:"tool_input,omitempty"`
	ToolResult string    `yaml:"tool_result,omitempty"`
	IsError    bool      `yaml:"is_error,omitempty"`
	CreatedAt  time.Time `yaml:"created_at"`
}

// MarshalYAML forces Text/ToolInput/ToolResult through yamlutil.Quoted —
// this is raw tool/build/test output, the exact shape goccy's own default
// plain-style heuristic doesn't safely handle (see yamlutil.Quoted's doc
// comment; a literal tab, exactly what `go test`'s own summary lines
// contain, silently corrupts on round-trip otherwise) — at a size, since
// this log is never truncated, where hitting that gap would be far more
// costly than in the capped conversation logs it was first found in. Kind
// is a short, controlled-vocabulary value with no such risk, left as a
// plain string.
func (e ExecutionLogEvent) MarshalYAML() (interface{}, error) {
	return struct {
		Kind       string          `yaml:"kind"`
		Text       yamlutil.Quoted `yaml:"text,omitempty"`
		ToolName   string          `yaml:"tool_name,omitempty"`
		ToolInput  yamlutil.Quoted `yaml:"tool_input,omitempty"`
		ToolResult yamlutil.Quoted `yaml:"tool_result,omitempty"`
		IsError    bool            `yaml:"is_error,omitempty"`
		CreatedAt  time.Time       `yaml:"created_at"`
	}{e.Kind, yamlutil.Quoted(e.Text), e.ToolName, yamlutil.Quoted(e.ToolInput), yamlutil.Quoted(e.ToolResult), e.IsError, e.CreatedAt}, nil
}

// ExecutionLog is the decoded shape of executions/exec-NNN.log.yaml: the
// full, untruncated, ordered event stream from every agentrunner.Execute
// call one execution attempt makes — the main run, and, if the workspace
// was left dirty, the follow-up cleanup turn — concatenated in the order
// they happened. Unlike Conversation, there is no Role/turn concept: an
// execution runs unattended, so nothing here is ever "from a human."
type ExecutionLog struct {
	ExecutionID string              `yaml:"execution_id"`
	Events      []ExecutionLogEvent `yaml:"events"`
}

func executionLogPath(dir, executionID string) string {
	return executionPath(dir, executionID+".log")
}

// CreateExecutionLog creates executionID's log file with an empty event
// list, before the run it will record even starts — so a crash in the
// first second of an attempt still leaves a file on disk proving the
// attempt began, rather than nothing at all.
func (s *FileStore) CreateExecutionLog(projectID, id, executionID string) error {
	if err := validateID(projectID); err != nil {
		return err
	}
	if err := validateID(id); err != nil {
		return err
	}
	if err := validateID(executionID); err != nil {
		return err
	}

	dir := s.taskDir(projectID, id)
	execDir := executionsDir(dir)
	if err := os.MkdirAll(execDir, 0o755); err != nil {
		return fmt.Errorf("creating executions directory %s: %w", execDir, err)
	}

	path := executionLogPath(dir, executionID)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("creating execution log %s for %s: %w", executionID, id, ErrExecutionLogAlreadyExists)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking existing execution log %s for %s: %w", executionID, id, err)
	}

	header, err := yamlutil.Marshal(struct {
		ExecutionID string `yaml:"execution_id"`
	}{executionID})
	if err != nil {
		return fmt.Errorf("encoding execution log header for %s/%s: %w", id, executionID, err)
	}
	content := append(header, []byte("events:\n")...)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// AppendExecutionLogEvent appends ev to executionID's log via a raw
// O_APPEND write — no read, no rewrite of anything already on disk, so a
// crash mid-write can only tear this one newest entry, never anything
// written by an earlier call. CreatedAt is always server-stamped here,
// the same "server always wins" treatment every other CreatedAt in this
// package gets. The log file must already exist (CreateExecutionLog).
func (s *FileStore) AppendExecutionLogEvent(projectID, id, executionID string, ev ExecutionLogEvent) error {
	if err := validateID(projectID); err != nil {
		return err
	}
	if err := validateID(id); err != nil {
		return err
	}
	if err := validateID(executionID); err != nil {
		return err
	}

	ev.CreatedAt = time.Now().UTC()

	// Marshal as a one-element slice, not the bare struct: the encoder
	// renders that as a ready-to-append block-sequence item
	// ("- kind: ...\n  ...") with continuation lines already correctly
	// indented to align under the leading "- ", so no manual bookkeeping
	// is needed for the item's own internal structure — only a uniform
	// prefix nesting it under the file's top-level "events:" key.
	data, err := yamlutil.Marshal([]ExecutionLogEvent{ev})
	if err != nil {
		return fmt.Errorf("encoding execution log event for %s/%s: %w", id, executionID, err)
	}

	path := executionLogPath(s.taskDir(projectID, id), executionID)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening execution log %s for %s: %w", executionID, id, err)
	}
	defer f.Close()

	// Four spaces, not two: matches yamlutil.Marshal's configured indent
	// (internal/yamlutil), so this file reads identically to every
	// conversation-*.yaml's messages: list instead of introducing a
	// second indent convention.
	if _, err := f.Write(indentBlock(data, "    ")); err != nil {
		return fmt.Errorf("appending to execution log %s for %s: %w", executionID, id, err)
	}
	return nil
}

// indentBlock prefixes every non-empty line of data with prefix, nesting
// an already-valid YAML fragment (e.g. one block-sequence item) one level
// deeper under a parent key. A uniform shift preserves every line's
// indentation relative to the others, which is all YAML requires for the
// nesting to stay valid — deliberately not a full re-parse-and-reindent,
// since this needs to work as a byte-level append with no read of prior
// content.
func indentBlock(data []byte, prefix string) []byte {
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	for i, line := range lines {
		if line == "" {
			continue
		}
		lines[i] = prefix + line
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

// GetExecutionLog reads back executionID's full event log.
func (s *FileStore) GetExecutionLog(projectID, id, executionID string) (ExecutionLog, error) {
	if err := validateID(projectID); err != nil {
		return ExecutionLog{}, err
	}
	if err := validateID(id); err != nil {
		return ExecutionLog{}, err
	}
	if err := validateID(executionID); err != nil {
		return ExecutionLog{}, err
	}

	path := executionLogPath(s.taskDir(projectID, id), executionID)
	data, err := os.ReadFile(path)
	if err != nil {
		return ExecutionLog{}, fmt.Errorf("reading %s: %w", path, err)
	}

	var log ExecutionLog
	if err := yamlutil.Unmarshal(data, &log); err != nil {
		return ExecutionLog{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return log, nil
}

// ExecutionLogExists reports whether executionID has a log file recorded —
// used by RecordExecution to mechanically refuse an execution record with
// no corresponding evidence of what the run actually did.
func (s *FileStore) ExecutionLogExists(projectID, id, executionID string) (bool, error) {
	if err := validateID(projectID); err != nil {
		return false, err
	}
	if err := validateID(id); err != nil {
		return false, err
	}
	if err := validateID(executionID); err != nil {
		return false, err
	}

	path := executionLogPath(s.taskDir(projectID, id), executionID)
	if _, err := os.Stat(path); err == nil {
		return true, nil
	} else if os.IsNotExist(err) {
		return false, nil
	} else {
		return false, fmt.Errorf("checking execution log %s for %s: %w", executionID, id, err)
	}
}
