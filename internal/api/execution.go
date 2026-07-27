package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
	"github.com/timmersuk/llm-workbench/internal/gitutil"
	"github.com/timmersuk/llm-workbench/internal/task"
)

// defaultExecutionExecutor is the agentRunners key handleStartExecution
// falls back to when the request doesn't name one. Only claude-code
// implements real Execute behavior in this milestone —
// ChatClientRunner.Execute returns agentrunner.ErrExecuteNotSupported
// until chatclient-tool-loop lands (see data/projects/llm-workbench/tasks/
// chatclient-tool-loop/) — so this is also, for now, the only executor
// worth selecting at all.
const defaultExecutionExecutor = "claude-code"

// executionMaxTurns bounds an autonomous Execute run's tool-call
// round-trips — set here, explicitly, by the caller that knows this is an
// Implementation-stage execution (build/test/fix/commit), not defaulted
// inside any AgentRunner implementation. Used for both the main execution
// attempt and its post-run cleanup turn below.
//
// data/projects/llm-workbench/tasks/configurable-execution-max-turns scopes
// making this configurable per task instead of one fixed global value.
const executionMaxTurns = 1000

// executionStartRequest is the request body for handleStartExecution.
type executionStartRequest struct {
	Model    string `json:"model"`
	Executor string `json:"executor,omitempty"`
	// ContinueFromExecutionID is the human's explicit choice to continue
	// from a prior failed/partial execution's branch, echoing the
	// execution_id handleGetContinuableExecution offered — re-validated
	// server-side against resolveFailureContinuation rather than trusted
	// outright, since the eligible attempt could have changed between the
	// hint being fetched and this request arriving.
	ContinueFromExecutionID string `json:"continue_from_execution_id,omitempty"`
}

// continuableExecutionResponse is handleGetContinuableExecution's response
// body — ExecutionID is empty when there's nothing a human could choose to
// continue from right now.
type continuableExecutionResponse struct {
	ExecutionID string `json:"execution_id"`
}

// executeStreamEvent is the SSE wire shape for a running execution — a
// discriminated union (Type names which of the optional fields is set),
// unlike chatStreamEvent's flat "at most one field ever set" shape
// (chat.go), because a single execution run legitimately produces many
// tool_call/tool_result events, not at most one tool call per turn.
type executeStreamEvent struct {
	Type string `json:"type"` // "text" | "tool_call" | "tool_result" | "error" | "done"

	Content string `json:"content,omitempty"`

	ToolName  string `json:"tool_name,omitempty"`
	ToolInput string `json:"tool_input,omitempty"`

	ToolResult string `json:"tool_result,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`

	Error string `json:"error,omitempty"`

	// Execution is set only on the final "done" event, so the frontend
	// doesn't need a second round-trip to learn the outcome.
	Execution *task.Execution `json:"execution,omitempty"`
}

// executeEventToWire maps one agentrunner.ExecuteEvent onto its SSE wire
// shape.
func executeEventToWire(ev agentrunner.ExecuteEvent) executeStreamEvent {
	return executeStreamEvent{
		Type: ev.Kind, Content: ev.Text,
		ToolName: ev.ToolName, ToolInput: ev.ToolInput,
		ToolResult: ev.ToolResult, IsError: ev.IsError,
	}
}

