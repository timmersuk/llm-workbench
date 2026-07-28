package task

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/timmersuk/llm-workbench/internal/yamlutil"
)

// Verification step kinds, per docs/adr/0008-structure-context-verification-entries.md
// and CONTEXT.md's **Verification step** entry. Review's per-step
// confirmation phase (Milestone 6) routes on this: an agent_executable step
// is attempted by the reviewing agent directly (hit an endpoint, run a
// command, drive a UI check) and reported; a human_judgment step is left for
// the human to perform, with the agent only recording their confirmation.
const (
	VerificationKindAgentExecutable = "agent_executable"
	VerificationKindHumanJudgment   = "human_judgment"
)

// VerificationStep is one entry in context.yaml's verification list —
// a human-readable Description plus a Kind classifying who performs it
// (agent_executable | human_judgment). Widened from a bare string per
// docs/adr/0008: GrillMe authors the classification once, at the point a
// human is already reviewing the draft context, and Review is its first
// consumer. Field-for-field mirror of docs/task schema v0.md §3.
type VerificationStep struct {
	Description string `yaml:"description" json:"description"`
	Kind        string `yaml:"kind" json:"kind"` // agent_executable | human_judgment
}

// ContextFile is one entry in context.yaml's files list — a repo-relative
// path plus an optional short note on why it matters to this task. Widened
// from a bare path string per docs/adr/0021: both live GrillMe turns and
// hand-authored context.yaml files kept independently reaching for a
// {path, role} shape to explain each file, not just name it.
type ContextFile struct {
	Path string `yaml:"path" json:"path"`
	Role string `yaml:"role,omitempty" json:"role,omitempty"`
}

// Context is the optional, derived narrative context behind a task, stored
// as <task root>/<id>/context.yaml. Field-for-field mirror of
// docs/task schema v0.md §3 ("context.yaml"). Produced by GrillMe via
// FinalizeRequirements (lifecycle.go), never hand-authored.
type Context struct {
	Summary       string             `yaml:"summary" json:"summary"`
	Background    string             `yaml:"background" json:"background"`
	Files         []ContextFile      `yaml:"files" json:"files"`
	Detail        string             `yaml:"detail" json:"detail"`
	Verification  []VerificationStep `yaml:"verification" json:"verification"`
	OpenQuestions []string           `yaml:"open_questions" json:"open_questions"`
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
func (s *FileStore) GetContext(projectID, id string) (Context, error) {
	if err := validateID(projectID); err != nil {
		return Context{}, err
	}
	if err := validateID(id); err != nil {
		return Context{}, err
	}

	path := filepath.Join(s.taskDir(projectID, id), "context.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return Context{}, fmt.Errorf("reading %s: %w", path, err)
	}

	var c Context
	if err := yamlutil.Unmarshal(data, &c); err != nil {
		return Context{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return c, nil
}

// writeContext writes context.yaml for id, creating the task directory if
// it doesn't already exist (it always will by this point, since a task's
// directory is created at task creation, but MkdirAll is a no-op in that
// case and keeps this symmetric with writeTask).
func (s *FileStore) writeContext(projectID, id string, c Context) error {
	dir := s.taskDir(projectID, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating task directory %s: %w", dir, err)
	}

	data, err := yamlutil.Marshal(c)
	if err != nil {
		return fmt.Errorf("encoding context for %s: %w", id, err)
	}

	path := filepath.Join(dir, "context.yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
