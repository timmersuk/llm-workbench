// Package gitstore implements a git-backed Store for tasks and projects,
// restoring the "git-backed storage" framing docs/project_summary.md and
// CLAUDE.md used before Milestone 2 walked it back to plain on-disk
// persistence (docs/milestones/milestone-orphans.md, "Git-backed
// task/project storage"). It shells out to the `git` binary (via os/exec)
// rather than a pure-Go library — a deliberate reversal of this package's
// original go-git-based design. go-git has no integration with OS
// credential helpers/SSH agents, so pushing to a real authenticated remote
// (GitHub, say) would have meant this package inventing its own credential
// storage; shelling out to `git` instead means every clone/commit/push
// runs through whatever credential helper the operator's machine already
// has configured, exactly as if they'd typed the command themselves. This
// does reintroduce `git` as a runtime dependency this package specifically
// avoided before — accepted, since internal/gitutil already requires an
// installed `git` for project-repository operations, so it was never
// actually absent from the operator's machine in practice.
//
// Store composes a project.FileStore and a task.FileStore, both rooted at
// the same git working directory, for every actual YAML read/write — the
// on-disk layout is identical to the plain-FileStore era
// (docs/engineering conventions.md's Storage & file layout section) — and
// layers git plumbing around them: startup clone/resume (this file),
// buffered pending-change tracking on every mutation (commit.go), and a
// background worker (push.go) that periodically turns pending changes into
// real commits and pushes them to origin. Read-only methods bypass git
// entirely and delegate straight through to the wrapped FileStores.
//
// project.Store and task.Store both declare methods named List/Get/Create/
// Update with different signatures (task.Store's take an extra leading
// projectID), so no single Go type can implement both interfaces at once —
// Go has no method overloading. Store is therefore a pair of distinct
// wrapper types, ProjectStore and TaskStore, sharing one underlying git
// checkout/mutex/pending-queue/push-worker via the unexported core they
// both hold a pointer to. This mirrors how internal/api.Server already
// takes Projects and Tasks as two separate fields (internal/api/router.go),
// so callers (cmd/server/main.go) wire Store.Projects/Store.Tasks in
// exactly the same shape FileStore-backed construction used.
package gitstore

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

// originRemoteName is the sole remote GitStore knows about — the same
// convention `git clone` itself uses, and the only remote this package's
// push worker (push.go) ever targets.
const originRemoteName = "origin"

// commitAuthorArgs fixes every commit's identity to a workbench-owned bot,
// not any real human's — these commits are mechanical persistence writes
// (the human decision already happened at the API layer that called
// Create/Update), not authored changes attributable to a person. Passed as
// `-c` overrides on every `git commit` invocation rather than relying on
// the operator's own global git config, which may have no identity set at
// all (unlike push/clone, which deliberately do rely on the operator's own
// credential configuration).
var commitAuthorArgs = []string{"-c", "user.name=llm-workbench", "-c", "user.email=llm-workbench@localhost"}

// ErrAmbiguousWorkspace is returned by Open when workspaceRoot is a
// non-empty directory that is neither an empty directory (safe to clone
// into) nor an existing git checkout whose "origin" remote already matches
// dataRepoURL (safe to resume). This package deliberately refuses to guess
// in that case — see this package's doc comment and the design note near
// cmd/server/main.go for the provisioning pattern that keeps a fresh
// deployment or e2e test out of this state.
var ErrAmbiguousWorkspace = errors.New("workspace root is non-empty and is not a matching clone of the configured data repository")

// core is the git plumbing shared by ProjectStore and TaskStore: the
// absolute workspace root, the dataRepoURL it tracks as "origin" (kept for
// logging), and the single process-wide mutex serializing every operation
// that touches the working tree — every FileStore write (commit.go's
// enqueue), and the push worker's own commit step (push.go) — but not the
// push itself (see push.go's doc comment for why that's deliberately
// excluded). A single process-wide write mutex serializing every commit
// across every project is a known future scaling risk, accepted for this
// milestone (docs/milestones/git-backed-storage's own assumptions) rather
// than mitigated with finer-grained locking.
type core struct {
	root        string
	dataRepoURL string

	// mu serializes every FileStore write (and the pending-change append
	// that follows it) plus the push worker's commit step — not just the
	// `git commit` itself, but the FileStore write that precedes it too:
	// two concurrent writers racing between "write file" and "enqueue"
	// could otherwise attribute one writer's file change to the other's
	// pending message (or vice versa). Deliberately not held across `git
	// push` (push.go) — push is network-bound and touches no working-tree
	// state, so holding mu for its duration would only make every API
	// write wait on a possibly-slow or stuck network call for no
	// correctness benefit.
	mu sync.Mutex

	// pending is every FileStore write since the last successful commit,
	// awaiting the push worker's next tick (push.go's commitPending) —
	// see commit.go's doc comment for why commits are deferred rather than
	// made synchronously inside each Create/Update call.
	pending []pendingChange
}

