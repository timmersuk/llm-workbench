package task

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// Execution status values, per docs/task schema v0.md's execution.yaml.
const (
	ExecutionStatusSuccess = "success"
	ExecutionStatusFailure = "failure"
	ExecutionStatusPartial = "partial"
)

// Failure type values, per docs/task schema v0.md's execution.yaml.
const (
	FailureTypeSpecification = "specification"
	FailureTypeInfeasible    = "infeasible"
	FailureTypeExecution     = "execution"
	FailureTypeResource      = "resource"
	FailureTypeQuality       = "quality"
)

// ErrExecutionAlreadyExists is returned by RecordExecution when an
// execution.yaml already exists for the given ExecutionID — executions are
// append-only per docs/task schema v0.md §5.2 ("Executions are never
// overwritten, only added"), so a collision is a caller bug (reusing an id
// instead of calling NextExecutionID) rather than something to silently
// accept.
var ErrExecutionAlreadyExists = errors.New("execution already exists")

// ExecutionExecutor identifies which executor produced an Execution.
type ExecutionExecutor struct {
	Type    string `yaml:"type" json:"type"` // claude-code | codex | local | human
	Version string `yaml:"version" json:"version"`
}

// ExecutionInput records what an Execution was given to work from.
type ExecutionInput struct {
	PlanRef     string   `yaml:"plan_ref" json:"plan_ref"`
	ContextRefs []string `yaml:"context_refs" json:"context_refs"`
	// ReviewFeedback is the prior review's notes when this execution was
	// triggered by a needs_changes verdict, empty otherwise. Archival only,
	// like PlanRef/ContextRefs — the prompt that actually primes the
	// execution is built from a fresh review lookup at execute-time
	// (docs/adr/0012), not read back from this field.
	ReviewFeedback string `yaml:"review_feedback" json:"review_feedback"`
	// ContinuedFromExecutionID is the prior execution's id when a human
	// explicitly chose to continue this attempt from a failed/partial
	// execution's branch (resolveFailureContinuation), empty otherwise.
	// Distinct from ReviewFeedback's needs_changes continuation, which is
	// inferred from reviews/ rather than recorded as an explicit choice
	// here — this field exists so that choice is itself an inspectable
	// fact, not something re-derived from ForkedFromBranch's branch-naming
	// convention (architectural invariants.md, "no hidden state").
	ContinuedFromExecutionID string `yaml:"continued_from_execution_id" json:"continued_from_execution_id"`
}

// ExecutionOutput records what an Execution produced.
type ExecutionOutput struct {
	Artifacts []string `yaml:"artifacts" json:"artifacts"`
	GitBranch string   `yaml:"git_branch" json:"git_branch"`
	Commits   []string `yaml:"commits" json:"commits"`
	// ForkedFromBranch is the branch/ref this attempt's worktree was
	// actually created from (docs/adr/0012) — empty for a first attempt
	// forked directly from the shared checkout's own branch. Unlike
	// ExecutionInput.ReviewFeedback (archival only), this field is read
	// back later: CollectExecutionOutput/CollectExecutionPatch use it to
	// scope an execution's own Commits to just what this attempt
	// contributed, rather than every ancestor attempt's commits too, since
	// the diff (unlike the commit list) is deliberately always cumulative
	// against BaseBranch/main.
	ForkedFromBranch string `yaml:"forked_from_branch" json:"forked_from_branch"`
	// WorkspaceDirty is true when a successful execution still left
	// uncommitted changes in its worktree after being given a dedicated
	// follow-up turn to commit or clean them up (handleStartExecution's
	// workspace-cleanup step) — the harness deliberately doesn't silently
	// commit or delete anything itself here (unlike the failure path's
	// mechanical safety commit): dirty state after success might be
	// scratch/junk the agent intentionally left out, so this is recorded
	// for a human to check rather than guessed at (architectural
	// invariants.md, "no hidden state"). Always false for a failed/partial
	// execution — that path's dirty state is instead swept into a commit,
	// never left dirty and merely flagged.
	WorkspaceDirty bool `yaml:"workspace_dirty" json:"workspace_dirty"`
}

// ExecutionMetrics records measurements about an Execution's run.
// TokensUsed/CostEstimate are left at zero when the underlying executor
// doesn't report them, rather than fabricated.
type ExecutionMetrics struct {
	DurationSeconds float64 `yaml:"duration_seconds" json:"duration_seconds"`
	TokensUsed      int     `yaml:"tokens_used" json:"tokens_used"`
	CostEstimate    float64 `yaml:"cost_estimate" json:"cost_estimate"`
}

