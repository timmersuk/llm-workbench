package task

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrWrongStage is returned by Finalize*/Revise* when the task isn't
// currently in the stage that action expects. Finalize only ever advances
// stage forward one step and Revise only ever moves it back to the
// specific prior stage it names — neither skips a stage nor is valid from
// an unrelated one.
var ErrWrongStage = errors.New("task is not in the expected stage for this action")

// FinalizeRequirements is the human "Finalize" action (CONTEXT.md) for
// GrillMe: it persists draft's task.yaml-subset fields and context.yaml,
// and advances Stage from "requirements" to "planning". The task must
// currently be in "requirements" stage. The Context is written first and
// task.yaml last, so task.yaml's Stage stays the single source of truth
// for how far Finalize actually got — if the Context write fails, nothing
// observably changed; if it succeeds but the task.yaml write then fails,
// Context is present but Stage hasn't advanced yet, a safely-retriable
// state (Finalize can just be called again).
func (s *FileStore) FinalizeRequirements(projectID, id string, draft RequirementsDraft) (Task, error) {
	t, err := s.Get(projectID, id)
	if err != nil {
		return Task{}, err
	}
	if t.Stage != StageRequirements {
		return Task{}, fmt.Errorf("finalizing requirements for %s (stage %q): %w", id, t.Stage, ErrWrongStage)
	}

	draft.Context.Summary = strings.TrimSpace(draft.Context.Summary)
	draft.Context.Background = strings.TrimSpace(draft.Context.Background)
	draft.Context.Detail = strings.TrimSpace(draft.Context.Detail)
	draft.Context.Files = trimmedContextFiles(draft.Context.Files)
	draft.Context.Verification = trimmedVerification(draft.Context.Verification)
	draft.Context.OpenQuestions = trimmedList(draft.Context.OpenQuestions)
	if err := s.writeContext(projectID, id, draft.Context); err != nil {
		return Task{}, err
	}

	t.Objective = strings.TrimSpace(draft.Objective)
	t.Constraints = trimmedList(draft.Constraints)
	t.Assumptions = trimmedList(draft.Assumptions)
	t.SuccessCriteria = trimmedList(draft.SuccessCriteria)
	fromStage := t.Stage
	t.Stage = StagePlanning
	t.UpdatedAt = time.Now().UTC()

	if err := s.AppendStageTransition(projectID, id, StageTransition{
		FromStage: fromStage,
		ToStage:   t.Stage,
		Trigger:   TransitionTriggerFinalizeRequirements,
	}); err != nil {
		return Task{}, err
	}
	if err := s.writeTask(projectID, t); err != nil {
		return Task{}, err
	}
	return t, nil
}

// FinalizePlan is the human "Finalize" action for Planning Mode: it
// persists plan.yaml and advances Stage from "planning" to
// "implementation". The task must currently be in "planning" stage.
func (s *FileStore) FinalizePlan(projectID, id string, plan Plan) (Task, error) {
	t, err := s.Get(projectID, id)
	if err != nil {
		return Task{}, err
	}
	if t.Stage != StagePlanning {
		return Task{}, fmt.Errorf("finalizing plan for %s (stage %q): %w", id, t.Stage, ErrWrongStage)
	}

	plan.Approach = strings.TrimSpace(plan.Approach)
	plan.RecommendedExecutor = strings.TrimSpace(plan.RecommendedExecutor)
	plan.Steps = trimmedList(plan.Steps)
	plan.Risks = trimmedList(plan.Risks)
	if err := s.writePlan(projectID, id, plan); err != nil {
		return Task{}, err
	}

	fromStage := t.Stage
	t.Stage = StageImplementation
	t.UpdatedAt = time.Now().UTC()

	if err := s.AppendStageTransition(projectID, id, StageTransition{
		FromStage: fromStage,
		ToStage:   t.Stage,
		Trigger:   TransitionTriggerFinalizePlan,
	}); err != nil {
		return Task{}, err
	}
	if err := s.writeTask(projectID, t); err != nil {
		return Task{}, err
	}
	return t, nil
}

