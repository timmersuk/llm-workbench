package task

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"gopkg.in/yaml.v3"
)

var idPattern = regexp.MustCompile(`^TASK-\d+$`)

// ErrInvalidID is returned by FileStore.Get when the id doesn't match the
// TASK-<digits> pattern.
var ErrInvalidID = errors.New("invalid task id")

// Store lists and retrieves tasks.
type Store interface {
	List() ([]Task, error)
	Get(id string) (Task, error)
}

// FileStore is a read-only Store backed by a directory of tasks/<id>/task.yaml
// files.
type FileStore struct {
	Root string
}

// NewFileStore returns a FileStore rooted at root (the directory containing
// TASK-* subdirectories).
func NewFileStore(root string) *FileStore {
	return &FileStore{Root: root}
}

// List returns every task under Root, sorted by id. Entries that are not
// directories, or whose name doesn't match TASK-<digits>, are skipped.
func (s *FileStore) List() ([]Task, error) {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return nil, fmt.Errorf("reading task root %s: %w", s.Root, err)
	}

	var tasks []Task
	for _, entry := range entries {
		if !entry.IsDir() || !idPattern.MatchString(entry.Name()) {
			continue
		}

		t, err := s.readTask(entry.Name())
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}

	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	return tasks, nil
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