// handleStartExecution runs one autonomous Implementation-stage execution
// attempt to completion: resolves an isolated git worktree
// (agentrunner.ResolveExecutionWorkspace) so the run can never touch the
// project's shared checkout, streams the agent's live tool activity as SSE,
// then persists a structured task.Execution (store.RecordExecution) —
// which itself advances the task's stage to review on success. 409s if the
// task isn't currently at StageImplementation.
//
// No restart-resume logic: if the server process exits mid-run, nothing
// survives to write a record — the worktree/branch is left for manual
// inspection and Stage simply never advances (docs/milestones/milestone5.md's
// resolved decisions).
func (s *Server) handleStartExecution() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req executionStartRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		executorKey := req.Executor
		if executorKey == "" {
			executorKey = defaultExecutionExecutor
		}
		runner, ok := s.AgentRunners[executorKey]
		if !ok {
			http.Error(w, fmt.Sprintf("unknown executor %q", executorKey), http.StatusBadRequest)
			return
		}

		projectId := r.PathValue("projectId")
		taskId := r.PathValue("taskId")

		proj, err := s.Projects.Get(projectId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		// Resolved before the SSE stream commits (below) so a fail-closed
		// determination failure reports as a normal HTTP error, not
		// something squeezed into the event stream.
		defaultBranch, err := s.ensureDefaultBranch(r.Context(), proj)
		if err != nil {
			http.Error(w, fmt.Sprintf("determining default branch: %v", err), http.StatusInternalServerError)
			return
		}
		root, err := s.Projects.TasksRoot(projectId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		store := s.TaskStores(root)

		t, err := store.Get(taskId)
		if err != nil {
			writeGetError(w, err)
			return
		}
		if t.Stage != task.StageImplementation {
			http.Error(w, fmt.Sprintf("task is not in implementation stage (currently %q)", t.Stage), http.StatusConflict)
			return
		}

		plan, err := store.GetPlan(taskId)
		if err != nil {
			writeGetError(w, err)
			return
		}

		forkFrom, reviewFeedback, err := resolveReviewContinuation(store, taskId)
		if err != nil {
			writeGetError(w, err)
			return
		}

		failureExecutionID, failureForkFrom, failureMessage, err := resolveFailureContinuation(store, taskId, forkFrom)
		if err != nil {
			writeGetError(w, err)
			return
		}

		// The human's choice is re-validated against a fresh
		// resolveFailureContinuation lookup, not trusted outright — the
		// eligible attempt (or its eligibility at all) could have changed
		// between handleGetContinuableExecution's hint and this request
		// arriving (e.g. a needs_changes review landed in between).
		continuedFromExecutionID := ""
		priorFailureMessage := ""
		if req.ContinueFromExecutionID != "" {
			if failureExecutionID == "" || req.ContinueFromExecutionID != failureExecutionID {
				http.Error(w, fmt.Sprintf("execution %q is no longer available to continue from", req.ContinueFromExecutionID), http.StatusConflict)
				return
			}
			forkFrom = failureForkFrom
			continuedFromExecutionID = failureExecutionID
			priorFailureMessage = failureMessage
		}

		executionID, err := store.NextExecutionID(taskId)
		if err != nil {
			writeGetError(w, err)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)

		writeEvent := func(ev executeStreamEvent) {
			data, _ := json.Marshal(ev)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}

		ws, wsErr := agentrunner.ResolveExecutionWorkspace(r.Context(), s.ReposRoot, proj.Repositories, taskId, executionID, forkFrom, defaultBranch)
		if wsErr != nil {
			writeEvent(executeStreamEvent{Type: "error", Error: fmt.Sprintf("resolving execution workspace: %v", wsErr)})
			return
		}

		// A needs_changes retry with a PR already open gets that PR's actual
		// review feedback fetched once, up front, and written to a scratch
		// file in the worktree — every executor already has a plain read_file
		// tool, so this works uniformly without a bespoke live tool call per
		// executor (docs/adr/0015). A fetch failure fails the whole request,
		// the same posture buildRejectedReviewContext's own review/execution
		// lookups already take.
		var prCommentsPath string
		if reviewFeedback != "" && t.PullRequest != nil {
			prCommentsPath = filepath.Join(ws.Path, prCommentsExecutionFilename)
			if err := s.writePRCommentsFile(r.Context(), ws.Path, prCommentsPath, t.PullRequest.Number); err != nil {
				writeEvent(executeStreamEvent{Type: "error", Error: fmt.Sprintf("fetching PR comments: %v", err)})
				return
			}
		}

		systemPrompt := buildExecutionPrompt(t, plan, reviewFeedback, priorFailureMessage, prCommentsPath != "")

		start := time.Now()
		out, execErr := runner.Execute(r.Context(), agentrunner.ExecuteInput{
			SessionKey:   taskId + ":execute",
			Workspace:    ws.Path,
			SystemPrompt: systemPrompt,
			Model:        req.Model,
			MaxTurns:     executionMaxTurns,
		}, func(ev agentrunner.ExecuteEvent) error {
			writeEvent(executeEventToWire(ev))
			return nil
		})

		// Deleted immediately, before the diff is collected below — not just
		// tidiness: the model's own commit (e.g. a broad `git add -A`) could
		// otherwise sweep this scratch file into the pushed branch, on top of
		// the .git/info/exclude protection writePRCommentsFile already
		// arranged (docs/adr/0015).
		if prCommentsPath != "" {
			if err := removePRCommentsFile(prCommentsPath); err != nil {
				logrus.WithError(err).WithFields(logrus.Fields{"task": taskId, "execution": executionID}).Warn("removing scratch pr-comments file")
			}
		}

		// A non-success end gets a mechanical safety commit for whatever the
		// agent wrote to disk but never committed itself — otherwise that
		// work is invisible to CollectExecutionOutput below (git log/diff
		// against HEAD) and would be silently left behind the moment a later
		// attempt forks a fresh worktree from this branch's tip
		// (resolveFailureContinuation). Uses context.Background(), like
		// CollectExecutionOutput below, since r.Context() may already be
		// canceled — a Stop or timeout is exactly one of the cases this
		// exists to protect. Best-effort: logged, not fatal, matching every
		// other post-Execute inspection step here.
		var workspaceDirty bool
		if execErr != nil {
			if dirty := gitutil.DirtyWorkingTree(context.Background(), ws.Path); dirty.Known && dirty.Dirty {
				msg := fmt.Sprintf("Safety commit: preserve uncommitted work after execution failure (%s)", executionID)
				if commitErr := gitutil.CommitAll(context.Background(), ws.Path, msg); commitErr != nil {
					logrus.WithError(commitErr).WithFields(logrus.Fields{"task": taskId, "execution": executionID}).Warn("safety-committing uncommitted work after execution failure")
				}
			}
		} else if dirty := gitutil.DirtyWorkingTree(r.Context(), ws.Path); dirty.Known && dirty.Dirty {
			// A successful run left something uncommitted. Unlike the
			// failure path above, this isn't presumed-interrupted work —
			// it could just as easily be scratch/temp output the agent
			// deliberately left out — so the harness doesn't silently
			// commit or delete it. Instead, it gives the agent one
			// dedicated follow-up turn, in the same workspace, naming
			// exactly what's dirty and asking it to decide. Best-effort:
			// a failure here doesn't change exec's success status, and if
			// the tree is still dirty afterward, that's recorded on
			// Output.WorkspaceDirty for a human to check rather than
			// guessed at.
			statusOut, statusErr := gitutil.RunGit(r.Context(), ws.Path, "status", "--porcelain")
			if statusErr != nil {
				logrus.WithError(statusErr).WithFields(logrus.Fields{"task": taskId, "execution": executionID}).Warn("reading workspace status before cleanup turn")
				workspaceDirty = true
			} else {
				cleanupOut, cleanupErr := runner.Execute(r.Context(), agentrunner.ExecuteInput{
					SessionKey:   taskId + ":execute-cleanup",
					Workspace:    ws.Path,
					SystemPrompt: buildWorkspaceCleanupPrompt(statusOut),
					Model:        req.Model,
					MaxTurns:     executionMaxTurns,
				}, func(ev agentrunner.ExecuteEvent) error {
					writeEvent(executeEventToWire(ev))
					return nil
				})
				if cleanupErr != nil {
					logrus.WithError(cleanupErr).WithFields(logrus.Fields{"task": taskId, "execution": executionID}).Warn("workspace cleanup turn failed")
				}
				out.DurationSeconds += cleanupOut.DurationSeconds
				out.TokensUsed += cleanupOut.TokensUsed
				out.CostEstimate += cleanupOut.CostEstimate

				if stillDirty := gitutil.DirtyWorkingTree(context.Background(), ws.Path); !stillDirty.Known || stillDirty.Dirty {
					workspaceDirty = true
				}
			}
		}

		// Best-effort: the execution itself already succeeded or failed
		// independently of whether this inspection works, so a failure here
		// is logged, not fatal to the response.
		commits, artifacts, collectErr := agentrunner.CollectExecutionOutput(context.Background(), ws, forkFrom)
		if collectErr != nil {
			logrus.WithError(collectErr).WithFields(logrus.Fields{"task": taskId, "execution": executionID}).Warn("collecting execution output")
			commits = []string{}
			artifacts = []string{}
		}

		durationSeconds := out.DurationSeconds
		if durationSeconds == 0 {
			durationSeconds = time.Since(start).Seconds()
		}

		exec := task.Execution{
			ExecutionID: executionID,
			Executor:    task.ExecutionExecutor{Type: executorKey},
			Input:       task.ExecutionInput{PlanRef: "plan.yaml", ReviewFeedback: reviewFeedback, ContinuedFromExecutionID: continuedFromExecutionID},
			Output:      task.ExecutionOutput{Artifacts: artifacts, GitBranch: ws.Branch, Commits: commits, ForkedFromBranch: forkFrom, WorkspaceDirty: workspaceDirty},
			Metrics: task.ExecutionMetrics{
				DurationSeconds: durationSeconds,
				TokensUsed:      out.TokensUsed,
				CostEstimate:    out.CostEstimate,
			},
		}
		classifyExecutionOutcome(&exec, execErr, r.Context())

		recorded, recordErr := store.RecordExecution(taskId, exec)
		if recordErr != nil {
			logrus.WithError(recordErr).WithFields(logrus.Fields{"task": taskId, "execution": executionID}).Error("persisting execution record")
			writeEvent(executeStreamEvent{Type: "error", Error: fmt.Sprintf("saving execution record: %v", recordErr)})
			return
		}

		writeEvent(executeStreamEvent{Type: "done", Execution: &recorded})
	}
}

