// Package gitstore implements a git-backed Store for tasks and projects,
// restoring the "git-backed storage" framing docs/project_summary.md and
// CLAUDE.md used before Milestone 2 walked it back to plain on-disk
// persistence (docs/milestones/milestone-orphans.md, "Git-backed
// task/project storage"). It is built on go-git
// (github.com/go-git/go-git/v5) — a pure-Go git implementation — rather
// than shelling out to a `git` binary: docs/architectural invariants.md's
// "prefer open standards" invariant calls for using an existing protocol/
// library where one fits, and this milestone's constraints explicitly rule
// out adding a running `git` binary as a hard runtime dependency (unlike
// internal/gitutil, which deliberately does shell out to `git` for
// project-repository operations — a different, already-installed-by-the-
// operator dependency than this store's own persistence layer).
//
// Store composes a project.FileStore and a task.FileStore, both rooted at
// the same git working directory, for every actual YAML read/write — the
// on-disk layout is identical to the plain-FileStore era
// (docs/engineering conventions.md's Storage & file layout section) — and
// layers git plumbing around them: startup clone/resume (this file),
// synchronous local commits on every mutation (commit.go), and a
// background push worker (push.go). Read-only methods bypass git
// entirely and delegate straight through to the wrapped FileStores.
package gitstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/sirupsen/logrus"

	"github.com/timmersuk/llm-workbench/internal/project"
	"github.com/timmersuk/llm-workbench/internal/task"
)

// originRemoteName is the sole remote GitStore knows about — the same
// convention `git clone` itself uses, and the only remote this milestone's
// push worker (push.go) ever targets.
const originRemoteName = "origin"

// ErrAmbiguousWorkspace is returned by Open when workspaceRoot is a
// non-empty directory that is neither an empty directory (safe to clone
// into) nor an existing git checkout whose "origin" remote already matches
// dataRepoURL (safe to resume). This milestone deliberately refuses to
// guess in that case — see this package's doc comment and the design note
// near cmd/server/main.go for the provisioning pattern that keeps a fresh
// deployment or e2e test out of this state.
var ErrAmbiguousWorkspace = errors.New("workspace root is non-empty and is not a matching clone of the configured data repository")

// Store is a git-backed implementation of project.Store, task.Store,
// api.ProjectStore, and api.TaskStore: every mutating call commits
// synchronously and locally to the git repository checked out at Root
// (commit.go), serialized by a single process-wide mutex — every
// task.yaml/project.yaml create or update becomes a real commit, giving
// full history/diff/blame. A single process-wide write mutex serializing
// every commit across every project is a known future scaling risk,
// accepted for this milestone (docs/milestones/git-backed-storage's own
// assumptions) rather than mitigated with finer-grained locking.
type Store struct {
	root string
	repo *git.Repository

	// mu serializes every Create/Update end to end (FileStore write + git
	// add + git commit), not just the git commit step — two concurrent
	// writers racing between "write file" and "commit" could otherwise
	// interleave, attributing one writer's file change to the other's
	// commit message (or vice versa).
	mu sync.Mutex

	projects *project.FileStore
	tasks    *task.FileStore
}

// Open resolves workspaceRoot into a working git checkout tracking
// dataRepoURL as its "origin" remote, implementing this milestone's
// startup contract:
//
//   - workspaceRoot is empty (no entries at all): clone dataRepoURL into
//     it. If dataRepoURL names a brand-new, still-empty bare repository —
//     the shape a fresh deployment or e2e test harness provisions via
//     `git init --bare` (see the design note near cmd/server/main.go) —
//     go-git's clone returns transport.ErrEmptyRemoteRepository rather
//     than an empty local repo (unlike the real `git` CLI, which clones an
//     empty remote successfully), so Open falls back to a local `git init`
//     equivalent (PlainInit) plus configuring dataRepoURL as origin.
//   - workspaceRoot already holds a git checkout whose "origin" remote
//     already matches dataRepoURL: resume it as-is. No pull, no fetch —
//     this milestone's push worker (push.go) only ever pushes, matching
//     "never pulls after startup."
//   - anything else (non-empty and not a matching clone): ErrAmbiguousWorkspace.
//
// dataRepoURL is treated strictly as a local filesystem path this
// milestone — no network transport, no auth (remote hosting/credentials
// are punted to a future milestone) — go-git resolves a bare filesystem
// path as a local (file) transport automatically, so no special handling
// is needed here for that constraint.
func Open(workspaceRoot, dataRepoURL string) (*Store, error) {
	empty, err := dirIsEmptyOrAbsent(workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("checking workspace root %s: %w", workspaceRoot, err)
	}

	var repo *git.Repository
	if empty {
		if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
			return nil, fmt.Errorf("creating workspace root %s: %w", workspaceRoot, err)
		}
		repo, err = git.PlainClone(workspaceRoot, false, &git.CloneOptions{URL: dataRepoURL})
		if errors.Is(err, transport.ErrEmptyRemoteRepository) {
			logrus.WithFields(logrus.Fields{"workspaceRoot": workspaceRoot, "dataRepoURL": dataRepoURL}).
				Info("data repo is empty, initializing a fresh local repository instead of cloning")
			repo, err = git.PlainInit(workspaceRoot, false)
			if err != nil {
				return nil, fmt.Errorf("initializing empty workspace root %s: %w", workspaceRoot, err)
			}
			if _, err := repo.CreateRemote(&config.RemoteConfig{Name: originRemoteName, URLs: []string{dataRepoURL}}); err != nil {
				return nil, fmt.Errorf("configuring origin remote %s: %w", dataRepoURL, err)
			}
		} else if err != nil {
			return nil, fmt.Errorf("cloning %s into %s: %w", dataRepoURL, workspaceRoot, err)
		}
	} else {
		repo, err = git.PlainOpen(workspaceRoot)
		if err != nil {
			return nil, fmt.Errorf("%w: %s is non-empty but not a git checkout: %v", ErrAmbiguousWorkspace, workspaceRoot, err)
		}
		if err := verifyOrigin(repo, dataRepoURL); err != nil {
			return nil, err
		}
	}

	projectsRoot := filepath.Join(workspaceRoot, "projects")
	if err := os.MkdirAll(projectsRoot, 0o755); err != nil {
		return nil, fmt.Errorf("creating projects root %s: %w", projectsRoot, err)
	}

	return &Store{
		root:     workspaceRoot,
		repo:     repo,
		projects: project.NewFileStore(projectsRoot),
		tasks:    task.NewFileStore(projectsRoot),
	}, nil
}

// verifyOrigin confirms repo's "origin" remote is already configured with
// dataRepoURL — the "resume" half of Open's contract. A repo with no
// origin remote at all, or one pointing somewhere else, is exactly the
// ambiguous state ErrAmbiguousWorkspace exists for: Open must never guess
// which of two different data repos a pre-existing checkout was meant to
// track.
func verifyOrigin(repo *git.Repository, dataRepoURL string) error {
	remote, err := repo.Remote(originRemoteName)
	if err != nil {
		return fmt.Errorf("%w: existing git checkout has no %q remote: %v", ErrAmbiguousWorkspace, originRemoteName, err)
	}
	urls := remote.Config().URLs
	if len(urls) == 0 || urls[0] != dataRepoURL {
		return fmt.Errorf("%w: existing checkout's %q remote is %v, not %q", ErrAmbiguousWorkspace, originRemoteName, urls, dataRepoURL)
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
