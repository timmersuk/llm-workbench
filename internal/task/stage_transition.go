package task

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/timmersuk/llm-workbench/internal/yamlutil"
)

// Stage-transition trigger vocabulary — the fixed set of ways Stage can
// move, one per lifecycle.go entry point (plus RecordExecution's success
// branch). A closed enum rather than free text so a consumer can group/
// filter on it without parsing prose.
const (
	TransitionTriggerFinalizeRequirements = "finalize_requirements"
	TransitionTriggerFinalizePlan         = "finalize_plan"
	TransitionTriggerFinalizeReview       = "finalize_review"
	TransitionTriggerExecutionSuccess     = "execution_success"
	TransitionTriggerMarkPRMerged         = "mark_pr_merged"
	TransitionTriggerReviseRequirements   = "revise_requirements"
	TransitionTriggerReviseToPlanning     = "revise_planning"
	// TransitionTriggerCleanupComplete is CompleteCleanup's trigger: the
	// StageCleanup -> StageMerged move once every one of a task's execution
	// worktrees has been removed or was already gone.
	TransitionTriggerCleanupComplete = "cleanup_complete"
)

// StageTransition is one Stage move, appended to
// <task root>/<id>/transitions.yaml — the append-only counterpart to
// Task.Stage's in-place overwrite (lifecycle.go), so a task's actual path
// through its lifecycle, including reversals, is a first-class queryable
// fact rather than only reconstructable from git log. ReviewID/ExecutionID
// link to the review/execution that drove the move (e.g. finalize_review's
// linked Review.Notes/Decision is the "why," not duplicated here); Reason
// is a short human-typed explanation, only ever set on a revise_*
// transition, and only when the caller supplied one.
type StageTransition struct {
	TaskID      string    `yaml:"task_id" json:"task_id"`
	FromStage   string    `yaml:"from_stage" json:"from_stage"`
	ToStage     string    `yaml:"to_stage" json:"to_stage"`
	Trigger     string    `yaml:"trigger" json:"trigger"`
	ReviewID    string    `yaml:"review_id,omitempty" json:"review_id,omitempty"`
	ExecutionID string    `yaml:"execution_id,omitempty" json:"execution_id,omitempty"`
	Reason      string    `yaml:"reason,omitempty" json:"reason,omitempty"`
	CreatedAt   time.Time `yaml:"created_at" json:"created_at"`
}

// MarshalYAML forces Reason through yamlutil.Quoted — it's free-form,
// human-typed text (the same risk ConversationMessage.Content carries), so
// goccy's default plain-style heuristic doesn't safely handle every
// character it can contain. Every other field is a short, controlled-
// vocabulary value with no such risk.
func (t StageTransition) MarshalYAML() (interface{}, error) {
	return struct {
		TaskID      string          `yaml:"task_id"`
		FromStage   string          `yaml:"from_stage"`
		ToStage     string          `yaml:"to_stage"`
		Trigger     string          `yaml:"trigger"`
		ReviewID    string          `yaml:"review_id,omitempty"`
		ExecutionID string          `yaml:"execution_id,omitempty"`
		Reason      yamlutil.Quoted `yaml:"reason,omitempty"`
		CreatedAt   time.Time       `yaml:"created_at"`
	}{t.TaskID, t.FromStage, t.ToStage, t.Trigger, t.ReviewID, t.ExecutionID, yamlutil.Quoted(t.Reason), t.CreatedAt}, nil
}

func transitionsPath(dir string) string {
	return filepath.Join(dir, "transitions.yaml")
}

// AppendStageTransition appends transition to id's transitions.yaml,
// stamping TaskID/CreatedAt server-side. Mirrors AppendConversationMessages'
// raw O_APPEND shape (conversation.go): the file is created with a
// "transitions:\n" header on the first append, and every subsequent append
// is a single os.O_APPEND write of one indented list item — never a
// read-modify-rewrite of the whole file, so a crash mid-write can only ever
// tear the newest entry, the same shape gitstore's repairTornYAML already
// handles for conversations/execution logs. One file for the task's whole
// life (unlike reviews/executions, which are one file per cycle/attempt) —
// a transition record is small and has no rich content of its own, closer
// in shape to a conversation message than a review verdict.
func (s *FileStore) AppendStageTransition(projectID, id string, transition StageTransition) error {
	if err := validateID(projectID); err != nil {
		return err
	}
	if err := validateID(id); err != nil {
		return err
	}

	dir := s.taskDir(projectID, id)
	path := transitionsPath(dir)

	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating task directory %s: %w", dir, err)
		}
		if err := os.WriteFile(path, []byte("transitions:\n"), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
	} else if statErr != nil {
		return fmt.Errorf("checking existing transitions %s: %w", path, statErr)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening transitions %s: %w", path, err)
	}
	defer f.Close()

	transition.TaskID = id
	transition.CreatedAt = time.Now().UTC()

	// No indentBlock shift here either — see AppendConversationMessages'
	// identical comment in conversation.go.
	data, err := yamlutil.Marshal([]StageTransition{transition})
	if err != nil {
		return fmt.Errorf("encoding stage transition for %s: %w", id, err)
	}
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("appending to transitions %s: %w", path, err)
	}
	return nil
}

// ListStageTransitions returns every recorded StageTransition for id in
// append order (oldest first) — a task with no transitions.yaml yet (no
// Stage move recorded, e.g. a brand-new task still in requirements) returns
// an empty slice, not an error, the same "missing file means normal
// starting state" treatment GetConversation/ListExecutions give.
func (s *FileStore) ListStageTransitions(projectID, id string) ([]StageTransition, error) {
	if err := validateID(projectID); err != nil {
		return nil, err
	}
	if err := validateID(id); err != nil {
		return nil, err
	}

	path := transitionsPath(s.taskDir(projectID, id))
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	var log struct {
		Transitions []StageTransition `yaml:"transitions"`
	}
	if err := yamlutil.Unmarshal(data, &log); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return log.Transitions, nil
}
