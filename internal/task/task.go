// Package task provides the Task type and a file-backed store over every
// project's tasks/ directory, per docs/task schema v0.md. A task belongs to
// exactly one project permanently, named by the Project field and by the
// explicit projectID parameter every Store method takes — the store itself
// is a single process-wide singleton rooted at the shared projects root
// (the same Root as project.FileStore), not one instance per project, and
// a task's id is unique only within its owning project, not globally.
package task

import "time"

// Stage values, per docs/task schema v0.md §2. Declared as constants so
// lifecycle.go's transitions don't rely on string literals scattered across
// the package.
//
// StagePRReview is introduced by Milestone 7 PR 1 (docs/milestones/done/milestone7.md)
// as real, unit-tested machinery, but is not yet reachable through any live
// path: FinalizeReview's approved branch still targets StageCompleted until a
// later PR retargets it alongside the "Push & Open PR" action and its
// frontend.
//
// StageCleanup sits between StagePRReview and StageCompleted: MarkPRMerged
// (lifecycle.go) moves a task here instead of straight to StageCompleted, and
// the synchronous, best-effort worktree-removal routine
// (agentrunner.CleanupTaskWorktrees) runs before CompleteCleanup advances it
// the rest of the way. A task only ever "sticks" at StageCleanup when that
// routine skipped or failed to remove at least one of its execution
// worktrees (CleanupStatus records why) — the human-facing Retry/Force
// action re-drives the same routine from here.
const (
	StageRequirements   = "requirements"
	StagePlanning       = "planning"
	StageImplementation = "implementation"
	StageReview         = "review"
	StagePRReview       = "pr_review"
	StageCleanup        = "cleanup"
	StageCompleted      = "completed"
)

// Cleanup outcome values recorded per execution worktree on
// Task.CleanupStatus by agentrunner.CleanupTaskWorktrees's caller
// (internal/api/pr.go), mirroring agentrunner.WorktreeCleanupResult.Outcome
// one-for-one (duplicated as a separate constant set there, not imported
// from this package, to avoid a new agentrunner->task dependency).
const (
	CleanupOutcomeRemoved     = "removed"
	CleanupOutcomeAlreadyGone = "already-gone"
	CleanupOutcomeSkipped     = "skipped"
	CleanupOutcomeFailed      = "failed"
)

// CleanupWorktreeStatus is one execution attempt's outcome from the most
// recent cleanup pass, persisted on Task.CleanupStatus so a human (or the
// frontend's CleanupPanel) can see exactly what happened without re-running
// anything — the same "no hidden state" posture ExecutionOutput.WorkspaceDirty
// already follows for a comparable best-effort, non-fatal signal.
type CleanupWorktreeStatus struct {
	ExecutionID string `yaml:"execution_id" json:"execution_id"`
	// Outcome is one of the CleanupOutcome* constants above.
	Outcome string `yaml:"outcome" json:"outcome"`
	// Reason is a short, human-readable explanation — always set for
	// "skipped"/"failed", empty for "removed"/"already-gone" unless a
	// secondary step (e.g. the branch delete after a successful worktree
	// remove) also hit a problem worth surfacing.
	Reason string `yaml:"reason,omitempty" json:"reason,omitempty"`
}

