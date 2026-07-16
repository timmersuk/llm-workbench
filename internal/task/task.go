// Package task provides the Task type and a file-backed store over a
// project's tasks/ directory, per docs/task schema v0.md. A task belongs to
// exactly one project permanently; this package has no notion of "project"
// beyond the opaque Project field — the store is always constructed rooted
// at a single project's task directory (see project.FileStore.TasksRoot),
// and a task's id is unique only within that root, not globally.
package task

import "time"

// Stage values, per docs/task schema v0.md §2. Declared as constants so
// lifecycle.go's transitions don't rely on string literals scattered across
// the package.
//
// StagePRReview is introduced by Milestone 7 PR 1 (docs/milestones/milestone7.md)
// as real, unit-tested machinery, but is not yet reachable through any live
// path: FinalizeReview's approved branch still targets StageMerged until a
// later PR retargets it alongside the "Push & Open PR" action and its
// frontend.
const (
	StageRequirements   = "requirements"
	StagePlanning       = "planning"
	StageImplementation = "implementation"
	StageReview         = "review"
	StagePRReview       = "pr_review"
	StageMerged         = "merged"
)

// Task is a versioned intent object stored as <task root>/<id>/task.yaml
// (e.g. data/projects/<projectId>/tasks/<id>/task.yaml with the default
// WORKSPACE_ROOT).
type Task struct {
	ID    string `yaml:"id" json:"id"`
	Title string `yaml:"title" json:"title"`

	Project string `yaml:"project" json:"project"`

	Status string `yaml:"status" json:"status"`
	Stage  string `yaml:"stage" json:"stage"`

	CreatedAt time.Time `yaml:"created_at" json:"created_at"`
	UpdatedAt time.Time `yaml:"updated_at" json:"updated_at"`

	Objective string `yaml:"objective" json:"objective"`

	Constraints     []string `yaml:"constraints" json:"constraints"`
	Assumptions     []string `yaml:"assumptions" json:"assumptions"`
	SuccessCriteria []string `yaml:"success_criteria" json:"success_criteria"`

	References References `yaml:"references" json:"references"`

	// PullRequest is set once the (not-yet-built) "Push & Open PR" action
	// has pushed this task's execution branch and opened a PR. Absent
	// (nil) until then — a task has at most one open PR at a time by
	// construction, so this is not an append-only store the way
	// reviews/executions are.
	PullRequest *PullRequest `yaml:"pull_request,omitempty" json:"pull_request,omitempty"`
}

// PullRequest records the GitHub PR opened for a task's approved execution,
// per docs/milestones/milestone7.md's "Schema changes".
type PullRequest struct {
	URL    string `yaml:"url" json:"url"`
	Number int    `yaml:"number" json:"number"`
	// Branch is the remote branch the PR actually tracks — needed because
	// a later execution attempt continuing after a rejection cycle lands
	// on a different local branch (ADR 0012 decision 1) and must push onto
	// this recorded branch via refspec, not its own name.
	Branch string `yaml:"branch" json:"branch"`
}

// References links a task to durable knowledge and code repositories.
type References struct {
	Knowledge []string `yaml:"knowledge" json:"knowledge"`
	Repo      []string `yaml:"repo" json:"repo"`
}
