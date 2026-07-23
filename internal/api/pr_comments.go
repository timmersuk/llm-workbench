package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/timmersuk/llm-workbench/internal/gitutil"
)

// prCommentsExecutionFilename is where a needs_changes retry's PR comments
// file is written, relative to the execution worktree root — buildExecutionPrompt's
// addendum references this exact name, so the model's workspace-relative
// read_file call matches without needing the number itself
// (docs/adr/0015-pr-feedback-delivered-as-a-file-not-a-live-tool.md).
const prCommentsExecutionFilename = "pr-comments.yaml"

// prCommentsScratchDir locates a rejected task's PR comments file under the
// shared checkout, gitignored via writePRCommentsFile since it's a
// workbench-owned scratch artifact, never the human's own tracked
// repository content (docs/adr/0015).
const prCommentsScratchDir = ".llm-workbench/pr-comments"

// prCommentsRequirementsPath is the rejected → Requirements-stage file
// location, task-scoped so two rejected tasks' comment dumps can't clobber
// each other in the shared checkout every Requirements/Planning conversation
// for the project uses concurrently.
func prCommentsRequirementsPath(workspace, taskID string) string {
	return filepath.Join(workspace, filepath.FromSlash(prCommentsScratchDir), taskID+".yaml")
}

// writePRCommentsFile fetches number's PR comments via s.PRClient and
// writes them to filePath (which must be inside workspace), first ensuring
// workspace's .git/info/exclude covers both scratch-file locations this
// milestone uses — a single call handles either call site, since
// .git/info/exclude is shared across a repository's checkout and every one
// of its worktrees (gitutil.EnsureGitExclude).
func (s *Server) writePRCommentsFile(ctx context.Context, workspace, filePath string, number int) error {
	comments, err := s.PRClient.Comments(ctx, workspace, number)
	if err != nil {
		return fmt.Errorf("fetching PR #%d comments: %w", number, err)
	}
	if err := gitutil.EnsureGitExclude(ctx, workspace, prCommentsExecutionFilename, prCommentsScratchDir+"/"); err != nil {
		return fmt.Errorf("ensuring pr-comments scratch paths are git-excluded: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(filePath), err)
	}
	if err := os.WriteFile(filePath, []byte(comments), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", filePath, err)
	}
	return nil
}

// removePRCommentsFile deletes a previously-written PR comments scratch
// file. A missing file is not an error — nothing to clean up.
func removePRCommentsFile(filePath string) error {
	if filePath == "" {
		return nil
	}
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
