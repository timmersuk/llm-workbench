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
const (
	StageRequirements   = "requirements"
	StagePlanning       = "planning"
	StageImplementation = "implementation"
	StageReview         = "review"
	StageComplete       = "complete"
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
}

// References links a task to durable knowledge and code repositories.
type References struct {
	Knowledge []string `yaml:"knowledge" json:"knowledge"`
	Repo      []string `yaml:"repo" json:"repo"`
}
