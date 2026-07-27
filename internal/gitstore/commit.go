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

// commit stages every change under Root (`git add -A` via go-git's
// AddOptions{All: true}) and commits it with message, timestamped now.
// Must only be called with s.mu already held — see Store.mu's doc comment
// for why the whole write (FileStore write + stage + commit), not just
// this step, needs to be serialized.
//
// If nothing actually changed (Status().IsClean()), this is a no-op rather
// than an empty commit — defensive only: every caller here follows a
// FileStore write that always changes something, so this path shouldn't
// normally be reached, but a no-op is strictly safer than either an empty
// commit or a spurious error.
func (s *Store) commit(message string) error {
	wt, err := s.repo.Worktree()
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

// withCommit runs fn (a FileStore write) with s.mu held, then commits
// message if fn succeeded. fn's own error, if any, is returned unchanged
// without attempting a commit — whatever fn already wrote to disk (if
// anything) is left uncommitted rather than lost: it's picked up
// automatically by this store's next successful commit's own `git add
// -A`, rather than needing its own recovery path.
func (s *Store) withCommit(message string, fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := fn(); err != nil {
		return err
	}
	if err := s.commit(message); err != nil {
		return fmt.Errorf("committing %q: %w", message, err)
	}
	return nil
}

// --- project.Store / api.ProjectStore ---

// List delegates straight to the wrapped project.FileStore — a read, no
// git or locking involved.
func (s *Store) List() (project.ListResult, error) { return s.projects.List() }

// Get delegates straight to the wrapped project.FileStore.
func (s *Store) Get(id string) (project.Project, error) { return s.projects.Get(id) }

// Create writes a new project via the wrapped project.FileStore and
// commits the result synchronously and locally.
func (s *Store) Create(in project.CreateInput) (project.Project, error) {
	var created project.Project
	err := s.withCommit(fmt.Sprintf("Create project %q", in.Name), func() error {
		var err error
		created, err = s.projects.Create(in)
		return err
	})
	if err != nil {
		return project.Project{}, err
	}
	return created, nil
}

// Update overwrites an existing project via the wrapped project.FileStore
// and commits the result synchronously and locally.
func (s *Store) Update(id string, in project.UpdateInput) (project.Project, error) {
	var updated project.Project
	err := s.withCommit(fmt.Sprintf("Update project %s", id), func() error {
		var err error
		updated, err = s.projects.Update(id, in)
		return err
	})
	if err != nil {
		return project.Project{}, err
	}
	return updated, nil
}

// --- task.Store / api.TaskStore ---

// List delegates straight to the wrapped task.FileStore.
func (s *Store) List(projectID string) (task.ListResult, error) { return s.tasks.List(projectID) }

// Get delegates straight to the wrapped task.FileStore.
func (s *Store) Get(projectID, id string) (task.Task, error) { return s.tasks.Get(projectID, id) }

// Create writes a new task via the wrapped task.FileStore and commits the
// result synchronously and locally.
func (s *Store) Create(projectID string, t task.Task) (task.Task, error) {
	var created task.Task
	err := s.withCommit(fmt.Sprintf("Create task %s/%s", projectID, t.ID), func() error {
		var err error
		created, err = s.tasks.Create(projectID, t)
		return err
	})
	if err != nil {
		return task.Task{}, err
	}
	return created, nil
}

// Update overwrites an existing task via the wrapped task.FileStore and
// commits the result synchronously and locally.
func (s *Store) Update(projectID, id string, t task.Task) (task.Task, error) {
	var updated task.Task
	err := s.withCommit(fmt.Sprintf("Update task %s/%s", projectID, id), func() error {
		var err error
		updated, err = s.tasks.Update(projectID, id, t)
		return err
	})
	if err != nil {
		return task.Task{}, err
	}
	return updated, nil
}

// GetContext delegates straight to the wrapped task.FileStore.
func (s *Store) GetContext(projectID, id string) (task.Context, error) {
	return s.tasks.GetContext(projectID, id)
}

// GetPlan delegates straight to the wrapped task.FileStore.
func (s *Store) GetPlan(projectID, id string) (task.Plan, error) {
	return s.tasks.GetPlan(projectID, id)
}

// GetConversation delegates straight to the wrapped task.FileStore.
func (s *Store) GetConversation(projectID, id, stage string) (task.Conversation, error) {
	return s.tasks.GetConversation(projectID, id, stage)
}

// AppendConversationMessages appends via the wrapped task.FileStore and
// commits the result synchronously and locally.
func (s *Store) AppendConversationMessages(projectID, id, stage string, msgs ...task.ConversationMessage) (task.Conversation, error) {
	var conv task.Conversation
	err := s.withCommit(fmt.Sprintf("Append %s conversation messages for %s/%s", stage, projectID, id), func() error {
		var err error
		conv, err = s.tasks.AppendConversationMessages(projectID, id, stage, msgs...)
		return err
	})
	if err != nil {
		return task.Conversation{}, err
	}
	return conv, nil
}

// ReplaceConversationMessages overwrites via the wrapped task.FileStore and
// commits the result synchronously and locally.
func (s *Store) ReplaceConversationMessages(projectID, id, stage string, msgs []task.ConversationMessage) (task.Conversation, error) {
	var conv task.Conversation
	err := s.withCommit(fmt.Sprintf("Replace %s conversation messages for %s/%s", stage, projectID, id), func() error {
		var err error
		conv, err = s.tasks.ReplaceConversationMessages(projectID, id, stage, msgs)
		return err
	})
	if err != nil {
		return task.Conversation{}, err
	}
	return conv, nil
}

// FinalizeRequirements persists via the wrapped task.FileStore and commits
// the result synchronously and locally.
func (s *Store) FinalizeRequirements(projectID, id string, draft task.RequirementsDraft) (task.Task, error) {
	var t task.Task
	err := s.withCommit(fmt.Sprintf("Finalize requirements for %s/%s", projectID, id), func() error {
		var err error
		t, err = s.tasks.FinalizeRequirements(projectID, id, draft)
		return err
	})
	if err != nil {
		return task.Task{}, err
	}
	return t, nil
}

// FinalizePlan persists via the wrapped task.FileStore and commits the
// result synchronously and locally.
func (s *Store) FinalizePlan(projectID, id string, plan task.Plan) (task.Task, error) {
	var t task.Task
	err := s.withCommit(fmt.Sprintf("Finalize plan for %s/%s", projectID, id), func() error {
		var err error
		t, err = s.tasks.FinalizePlan(projectID, id, plan)
		return err
	})
	if err != nil {
		return task.Task{}, err
	}
	return t, nil
}

// FinalizeReview persists via the wrapped task.FileStore and commits the
// result synchronously and locally.
func (s *Store) FinalizeReview(projectID, id string, draft task.ReviewDraft) (task.Task, error) {
	var t task.Task
	err := s.withCommit(fmt.Sprintf("Finalize review for %s/%s", projectID, id), func() error {
		var err error
		t, err = s.tasks.FinalizeReview(projectID, id, draft)
		return err
	})
	if err != nil {
		return task.Task{}, err
	}
	return t, nil
}

// MarkPRMerged persists via the wrapped task.FileStore and commits the
// result synchronously and locally.
func (s *Store) MarkPRMerged(projectID, id string) (task.Task, error) {
	var t task.Task
	err := s.withCommit(fmt.Sprintf("Mark PR merged for %s/%s", projectID, id), func() error {
		var err error
		t, err = s.tasks.MarkPRMerged(projectID, id)
		return err
	})
	if err != nil {
		return task.Task{}, err
	}
	return t, nil
}

// RecordPullRequest persists via the wrapped task.FileStore and commits
// the result synchronously and locally.
func (s *Store) RecordPullRequest(projectID, id string, pr task.PullRequest) (task.Task, error) {
	var t task.Task
	err := s.withCommit(fmt.Sprintf("Record pull request for %s/%s", projectID, id), func() error {
		var err error
		t, err = s.tasks.RecordPullRequest(projectID, id, pr)
		return err
	})
	if err != nil {
		return task.Task{}, err
	}
	return t, nil
}

// ReviseToRequirements persists via the wrapped task.FileStore and commits
// the result synchronously and locally.
func (s *Store) ReviseToRequirements(projectID, id string) (task.Task, error) {
	var t task.Task
	err := s.withCommit(fmt.Sprintf("Revise %s/%s to requirements", projectID, id), func() error {
		var err error
		t, err = s.tasks.ReviseToRequirements(projectID, id)
		return err
	})
	if err != nil {
		return task.Task{}, err
	}
	return t, nil
}

// ReviseToPlanning persists via the wrapped task.FileStore and commits the
// result synchronously and locally.
func (s *Store) ReviseToPlanning(projectID, id string) (task.Task, error) {
	var t task.Task
	err := s.withCommit(fmt.Sprintf("Revise %s/%s to planning", projectID, id), func() error {
		var err error
		t, err = s.tasks.ReviseToPlanning(projectID, id)
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
func (s *Store) NextExecutionID(projectID, id string) (string, error) {
	return s.tasks.NextExecutionID(projectID, id)
}

// RecordExecution persists via the wrapped task.FileStore and commits the
// result synchronously and locally.
func (s *Store) RecordExecution(projectID, id string, exec task.Execution) (task.Execution, error) {
	var recorded task.Execution
	err := s.withCommit(fmt.Sprintf("Record execution %s for %s/%s", exec.ExecutionID, projectID, id), func() error {
		var err error
		recorded, err = s.tasks.RecordExecution(projectID, id, exec)
		return err
	})
	if err != nil {
		return task.Execution{}, err
	}
	return recorded, nil
}

// ListExecutions delegates straight to the wrapped task.FileStore.
func (s *Store) ListExecutions(projectID, id string) ([]task.Execution, error) {
	return s.tasks.ListExecutions(projectID, id)
}

// ListReviews delegates straight to the wrapped task.FileStore.
func (s *Store) ListReviews(projectID, id string) ([]task.Review, error) {
	return s.tasks.ListReviews(projectID, id)
}
