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

// Store lists, retrieves, creates, and updates tasks within a single
// project's task root.
type Store interface {
	List() (ListResult, error)
	Get(id string) (Task, error)
	Create(t Task) (Task, error)
	Update(id string, t Task) (Task, error)
}

// FileStore is a Store backed by a directory of <id>/task.yaml files (e.g.
// data/projects/<projectId>/tasks/<id>/task.yaml with the default
// WORKSPACE_ROOT), rooted at a single project's tasks directory.
type FileStore struct {
	Root string
}

// NewFileStore returns a FileStore rooted at root (the directory containing
// TASK-* subdirectories).
func NewFileStore(root string) *FileStore {
	return &FileStore{Root: root}
}

// List returns every task under Root, sorted by id. Non-directory entries
// are silently skipped. Directory entries that fail to read or parse (e.g.
// no task.yaml inside) are skipped too, logged, and reported in the
// result's Errors rather than failing the whole call — one malformed task
// shouldn't take down every other task.
func (s *FileStore) List() (ListResult, error) {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return ListResult{}, fmt.Errorf("reading task root %s: %w", s.Root, err)
	}

	var result ListResult
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		t, err := s.readTask(entry.Name())
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

// Get returns the task with the given id. It rejects ids containing path
// separators or ".." to guard against path traversal, since task ids are
// client-chosen slugs rather than a fixed pattern.
func (s *FileStore) Get(id string) (Task, error) {
	if err := validateID(id); err != nil {
		return Task{}, err
	}
	return s.readTask(id)
}

// Create writes a new task under <root>/<t.ID>/task.yaml. t.ID must be a
// valid, non-colliding id — ids are client-specified and not
// disambiguated, so Create fails with ErrAlreadyExists if the id is already
// taken within this store's (project-scoped) root. CreatedAt/UpdatedAt are
// always server-set, ignoring any client-supplied values. Stage/Status are
// likewise always server-set to "requirements"/"draft" — a task always
// starts at the beginning of its lifecycle; only Finalize/Revise
// (lifecycle.go) move Stage after creation.
func (s *FileStore) Create(t Task) (Task, error) {
	if err := validateID(t.ID); err != nil {
		return Task{}, err
	}

	if _, err := os.Stat(filepath.Join(s.Root, t.ID)); err == nil {
		return Task{}, fmt.Errorf("creating task %s: %w", t.ID, ErrAlreadyExists)
	} else if !os.IsNotExist(err) {
		return Task{}, fmt.Errorf("checking existing task %s: %w", t.ID, err)
	}

	now := time.Now().UTC()
	t.CreatedAt = now
	t.UpdatedAt = now
	t.Stage = StageRequirements
	t.Status = "draft"

	if err := s.writeTask(t); err != nil {
		return Task{}, err
	}
	return t, nil
}

// Update overwrites the task at id with t's fields, preserving id and
// CreatedAt from the existing task.yaml and bumping UpdatedAt. If t.ID is
// set, it must match id (ErrIDMismatch) — a task's id, like its project,
// never changes after creation.
func (s *FileStore) Update(id string, t Task) (Task, error) {
	if err := validateID(id); err != nil {
		return Task{}, err
	}
	if t.ID != "" && t.ID != id {
		return Task{}, fmt.Errorf("updating task %s: %w", id, ErrIDMismatch)
	}

	existing, err := s.readTask(id)
	if err != nil {
		return Task{}, err
	}

	t.ID = id
	t.CreatedAt = existing.CreatedAt
	t.UpdatedAt = time.Now().UTC()

	if err := s.writeTask(t); err != nil {
		return Task{}, err
	}
	return t, nil
}

func (s *FileStore) writeTask(t Task) error {
	dir := filepath.Join(s.Root, t.ID)
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

func (s *FileStore) readTask(id string) (Task, error) {
	path := filepath.Join(s.Root, id, "task.yaml")
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
// filesystem path. Checked before every path-joining operation.
func validateID(id string) error {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	return nil
}
