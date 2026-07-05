package task

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

var idPattern = regexp.MustCompile(`^TASK-\d+$`)

// ErrInvalidID is returned by FileStore.Get when the id doesn't match the
// TASK-<digits> pattern.
var ErrInvalidID = errors.New("invalid task id")

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

// Store lists and retrieves tasks.
type Store interface {
	List() (ListResult, error)
	Get(id string) (Task, error)
}

// FileStore is a read-only Store backed by a directory of <id>/task.yaml
// files (e.g. data/tasks/<id>/task.yaml with the default WORKSPACE_ROOT).
type FileStore struct {
	Root string
}

// NewFileStore returns a FileStore rooted at root (the directory containing
// TASK-* subdirectories).
func NewFileStore(root string) *FileStore {
	return &FileStore{Root: root}
}

// List returns every task under Root, sorted by id. Entries that are not
// directories, or whose name doesn't match TASK-<digits>, are silently
// skipped. Entries that do match but fail to read or parse are skipped too,
// logged, and reported in the result's Errors rather than failing the whole
// call — one malformed task.yaml shouldn't take down every other task.
func (s *FileStore) List() (ListResult, error) {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return ListResult{}, fmt.Errorf("reading task root %s: %w", s.Root, err)
	}

	var result ListResult
	for _, entry := range entries {
		if !entry.IsDir() || !idPattern.MatchString(entry.Name()) {
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

// Get returns the task with the given id. It returns an error if id doesn't
// match the TASK-<digits> pattern (this also guards against path traversal).
func (s *FileStore) Get(id string) (Task, error) {
	if !idPattern.MatchString(id) {
		return Task{}, fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	return s.readTask(id)
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
