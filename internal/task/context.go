package task

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Context is the optional, derived narrative context behind a task, stored
// as <task root>/<id>/context.yaml. Field-for-field mirror of
// docs/task schema v0.md §3 ("context.yaml"). Produced by GrillMe via
// FinalizeRequirements (lifecycle.go), never hand-authored.
type Context struct {
	Summary       string   `yaml:"summary" json:"summary"`
	Background    string   `yaml:"background" json:"background"`
	Files         []string `yaml:"files" json:"files"`
	Detail        string   `yaml:"detail" json:"detail"`
	Verification  []string `yaml:"verification" json:"verification"`
	OpenQuestions []string `yaml:"open_questions" json:"open_questions"`
}

// RequirementsDraft is the wire shape GrillMe's propose_context tool call
// and Finalize endpoint use — it spans both the task.yaml-subset fields
// GrillMe is responsible for and the full Context, since GrillMe is the
// primary way both get set (see CONTEXT.md's "Draft" entry). It's a
// request/response body only, never persisted as this exact shape, same
// rationale as project.CreateInput/UpdateInput.
type RequirementsDraft struct {
	Objective       string   `json:"objective"`
	Constraints     []string `json:"constraints"`
	Assumptions     []string `json:"assumptions"`
	SuccessCriteria []string `json:"success_criteria"`
	Context         Context  `json:"context"`
}

// GetContext returns the task's context.yaml. A task that hasn't finished
// GrillMe yet has no context.yaml at all — that's reported as the
// underlying fs.ErrNotExist (via %w), the same "not yet finalized" signal
// GetPlan uses, not a distinct sentinel.
func (s *FileStore) GetContext(id string) (Context, error) {
	if err := validateID(id); err != nil {
		return Context{}, err
	}

	path := filepath.Join(s.Root, id, "context.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return Context{}, fmt.Errorf("reading %s: %w", path, err)
	}

	var c Context
	if err := yaml.Unmarshal(data, &c); err != nil {
		return Context{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return c, nil
}

// writeContext writes context.yaml for id, creating the task directory if
// it doesn't already exist (it always will by this point, since a task's
// directory is created at task creation, but MkdirAll is a no-op in that
// case and keeps this symmetric with writeTask).
func (s *FileStore) writeContext(id string, c Context) error {
	dir := filepath.Join(s.Root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating task directory %s: %w", dir, err)
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encoding context for %s: %w", id, err)
	}

	path := filepath.Join(dir, "context.yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