// ExecutionFailure describes why an Execution failed, present only when
// Status is "failure" or "partial".
type ExecutionFailure struct {
	Type    string `yaml:"type" json:"type"` // specification | infeasible | execution | resource | quality
	Message string `yaml:"message" json:"message"`
}

// Execution is one execution attempt, stored as
// <task root>/<id>/executions/<execution_id>.yaml — one file per attempt,
// never overwritten (docs/task schema v0.md §5.2). Field-for-field mirror
// of docs/task schema v0.md's execution.yaml.
type Execution struct {
	ExecutionID string            `yaml:"execution_id" json:"execution_id"`
	TaskID      string            `yaml:"task_id" json:"task_id"`
	Executor    ExecutionExecutor `yaml:"executor" json:"executor"`
	Input       ExecutionInput    `yaml:"input" json:"input"`
	Output      ExecutionOutput   `yaml:"output" json:"output"`
	Metrics     ExecutionMetrics  `yaml:"metrics" json:"metrics"`
	Status      string            `yaml:"status" json:"status"` // success | failure | partial
	Failure     *ExecutionFailure `yaml:"failure,omitempty" json:"failure,omitempty"`
	CreatedAt   time.Time         `yaml:"created_at" json:"created_at"`
}

// executionIDPattern matches the "exec-NNN" ids this package generates
// (NextExecutionID) and expects (RecordExecution). Not a hard requirement
// of docs/task schema v0.md (which only gives "exec-001" as an example),
// but a fixed format keeps NextExecutionID's sequencing and ListExecutions'
// sort order simple and predictable.
var executionIDPattern = regexp.MustCompile(`^exec-(\d{3,})$`)

func executionsDir(dir string) string {
	return filepath.Join(dir, "executions")
}

func executionPath(dir, executionID string) string {
	return filepath.Join(executionsDir(dir), executionID+".yaml")
}

// NextExecutionID returns the next sequential "exec-NNN" id for id's task,
// by scanning its executions/ directory for the highest existing number —
// "exec-001" if none exist yet. Callers pass the result to RecordExecution;
// this method itself never writes anything, so a caller that ends up not
// recording an execution (e.g. the run failed before producing any result
// at all) simply leaves a gap in the sequence, which is fine — ids only
// need to be unique and increasing, not contiguous.
func (s *FileStore) NextExecutionID(projectID, id string) (string, error) {
	if err := validateID(projectID); err != nil {
		return "", err
	}
	if err := validateID(id); err != nil {
		return "", err
	}

	entries, err := os.ReadDir(executionsDir(s.taskDir(projectID, id)))
	if os.IsNotExist(err) {
		return "exec-001", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading executions dir for %s: %w", id, err)
	}

	max := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := filepath.Ext(name)
		if ext != ".yaml" {
			continue
		}
		m := executionIDPattern.FindStringSubmatch(name[:len(name)-len(ext)])
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if n > max {
			max = n
		}
	}
	return fmt.Sprintf("exec-%03d", max+1), nil
}

// ErrExecutionLogMissing is returned by RecordExecution when no
// exec-NNN.log.yaml exists for the execution being recorded. This is
// unconditional — success, failure, and partial all require it — because
// the log is the only durable evidence of what an unattended run actually
// did, and a crash is exactly the case that evidence matters most for
// (architectural invariants.md, "no hidden state"). Every real caller
// (handleStartExecution) creates the log via CreateExecutionLog before
// the run it covers even starts, so this only ever fires for a caller
// that skipped that step entirely, not a run that produced zero events.
var ErrExecutionLogMissing = errors.New("execution log missing for this execution_id")