// classifyExecutionOutcome sets exec.Status/Failure from err, the
// deterministic classifier the resolved milestone-5 decisions call for
// (wrapper code, not agent self-reporting): no error -> success; ctx
// already done (deadline exceeded, or canceled — which covers a
// human-triggered Stop the same way, since context.Context can't tell
// those apart) -> failure.type "resource"; any other error -> failure.type
// "execution". "specification"/"infeasible"/"quality" are deliberately
// never set here — those are judgment calls left for a human to make
// later, not something wrapper code can detect mechanically.
func classifyExecutionOutcome(exec *task.Execution, err error, ctx context.Context) {
	if err == nil {
		exec.Status = task.ExecutionStatusSuccess
		return
	}

	failureType := task.FailureTypeExecution
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled) {
		failureType = task.FailureTypeResource
	}
	exec.Status = task.ExecutionStatusFailure
	exec.Failure = &task.ExecutionFailure{Type: failureType, Message: err.Error()}
}

// handleListExecutions returns every recorded execution attempt for a
// task, oldest first — used by the frontend's Execute panel to show past
// attempts' status without reaching into Review-stage diff territory
// (out of scope for this milestone).
func (s *Server) handleListExecutions() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		store, ok := s.resolveTaskStore(w, r.PathValue("projectId"))
		if !ok {
			return
		}

		executions, err := store.ListExecutions(r.PathValue("taskId"))
		if err != nil {
			writeGetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string][]task.Execution{"executions": executions})
	}
}

