package task

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// Review decision values, per docs/task schema v0.md and CONTEXT.md's
// **Review** entry. A review's decision drives FinalizeReview's stage
// transition (lifecycle.go): approved → complete, needs_changes →
// implementation, rejected → requirements.
const (
	ReviewDecisionApproved     = "approved"
	ReviewDecisionRejected     = "rejected"
	ReviewDecisionNeedsChanges = "needs_changes"
)

// ErrReviewAlreadyExists is returned by RecordReview when a review with the
// same review_id was already recorded — the reviews/ store is append-only,
// never overwritten, exactly like executions/ (ErrExecutionAlreadyExists).
var ErrReviewAlreadyExists = errors.New("review already exists")

// Review is one review verdict, stored as
// <task root>/<id>/reviews/<review_id>.yaml — one file per cycle, never
// overwritten. A task that bounces review → implementation (needs_changes) →
// review records a new review-NNN.yaml each time, so the full history of
// every verdict is a first-class queryable fact, the same way every
// execution attempt already is. Field-for-field mirror of
// docs/task schema v0.md's review.yaml.
type Review struct {
	ReviewID string `yaml:"review_id" json:"review_id"`
	TaskID   string `yaml:"task_id" json:"task_id"`
	// ExecutionID names the specific execution attempt this verdict is
	// about, set by FinalizeReview (lifecycle.go) at the one moment it's
	// unambiguous: the task is confirmed at StageReview when FinalizeReview
	// runs, and only a successful execution ever advances Stage there, with
	// no new execution recordable while still at review — so
	// ListExecutions' last entry at that exact moment is guaranteed to be
	// the reviewed attempt. Recording it explicitly here means later
	// consumers (resolveReviewContinuation, internal/api/execution.go) look
	// this up directly instead of re-inferring "which execution was this
	// review about" after however many further stage transitions or failed
	// retries have happened since.
	ExecutionID string    `yaml:"execution_id" json:"execution_id"`
	Decision    string    `yaml:"decision" json:"decision"` // approved | rejected | needs_changes
	Notes       string    `yaml:"notes" json:"notes"`
	CreatedAt   time.Time `yaml:"created_at" json:"created_at"`
}

// ReviewDraft is the wire shape the propose_review tool call and the Review
// Finalize endpoint use — a three-way decision plus notes about a prior
// execution's diff (CONTEXT.md's **Review** entry). It's a request/response
// body only, never persisted as this exact shape (FinalizeReview turns it
// into a persisted Review), same rationale as RequirementsDraft.
type ReviewDraft struct {
	Decision string `json:"decision"` // approved | rejected | needs_changes
	Notes    string `json:"notes"`
}

// reviewIDPattern matches the "review-NNN" ids this package generates
// (NextReviewID) and expects (RecordReview), mirroring executionIDPattern.
var reviewIDPattern = regexp.MustCompile(`^review-(\d{3,})$`)

func reviewsDir(dir string) string {
	return filepath.Join(dir, "reviews")
}

func reviewPath(dir, reviewID string) string {
	return filepath.Join(reviewsDir(dir), reviewID+".yaml")
}

// NextReviewID returns the next sequential "review-NNN" id for id's task, by
// scanning its reviews/ directory for the highest existing number —
// "review-001" if none exist yet. Mirrors NextExecutionID: it never writes
// anything, so a caller that ends up not recording a review simply leaves a
// gap in the sequence, which is fine — ids only need to be unique and
// increasing, not contiguous.
func (s *FileStore) NextReviewID(projectID, id string) (string, error) {
	if err := validateID(projectID); err != nil {
		return "", err
	}
	if err := validateID(id); err != nil {
		return "", err
	}

	entries, err := os.ReadDir(reviewsDir(s.taskDir(projectID, id)))
	if os.IsNotExist(err) {
		return "review-001", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading reviews dir for %s: %w", id, err)
	}

	max := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := filepath.Ext(name)
		if ext != ".yaml" {
			continue
		}
		m := reviewIDPattern.FindStringSubmatch(name[:len(name)-len(ext)])
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if n > max {
			max = n
		}
	}
	return fmt.Sprintf("review-%03d", max+1), nil
}

// RecordReview persists review under id's reviews/ directory —
// ErrReviewAlreadyExists if that review_id was already recorded
// (append-only, never overwritten). TaskID and CreatedAt are always
// server-set, matching RecordExecution. Unlike RecordExecution, this never
// touches the task's Stage: the review verdict's stage transition is
// FinalizeReview's responsibility (lifecycle.go), keeping this a pure
// append-only store.
func (s *FileStore) RecordReview(projectID, id string, review Review) (Review, error) {
	if err := validateID(projectID); err != nil {
		return Review{}, err
	}
	if err := validateID(id); err != nil {
		return Review{}, err
	}
	if err := validateID(review.ReviewID); err != nil {
		return Review{}, err
	}

	taskDir := s.taskDir(projectID, id)
	path := reviewPath(taskDir, review.ReviewID)
	if _, err := os.Stat(path); err == nil {
		return Review{}, fmt.Errorf("recording review %s for %s: %w", review.ReviewID, id, ErrReviewAlreadyExists)
	} else if !os.IsNotExist(err) {
		return Review{}, fmt.Errorf("checking existing review %s for %s: %w", review.ReviewID, id, err)
	}

	review.TaskID = id
	review.CreatedAt = time.Now().UTC()

	dir := reviewsDir(taskDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Review{}, fmt.Errorf("creating reviews directory %s: %w", dir, err)
	}

	data, err := yaml.Marshal(review)
	if err != nil {
		return Review{}, fmt.Errorf("encoding review %s for %s: %w", review.ReviewID, id, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return Review{}, fmt.Errorf("writing %s: %w", path, err)
	}

	return review, nil
}

// ListReviews returns every recorded Review for id, sorted by review_id
// (ascending, so the zero-padded "review-NNN" format also sorts
// chronologically). A task with no reviews/ directory yet returns an empty
// slice, not an error — the same "missing file means normal starting state"
// treatment ListExecutions gives.
func (s *FileStore) ListReviews(projectID, id string) ([]Review, error) {
	if err := validateID(projectID); err != nil {
		return nil, err
	}
	if err := validateID(id); err != nil {
		return nil, err
	}

	dir := reviewsDir(s.taskDir(projectID, id))
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading reviews dir for %s: %w", id, err)
	}

	var reviews []Review
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		var review Review
		if err := yaml.Unmarshal(data, &review); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}
		reviews = append(reviews, review)
	}

	sort.Slice(reviews, func(i, j int) bool {
		ni, iOK := reviewSeq(reviews[i].ReviewID)
		nj, jOK := reviewSeq(reviews[j].ReviewID)
		if iOK && jOK {
			return ni < nj
		}
		return reviews[i].ReviewID < reviews[j].ReviewID
	})
	return reviews, nil
}

// reviewSeq extracts the numeric sequence from a "review-NNN" id, for
// numeric (not lexical) ordering — mirrors execution.go's executionSeq and
// exists for the same reason: past review-999, lexical string comparison
// would put "review-1000" before "review-999", silently breaking every
// "the last entry in ListReviews is the latest verdict" convention this
// codebase relies on.
func reviewSeq(id string) (int, bool) {
	m := reviewIDPattern.FindStringSubmatch(id)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}
