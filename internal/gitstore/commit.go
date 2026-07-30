package gitstore

import (
	"fmt"
	"path/filepath"

	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

// projectDir returns the directory holding a project's project.yaml —
// mirroring project.FileStore's own `filepath.Join(Root, id)` layout
// (internal/project/store.go). gitstore needs this so a pending change can
// be committed by `git add`-ing just this directory (push.go's
// commitPending) rather than the whole working tree, keeping one commit
// per operation even when several are queued up between push ticks.
func (c *core) projectDir(id string) string {
	return filepath.Join(c.root, "projects", id)
}

// taskDir returns the directory holding a task's task.yaml and every
// sibling artifact (context.yaml, plan.yaml, conversations, executions/,
// reviews/) — mirroring task.FileStore's own taskDir layout
// (internal/task/store.go). Every task mutation, whatever it touches,
// lives under this one directory, so `git add`-ing it is always exactly
// the scope of that one operation's change.
func (c *core) taskDir(projectID, id string) string {
	return filepath.Join(c.root, "projects", projectID, "tasks", id)
}

// withPending runs fn (a FileStore write) with c.mu held, then — if fn
// succeeded — enqueues a pending change for the push worker's next tick to
// actually commit (push.go's commitPending), rather than committing
// immediately. dirFn is called after fn returns (not before) since some
// operations (Create, which slugifies an id from a human-supplied name)
// only know the affected entity's directory once the write itself has
// produced it; others (Update, which already have the id as a parameter)
// could compute it upfront, but using the same after-the-fact shape
// everywhere keeps every call site identical.
//
// Deferring the actual `git add`/`git commit` out of the request path is
// what makes shelling out to the `git` binary (rather than the pure-Go
// go-git library this package used before) viable without adding a `git`
// subprocess spawn to every API request's latency — see push.go's
// commitPending and this package's doc comment.
func (c *core) withPending(message string, dirFn func() string, fn func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := fn(); err != nil {
		return err
	}
	c.pending = append(c.pending, pendingChange{dir: dirFn(), message: message})
	return nil
}

// --- project.Store / api.ProjectStore ---

// List delegates straight to the wrapped project.FileStore — a read, no
// git or locking involved.
func (s *ProjectStore) List() (project.ListResult, error) { return s.files.List() }

// Get delegates straight to the wrapped project.FileStore.
func (s *ProjectStore) Get(id string) (project.Project, error) { return s.files.Get(id) }

// Create writes a new project via the wrapped project.FileStore and
// enqueues the result to be committed on the push worker's next tick.
func (s *ProjectStore) Create(in project.CreateInput) (project.Project, error) {
	var created project.Project
	err := s.core.withPending(
		fmt.Sprintf("Create project %q", in.Name),
		func() string { return s.core.projectDir(created.ID) },
		func() error {
			var err error
			created, err = s.files.Create(in)
			return err
		},
	)
	if err != nil {
		return project.Project{}, err
	}
	return created, nil
}

// Update overwrites an existing project via the wrapped project.FileStore
// and enqueues the result to be committed on the push worker's next tick.
func (s *ProjectStore) Update(id string, in project.UpdateInput) (project.Project, error) {
	var updated project.Project
	err := s.core.withPending(
		fmt.Sprintf("Update project %s", id),
		func() string { return s.core.projectDir(id) },
		func() error {
			var err error
			updated, err = s.files.Update(id, in)
			return err
		},
	)
	if err != nil {
		return project.Project{}, err
	}
	return updated, nil
}

// --- task.Store / api.TaskStore ---

// List delegates straight to the wrapped task.FileStore.
func (s *TaskStore) List(projectID string) (task.ListResult, error) { return s.files.List(projectID) }

// Get delegates straight to the wrapped task.FileStore.
func (s *TaskStore) Get(projectID, id string) (task.Task, error) { return s.files.Get(projectID, id) }

// Create writes a new task via the wrapped task.FileStore and enqueues the
// result to be committed on the push worker's next tick.
func (s *TaskStore) Create(projectID string, t task.Task) (task.Task, error) {
	var created task.Task
	err := s.core.withPending(
		fmt.Sprintf("Create task %s/%s", projectID, t.ID),
		func() string { return s.core.taskDir(projectID, t.ID) },
		func() error {
			var err error
			created, err = s.files.Create(projectID, t)
			return err
		},
	)
	if err != nil {
		return task.Task{}, err
	}
	return created, nil
}

// Update overwrites an existing task via the wrapped task.FileStore and
// enqueues the result to be committed on the push worker's next tick.
func (s *TaskStore) Update(projectID, id string, t task.Task) (task.Task, error) {
	var updated task.Task
	err := s.core.withPending(
		fmt.Sprintf("Update task %s/%s", projectID, id),
		func() string { return s.core.taskDir(projectID, id) },
		func() error {
			var err error
			updated, err = s.files.Update(projectID, id, t)
			return err
		},
	)
	if err != nil {
		return task.Task{}, err
	}
	return updated, nil
}

// GetContext delegates straight to the wrapped task.FileStore.
func (s *TaskStore) GetContext(projectID, id string) (task.Context, error) {
	return s.files.GetContext(projectID, id)
}

// GetPlan delegates straight to the wrapped task.FileStore.
func (s *TaskStore) GetPlan(projectID, id string) (task.Plan, error) {
	return s.files.GetPlan(projectID, id)
}

// GetConversation delegates straight to the wrapped task.FileStore.
func (s *TaskStore) GetConversation(projectID, id, stage string) (task.Conversation, error) {
	return s.files.GetConversation(projectID, id, stage)
}

// AppendConversationMessages appends via the wrapped task.FileStore and
// enqueues the result to be committed on the push worker's next tick.
func (s *TaskStore) AppendConversationMessages(projectID, id, stage string, msgs ...task.ConversationMessage) (task.Conversation, error) {
	var conv task.Conversation
	err := s.core.withPending(
		fmt.Sprintf("Append %s conversation messages for %s/%s", stage, projectID, id),
		func() string { return s.core.taskDir(projectID, id) },
		func() error {
			var err error
			conv, err = s.files.AppendConversationMessages(projectID, id, stage, msgs...)
			return err
		},
	)
	if err != nil {
		return task.Conversation{}, err
	}
	return conv, nil
}

// ReplaceConversationMessages overwrites via the wrapped task.FileStore and
// enqueues the result to be committed on the push worker's next tick.
func (s *TaskStore) ReplaceConversationMessages(projectID, id, stage string, msgs []task.ConversationMessage) (task.Conversation, error) {
	var conv task.Conversation
	err := s.core.withPending(
		fmt.Sprintf("Replace %s conversation messages for %s/%s", stage, projectID, id),
		func() string { return s.core.taskDir(projectID, id) },
		func() error {
			var err error
			conv, err = s.files.ReplaceConversationMessages(projectID, id, stage, msgs)
			return err
		},
	)
	if err != nil {
		return task.Conversation{}, err
	}
	return conv, nil
}

// GetTaskDraftConversation delegates straight to the wrapped
// task.FileStore.
func (s *TaskStore) GetTaskDraftConversation(projectID, sessionID string) (task.Conversation, error) {
	return s.files.GetTaskDraftConversation(projectID, sessionID)
}

// taskDraftDir returns the directory holding a task-drafts session's
// conversation.yaml, mirroring task.FileStore's own taskDraftDir layout —
// the same rationale as core.taskDir above: `git add`-ing just this
// directory scopes a commit to exactly this session's own change.
func (c *core) taskDraftDir(projectID, sessionID string) string {
	return filepath.Join(c.root, "projects", projectID, "task-drafts", sessionID)
}

// AppendTaskDraftConversationMessages appends via the wrapped
// task.FileStore and enqueues the result to be committed on the push
// worker's next tick.
func (s *TaskStore) AppendTaskDraftConversationMessages(projectID, sessionID string, msgs ...task.ConversationMessage) (task.Conversation, error) {
	var conv task.Conversation
	err := s.core.withPending(
		fmt.Sprintf("Append task draft conversation messages for %s/%s", projectID, sessionID),
		func() string { return s.core.taskDraftDir(projectID, sessionID) },
		func() error {
			var err error
			conv, err = s.files.AppendTaskDraftConversationMessages(projectID, sessionID, msgs...)
			return err
		},
	)
	if err != nil {
		return task.Conversation{}, err
	}
	return conv, nil
}

// ReplaceTaskDraftConversationMessages overwrites via the wrapped
// task.FileStore and enqueues the result to be committed on the push
// worker's next tick.
func (s *TaskStore) ReplaceTaskDraftConversationMessages(projectID, sessionID string, msgs []task.ConversationMessage) (task.Conversation, error) {
	var conv task.Conversation
	err := s.core.withPending(
		fmt.Sprintf("Replace task draft conversation messages for %s/%s", projectID, sessionID),
		func() string { return s.core.taskDraftDir(projectID, sessionID) },
		func() error {
			var err error
			conv, err = s.files.ReplaceTaskDraftConversationMessages(projectID, sessionID, msgs)
			return err
		},
	)
	if err != nil {
		return task.Conversation{}, err
	}
	return conv, nil
}

// FinalizeRequirements persists via the wrapped task.FileStore and enqueues
// the result to be committed on the push worker's next tick.
func (s *TaskStore) FinalizeRequirements(projectID, id string, draft task.RequirementsDraft) (task.Task, error) {
	var t task.Task
	err := s.core.withPending(
		fmt.Sprintf("Finalize requirements for %s/%s", projectID, id),
		func() string { return s.core.taskDir(projectID, id) },
		func() error {
			var err error
			t, err = s.files.FinalizeRequirements(projectID, id, draft)
			return err
		},
	)
	if err != nil {
		return task.Task{}, err
	}
	return t, nil
}

// FinalizePlan persists via the wrapped task.FileStore and enqueues the
// result to be committed on the push worker's next tick.
func (s *TaskStore) FinalizePlan(projectID, id string, plan task.Plan) (task.Task, error) {
	var t task.Task
	err := s.core.withPending(
		fmt.Sprintf("Finalize plan for %s/%s", projectID, id),
		func() string { return s.core.taskDir(projectID, id) },
		func() error {
			var err error
			t, err = s.files.FinalizePlan(projectID, id, plan)
			return err
		},
	)
	if err != nil {
		return task.Task{}, err
	}
	return t, nil
}

// FinalizeReview persists via the wrapped task.FileStore and enqueues the
// result to be committed on the push worker's next tick.
func (s *TaskStore) FinalizeReview(projectID, id string, draft task.ReviewDraft) (task.Task, error) {
	var t task.Task
	err := s.core.withPending(
		fmt.Sprintf("Finalize review for %s/%s", projectID, id),
		func() string { return s.core.taskDir(projectID, id) },
		func() error {
			var err error
			t, err = s.files.FinalizeReview(projectID, id, draft)
			return err
		},
	)
	if err != nil {
		return task.Task{}, err
	}
	return t, nil
}

// MarkPRMerged persists via the wrapped task.FileStore and enqueues the
// result to be committed on the push worker's next tick.
func (s *TaskStore) MarkPRMerged(projectID, id string) (task.Task, error) {
	var t task.Task
	err := s.core.withPending(
		fmt.Sprintf("Mark PR merged for %s/%s", projectID, id),
		func() string { return s.core.taskDir(projectID, id) },
		func() error {
			var err error
			t, err = s.files.MarkPRMerged(projectID, id)
			return err
		},
	)
	if err != nil {
		return task.Task{}, err
	}
	return t, nil
}

// RecordPullRequest persists via the wrapped task.FileStore and enqueues
// the result to be committed on the push worker's next tick.
func (s *TaskStore) RecordPullRequest(projectID, id string, pr task.PullRequest) (task.Task, error) {
	var t task.Task
	err := s.core.withPending(
		fmt.Sprintf("Record pull request for %s/%s", projectID, id),
		func() string { return s.core.taskDir(projectID, id) },
		func() error {
			var err error
			t, err = s.files.RecordPullRequest(projectID, id, pr)
			return err
		},
	)
	if err != nil {
		return task.Task{}, err
	}
	return t, nil
}

// ReviseToRequirements persists via the wrapped task.FileStore and enqueues
// the result to be committed on the push worker's next tick.
func (s *TaskStore) ReviseToRequirements(projectID, id, reason string) (task.Task, error) {
	var t task.Task
	err := s.core.withPending(
		fmt.Sprintf("Revise %s/%s to requirements", projectID, id),
		func() string { return s.core.taskDir(projectID, id) },
		func() error {
			var err error
			t, err = s.files.ReviseToRequirements(projectID, id, reason)
			return err
		},
	)
	if err != nil {
		return task.Task{}, err
	}
	return t, nil
}

// ReviseToPlanning persists via the wrapped task.FileStore and enqueues the
// result to be committed on the push worker's next tick.
func (s *TaskStore) ReviseToPlanning(projectID, id, reason string) (task.Task, error) {
	var t task.Task
	err := s.core.withPending(
		fmt.Sprintf("Revise %s/%s to planning", projectID, id),
		func() string { return s.core.taskDir(projectID, id) },
		func() error {
			var err error
			t, err = s.files.ReviseToPlanning(projectID, id, reason)
			return err
		},
	)
	if err != nil {
		return task.Task{}, err
	}
	return t, nil
}

// NextExecutionID delegates straight to the wrapped task.FileStore — it
// never writes anything (see task.FileStore.NextExecutionID's doc
// comment), so nothing is enqueued.
func (s *TaskStore) NextExecutionID(projectID, id string) (string, error) {
	return s.files.NextExecutionID(projectID, id)
}

// RecordExecution persists via the wrapped task.FileStore and enqueues the
// result to be committed on the push worker's next tick.
func (s *TaskStore) RecordExecution(projectID, id string, exec task.Execution) (task.Execution, error) {
	var recorded task.Execution
	err := s.core.withPending(
		fmt.Sprintf("Record execution %s for %s/%s", exec.ExecutionID, projectID, id),
		func() string { return s.core.taskDir(projectID, id) },
		func() error {
			var err error
			recorded, err = s.files.RecordExecution(projectID, id, exec)
			return err
		},
	)
	if err != nil {
		return task.Execution{}, err
	}
	return recorded, nil
}

// ListExecutions delegates straight to the wrapped task.FileStore.
func (s *TaskStore) ListExecutions(projectID, id string) ([]task.Execution, error) {
	return s.files.ListExecutions(projectID, id)
}

// CreateExecutionLog writes via the wrapped task.FileStore and enqueues the
// result to be committed on the push worker's next tick — same treatment
// RecordExecution gets, so the log's existence is itself durable/pushed
// git history, not just a local file.
func (s *TaskStore) CreateExecutionLog(projectID, id, executionID string) error {
	return s.core.withPending(
		fmt.Sprintf("Start execution log %s for %s/%s", executionID, projectID, id),
		func() string { return s.core.taskDir(projectID, id) },
		func() error { return s.files.CreateExecutionLog(projectID, id, executionID) },
	)
}

// AppendExecutionLogEvent writes via the wrapped task.FileStore and
// enqueues the result to be committed on the push worker's next tick, same
// as every other task mutation — including the log's own append writes,
// so every event an execution attempt produces ends up in durable, pushed
// git history, not just a local file that could vanish with the machine.
func (s *TaskStore) AppendExecutionLogEvent(projectID, id, executionID string, ev task.ExecutionLogEvent) error {
	return s.core.withPending(
		fmt.Sprintf("Append execution log event for %s/%s execution %s", projectID, id, executionID),
		func() string { return s.core.taskDir(projectID, id) },
		func() error { return s.files.AppendExecutionLogEvent(projectID, id, executionID, ev) },
	)
}

// GetExecutionLog delegates straight to the wrapped task.FileStore.
func (s *TaskStore) GetExecutionLog(projectID, id, executionID string) (task.ExecutionLog, error) {
	return s.files.GetExecutionLog(projectID, id, executionID)
}

// ListReviews delegates straight to the wrapped task.FileStore.
func (s *TaskStore) ListReviews(projectID, id string) ([]task.Review, error) {
	return s.files.ListReviews(projectID, id)
}

// ListStageTransitions delegates straight to the wrapped task.FileStore —
// a read, no git or locking involved (the writes happen inside
// FinalizeRequirements/FinalizePlan/FinalizeReview/MarkPRMerged/
// ReviseToRequirements/ReviseToPlanning/RecordExecution above, each of
// which already commits the whole task directory via withPending, so
// transitions.yaml rides along in the same commit automatically).
func (s *TaskStore) ListStageTransitions(projectID, id string) ([]task.StageTransition, error) {
	return s.files.ListStageTransitions(projectID, id)
}