// FinalizeReview is the human "Finalize" action for Review: it records an
// append-only reviews/review-NNN.yaml verdict (RecordReview) and moves Stage
// according to the draft's three-way decision. The task must currently be in
// "review" stage, or (Milestone 7) "pr_review" — the same function handles
// both an internal Review verdict and a pr_review rejection, since PR 4's
// fork-from-prior-branch gate and PR 5's rejected-context addendum both key
// off the latest recorded review's decision, not which stage produced it.
// Unlike GrillMe/Planning Finalize (which only ever advance one step),
// Review's Finalize can move Stage in either direction, because its decision
// encodes which direction is correct (CONTEXT.md's **Review**):
//
//   - approved      → "pr_review" (Milestone 7 PR 2; was "merged" through PR 1).
//     Only valid from "review" — "pr_review" only ever surfaces the reject
//     decisions below; a bare "approved" reaching FinalizeReview from
//     "pr_review" would otherwise silently no-op the stage while still
//     writing a spurious review record, since "pr_review" is itself the
//     stage an approval already landed on.
//   - needs_changes → "implementation" (a fresh execution attempt; the
//     verdict's notes are preserved in the review record for the
//     execute-retrigger path to surface)
//   - rejected      → "requirements" (the requirements/plan themselves were
//     wrong, not just the implementation; reopens the GrillMe conversation)
//
// The review record is written before the Stage advance, so Stage stays the
// single source of truth for how far Finalize got: if RecordReview succeeds
// but the task.yaml write then fails, the verdict is on disk but Stage hasn't
// moved yet — a safely-retriable state (the append-only store means a retry
// records a fresh verdict rather than corrupting the prior one).
func (s *FileStore) FinalizeReview(projectID, id string, draft ReviewDraft) (Task, error) {
	t, err := s.Get(projectID, id)
	if err != nil {
		return Task{}, err
	}
	if t.Stage != StageReview && t.Stage != StagePRReview {
		return Task{}, fmt.Errorf("finalizing review for %s (stage %q): %w", id, t.Stage, ErrWrongStage)
	}
	if draft.Decision == ReviewDecisionApproved && t.Stage != StageReview {
		return Task{}, fmt.Errorf("finalizing review for %s (stage %q): approved is only valid from %q: %w", id, t.Stage, StageReview, ErrWrongStage)
	}

	var nextStage string
	switch draft.Decision {
	case ReviewDecisionApproved:
		nextStage = StagePRReview
	case ReviewDecisionNeedsChanges:
		nextStage = StageImplementation
	case ReviewDecisionRejected:
		nextStage = StageRequirements
	default:
		return Task{}, fmt.Errorf("finalizing review for %s: unknown decision %q", id, draft.Decision)
	}

	// The task is confirmed at StageReview or StagePRReview above, so the
	// execution this review is about is unambiguous right now: only a
	// successful execution ever advances Stage to review, and
	// RecordExecution requires StageImplementation for a success, so no new
	// execution can have been recorded since — true whether the task has
	// since moved on to StagePRReview or not, since reaching StageImplementation
	// again requires passing back through a fresh FinalizePlan first.
	// Capturing that link now (Review.ExecutionID) is safer than
	// reconstructing it later after further stage transitions/retries.
	executions, err := s.ListExecutions(projectID, id)
	if err != nil {
		return Task{}, err
	}
	var executionID string
	if len(executions) > 0 {
		executionID = executions[len(executions)-1].ExecutionID
	}

	reviewID, err := s.NextReviewID(projectID, id)
	if err != nil {
		return Task{}, err
	}
	if _, err := s.RecordReview(projectID, id, Review{
		ReviewID:    reviewID,
		ExecutionID: executionID,
		Decision:    draft.Decision,
		Notes:       strings.TrimSpace(draft.Notes),
	}); err != nil {
		return Task{}, err
	}

	fromStage := t.Stage
	t.Stage = nextStage
	t.UpdatedAt = time.Now().UTC()
	if err := s.AppendStageTransition(projectID, id, StageTransition{
		FromStage: fromStage,
		ToStage:   t.Stage,
		Trigger:   TransitionTriggerFinalizeReview,
		ReviewID:  reviewID,
	}); err != nil {
		return Task{}, err
	}
	if err := s.writeTask(projectID, t); err != nil {
		return Task{}, err
	}
	return t, nil
}