// handleGetContinuableExecution exposes resolveFailureContinuation's
// result so ExecutePanel can offer "continue from exec-00N" without
// re-deriving the eligibility rule (non-success, has commits, and no
// needs_changes retry already claiming forkFrom) in the frontend — the
// backend stays the one place that rule lives, mirroring how
// resolveReviewContinuation itself is never re-implemented client-side.
// ExecutionID is empty in the response when there's nothing to offer.
func (s *Server) handleGetContinuableExecution() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		store, ok := s.resolveTaskStore(w, r.PathValue("projectId"))
		if !ok {
			return
		}
		taskId := r.PathValue("taskId")

		reviewForkFrom, _, err := resolveReviewContinuation(store, taskId)
		if err != nil {
			writeGetError(w, err)
			return
		}

		executionID, _, _, err := resolveFailureContinuation(store, taskId, reviewForkFrom)
		if err != nil {
			writeGetError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, continuableExecutionResponse{ExecutionID: executionID})
	}
}

// resolveReviewContinuation looks up the latest review recorded for taskId
// and, only when its decision is needs_changes, resolves the branch to
// continue from and the notes to carry into the new attempt's prompt — a
// fresh lookup done on every execute rather than a persisted flag
// (docs/adr/0012). Any other decision (or no review yet) returns both
// empty, so the caller forks a fresh worktree off main exactly as before.
//
// The branch to continue from is the specific execution named by the
// review's own ExecutionID (internal/task/review.go), not simply the last
// entry in ListExecutions. RecordExecution only advances Stage to review on
// success, so a needs_changes retry that itself fails is still recorded
// here without ever producing a new review — the last entry in
// ListExecutions could then be that failed retry rather than the one the
// latest review actually reviewed. ExecutionID is captured by FinalizeReview
// at the one moment that ambiguity can't exist, so this just looks it up
// directly instead of re-inferring it here.
func resolveReviewContinuation(store TaskStore, taskId string) (forkFrom, reviewFeedback string, err error) {
	reviews, err := store.ListReviews(taskId)
	if err != nil {
		return "", "", fmt.Errorf("listing reviews for %s: %w", taskId, err)
	}
	if len(reviews) == 0 {
		return "", "", nil
	}
	latest := reviews[len(reviews)-1]
	if latest.Decision != task.ReviewDecisionNeedsChanges {
		return "", "", nil
	}

	executions, err := store.ListExecutions(taskId)
	if err != nil {
		return "", "", fmt.Errorf("listing executions for %s: %w", taskId, err)
	}
	for _, e := range executions {
		if e.ExecutionID == latest.ExecutionID {
			forkFrom = e.Output.GitBranch
			break
		}
	}
	return forkFrom, latest.Notes, nil
}

