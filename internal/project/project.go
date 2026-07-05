// Package project provides the Project type and a read-only file-backed store
// over a projects/ directory (rooted at WORKSPACE_ROOT, which defaults to
// data/).
package project

import "time"

// Project is a stable grouping and context scope for tasks, stored as
// <project root>/<id>/project.yaml (e.g. data/projects/<id>/project.yaml
// with the default WORKSPACE_ROOT).
type Project struct {
	ID          string `yaml:"id" json:"id"`
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`

	Repositories []string `yaml:"repositories" json:"repositories"`
	Knowledge    []string `yaml:"knowledge" json:"knowledge"`
	Constraints  []string `yaml:"constraints" json:"constraints"`

	CreatedAt time.Time `yaml:"created_at" json:"created_at"`
	UpdatedAt time.Time `yaml:"updated_at" json:"updated_at"`
}
