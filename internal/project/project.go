// Package project provides the Project type and a read-only file-backed store
// over the projects/ directory.
package project

import "time"

// Project is a stable grouping and context scope for tasks, stored as
// projects/<id>/project.yaml.
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
