// Package knowledge provides a minimal, read-only reader over the OKF
// (Open Knowledge Format) concept bundle at data/knowledge/, per
// docs/knowledge schema v0.md. It is intentionally a narrow Get-by-ID slice
// of the eventual full Knowledge store — no Create/Update/List/index — used
// to seed GrillMe/Planning Mode's interviews with the content behind a
// Project's or Task's referenced knowledge concept IDs. See that doc's §6
// ("exposed as a narrow provider-shaped interface") and the "Providers are
// replaceable" invariant.
package knowledge

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrInvalidConceptID is returned when a concept id is empty, absolute, or
// contains a "." or ".." path segment — checked before the id is joined
// into a filesystem path. Unlike task/project ids, concept ids legitimately
// contain "/" for nested bundle directories, so this is its own guard
// rather than the shared task/project slug validator.
var ErrInvalidConceptID = errors.New("invalid knowledge concept id")

// ErrMissingType is returned when a concept document has no frontmatter
// block, or its frontmatter has no (non-empty) "type" field — the one
// field OKF v0.1 requires.
var ErrMissingType = errors.New("concept missing required type field")

// Concept is one parsed OKF concept document.
type Concept struct {
	Type string
	// Frontmatter holds every parsed frontmatter field, including Type,
	// with unknown producer-defined keys preserved verbatim rather than
	// validated against a closed schema (per docs/knowledge schema v0.md §2).
	Frontmatter map[string]any
	// Body is the markdown body after the frontmatter block, verbatim.
	Body string
}

// FileReader resolves concept IDs (e.g. "coding-standards/logging") to
// Concepts under a bundle root (data/knowledge with the default
// WORKSPACE_ROOT).
type FileReader struct {
	Root string
}

// NewFileReader returns a FileReader rooted at root.
func NewFileReader(root string) *FileReader {
	return &FileReader{Root: root}
}

// frontmatterPattern splits a concept document into its YAML frontmatter
// block and markdown body: a leading "---" line, the frontmatter, a
// closing "---" line, then the body (which may be empty).
var frontmatterPattern = regexp.MustCompile(`(?s)\A---\n(.*?)\n---\n?(.*)\z`)

// Get reads <Root>/<conceptID>.md, splits its YAML frontmatter from the
// markdown body, and parses the frontmatter tolerating unknown fields. A
// missing file surfaces as the underlying fs.ErrNotExist (via %w).
func (r *FileReader) Get(conceptID string) (Concept, error) {
	if err := validateConceptID(conceptID); err != nil {
		return Concept{}, err
	}

	path := filepath.Join(r.Root, filepath.FromSlash(conceptID)+".md")

	data, err := os.ReadFile(path)
	if err != nil {
		return Concept{}, fmt.Errorf("reading %s: %w", path, err)
	}

	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	match := frontmatterPattern.FindStringSubmatch(content)
	if match == nil {
		return Concept{}, fmt.Errorf("parsing %s: %w", path, ErrMissingType)
	}

	var fm map[string]any
	if err := yaml.Unmarshal([]byte(match[1]), &fm); err != nil {
		return Concept{}, fmt.Errorf("parsing frontmatter of %s: %w", path, err)
	}

	conceptType, _ := fm["type"].(string)
	if conceptType == "" {
		return Concept{}, fmt.Errorf("parsing %s: %w", path, ErrMissingType)
	}

	return Concept{
		Type:        conceptType,
		Frontmatter: fm,
		Body:        match[2],
	}, nil
}

// validateConceptID rejects ids that are empty, absolute, or contain a "."
// or ".." path segment, before the id is joined into a filesystem path.
func validateConceptID(id string) error {
	if id == "" {
		return fmt.Errorf("%w: %q", ErrInvalidConceptID, id)
	}

	slashed := filepath.ToSlash(id)
	if strings.HasPrefix(slashed, "/") {
		return fmt.Errorf("%w: %q", ErrInvalidConceptID, id)
	}

	for _, seg := range strings.Split(slashed, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("%w: %q", ErrInvalidConceptID, id)
		}
	}
	return nil
}