// resolveFailureContinuation finds the most recent execution a human could
// explicitly choose to continue from after it failed or only partially
// completed — the counterpart to resolveReviewContinuation for the case
// where no review has ever driven a retry. Scoped strictly to when
// reviewForkFrom is empty: whenever a needs_changes review is already
// forking a retry automatically (docs/adr/0012), that path owns forkFrom
// and this one stays silent rather than compete with it for the same
// decision.
//
// Eligible means the most recent execution didn't succeed and it actually
// produced commits — an instant failure before any work happened (e.g. a
// timed-out first attempt) has nothing worth continuing from. Anything
// else (no executions yet, most recent succeeded, or it has no commits)
// returns an empty executionID, the signal both handleStartExecution's
// validation and handleGetContinuableExecution's hint use for "nothing to
// offer here."
func resolveFailureContinuation(store TaskStore, taskId, reviewForkFrom string) (executionID, forkFrom, failureMessage string, err error) {
	if reviewForkFrom != "" {
		return "", "", "", nil
	}

	executions, err := store.ListExecutions(taskId)
	if err != nil {
		return "", "", "", fmt.Errorf("listing executions for %s: %w", taskId, err)
	}
	if len(executions) == 0 {
		return "", "", "", nil
	}

	latest := executions[len(executions)-1]
	if latest.Status == task.ExecutionStatusSuccess || len(latest.Output.Commits) == 0 {
		return "", "", "", nil
	}

	if latest.Failure != nil {
		failureMessage = latest.Failure.Message
	}
	return latest.ExecutionID, latest.Output.GitBranch, failureMessage, nil
}

