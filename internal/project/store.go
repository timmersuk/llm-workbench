package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrInvalidID is returned by FileStore.Get when the id is empty or contains
// path separators/"..".
var ErrInvalidID = errors.New("invalid project id")

// Store lists and retrieves projects.
type Store interface {
	List() ([]Project, error)
	Get(id string) (Project, error)
}

// FileStore is a read-only Store backed by a directory of
// projects/<id>/project.yaml files.
type FileStore struct {
	Root string
}

// NewFileStore returns a FileStore rooted at root (the directory containing
// project subdirectories).
func NewFileStore(root string) *FileStore {
	return &FileStore{Root: root}
}

// List returns every project under Root, sorted by id. Non-directory entries
// are skipped.
func (s *FileStore) List() ([]Project, error) {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return nil, fmt.Errorf("reading project root %s: %w", s.Root, err)
	}

	var projects []Project
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		p, err := s.readProject(entry.Name())
		if err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}

	sort.Slice(projects, func(i, j int) bool { return projects[i].ID < projects[j].ID })
	return projects, nil
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