// RecordExecution persists exec under id's executions/ directory —
// ErrExecutionAlreadyExists if that execution_id was already recorded
// (append-only, never overwritten). TaskID and CreatedAt are always
// server-set, matching how Task.CreatedAt/UpdatedAt and
// ConversationMessage.CreatedAt are handled elsewhere in this package.
//
// When exec.Status is "success", this also advances the task's Stage from
// "implementation" to "review" (ErrWrongStage if the task isn't currently
// at "implementation") — the same forward-only auto-advance FinalizePlan
// performs, and checked before the execution file is written so a
// wrong-stage success attempt never leaves an orphaned record with no
// corresponding stage advance. A "failure"/"partial" execution is recorded
// unconditionally and never touches Stage.
//
// Every status also requires a matching exec-NNN.log.yaml to already exist
// (ErrExecutionLogMissing otherwise) — unconditional, unlike the stage
// check above, since the log is the only durable evidence of what an
// unattended run actually did and a crash is exactly the case that
// evidence matters most for (architectural invariants.md, "no hidden
// state"). Checked after the stage guard, not before, so a wrong-stage
// call still reports ErrWrongStage rather than being masked by a missing
// log the caller had no reason to create yet.
func (s *FileStore) RecordExecution(projectID, id string, exec Execution) (Execution, error) {
	if err := validateID(projectID); err != nil {
		return Execution{}, err
	}
	if err := validateID(id); err != nil {
		return Execution{}, err
	}
	if err := validateID(exec.ExecutionID); err != nil {
		return Execution{}, err
	}

	dir := s.taskDir(projectID, id)

	var t Task
	if exec.Status == ExecutionStatusSuccess {
		var err error
		t, err = s.Get(projectID, id)
		if err != nil {
			return Execution{}, err
		}
		if t.Stage != StageImplementation {
			return Execution{}, fmt.Errorf("recording execution for %s (stage %q): %w", id, t.Stage, ErrWrongStage)
		}
	}

	if logExists, err := s.ExecutionLogExists(projectID, id, exec.ExecutionID); err != nil {
		return Execution{}, err
	} else if !logExists {
		return Execution{}, fmt.Errorf("recording execution %s for %s: %w", exec.ExecutionID, id, ErrExecutionLogMissing)
	}

	path := executionPath(dir, exec.ExecutionID)
	if _, err := os.Stat(path); err == nil {
		return Execution{}, fmt.Errorf("recording execution %s for %s: %w", exec.ExecutionID, id, ErrExecutionAlreadyExists)
	} else if !os.IsNotExist(err) {
		return Execution{}, fmt.Errorf("checking existing execution %s for %s: %w", exec.ExecutionID, id, err)
	}

	exec.TaskID = id
	exec.CreatedAt = time.Now().UTC()

	execDir := executionsDir(dir)
	if err := os.MkdirAll(execDir, 0o755); err != nil {
		return Execution{}, fmt.Errorf("creating executions directory %s: %w", execDir, err)
	}

	data, err := yaml.Marshal(exec)
	if err != nil {
		return Execution{}, fmt.Errorf("encoding execution %s for %s: %w", exec.ExecutionID, id, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return Execution{}, fmt.Errorf("writing %s: %w", path, err)
	}

	if exec.Status == ExecutionStatusSuccess {
		t.Stage = StageReview
		t.UpdatedAt = time.Now().UTC()
		if err := s.writeTask(projectID, t); err != nil {
			return Execution{}, err
		}
	}

	return exec, nil
}

// ListExecutions returns every recorded Execution for id, sorted by
// execution_id (ascending, so the zero-padded "exec-NNN" format also sorts
// chronologically). A task with no executions/ directory yet (no attempts
// recorded) returns an empty slice, not an error — the same "missing file
// means normal starting state" treatment GetConversation gives a
// not-yet-started stage.
func (s *FileStore) ListExecutions(projectID, id string) ([]Execution, error) {
	if err := validateID(projectID); err != nil {
		return nil, err
	}
	if err := validateID(id); err != nil {
		return nil, err
	}

	dir := executionsDir(s.taskDir(projectID, id))
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading executions dir for %s: %w", id, err)
	}

	var executions []Execution
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := filepath.Ext(entry.Name())
		if ext != ".yaml" {
			continue
		}
		// Match execution.yaml records by the same executionIDPattern
		// NextExecutionID already scans by, not just "any .yaml file" —
		// otherwise a sibling file that also happens to end in .yaml
		// (e.g. an execution's exec-NNN.log.yaml event log, which has its
		// own execution_id field) would parse as a second, bogus,
		// mostly-empty Execution record.
		if executionIDPattern.FindStringSubmatch(entry.Name()[:len(entry.Name())-len(ext)]) == nil {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		var exec Execution
		if err := yaml.Unmarshal(data, &exec); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
		executions = append(executions, exec)
	}

	sort.Slice(executions, func(i, j int) bool {
		ni, iOK := executionSeq(executions[i].ExecutionID)
		nj, jOK := executionSeq(executions[j].ExecutionID)
		if iOK && jOK {
			return ni < nj
		}
		return executions[i].ExecutionID < executions[j].ExecutionID
	})
	return executions, nil
}

// executionSeq extracts the numeric sequence from an "exec-NNN" id, for
// numeric (not lexical) ordering: past exec-999, lexical string comparison
// puts "exec-1000" before "exec-999", silently breaking every "the last
// entry in ListExecutions is the latest attempt" convention this codebase
// relies on (e.g. resolveReviewContinuation in internal/api/execution.go).
// Returns false for anything that doesn't match executionIDPattern, so
// callers can fall back to the old string comparison rather than treat an
// unparseable id as an error.
func executionSeq(id string) (int, bool) {
	m := executionIDPattern.FindStringSubmatch(id)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}
