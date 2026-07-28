package task

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

// ErrInvalidID is returned when an id is empty or contains path
// separators/"..".
var ErrInvalidID = errors.New("invalid task id")

// ErrAlreadyExists is returned by Create when the given id already names an
// existing task within this store's root. Ids are client-specified and not
// disambiguated — a collision is a real conflict the caller must resolve.
var ErrAlreadyExists = errors.New("task already exists")

// ErrStageImmutable is returned by Update when the body's stage doesn't
// match the task's current stage. Stage only ever moves through the
// guarded Finalize*/Revise*/MarkPRMerged transitions in lifecycle.go, each
// of which checks the task's current stage before changing it and commits
// the new value itself — Update is the generic "edit task in place" path
// and must never be a second, unguarded way to change Stage (it would
// bypass every one of those checks and let gitstore commit the jump as if
// it were legitimate). A body that simply echoes the task's current stage
// (the common case: a client re-submitting the full object it just read)
// is not an error, matching ErrIDMismatch's "omitted or already-correct is
// fine, wrong is not" treatment below.
var ErrStageImmutable = errors.New("stage can only be changed via workflow actions (Finalize/Revise/MarkPRMerged), not a direct update")

// ErrIDMismatch is returned by Update when the body's id doesn't match the
// id being updated. A task's id (and project) never change after creation.
var ErrIDMismatch = errors.New("task id mismatch")

// LoadError describes a single task directory that failed to load during
// List, so one malformed task doesn't take the rest of the list down with
// it.
type LoadError struct {
	ID    string `yaml:"id" json:"id"`
	Error string `yaml:"error" json:"error"`
}

// ListResult holds the tasks that loaded successfully plus any per-task
// errors encountered along the way.
type ListResult struct {
	Tasks  []Task      `yaml:"tasks" json:"tasks"`
	Errors []LoadError `yaml:"errors" json:"errors"`
}

// Store lists, retrieves, creates, and updates tasks across every project,
// keyed by an explicit projectID on every call — a single process-wide
// store, not one instance per project (see FileStore's doc comment).
type Store interface {
	List(projectID string) (ListResult, error)
	Get(projectID, id string) (Task, error)
	Create(projectID string, t Task) (Task, error)
	Update(projectID, id string, t Task) (Task, error)
}

// FileStore is a Store backed by a directory of
// <projectID>/tasks/<id>/task.yaml files (e.g.
// data/projects/<projectId>/tasks/<id>/task.yaml with the default
// WORKSPACE_ROOT), rooted at the shared projects root — the same Root value
// as project.FileStore, since tasks nest under their owning project's
// directory. A single FileStore instance serves every project (a
// process-wide singleton, constructed once in cmd/server/main.go), not one
// instance per project as earlier milestones did: every method takes an
// explicit projectID so the caller (internal/api) — not a factory closure —
// names which project's tasks it means.
type FileStore struct {
	Root string
}

// NewFileStore returns a FileStore rooted at root (the shared projects
// directory containing every <projectID>/ subdirectory).
func NewFileStore(root string) *FileStore {
	return &FileStore{Root: root}
}

// taskDir returns the directory holding id's task.yaml (and its sibling
// context.yaml/plan.yaml/conversation-*.yaml/executions//reviews/) within
// projectID's tasks root.
func (s *FileStore) taskDir(projectID, id string) string {
	return filepath.Join(s.Root, projectID, "tasks", id)
}

// tasksRoot returns the directory containing every task subdirectory for
// projectID.
func (s *FileStore) tasksRoot(projectID string) string {
	return filepath.Join(s.Root, projectID, "tasks")
}

// List returns every task under projectID's tasks root, sorted by id.
// Non-directory entries are silently skipped. Directory entries that fail
// to read or parse (e.g. no task.yaml inside) are skipped too, logged, and
// reported in the result's Errors rather than failing the whole call — one
// malformed task shouldn't take down every other task.
func (s *FileStore) List(projectID string) (ListResult, error) {
	if err := validateID(projectID); err != nil {
		return ListResult{}, err
	}

	root := s.tasksRoot(projectID)
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return ListResult{}, nil
	}
	if err != nil {
		return ListResult{}, fmt.Errorf("reading task root %s: %w", root, err)
	}

	var result ListResult
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		t, err := s.readTask(projectID, entry.Name())
		if err != nil {
			logrus.WithError(err).WithField("id", entry.Name()).Warn("skipping task that failed to load")
			result.Errors = append(result.Errors, LoadError{ID: entry.Name(), Error: err.Error()})
			continue
		}
		result.Tasks = append(result.Tasks, t)
	}

	sort.Slice(result.Tasks, func(i, j int) bool { return result.Tasks[i].ID < result.Tasks[j].ID })
	sort.Slice(result.Errors, func(i, j int) bool { return result.Errors[i].ID < result.Errors[j].ID })
	return result, nil
}

