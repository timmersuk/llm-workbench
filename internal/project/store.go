package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

// ErrInvalidID is returned by FileStore.Get when the id is empty or contains
// path separators/"..".
var ErrInvalidID = errors.New("invalid project id")

// LoadError describes a single project directory that failed to load during
// List, so one malformed project doesn't take the rest of the list down
// with it.
type LoadError struct {
	ID    string `yaml:"id" json:"id"`
	Error string `yaml:"error" json:"error"`
}

// ListResult holds the projects that loaded successfully plus any per-
// project errors encountered along the way.
type ListResult struct {
	Projects []Project   `yaml:"projects" json:"projects"`
	Errors   []LoadError `yaml:"errors" json:"errors"`
}

// Store lists and retrieves projects.
type Store interface {
	List() (ListResult, error)
	Get(id string) (Project, error)
}

// FileStore is a read-only Store backed by a directory of <id>/project.yaml
// files (e.g. data/projects/<id>/project.yaml with the default
// WORKSPACE_ROOT).
type FileStore struct {
	Root string
}

// NewFileStore returns a FileStore rooted at root (the directory containing
// project subdirectories).
func NewFileStore(root string) *FileStore {
	return &FileStore{Root: root}
}

// List returns every project under Root, sorted by id. Non-directory
// entries are silently skipped. Directory entries that fail to read or
// parse are skipped too, logged, and reported in the result's Errors rather
// than failing the whole call — one malformed project.yaml shouldn't take
// down every other project.
func (s *FileStore) List() (ListResult, error) {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return ListResult{}, fmt.Errorf("reading project root %s: %w", s.Root, err)
	}

	var result ListResult
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		p, err := s.readProject(entry.Name())
		if err != nil {
			logrus.WithError(err).WithField("id", entry.Name()).Warn("skipping project that failed to load")
			result.Errors = append(result.Errors, LoadError{ID: entry.Name(), Error: err.Error()})
			continue
		}
		result.Projects = append(result.Projects, p)
	}

	sort.Slice(result.Projects, func(i, j int) bool { return result.Projects[i].ID < result.Projects[j].ID })
	sort.Slice(result.Errors, func(i, j int) bool { return result.Errors[i].ID < result.Errors[j].ID })
	return result, nil
}

// Get returns the project with the given id. It rejects ids containing path
// separators or ".." to guard against path traversal, since project ids are
// free-form slugs rather than a fixed pattern.
func (s *FileStore) Get(id string) (Project, error) {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return Project{}, fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	return s.readProject(id)
}

func (s *FileStore) readProject(id string) (Project, error) {
	path := filepath.Join(s.Root, id, "project.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return Project{}, fmt.Errorf("reading %s: %w", path, err)
	}

	var p Project
	if err := yaml.Unmarshal(data, &p); err != nil {
		return Project{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return p, nil
}