// pendingChange is one FileStore write awaiting commit: dir is the
// project's or task's own directory (never touched by any other entity,
// so `git add`-ing just it can't accidentally scoop up an unrelated
// pending change — see commit.go's doc comment), message is the same
// human-readable description this package always used as a commit
// message (e.g. `Create project "Demo Project"`).
type pendingChange struct {
	dir     string
	message string
}

// Store bundles the two git-backed stores this package provides — Projects
// (satisfying project.Store / api.ProjectStore) and Tasks (satisfying
// task.Store / api.TaskStore) — both backed by the same git checkout. See
// this package's doc comment for why Store itself implements neither
// interface directly.
type Store struct {
	Projects *ProjectStore
	Tasks    *TaskStore

	core *core
}

// ProjectStore is the git-backed half of Store satisfying project.Store /
// api.ProjectStore: reads delegate straight to the wrapped
// project.FileStore, writes enqueue a pending change instead of committing
// immediately (commit.go).
type ProjectStore struct {
	core  *core
	files *project.FileStore
}

// TaskStore is the git-backed half of Store satisfying task.Store /
// api.TaskStore: reads delegate straight to the wrapped task.FileStore,
// writes enqueue a pending change instead of committing immediately
// (commit.go). Like task.FileStore, a single instance serves every
// project — every method takes an explicit projectID (task.Store's doc
// comment).
type TaskStore struct {
	core  *core
	files *task.FileStore
}

// Open resolves workspaceRoot into a working git checkout tracking
// dataRepoURL as its "origin" remote, implementing this package's startup
// contract:
//
//   - workspaceRoot is empty (no entries at all): `git clone dataRepoURL
//     workspaceRoot`. Unlike this package's original go-git-based
//     implementation, the real `git` binary clones an empty remote
//     repository successfully (no special-case fallback needed) — a fresh
//     deployment or e2e test harness provisioning dataRepoURL via `git init
//     --bare` (see the design note near cmd/server/main.go) just produces
//     an empty local checkout with no commits yet, exactly as `git clone`
//     of an empty remote does for a human.
//   - workspaceRoot already holds a git checkout whose "origin" remote
//     already matches dataRepoURL: resume it as-is. No pull, no fetch —
//     this package's push worker (push.go) only ever pushes, matching
//     "never pulls after startup."
//   - anything else (non-empty and not a matching clone): ErrAmbiguousWorkspace.
func Open(workspaceRoot, dataRepoURL string) (*Store, error) {
	absRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving workspace root %s: %w", workspaceRoot, err)
	}

	empty, err := dirIsEmptyOrAbsent(absRoot)
	if err != nil {
		return nil, fmt.Errorf("checking workspace root %s: %w", absRoot, err)
	}

	if empty {
		if _, err := runGit("", "clone", dataRepoURL, absRoot); err != nil {
			return nil, fmt.Errorf("cloning %s into %s: %w", dataRepoURL, absRoot, err)
		}
	} else if err := verifyOrigin(absRoot, dataRepoURL); err != nil {
		return nil, err
	}

	projectsRoot := filepath.Join(absRoot, "projects")
	if err := os.MkdirAll(projectsRoot, 0o755); err != nil {
		return nil, fmt.Errorf("creating projects root %s: %w", projectsRoot, err)
	}

	c := &core{root: absRoot, dataRepoURL: dataRepoURL}
	return &Store{
		core:     c,
		Projects: &ProjectStore{core: c, files: project.NewFileStore(projectsRoot)},
		Tasks:    &TaskStore{core: c, files: task.NewFileStore(projectsRoot)},
	}, nil
}

// verifyOrigin confirms root is a git checkout whose "origin" remote is
// already configured with dataRepoURL — the "resume" half of Open's
// contract. Anything else (not a git checkout at all, a checkout with no
// origin remote, or one pointing somewhere else) is exactly the ambiguous
// state ErrAmbiguousWorkspace exists for: Open must never guess which of
// two different data repos a pre-existing checkout was meant to track.
func verifyOrigin(root, dataRepoURL string) error {
	out, err := runGit(root, "remote", "get-url", originRemoteName)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrAmbiguousWorkspace, root, err)
	}
	if strings.TrimSpace(out) != dataRepoURL {
		return fmt.Errorf("%w: existing checkout's %q remote is %q, not %q", ErrAmbiguousWorkspace, originRemoteName, strings.TrimSpace(out), dataRepoURL)
	}
	return nil
}

// dirIsEmptyOrAbsent reports whether dir doesn't exist yet or exists but
// has no entries — both are "safe to clone into" as far as Open is
// concerned, since WORKSPACE_ROOT (data/ by default) may not have been
// created at all on a fresh deployment.
func dirIsEmptyOrAbsent(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

// runGit runs `git` with args, its working directory set to dir (unless
// dir is empty, e.g. for `clone`, which names its own destination as an
// argument instead). Every gitstore git invocation goes through this one
// helper so error wrapping (stderr included, not just exec's own generic
// "exit status 1") is consistent everywhere.
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
