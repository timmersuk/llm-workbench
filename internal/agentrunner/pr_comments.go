package agentrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// PRCommentsYAML is the normalized, chronologically-sorted YAML document
// GitHubPRClient.Comments returns — a distinct named type (not a bare
// string) so a caller can't mistake it for any other formatted text
// (docs/adr/0015-pr-feedback-delivered-as-a-file-not-a-live-tool.md).
type PRCommentsYAML string

// prCommentEntry is one normalized entry spanning all three of GitHub's
// PR-feedback shapes — general conversation comments, review summaries, and
// inline per-line code comments — so mergePRComments produces one
// consistent shape instead of leaving a reader to reconcile three raw,
// differently-cased payloads itself. Path/Line/DiffHunk are only ever set
// on a "inline_comment" entry; State only on a "review" entry.
type prCommentEntry struct {
	Kind      string    `yaml:"kind"` // comment | review | inline_comment
	Author    string    `yaml:"author"`
	CreatedAt time.Time `yaml:"created_at"`
	Body      string    `yaml:"body"`
	State     string    `yaml:"state,omitempty"`
	Path      string    `yaml:"path,omitempty"`
	Line      int       `yaml:"line,omitempty"`
	DiffHunk  string    `yaml:"diff_hunk,omitempty"`
}

// prViewCommentsAndReviews is the shape of `gh pr view <number> --json
// comments,reviews` — gh's own GraphQL-backed, already-paginated JSON
// (camelCase).
type prViewCommentsAndReviews struct {
	Comments []struct {
		Author    prAuthor  `json:"author"`
		Body      string    `json:"body"`
		CreatedAt time.Time `json:"createdAt"`
	} `json:"comments"`
	Reviews []struct {
		Author      prAuthor  `json:"author"`
		Body        string    `json:"body"`
		State       string    `json:"state"`
		SubmittedAt time.Time `json:"submittedAt"`
	} `json:"reviews"`
}

type prAuthor struct {
	Login string `json:"login"`
}

// prReviewComment is one element of the raw REST shape `gh api
// repos/{owner}/{repo}/pulls/{number}/comments` returns (snake_case) — the
// inline per-line code comments, which have no equivalent in `gh pr view
// --json`'s fields.
type prReviewComment struct {
	User      prAuthor  `json:"user"`
	Body      string    `json:"body"`
	Path      string    `json:"path"`
	Line      int       `json:"line"`
	DiffHunk  string    `json:"diff_hunk"`
	CreatedAt time.Time `json:"created_at"`
}

// Comments implements GitHubPRClient. It fetches all three of GitHub's PR
// feedback sources for number and merges them into one normalized YAML
// document (mergePRComments). Inline comments are capped at 100 per page
// (GitHub REST's own maximum) with no further pagination — a PR with more
// than 100 inline comments is a known, unhandled limitation rather than a
// deliberately engineered cap.
func (realGitHubPRClient) Comments(ctx context.Context, dir string, number int) (PRCommentsYAML, error) {
	viewOut, err := runGH(ctx, dir, "pr", "view", strconv.Itoa(number), "--json", "comments,reviews")
	if err != nil {
		return "", fmt.Errorf("fetching PR #%d comments/reviews: %w", number, err)
	}
	inlineOut, err := runGH(ctx, dir, "api", fmt.Sprintf("repos/{owner}/{repo}/pulls/%d/comments", number), "-F", "per_page=100")
	if err != nil {
		return "", fmt.Errorf("fetching PR #%d inline comments: %w", number, err)
	}
	return mergePRComments([]byte(viewOut), []byte(inlineOut))
}

// mergePRComments parses gh's two independently-shaped JSON payloads and
// merges them into one flat, chronologically-sorted PRCommentsYAML document.
// Kept as a pure function, decoupled from the gh subprocess calls, so the
// merge/normalize/sort logic is directly unit-testable against fixture JSON
// without shelling out to a real `gh`.
func mergePRComments(viewJSON, inlineJSON []byte) (PRCommentsYAML, error) {
	var view prViewCommentsAndReviews
	if err := json.Unmarshal(viewJSON, &view); err != nil {
		return "", fmt.Errorf("parsing gh pr view output: %w", err)
	}
	var inline []prReviewComment
	if err := json.Unmarshal(inlineJSON, &inline); err != nil {
		return "", fmt.Errorf("parsing gh api pulls/comments output: %w", err)
	}

	entries := make([]prCommentEntry, 0, len(view.Comments)+len(view.Reviews)+len(inline))
	for _, c := range view.Comments {
		entries = append(entries, prCommentEntry{
			Kind:      "comment",
			Author:    c.Author.Login,
			CreatedAt: c.CreatedAt,
			Body:      c.Body,
		})
	}
	for _, rv := range view.Reviews {
		entries = append(entries, prCommentEntry{
			Kind:      "review",
			Author:    rv.Author.Login,
			CreatedAt: rv.SubmittedAt,
			Body:      rv.Body,
			State:     rv.State,
		})
	}
	for _, ic := range inline {
		entries = append(entries, prCommentEntry{
			Kind:      "inline_comment",
			Author:    ic.User.Login,
			CreatedAt: ic.CreatedAt,
			Body:      ic.Body,
			Path:      ic.Path,
			Line:      ic.Line,
			DiffHunk:  ic.DiffHunk,
		})
	}

	sort.SliceStable(entries, func(i, j int) bool { return entries[i].CreatedAt.Before(entries[j].CreatedAt) })

	out, err := yaml.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("marshaling PR comments: %w", err)
	}
	return PRCommentsYAML(out), nil
}
