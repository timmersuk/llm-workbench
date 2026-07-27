package gitstore

import (
	"fmt"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

// commitAuthor identifies every commit this store makes — a fixed,
// workbench-owned identity rather than any real human's, since these
// commits are mechanical persistence writes (the human decision already
// happened at the API layer that called Create/Update), not authored
// changes attributable to a person. Mirrors gitutil.CommitAll's own
// "mechanical safety commit" posture for the same reason.
var commitAuthor = &object.Signature{Name: "llm-workbench", Email: "llm-workbench@localhost"}

// commit stages every change under root (`git add -A` via go-git's
// AddOptions{All: true}) and commits it with message, timestamped now.
// Must only be called with c.mu already held — see core.mu's doc comment
// for why the whole write (FileStore write + stage + commit), not just
// this step, needs to be serialized.
//
// If nothing actually changed (Status().IsClean()), this is a no-op rather
// than an empty commit — defensive only: every caller here follows a
// FileStore write that always changes something, so this path shouldn't
// normally be reached, but a no-op is strictly safer than either an empty
// commit or a spurious error.
func (c *core) commit(message string) error {
	wt, err := c.repo.Worktree()
	if err != nil {
		return fmt.Errorf("resolving worktree: %w", err)
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return fmt.Errorf("staging changes: %w", err)
	}
	status, err := wt.Status()
	if err != nil {
		return fmt.Errorf("checking worktree status: %w", err)
	}
	if status.IsClean() {
		return nil
	}
	author := *commitAuthor
	author.When = time.Now().UTC()
	if _, err := wt.Commit(message, &git.CommitOptions{Author: &author}); err != nil {
		return fmt.Errorf("committing: %w", err)
	}
	return nil
}

// withCommit runs fn (a FileStore write) with c.mu held, then commits
// message if fn succeeded. fn's own error, if any, is returned unchanged
// without attempting a commit — whatever fn already wrote to disk (if
// anything) is left uncommitted rather than lost: it's picked up
// automatically by this store's next successful commit's own `git add
// -A`, rather than needing its own recovery path.
func (c *core) withCommit(message string, fn func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := fn(); err != nil {
		return err
	}
	if err := c.commit(message); err != nil {
		return fmt.Errorf("committing %q: %w", message, err)
	}
	return nil
}

// --- project.Store / api.ProjectStore ---

// List delegates straight to the wrapped project.FileStore — a read, no
// git or locking involved.
func (s *ProjectStore) List() (project.ListResult, error) { return s.files.List() }

// Get delegates straight to the wrapped project.FileStore.
func (s *ProjectStore) Get(id string) (project.Project, error) { return s.files.Get(id) }

// Create writes a new project via the wrapped project.FileStore and
// commits the result synchronously and locally.
func (s *ProjectStore) Create(in project.CreateInput) (project.Project, error) {
	var created project.Project
	err := s.core.withCommit(fmt.Sprintf("Create project %q", in.Name), func() error {
		var err error
		created, err = s.files.Create(in)
		return err
	})
	if err != nil {
		return project.Project{}, err
	}
	return created, nil
}

// Update overwrites an existing project via the wrapped project.FileStore
// and commits the result synchronously and locally.
func (s *ProjectStore) Update(id string, in project.UpdateInput) (project.Project, error) {
	var updated project.Project
	err := s.core.withCommit(fmt.Sprintf("Update project %s", id), func() error {
		var err error
		updated, err = s.files.Update(id, in)
		return err
	})
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

// Create writes a new task via the wrapped task.FileStore and commits the
// result synchronously and locally.
func (s *TaskStore) Create(projectID string, t task.Task) (task.Task, error) {
	var created task.Task
	err := s.core.withCommit(fmt.Sprintf("Create task %s/%s", projectID, t.ID), func() error {
		var err error
		created, err = s.files.Create(projectID, t)
		return err
	})
	if err != nil {
		return task.Task{}, err
	}
	return created, nil
}

// Update overwrites an existing task via the wrapped task.FileStore and
// commits the result synchronously and locally.
func (s *TaskStore) Update(projectID, id string, t task.Task) (task.Task, error) {
	var updated task.Task
	err := s.core.withCommit(fmt.Sprintf("Update task %s/%s", projectID, id), func() error {
		var err error
		updated, err = s.files.Update(projectID, id, t)
		return err
	})
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
// commits the result synchronously and locally.
func (s *TaskStore) AppendConversationMessages(projectID, id, stage string, msgs ...task.ConversationMessage) (task.Conversation, error) {
	var conv task.Conversation
	err := s.core.withCommit(fmt.Sprintf("Append %s conversation messages for %s/%s", stage, projectID, id), func() error {
		var err error
		conv, err = s.files.AppendConversationMessages(projectID, id, stage, msgs...)
		return err
	})
	if err != nil {
		return task.Conversation{}, err
	}
	return conv, nil
}

// ReplaceConversationMessages overwrites via the wrapped task.FileStore and
// commits the result synchronously and locally.
func (s *TaskStore) ReplaceConversationMessages(projectID, id, stage string, msgs []task.ConversationMessage) (task.Conversation, error) {
	var conv task.Conversation
	err := s.core.withCommit(fmt.Sprintf("Replace %s conversation messages for %s/%s", stage, projectID, id), func() error {
		var err error
		conv, err = s.files.ReplaceConversationMessages(projectID, id, stage, msgs)
		return err
	})
	if err != nil {
		return task.Conversation{}, err
	}
	return conv, nil
}

// FinalizeRequirements persists via the wrapped task.FileStore and commits
// the result synchronously and locally.
func (s *TaskStore) FinalizeRequirements(projectID, id string, draft task.RequirementsDraft) (task.Task, error) {
	var t task.Task
	err := s.core.withCommit(fmt.Sprintf("Finalize requirements for %s/%s", projectID, id), func() error {
		var err error
		t, err = s.files.FinalizeRequirements(projectID, id, draft)
		return err
	})
	if err != nil {
		return task.Task{}, err
	}
	return t, nil
}

// FinalizePlan persists via the wrapped task.FileStore and commits the
// result synchronously and locally.
func (s *TaskStore) FinalizePlan(projectID, id string, plan task.Plan) (task.Task, error) {
	var t task.Task
	err := s.core.withCommit(fmt.Sprintf("Finalize plan for %s/%s", projectID, id), func() error {
		var err error
		t, err = s.files.FinalizePlan(projectID, id, plan)
		return err
	})
	if err != nil {
		return task.Task{}, err
	}
	return t, nil
}

// FinalizeReview persists via the wrapped task.FileStore and commits the
// result synchronously and locally.
func (s *TaskStore) FinalizeReview(projectID, id string, draft task.ReviewDraft) (task.Task, error) {
	var t task.Task
	err := s.core.withCommit(fmt.Sprintf("Finalize review for %s/%s", projectID, id), func() error {
		var err error
		t, err = s.files.FinalizeReview(projectID, id, draft)
		return err
	})
	if err != nil {
		return task.Task{}, err
	}
	return t, nil
}

// MarkPRMerged persists via the wrapped task.FileStore and commits the
// result synchronously and locally.
func (s *TaskStore) MarkPRMerged(projectID, id string) (task.Task, error) {
	var t task.Task
	err := s.core.withCommit(fmt.Sprintf("Mark PR merged for %s/%s", projectID, id), func() error {
		var err error
		t, err = s.files.MarkPRMerged(projectID, id)
		return err
	})
	if err != nil {
		return task.Task{}, err
	}
	return t, nil
}

// RecordPullRequest persists via the wrapped task.FileStore and commits
// the result synchronously and locally.
func (s *TaskStore) RecordPullRequest(projectID, id string, pr task.PullRequest) (task.Task, error) {
	var t task.Task
	err := s.core.withCommit(fmt.Sprintf("Record pull request for %s/%s", projectID, id), func() error {
		var err error
		t, err = s.files.RecordPullRequest(projectID, id, pr)
		return err
	})
	if err != nil {
		return task.Task{}, err
	}
	return t, nil
}

// ReviseToRequirements persists via the wrapped task.FileStore and commits
// the result synchronously and locally.
func (s *TaskStore) ReviseToRequirements(projectID, id string) (task.Task, error) {
	var t task.Task
	err := s.core.withCommit(fmt.Sprintf("Revise %s/%s to requirements", projectID, id), func() error {
		var err error
		t, err = s.files.ReviseToRequirements(projectID, id)
		return err
	})
	if err != nil {
		return task.Task{}, err
	}
	return t, nil
}

// ReviseToPlanning persists via the wrapped task.FileStore and commits the
// result synchronously and locally.
func (s *TaskStore) ReviseToPlanning(projectID, id string) (task.Task, error) {
	var t task.Task
	err := s.core.withCommit(fmt.Sprintf("Revise %s/%s to planning", projectID, id), func() error {
		var err error
		t, err = s.files.ReviseToPlanning(projectID, id)
		return err
	})
	if err != nil {
		return task.Task{}, err
	}
	return t, nil
}

// NextExecutionID delegates straight to the wrapped task.FileStore — it
// never writes anything (see task.FileStore.NextExecutionID's doc
// comment), so no commit is needed.
func (s *TaskStore) NextExecutionID(projectID, id string) (string, error) {
	return s.files.NextExecutionID(projectID, id)
}

// RecordExecution persists via the wrapped task.FileStore and commits the
// result synchronously and locally.
func (s *TaskStore) RecordExecution(projectID, id string, exec task.Execution) (task.Execution, error) {
	var recorded task.Execution
	err := s.core.withCommit(fmt.Sprintf("Record execution %s for %s/%s", exec.ExecutionID, projectID, id), func() error {
		var err error
		recorded, err = s.files.RecordExecution(projectID, id, exec)
		return err
	})
	if err != nil {
		return task.Execution{}, err
	}
	return recorded, nil
}

// ListExecutions delegates straight to the wrapped task.FileStore.
func (s *TaskStore) ListExecutions(projectID, id string) ([]task.Execution, error) {
	return s.files.ListExecutions(projectID, id)
}

// ListReviews delegates straight to the wrapped task.FileStore.
func (s *TaskStore) ListReviews(projectID, id string) ([]task.Review, error) {
	return s.files.ListReviews(projectID, id)
}