// Task is a versioned intent object stored as <task root>/<id>/task.yaml
// (e.g. data/projects/<projectId>/tasks/<id>/task.yaml with the default
// WORKSPACE_ROOT).
type Task struct {
	ID    string `yaml:"id" json:"id"`
	Title string `yaml:"title" json:"title"`

	Project string `yaml:"project" json:"project"`

	Stage string `yaml:"stage" json:"stage"`

	CreatedAt time.Time `yaml:"created_at" json:"created_at"`
	UpdatedAt time.Time `yaml:"updated_at" json:"updated_at"`

	Objective string `yaml:"objective" json:"objective"`

	Constraints     []string `yaml:"constraints" json:"constraints"`
	Assumptions     []string `yaml:"assumptions" json:"assumptions"`
	SuccessCriteria []string `yaml:"success_criteria" json:"success_criteria"`

	AgentDefaults *AgentDefaults `yaml:"agent_defaults,omitempty" json:"agent_defaults,omitempty"`

	References References `yaml:"references" json:"references"`

	// DraftSessionID, when set, is the task-drafts session id (internal/api's
	// task-drafts backend) whose pre-creation conversation produced this
	// task — a permanent pointer to
	// <projects root>/<projectId>/task-drafts/<DraftSessionID>/conversation.yaml,
	// set once at Create time and never changed afterward. Absent (empty)
	// for a task created before this mechanism existed, or hypothetically
	// any other way. Two things read it: the read-only "view pre-creation
	// conversation" screen (TaskDraftView.tsx), and GrillMe's first
	// Requirements-stage turn, which folds that frozen transcript into its
	// system prompt as an addendum (buildTaskDraftContext,
	// internal/api/stage_conversation.go) so the interview doesn't re-ask
	// what the human already settled before the task existed.
	DraftSessionID string `yaml:"draft_session_id,omitempty" json:"draft_session_id,omitempty"`

	// PullRequest is set once the (not-yet-built) "Push & Open PR" action
	// has pushed this task's execution branch and opened a PR. Absent
	// (nil) until then — a task has at most one open PR at a time by
	// construction, so this is not an append-only store the way
	// reviews/executions are.
	PullRequest *PullRequest `yaml:"pull_request,omitempty" json:"pull_request,omitempty"`

	// KnowledgeActivity is an append-only log of every knowledge concept
	// this task's Review conversation proposed and a human then accepted or
	// rejected (handleFinalizeKnowledge, internal/api/knowledge_draft.go) —
	// the durable answer to "did this task's knowledge proposal actually
	// happen," independent of KnowledgeStore's own concept files (which live
	// in a workspace-wide store with no back-reference to the task that
	// produced them). Never affects Stage or any other task field.
	KnowledgeActivity []KnowledgeActivityEntry `yaml:"knowledge_activity,omitempty" json:"knowledge_activity,omitempty"`

	// CleanupStatus is the most recent execution-worktree cleanup pass's
	// per-attempt report (SetCleanupStatus, lifecycle.go), populated once a
	// task first reaches StageCleanup. Overwritten wholesale by every
	// subsequent pass (a retry/force re-run), not append-only like
	// reviews/executions — only the latest pass's outcome is ever
	// actionable. Absent (nil) for a task that has never reached
	// StageCleanup, including every task merged before this mechanism
	// existed.
	CleanupStatus []CleanupWorktreeStatus `yaml:"cleanup_status,omitempty" json:"cleanup_status,omitempty"`
}

// KnowledgeActivityAction is the fixed vocabulary of what a knowledge
// Finalize decision (internal/api/knowledge_draft.go) resulted in.
type KnowledgeActivityAction string

const (
	KnowledgeActivityCreated  KnowledgeActivityAction = "created"
	KnowledgeActivityUpdated  KnowledgeActivityAction = "updated"
	KnowledgeActivityRejected KnowledgeActivityAction = "rejected"
)

// KnowledgeActivityEntry is one row of Task.KnowledgeActivity, appended by
// AppendKnowledgeActivity (knowledge_activity.go).
type KnowledgeActivityEntry struct {
	ConceptID string                  `yaml:"concept_id" json:"concept_id"`
	Type      string                  `yaml:"type,omitempty" json:"type,omitempty"`
	Action    KnowledgeActivityAction `yaml:"action" json:"action"`
	CreatedAt time.Time               `yaml:"created_at" json:"created_at"`
}

type AgentSelection struct {
	Executor string `yaml:"executor" json:"executor"`
	Model    string `yaml:"model" json:"model"`
	Effort   string `yaml:"effort" json:"effort"`
}

type AgentDefaults struct {
	StageConversation AgentSelection `yaml:"stage_conversation" json:"stage_conversation"`
	Execution         AgentSelection `yaml:"execution" json:"execution"`
}

// PullRequest records the GitHub PR opened for a task's approved execution,
// per docs/milestones/done/milestone7.md's "Schema changes".
type PullRequest struct {
	URL    string `yaml:"url" json:"url"`
	Number int    `yaml:"number" json:"number"`
	// Branch is the remote branch the PR actually tracks — needed because
	// a later execution attempt continuing after a rejection cycle lands
	// on a different local branch (ADR 0012 decision 1) and must push onto
	// this recorded branch via refspec, not its own name.
	Branch string `yaml:"branch" json:"branch"`
}

// References links a task to durable knowledge and code repositories.
type References struct {
	Knowledge []string `yaml:"knowledge" json:"knowledge"`
	Repo      []string `yaml:"repo" json:"repo"`
}