// buildExecutionPrompt seeds an execution's system prompt with the task's
// own fields and its finalized plan — the Implementation-stage analog of
// buildStagePrompt (stage_conversation.go), but for an autonomous run
// rather than an interview: explicit instructions to implement the plan,
// verify it, and commit, since there is no human turn-by-turn to steer it.
// reviewFeedback is non-empty only for a needs_changes retry
// (resolveReviewContinuation) — the workspace itself already contains the
// prior attempt's code (ResolveExecutionWorkspace's forkFrom), so this only
// needs to explain why, not restate what changed. priorFailureMessage is
// non-empty only for a human-chosen continuation from a failed/partial
// execution (resolveFailureContinuation) — mutually exclusive with
// reviewFeedback by construction, since the latter path is hidden whenever
// the former is active. hasPRComments is set only when handleStartExecution
// wrote prCommentsExecutionFilename to the worktree (a needs_changes retry
// with a PR already open) — the model only needs the file's name to read
// it, not the PR number itself
// (docs/adr/0015-pr-feedback-delivered-as-a-file-not-a-live-tool.md).
func buildExecutionPrompt(t task.Task, plan task.Plan, reviewFeedback, priorFailureMessage string, hasPRComments bool) string {
	var b strings.Builder

	b.WriteString("You are executing an already-approved implementation plan for this task, autonomously and to completion. You are already on an isolated git branch inside the target repository — implement the plan, run relevant tests, and commit your work as you go. Do not ask questions; make reasonable decisions and proceed.\n\n")

	fmt.Fprintf(&b, "## Task\nObjective: %s\n", t.Objective)
	if len(t.Constraints) > 0 {
		fmt.Fprintf(&b, "Constraints:\n- %s\n", strings.Join(t.Constraints, "\n- "))
	}
	if len(t.SuccessCriteria) > 0 {
		fmt.Fprintf(&b, "Success criteria:\n- %s\n", strings.Join(t.SuccessCriteria, "\n- "))
	}

	fmt.Fprintf(&b, "\n## Plan\nApproach: %s\n", plan.Approach)
	if len(plan.Steps) > 0 {
		fmt.Fprintf(&b, "Steps:\n- %s\n", strings.Join(plan.Steps, "\n- "))
	}
	if len(plan.Risks) > 0 {
		fmt.Fprintf(&b, "Known risks:\n- %s\n", strings.Join(plan.Risks, "\n- "))
	}

	if reviewFeedback != "" {
		fmt.Fprintf(&b, "\n## Continuing prior work\nYour workspace already contains your previous attempt at this plan — a reviewer looked at it and requested changes rather than approving it. Read what's already there before making changes, and address this feedback directly:\n%s\n", reviewFeedback)
	}

	if priorFailureMessage != "" {
		fmt.Fprintf(&b, "\n## Continuing a prior failed attempt\nYour workspace already contains commits from a previous attempt at this plan that did not finish successfully: %s\nInspect `git log` and the current file/test state before making any changes — don't redo work that's already committed; focus on finishing what's left.\n", priorFailureMessage)
	}

	if hasPRComments {
		fmt.Fprintf(&b, "\nThe PR opened for this task's prior attempt has real reviewer feedback on GitHub (comments, review verdicts, and inline code comments), saved to %s at the root of your workspace — read it with your file-reading tool for the reviewer's own words, in addition to the summary above.\n", prCommentsExecutionFilename)
	}

	return b.String()
}

// buildWorkspaceCleanupPrompt seeds the follow-up turn a successful
// execution gets when it finishes with a dirty worktree
// (handleStartExecution) — deliberately reactive, sent only when `git
// status --porcelain` actually reports something, rather than a standing
// instruction folded into buildExecutionPrompt: most successful runs
// already leave a clean tree, and don't need it. statusOutput is that raw
// porcelain output, given to the agent verbatim so it knows exactly what's
// there rather than being told generically to "check for dirty files."
func buildWorkspaceCleanupPrompt(statusOutput string) string {
	return fmt.Sprintf("Your implementation work for this task is complete. Before finishing, your workspace still has uncommitted changes:\n\n%s\nFor each: if it's part of the work (source, tests, docs, config), commit it. If it's scratch or temporary output that shouldn't be part of this change (build artifacts, logs, caches, temp files), delete it. Don't leave anything uncommitted or unexplained. Do not ask questions; make reasonable decisions and proceed.\n", statusOutput)
}
