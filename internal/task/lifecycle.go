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
func (s *FileStore) FinalizeRequirements(id string, draft RequirementsDraft) (Task, error) {
	t, err := s.Get(id)
	if err != nil {
		return Task{}, err
	}
	if t.Stage != StageRequirements {
		return Task{}, fmt.Errorf("finalizing requirements for %s (stage %q): %w", id, t.Stage, ErrWrongStage)
	}

	draft.Context.Summary = strings.TrimSpace(draft.Context.Summary)
	draft.Context.Background = strings.TrimSpace(draft.Context.Background)
	draft.Context.Detail = strings.TrimSpace(draft.Context.Detail)
	draft.Context.Files = trimmedList(draft.Context.Files)
	draft.Context.Verification = trimmedVerification(draft.Context.Verification)
	draft.Context.OpenQuestions = trimmedList(draft.Context.OpenQuestions)
	if err := s.writeContext(id, draft.Context); err != nil {
		return Task{}, err
	}

	t.Objective = strings.TrimSpace(draft.Objective)
	t.Constraints = trimmedList(draft.Constraints)
	t.Assumptions = trimmedList(draft.Assumptions)
	t.SuccessCriteria = trimmedList(draft.SuccessCriteria)
	t.Stage = StagePlanning
	t.UpdatedAt = time.Now().UTC()

	if err := s.writeTask(t); err != nil {
		return Task{}, err
	}
	return t, nil
}

// FinalizePlan is the human "Finalize" action for Planning Mode: it
// persists plan.yaml and advances Stage from "planning" to
// "implementation". The task must currently be in "planning" stage.
func (s *FileStore) FinalizePlan(id string, plan Plan) (Task, error) {
	t, err := s.Get(id)
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
	if err := s.writePlan(id, plan); err != nil {
		return Task{}, err
	}

	t.Stage = StageImplementation
	t.UpdatedAt = time.Now().UTC()

	if err := s.writeTask(t); err != nil {
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
func (s *FileStore) FinalizeReview(id string, draft ReviewDraft) (Task, error) {
	t, err := s.Get(id)
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
	executions, err := s.ListExecutions(id)
	if err != nil {
		return Task{}, err
	}
	var executionID string
	if len(executions) > 0 {
		executionID = executions[len(executions)-1].ExecutionID
	}

	reviewID, err := s.NextReviewID(id)
	if err != nil {
		return Task{}, err
	}
	if _, err := s.RecordReview(id, Review{
		ReviewID:    reviewID,
		ExecutionID: executionID,
		Decision:    draft.Decision,
		Notes:       strings.TrimSpace(draft.Notes),
	}); err != nil {
		return Task{}, err
	}

	t.Stage = nextStage
	t.UpdatedAt = time.Now().UTC()
	if err := s.writeTask(t); err != nil {
		return Task{}, err
	}
	return t, nil
}

// MarkPRMerged is the human "Mark as merged" action for pr_review
// (docs/milestones/done/milestone7.md): a human assertion that the PR was merged
// on GitHub, with no polling and no review-record write — there's no
// approved/rejected/needs_changes decision being made, the PR already got
// its verdict externally. Moves Stage directly from "pr_review" to "merged".
// Requires PullRequest to already be set (populated by RecordPullRequest,
// below): a task shouldn't be markable "merged" if the system never
// recorded a PR against it. Both guards wrap ErrWrongStage (Milestone 7 PR 3)
// — the task isn't in a state this action can be taken from either way, so
// both should map to the same 409, not one 409ing and the other falling
// through to a 500 as "no pull_request recorded" originally did.
func (s *FileStore) MarkPRMerged(id string) (Task, error) {
	t, err := s.Get(id)
	if err != nil {
		return Task{}, err
	}
	if t.Stage != StagePRReview {
		return Task{}, fmt.Errorf("marking PR merged for %s (stage %q): %w", id, t.Stage, ErrWrongStage)
	}
	if t.PullRequest == nil {
		return Task{}, fmt.Errorf("marking PR merged for %s: no pull_request recorded: %w", id, ErrWrongStage)
	}

	t.Stage = StageMerged
	t.UpdatedAt = time.Now().UTC()
	if err := s.writeTask(t); err != nil {
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
func (s *FileStore) RecordPullRequest(id string, pr PullRequest) (Task, error) {
	t, err := s.Get(id)
	if err != nil {
		return Task{}, err
	}
	if t.Stage != StagePRReview {
		return Task{}, fmt.Errorf("recording pull request for %s (stage %q): %w", id, t.Stage, ErrWrongStage)
	}

	t.PullRequest = &pr
	t.UpdatedAt = time.Now().UTC()
	if err := s.writeTask(t); err != nil {
		return Task{}, err
	}
	return t, nil
}

// ReviseToRequirements is the "Revise Requirements" action (CONTEXT.md's
// "Revise"): moves Stage back from "planning" to "requirements", reopening
// the requirements Conversation (GetConversation/AppendConversationMessages
// already resume the same file — no separate action needed for that part).
// Only valid from "planning".
func (s *FileStore) ReviseToRequirements(id string) (Task, error) {
	t, err := s.Get(id)
	if err != nil {
		return Task{}, err
	}
	if t.Stage != StagePlanning {
		return Task{}, fmt.Errorf("revising requirements for %s (stage %q): %w", id, t.Stage, ErrWrongStage)
	}

	t.Stage = StageRequirements
	t.UpdatedAt = time.Now().UTC()

	if err := s.writeTask(t); err != nil {
		return Task{}, err
	}
	return t, nil
}

// ReviseToPlanning is the "Revise Plan" action: moves Stage back from
// "implementation" or "review" to "planning". Milestone 5 (the executor
// that actually reaches "implementation") doesn't exist yet, so this
// boundary won't be exercised in practice until then, but the transition
// is still valid structurally per CLAUDE.md ("each stage... can be
// revisited").
func (s *FileStore) ReviseToPlanning(id string) (Task, error) {
	t, err := s.Get(id)
	if err != nil {
		return Task{}, err
	}
	if t.Stage != StageImplementation && t.Stage != StageReview {
		return Task{}, fmt.Errorf("revising plan for %s (stage %q): %w", id, t.Stage, ErrWrongStage)
	}

	t.Stage = StagePlanning
	t.UpdatedAt = time.Now().UTC()

	if err := s.writeTask(t); err != nil {
		return Task{}, err
	}
	return t, nil
}