// Get returns the task with the given id within projectID. It rejects ids
// containing path separators or ".." to guard against path traversal,
// since task (and project) ids are client-chosen slugs rather than a fixed
// pattern.
func (s *FileStore) Get(projectID, id string) (Task, error) {
	if err := validateID(projectID); err != nil {
		return Task{}, err
	}
	if err := validateID(id); err != nil {
		return Task{}, err
	}
	return s.readTask(projectID, id)
}

// Create writes a new task under projectID's tasks root. t.ID must be a
// valid, non-colliding id — ids are client-specified and not
// disambiguated, so Create fails with ErrAlreadyExists if the id is already
// taken within this project. CreatedAt/UpdatedAt are always server-set,
// ignoring any client-supplied values. Stage is likewise always server-set
// to "requirements" — a task always starts at the beginning of its
// lifecycle; only Finalize/Revise (lifecycle.go) move Stage after creation.
// Project is always server-set to projectID, ignoring any client-supplied
// value — like id, a task's project never changes after creation.
func (s *FileStore) Create(projectID string, t Task) (Task, error) {
	if err := validateID(projectID); err != nil {
		return Task{}, err
	}
	if err := validateID(t.ID); err != nil {
		return Task{}, err
	}

	if _, err := os.Stat(s.taskDir(projectID, t.ID)); err == nil {
		return Task{}, fmt.Errorf("creating task %s: %w", t.ID, ErrAlreadyExists)
	} else if !os.IsNotExist(err) {
		return Task{}, fmt.Errorf("checking existing task %s: %w", t.ID, err)
	}

	now := time.Now().UTC()
	t.Project = projectID
	t.CreatedAt = now
	t.UpdatedAt = now
	t.Stage = StageRequirements

	if err := s.writeTask(projectID, t); err != nil {
		return Task{}, err
	}
	return t, nil
}

// Update overwrites the task at id with t's fields, preserving id,
// CreatedAt, and Stage from the existing task.yaml and bumping UpdatedAt.
// If t.ID is set, it must match id (ErrIDMismatch) — a task's id, like its
// project, never changes after creation. Project is always server-set to
// projectID. If t.Stage is set, it must match the existing stage
// (ErrStageImmutable) — Stage only ever changes through lifecycle.go's
// guarded transitions, never through this generic update path.
func (s *FileStore) Update(projectID, id string, t Task) (Task, error) {
	if err := validateID(projectID); err != nil {
		return Task{}, err
	}
	if err := validateID(id); err != nil {
		return Task{}, err
	}
	if t.ID != "" && t.ID != id {
		return Task{}, fmt.Errorf("updating task %s: %w", id, ErrIDMismatch)
	}

	existing, err := s.readTask(projectID, id)
	if err != nil {
		return Task{}, err
	}
	if t.Stage != "" && t.Stage != existing.Stage {
		return Task{}, fmt.Errorf("updating task %s: %w", id, ErrStageImmutable)
	}

	t.ID = id
	t.Project = projectID
	t.CreatedAt = existing.CreatedAt
	t.Stage = existing.Stage
	t.UpdatedAt = time.Now().UTC()

	if err := s.writeTask(projectID, t); err != nil {
		return Task{}, err
	}
	return t, nil
}

func (s *FileStore) writeTask(projectID string, t Task) error {
	dir := s.taskDir(projectID, t.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating task directory %s: %w", dir, err)
	}

	data, err := yaml.Marshal(t)
	if err != nil {
		return fmt.Errorf("encoding task %s: %w", t.ID, err)
	}

	path := filepath.Join(dir, "task.yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

func (s *FileStore) readTask(projectID, id string) (Task, error) {
	path := filepath.Join(s.taskDir(projectID, id), "task.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return Task{}, fmt.Errorf("reading %s: %w", path, err)
	}

	var t Task
	if err := yaml.Unmarshal(data, &t); err != nil {
		return Task{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return t, nil
}

// validateID rejects ids that are empty or contain path separators/"..",
// guarding against path traversal before the id is joined into a
// filesystem path. Checked before every path-joining operation. Used for
// both projectID and task id — both are client-chosen slugs.
func validateID(id string) error {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	return nil
}
