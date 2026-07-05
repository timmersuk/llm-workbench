// Package task provides the Task type and a read-only file-backed store over
// the tasks/ directory, per docs/task schema v0.md.
package task

import "time"

// Task is a versioned intent object stored as tasks/<id>/task.yaml.
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
