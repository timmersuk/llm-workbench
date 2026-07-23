// Package knowledge provides a store over the OKF (Open Knowledge Format)
// concept bundle at data/knowledge/, per docs/knowledge schema v0.md — a
// Get/List/Put surface (Milestone 9) widened from the narrower read-only
// Get-by-ID slice Milestone 4 built to seed GrillMe/Planning Mode's
// interviews with the content behind a Project's or Task's referenced
// knowledge concept IDs. See that doc's §6 ("exposed as a narrow
// provider-shaped interface") and the "Providers are replaceable" invariant.
package knowledge

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sirupsen/logrus"
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

// reservedConceptNames are the two filenames OKF reserves at any level of
// the bundle (docs/knowledge schema v0.md §4) — index/log files, never
// concept documents, so List skips them rather than treating them as
// malformed concepts.
var reservedConceptNames = map[string]bool{"index.md": true, "log.md": true}

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

// ConceptSummary is the browsable subset of a Concept's frontmatter List
// returns — enough for a consumer to decide whether to Get the full
// document, without reading and parsing every file in the bundle just to
// browse it.
type ConceptSummary struct {
	ConceptID   string
	Type        string
	Title       string
	Description string
	Tags        []string
}

// FileStore resolves concept IDs (e.g. "coding-standards/logging") to
// Concepts under a bundle root (data/knowledge with the default
// WORKSPACE_ROOT), and lists/writes concept documents there.
type FileStore struct {
	Root string
}

// NewFileStore returns a FileStore rooted at root.
func NewFileStore(root string) *FileStore {
	return &FileStore{Root: root}
}

// frontmatterPattern splits a concept document into its YAML frontmatter
// block and markdown body: a leading "---" line, the frontmatter, a
// closing "---" line, then the body (which may be empty).
var frontmatterPattern = regexp.MustCompile(`(?s)\A---\n(.*?)\n---\n?(.*)\z`)

// parseConcept splits content (an already-read, CRLF-normalized concept
// document) into a Concept, tolerating unknown frontmatter fields per
// docs/knowledge schema v0.md §2. Shared by Get (one file, error on any
// problem) and List (every file in the bundle, skip-and-log on a per-file
// problem).
func parseConcept(content string) (Concept, error) {
	match := frontmatterPattern.FindStringSubmatch(content)
	if match == nil {
		return Concept{}, ErrMissingType
	}

	var fm map[string]any
	if err := yaml.Unmarshal([]byte(match[1]), &fm); err != nil {
		return Concept{}, fmt.Errorf("parsing frontmatter: %w", err)
	}

	conceptType, _ := fm["type"].(string)
	if conceptType == "" {
		return Concept{}, ErrMissingType
	}

	return Concept{
		Type:        conceptType,
		Frontmatter: fm,
		Body:        match[2],
	}, nil
}

// Get reads <Root>/<conceptID>.md, splits its YAML frontmatter from the
// markdown body, and parses the frontmatter tolerating unknown fields. A
// missing file surfaces as the underlying fs.ErrNotExist (via %w).
func (s *FileStore) Get(conceptID string) (Concept, error) {
	if err := validateConceptID(conceptID); err != nil {
		return Concept{}, err
	}

	path := filepath.Join(s.Root, filepath.FromSlash(conceptID)+".md")

	data, err := os.ReadFile(path)
	if err != nil {
		return Concept{}, fmt.Errorf("reading %s: %w", path, err)
	}

	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	concept, err := parseConcept(content)
	if err != nil {
		return Concept{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return concept, nil
}

// List walks every concept document under Root (skipping index.md/log.md,
// OKF's reserved filenames) and returns a ConceptSummary per concept,
// enough to browse without fetching each one's full body. A concept that
// fails to parse (missing type, malformed frontmatter) is logged and
// skipped rather than failing the whole listing — the same "one bad entry
// doesn't fail everything" spirit stage_conversation.go's buildStagePrompt
// already applies when resolving a Project/Task's referenced concept IDs.
// A missing Root is not an error: an empty, not-yet-populated bundle is a
// normal starting state, so this returns an empty slice rather than
// surfacing fs.ErrNotExist.
func (s *FileStore) List() ([]ConceptSummary, error) {
	var summaries []ConceptSummary

	err := filepath.WalkDir(s.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && path == s.Root {
				return filepath.SkipDir
			}
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".md" || reservedConceptNames[d.Name()] {
			return nil
		}

		rel, relErr := filepath.Rel(s.Root, path)
		if relErr != nil {
			return relErr
		}
		conceptID := filepath.ToSlash(strings.TrimSuffix(rel, ".md"))

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			logrus.WithError(readErr).WithField("concept", conceptID).Warn("knowledge: skipping concept that failed to read")
			return nil
		}
		content := strings.ReplaceAll(string(data), "\r\n", "\n")
		concept, parseErr := parseConcept(content)
		if parseErr != nil {
			logrus.WithError(parseErr).WithField("concept", conceptID).Warn("knowledge: skipping concept that failed to parse")
			return nil
		}

		title, _ := concept.Frontmatter["title"].(string)
		description, _ := concept.Frontmatter["description"].(string)
		summaries = append(summaries, ConceptSummary{
			ConceptID:   conceptID,
			Type:        concept.Type,
			Title:       title,
			Description: description,
			Tags:        stringTags(concept.Frontmatter["tags"]),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing concepts under %s: %w", s.Root, err)
	}
	return summaries, nil
}

// stringTags best-effort coerces a frontmatter "tags" value (parsed by
// yaml.v3 as []any for a YAML sequence) into []string, dropping any
// non-string entries rather than failing the whole concept over one
// malformed tag.
func stringTags(v any) []string {
	seq, ok := v.([]any)
	if !ok {
		return nil
	}
	tags := make([]string, 0, len(seq))
	for _, item := range seq {
		if s, ok := item.(string); ok {
			tags = append(tags, s)
		}
	}
	return tags
}

// Put writes c as <Root>/<conceptID>.md — a whole-file replace, never a
// partial/merge update, matching the propose_knowledge Draft tool's own
// whole-content-every-time shape (docs/milestones/done/milestone9.md: "always
// carrying the full resulting content... never a diff"). c.Type is
// authoritative and is written into the serialized frontmatter's "type"
// key even if c.Frontmatter disagrees or omits it, so Get(conceptID) after
// a Put always sees a consistent value. Creates conceptID's parent
// directories as needed.
func (s *FileStore) Put(conceptID string, c Concept) error {
	if err := validateConceptID(conceptID); err != nil {
		return err
	}
	if c.Type == "" {
		return ErrMissingType
	}

	fm := make(map[string]any, len(c.Frontmatter)+1)
	for k, v := range c.Frontmatter {
		fm[k] = v
	}
	fm["type"] = c.Type

	fmBytes, err := yaml.Marshal(fm)
	if err != nil {
		return fmt.Errorf("encoding frontmatter for %s: %w", conceptID, err)
	}

	path := filepath.Join(s.Root, filepath.FromSlash(conceptID)+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating directory for %s: %w", conceptID, err)
	}

	var b strings.Builder
	b.WriteString("---\n")
	b.Write(fmBytes)
	b.WriteString("---\n")
	b.WriteString(c.Body)

	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
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
