// Package project provides the Project type and a file-backed store over a
// projects/ directory (rooted at WORKSPACE_ROOT, which defaults to data/).
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

// CreateInput is the client-supplied shape for creating a project. It has no
// id field: project ids are always server-derived from Name by slugifying
// it, never client-specified (unlike task ids, which the client chooses).
type CreateInput struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Repositories []string `json:"repositories"`
	Knowledge    []string `json:"knowledge"`
	Constraints  []string `json:"constraints"`
}

// UpdateInput is the client-supplied shape for editing a project. Like
// CreateInput, it has no id field — a project's id is immutable once
// created and always comes from the URL path, never the request body.
type UpdateInput struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Repositories []string `json:"repositories"`
	Knowledge    []string `json:"knowledge"`
	Constraints  []string `json:"constraints"`
}
