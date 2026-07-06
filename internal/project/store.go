package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

// ErrInvalidID is returned when an id is empty or contains path
// separators/"..".
var ErrInvalidID = errors.New("invalid project id")

// ErrMissingName is returned by Create/Update when Name is blank.
var ErrMissingName = errors.New("missing name")

// ErrAlreadyExists is returned by Create when the slug derived from Name
// already names an existing project. Names are not disambiguated (no
// "-2" suffixing) — a colliding name is a real conflict the caller must
// resolve, e.g. by picking a different name.
var ErrAlreadyExists = errors.New("project already exists")

var slugInvalidRun = regexp.MustCompile(`[^a-z0-9]+`)

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

// Store lists, retrieves, creates, and updates projects, and resolves the
// task-store root for a given project id.
type Store interface {
	List() (ListResult, error)
	Get(id string) (Project, error)
	Create(in CreateInput) (Project, error)
	Update(id string, in UpdateInput) (Project, error)
	TasksRoot(id string) (string, error)
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
	if err := validateID(id); err != nil {
		return Project{}, err
	}
	return s.readProject(id)
}

// Create derives a project id by slugifying in.Name and writes a new
// project.yaml under <root>/<id>/. Names are not disambiguated: if the
// derived id already exists, Create fails with ErrAlreadyExists rather than
// silently picking a different id.
func (s *FileStore) Create(in CreateInput) (Project, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return Project{}, ErrMissingName
	}

	id := slugify(name)
	if _, err := os.Stat(filepath.Join(s.Root, id)); err == nil {
		return Project{}, fmt.Errorf("creating project %s: %w", id, ErrAlreadyExists)
	} else if !os.IsNotExist(err) {
		return Project{}, fmt.Errorf("checking existing project %s: %w", id, err)
	}

	now := time.Now().UTC()
	p := Project{
		ID:           id,
		Name:         in.Name,
		Description:  in.Description,
		Repositories: in.Repositories,
		Knowledge:    in.Knowledge,
		Constraints:  in.Constraints,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := s.writeProject(p); err != nil {
		return Project{}, err
	}
	return p, nil
}

// Update overwrites the project at id with in's fields, preserving id and
// CreatedAt from the existing project.yaml and bumping UpdatedAt.
func (s *FileStore) Update(id string, in UpdateInput) (Project, error) {
	if err := validateID(id); err != nil {
		return Project{}, err
	}

	existing, err := s.readProject(id)
	if err != nil {
		return Project{}, err
	}

	name := strings.TrimSpace(in.Name)
	if name == "" {
		return Project{}, ErrMissingName
	}

	p := Project{
		ID:           id,
		Name:         in.Name,
		Description:  in.Description,
		Repositories: in.Repositories,
		Knowledge:    in.Knowledge,
		Constraints:  in.Constraints,
		CreatedAt:    existing.CreatedAt,
		UpdatedAt:    time.Now().UTC(),
	}

	if err := s.writeProject(p); err != nil {
		return Project{}, err
	}
	return p, nil
}

// TasksRoot returns the directory containing task subdirectories for the
// given project id (<root>/<id>/tasks), without checking the project
// actually exists — callers that need a 404 for a missing project should
// call Get first.
func (s *FileStore) TasksRoot(id string) (string, error) {
	if err := validateID(id); err != nil {
		return "", err
	}
	return filepath.Join(s.Root, id, "tasks"), nil
}

func (s *FileStore) writeProject(p Project) error {
	dir := filepath.Join(s.Root, p.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating project directory %s: %w", dir, err)
	}

	data, err := yaml.Marshal(p)
	if err != nil {
		return fmt.Errorf("encoding project %s: %w", p.ID, err)
	}

	path := filepath.Join(dir, "project.yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
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

// validateID rejects ids that are empty or contain path separators/"..",
// guarding against path traversal before the id is joined into a
// filesystem path. Checked before every path-joining operation.
func validateID(id string) error {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	return nil
}

// slugify lowercases name and collapses runs of non-alphanumeric characters
// into a single '-', trimming leading/trailing '-'. Falls back to "project"
// if nothing alphanumeric remains.
func slugify(name string) string {
	s := strings.ToLower(name)
	s = slugInvalidRun.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "project"
	}
	return s
}