// MarkPRMerged is the human "Mark as merged" action for pr_review
// (docs/milestones/done/milestone7.md): a human assertion that the PR was merged
// on GitHub, with no polling and no review-record write — there's no
// approved/rejected/needs_changes decision being made, the PR already got
// its verdict externally. Moves Stage from "pr_review" to "cleanup", not
// straight to "merged" (as of the execution-worktree-cleanup milestone): the
// caller (internal/api's handleMarkPRMerged) runs the best-effort worktree-
// removal routine synchronously right after this call and only calls
// CompleteCleanup once every worktree is actually gone, so a task never
// silently sits at "merged" while its execution worktrees are still
// unaccounted for. The trigger stays "mark_pr_merged" — this is still the
// same human action, just landing one stage earlier than it used to.
// Requires PullRequest to already be set (populated by RecordPullRequest,
// below): a task shouldn't be markable "merged" if the system never
// recorded a PR against it. Both guards wrap ErrWrongStage (Milestone 7 PR 3)
// — the task isn't in a state this action can be taken from either way, so
// both should map to the same 409, not one 409ing and the other falling
// through to a 500 as "no pull_request recorded" originally did.
func (s *FileStore) MarkPRMerged(projectID, id string) (Task, error) {
	t, err := s.Get(projectID, id)
	if err != nil {
		return Task{}, err
	}
	if t.Stage != StagePRReview {
		return Task{}, fmt.Errorf("marking PR merged for %s (stage %q): %w", id, t.Stage, ErrWrongStage)
	}
	if t.PullRequest == nil {
		return Task{}, fmt.Errorf("marking PR merged for %s: no pull_request recorded: %w", id, ErrWrongStage)
	}

	fromStage := t.Stage
	t.Stage = StageCleanup
	t.UpdatedAt = time.Now().UTC()
	if err := s.AppendStageTransition(projectID, id, StageTransition{
		FromStage: fromStage,
		ToStage:   t.Stage,
		Trigger:   TransitionTriggerMarkPRMerged,
	}); err != nil {
		return Task{}, err
	}
	if err := s.writeTask(projectID, t); err != nil {
		return Task{}, err
	}
	return t, nil
}

// CompleteCleanup advances Stage from "cleanup" to "merged" once the caller
// has confirmed every one of a task's execution worktrees was removed or
// was already gone (internal/api/pr.go, cmd/sweep-merged-worktrees) — the
// terminal step of the flow MarkPRMerged starts. Only valid from "cleanup";
// a caller that hasn't actually driven the cleanup routine to a clean
// result has no business calling this.
func (s *FileStore) CompleteCleanup(projectID, id string) (Task, error) {
	t, err := s.Get(projectID, id)
	if err != nil {
		return Task{}, err
	}
	if t.Stage != StageCleanup {
		return Task{}, fmt.Errorf("completing cleanup for %s (stage %q): %w", id, t.Stage, ErrWrongStage)
	}

	fromStage := t.Stage
	t.Stage = StageMerged
	t.UpdatedAt = time.Now().UTC()
	if err := s.AppendStageTransition(projectID, id, StageTransition{
		FromStage: fromStage,
		ToStage:   t.Stage,
		Trigger:   TransitionTriggerCleanupComplete,
	}); err != nil {
		return Task{}, err
	}
	if err := s.writeTask(projectID, t); err != nil {
		return Task{}, err
	}
	return t, nil
}

// SetCleanupStatus persists status as the task's latest CleanupStatus
// report, without touching Stage — the routine that ran cleanup (whether it
// left the task parked at "cleanup" or is about to call CompleteCleanup)
// always calls this first, mirroring RecordPullRequest's "persist first,
// advance stage separately" shape. Deliberately not stage-guarded: the
// one-off sweep CLI calls this against tasks already sitting at "merged"
// (cleaning up worktrees orphaned before this mechanism existed), which
// SetCleanupStatus itself has no reason to refuse.
func (s *FileStore) SetCleanupStatus(projectID, id string, status []CleanupWorktreeStatus) (Task, error) {
	t, err := s.Get(projectID, id)
	if err != nil {
		return Task{}, err
	}

	t.CleanupStatus = status
	t.UpdatedAt = time.Now().UTC()
	if err := s.writeTask(projectID, t); err != nil {
		return Task{}, err
	}
	return t, nil
}

