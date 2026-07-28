package task

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/timmersuk/llm-workbench/internal/yamlutil"
)

// Plan is the optional generated execution plan, stored as
// <task root>/<id>/plan.yaml. Field-for-field mirror of
// docs/task schema v0.md §3 ("plan.yaml"). Produced by Planning Mode via
// FinalizePlan (lifecycle.go) — the finalize wire body is this type
// directly, no separate draft type needed since Plan has no overlap with
// task.yaml's own fields (unlike RequirementsDraft/Context).
type Plan struct {
	Approach            string   `yaml:"approach" json:"approach"`
	Steps               []string `yaml:"steps" json:"steps"`
	Risks               []string `yaml:"risks" json:"risks"`
	EstimatedComplexity string   `yaml:"estimated_complexity" json:"estimated_complexity"` // low | medium | high
	RecommendedExecutor string   `yaml:"recommended_executor" json:"recommended_executor"`
}

// GetPlan returns the task's plan.yaml. A task that hasn't finished
// Planning Mode yet has no plan.yaml at all — reported as the underlying
// fs.ErrNotExist (via %w).
func (s *FileStore) GetPlan(projectID, id string) (Plan, error) {
	if err := validateID(projectID); err != nil {
		return Plan{}, err
	}
	if err := validateID(id); err != nil {
		return Plan{}, err
	}

	path := filepath.Join(s.taskDir(projectID, id), "plan.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return Plan{}, fmt.Errorf("reading %s: %w", path, err)
	}

	var p Plan
	if err := yamlutil.Unmarshal(data, &p); err != nil {
		return Plan{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return p, nil
}

// writePlan writes plan.yaml for id.
func (s *FileStore) writePlan(projectID, id string, p Plan) error {
	dir := s.taskDir(projectID, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating task directory %s: %w", dir, err)
	}

	data, err := yamlutil.Marshal(p)
	if err != nil {
		return fmt.Errorf("encoding plan for %s: %w", id, err)
	}

	path := filepath.Join(dir, "plan.yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
