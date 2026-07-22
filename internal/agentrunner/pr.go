package agentrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/timmersuk/llm-workbench/internal/gitutil"
)

// GitHubPRClient is the seam between PushAndOpenPR and the real `gh` CLI,
// per docs/milestones/done/milestone7.md PR 2: both operations are always used
// together on this one path, so a single fake implements both for tests
// rather than wiring two independent function values through the call
// chain. The production implementation (NewGitHubPRClient) shells to real
// `gh` via argv, never a shell string, continuing ADR 0013's discipline.
type GitHubPRClient interface {
	// Create opens a new PR for head against base, returning its URL and
	// number.
	Create(ctx context.Context, dir, head, base, title, body string) (url string, number int, err error)
	// State returns the PR's current state (e.g. "OPEN", "CLOSED",
	// "MERGED").
	State(ctx context.Context, dir string, number int) (state string, err error)
	// Comments returns number's general comments, review summaries, and
	// inline code comments merged into one normalized, chronologically-sorted
	// YAML document (docs/adr/0015-pr-feedback-delivered-as-a-file-not-a-live-tool.md).
	Comments(ctx context.Context, dir string, number int) (PRCommentsYAML, error)
}

// NewGitHubPRClient returns the real GitHubPRClient, shelling out to the
// `gh` CLI. Relies entirely on ambient `gh auth`/git credentials already
// configured on the host machine — never stores, requests, or handles a
// credential itself (docs/milestones/done/milestone7.md's "Out of scope").
func NewGitHubPRClient() GitHubPRClient {
	return realGitHubPRClient{}
}

type realGitHubPRClient struct{}

func (realGitHubPRClient) Create(ctx context.Context, dir, head, base, title, body string) (string, int, error) {
	out, err := runGH(ctx, dir, "pr", "create", "--head", head, "--base", base, "--title", title, "--body", body)
	if err != nil {
		return "", 0, err
	}
	// `gh pr create` prints the new PR's URL as the last line of stdout.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	url := strings.TrimSpace(lines[len(lines)-1])
	number, err := parsePRNumber(url)
	if err != nil {
		return "", 0, fmt.Errorf("parsing PR number from %q: %w", url, err)
	}
	return url, number, nil
}

func (realGitHubPRClient) State(ctx context.Context, dir string, number int) (string, error) {
	out, err := runGH(ctx, dir, "pr", "view", strconv.Itoa(number), "--json", "state")
	if err != nil {
		return "", err
	}
	var parsed struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return "", fmt.Errorf("parsing gh pr view output: %w", err)
	}
	return parsed.State, nil
}

// runGH runs `gh <args...>` with dir as its working directory, via argv
// only (never a shell string) — mirroring runGit's shape. `gh` resolves
// which GitHub repository it's talking to from dir's own git remote
// config, the same way any `git` command run there would.
func runGH(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func parsePRNumber(url string) (int, error) {
	idx := strings.LastIndex(url, "/")
	if idx == -1 || idx == len(url)-1 {
		return 0, fmt.Errorf("no trailing path segment in %q", url)
	}
	return strconv.Atoi(url[idx+1:])
}

// PushAndOpenPR is the "Push & Open PR" action's mechanism
// (docs/milestones/done/milestone7.md): pushes newBranch and, the first time
// this is called for a task, opens a PR for it via client.Create. dir is
// the shared checkout (ResolveWorkspace's return value), not a resolved
// execution/review worktree — a branch created via `git worktree add -b`
// is a ref in the same shared object database every worktree of that repo
// uses (the same fact that already lets the ref-aware read tools run `git
// show <branch>:<path>` from the shared checkout for a branch checked out
// elsewhere), so pushing it by name needs no worktree lookup.
//
// The PR's base branch is dir's own currently-checked-out branch
// (gitutil.CurrentBranch) — the shared checkout's own branch (typically
// "main"), the same fact worktree.go already relies on to derive
// ExecutionWorkspace.BaseBranch. There's no caller-supplied baseBranch
// parameter (Milestone 7 PR 3): the shared checkout is the one place this
// is unambiguous, so callers don't need their own way to determine it.
//
// existingNumber == 0 means no PR has been recorded for this task yet:
// newBranch is pushed as-is, then client.Create opens a fresh PR.
//
// existingNumber != 0 means a task.PullRequest is already on record
// (existingURL/existingBranch are its URL and tracked remote branch).
// client.State is checked first: if the PR isn't closed, newBranch is
// refspec-pushed onto existingBranch (landing this attempt's commits on
// the PR GitHub already has open, without renaming or reusing the local
// branch itself — Milestone 6's one-worktree-per-attempt invariant stays
// intact) and the existing URL/number/branch are returned unchanged, with
// no new Create call. If the PR is closed, a plain refspec push wouldn't
// reopen it — landing commits behind a silently-stale PR — so this is
// treated as if there were no existing PR: a fresh push of newBranch plus
// a new Create call, and the returned values overwrite the stale record
// entirely.
func PushAndOpenPR(ctx context.Context, dir, newBranch, title, body string, existingURL string, existingNumber int, existingBranch string, client GitHubPRClient) (url string, number int, branch string, err error) {
	if existingNumber != 0 {
		state, err := client.State(ctx, dir, existingNumber)
		if err != nil {
			return "", 0, "", fmt.Errorf("checking PR #%d state: %w", existingNumber, err)
		}
		if !strings.EqualFold(state, "CLOSED") {
			if _, err := gitutil.RunGit(ctx, dir, "push", "origin", newBranch+":"+existingBranch); err != nil {
				return "", 0, "", fmt.Errorf("pushing %s onto existing PR branch %s: %w", newBranch, existingBranch, err)
			}
			return existingURL, existingNumber, existingBranch, nil
		}
		// Closed: fall through to the fresh-push-and-create path below.
	}

	baseBranch, err := gitutil.CurrentBranch(ctx, dir)
	if err != nil {
		return "", 0, "", err
	}

	if _, err := gitutil.RunGit(ctx, dir, "push", "origin", newBranch); err != nil {
		return "", 0, "", fmt.Errorf("pushing %s: %w", newBranch, err)
	}
	url, number, err = client.Create(ctx, dir, newBranch, baseBranch, title, body)
	if err != nil {
		return "", 0, "", fmt.Errorf("opening PR for %s: %w", newBranch, err)
	}
	return url, number, newBranch, nil
}