// RecordPullRequest is the "Push & Open PR" action's persistence step
// (docs/milestones/done/milestone7.md PR 2): records the PR
// agentrunner.PushAndOpenPR just pushed/opened onto the task, without
// changing Stage. Guarded to StagePRReview like MarkPRMerged — a PR is only
// ever meaningful for a task actually sitting in pr_review. Called
// uniformly whether this is the first PR opened for the task or a later
// refspec-push continuing an existing one (see PushAndOpenPR's doc
// comment): the persisted shape is the same either way, so there's no
// special-casing at this layer.
func (s *FileStore) RecordPullRequest(projectID, id string, pr PullRequest) (Task, error) {
	t, err := s.Get(projectID, id)
	if err != nil {
		return Task{}, err
	}
	if t.Stage != StagePRReview {
		return Task{}, fmt.Errorf("recording pull request for %s (stage %q): %w", id, t.Stage, ErrWrongStage)
	}

	t.PullRequest = &pr
	t.UpdatedAt = time.Now().UTC()
	if err := s.writeTask(projectID, t); err != nil {
		return Task{}, err
	}
	return t, nil
}

// ReviseToRequirements is the "Revise Requirements" action (CONTEXT.md's
// "Revise"): moves Stage back from "planning" to "requirements", reopening
// the requirements Conversation (GetConversation/AppendConversationMessages
// already resume the same file — no separate action needed for that part).
// Only valid from "planning". reason is an optional, human-typed
// explanation for why (e.g. "the plan missed X") — recorded on the
// StageTransition if non-empty, left empty otherwise; callers with no
// reason to give should pass "".
func (s *FileStore) ReviseToRequirements(projectID, id, reason string) (Task, error) {
	t, err := s.Get(projectID, id)
	if err != nil {
		return Task{}, err
	}
	if t.Stage != StagePlanning {
		return Task{}, fmt.Errorf("revising requirements for %s (stage %q): %w", id, t.Stage, ErrWrongStage)
	}

	fromStage := t.Stage
	t.Stage = StageRequirements
	t.UpdatedAt = time.Now().UTC()

	if err := s.AppendStageTransition(projectID, id, StageTransition{
		FromStage: fromStage,
		ToStage:   t.Stage,
		Trigger:   TransitionTriggerReviseRequirements,
		Reason:    strings.TrimSpace(reason),
	}); err != nil {
		return Task{}, err
	}
	if err := s.writeTask(projectID, t); err != nil {
		return Task{}, err
	}
	return t, nil
}

// ReviseToPlanning is the "Revise Plan" action: moves Stage back from
// "implementation" or "review" to "planning". reason is an optional,
// human-typed explanation for why (e.g. "I wanted icons, not words") —
// recorded on the StageTransition if non-empty, left empty otherwise;
// callers with no reason to give should pass "".
func (s *FileStore) ReviseToPlanning(projectID, id, reason string) (Task, error) {
	t, err := s.Get(projectID, id)
	if err != nil {
		return Task{}, err
	}
	if t.Stage != StageImplementation && t.Stage != StageReview {
		return Task{}, fmt.Errorf("revising plan for %s (stage %q): %w", id, t.Stage, ErrWrongStage)
	}

	fromStage := t.Stage
	t.Stage = StagePlanning
	t.UpdatedAt = time.Now().UTC()

	if err := s.AppendStageTransition(projectID, id, StageTransition{
		FromStage: fromStage,
		ToStage:   t.Stage,
		Trigger:   TransitionTriggerReviseToPlanning,
		Reason:    strings.TrimSpace(reason),
	}); err != nil {
		return Task{}, err
	}
	if err := s.writeTask(projectID, t); err != nil {
		return Task{}, err
	}
	return t, nil
}
