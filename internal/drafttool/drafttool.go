// Package drafttool holds the Draft-proposing tool definitions
// (propose_context, propose_plan) plus the ask_question interview
// affordance, shared between internal/api's stage conversations (which
// register them per-conversation against ClaudeRunner/ChatClientRunner) and
// cmd/draftmcp (which exposes them as a static MCP server for CodexRunner,
// since codex-agent-sdk-go has no in-process "SDK MCP server" equivalent —
// a real external MCP server is the only way to offer a custom tool to a
// codex thread). Defining the name/description/schema once here means both
// call sites see the same tool shape by construction, not by convention.
// ask_question isn't itself a Draft proposal — it never produces something
// Finalize writes to disk — but it's defined with the same Definition shape
// and travels the same registration/approval plumbing (stageTool in
// internal/api/stage_conversation.go, drafttool.All() below), so it gets
// its own Definition var here rather than a separate package.
package drafttool

import "encoding/json"

const (
	ProposeTaskName      = "propose_task"
	ProposeContextName   = "propose_context"
	ProposePlanName      = "propose_plan"
	ProposeReviewName    = "propose_review"
	ProposeKnowledgeName = "propose_knowledge"
	AskQuestionName      = "ask_question"
)

// Definition is one Draft tool's name, human-readable description, and
// JSON Schema for its arguments — enough to build either a chat.Tool
// (internal/api/stage_conversation.go) or an MCP tools/list entry
// (cmd/draftmcp).
type Definition struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

var proposeTaskSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "id": {"type": "string", "description": "A short, url-safe slug for the task, unique within the project (e.g. fix-login-bug). Propose one derived from the title, but this is exactly the kind of field the human is expected to review or override before creation."},
    "title": {"type": "string"},
    "objective": {"type": "string"},
    "constraints": {"type": "array", "items": {"type": "string"}},
    "assumptions": {"type": "array", "items": {"type": "string"}},
    "success_criteria": {"type": "array", "items": {"type": "string"}},
    "references": {
      "type": "object",
      "properties": {
        "knowledge": {"type": "array", "items": {"type": "string"}, "description": "Knowledge concept ids (data/knowledge/ paths, .md suffix removed) relevant to this task."},
        "repo": {"type": "array", "items": {"type": "string"}, "description": "Repositories relevant to this task, if narrower than the whole project."}
      }
    }
  },
  "required": ["id", "title", "objective"]
}`)

var proposeContextSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "objective": {"type": "string"},
    "constraints": {"type": "array", "items": {"type": "string"}},
    "assumptions": {"type": "array", "items": {"type": "string"}},
    "success_criteria": {"type": "array", "items": {"type": "string"}},
    "context": {
      "type": "object",
      "properties": {
        "summary": {"type": "string"},
        "background": {"type": "string"},
        "files": {
          "type": "array",
          "items": {
            "type": "object",
            "properties": {
              "path": {"type": "string"},
              "role": {"type": "string"}
            },
            "required": ["path"]
          }
        },
        "detail": {"type": "string"},
        "verification": {
          "type": "array",
          "items": {
            "type": "object",
            "properties": {
              "description": {"type": "string"},
              "kind": {"type": "string", "enum": ["agent_executable", "human_judgment"]}
            },
            "required": ["description", "kind"]
          }
        },
        "open_questions": {"type": "array", "items": {"type": "string"}}
      },
      "required": ["summary"]
    }
  },
  "required": ["objective", "context"]
}`)

var proposePlanSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "approach": {"type": "string"},
    "steps": {"type": "array", "items": {"type": "string"}},
    "risks": {"type": "array", "items": {"type": "string"}},
    "estimated_complexity": {"type": "string", "enum": ["low", "medium", "high"]},
    "recommended_executor": {"type": "string"}
  },
  "required": ["approach", "steps", "estimated_complexity"]
}`)

var proposeReviewSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "decision": {"type": "string", "enum": ["approved", "rejected", "needs_changes"]},
    "notes": {"type": "string"}
  },
  "required": ["decision", "notes"]
}`)

var askQuestionSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "options": {
      "type": "array",
      "items": {"type": "string"},
      "minItems": 1,
      "description": "The distinct answers offered for this turn's question. The question text itself belongs in your normal reply content, not here — this only carries the selectable choices."
    },
    "recommended_option": {"type": "string", "description": "Your recommended answer — must match one of options verbatim."},
    "recommendation_reason": {"type": "string", "description": "A short reason for the recommendation, shown alongside it."}
  },
  "required": ["options", "recommended_option", "recommendation_reason"]
}`)

var proposeKnowledgeSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "concept_id": {"type": "string", "description": "The OKF concept id (its path under data/knowledge/ with the .md suffix removed), e.g. coding-standards/logging. Reuse an existing id to propose an edit; use a new one to propose a brand-new concept."},
    "type": {"type": "string", "description": "The concept's OKF type, e.g. Coding Standard, Architecture Decision, Design Note, Domain Note, Operational Practice, Reference."},
    "frontmatter": {"type": "object", "description": "Any additional frontmatter fields beyond type — e.g. title, description, tags, resource."},
    "body": {"type": "string", "description": "The concept's full markdown body."}
  },
  "required": ["concept_id", "type", "body"]
}`)

// ProposeTask is the task-drafts (pre-creation "New Task" chat) Draft tool
// definition — proposes a fully-populated draft task.yaml (id, title,
// objective, constraints, assumptions, success criteria, references) for
// the human to review/edit before the existing Create Task API is ever
// called. Distinct from ProposeContext: this runs before a task exists at
// all, so it also carries id/title (which task.yaml itself owns but
// ProposeContext never touches, since GrillMe always starts from an
// already-created task) and never touches context.yaml's narrative fields
// (summary/background/files/detail/verification/open_questions) — those
// stay GrillMe's job, run immediately after creation, informed by this
// conversation's transcript (see the Requirements-stage prompt addendum,
// internal/api/stage_conversation.go's buildTaskDraftContext).
var ProposeTask = Definition{
	Name:        ProposeTaskName,
	Description: "Propose a fully-populated draft task (id, title, objective, constraints, assumptions, success criteria, references) for the human to review and edit before the task is actually created.",
	Schema:      proposeTaskSchema,
}

// ProposeContext is the Requirements-stage Draft tool definition.
var ProposeContext = Definition{
	Name:        ProposeContextName,
	Description: "Propose the task's requirements (objective, constraints, assumptions, success criteria) and narrative context for the human to review before Finalize.",
	Schema:      proposeContextSchema,
}

// ProposePlan is the Planning-stage Draft tool definition.
var ProposePlan = Definition{
	Name:        ProposePlanName,
	Description: "Propose a structured execution plan for the human to review before Finalize.",
	Schema:      proposePlanSchema,
}

// ProposeReview is the Review-stage Draft tool definition.
var ProposeReview = Definition{
	Name:        ProposeReviewName,
	Description: "Propose this execution's review outcome (decision + notes) for the human to review before Finalize.",
	Schema:      proposeReviewSchema,
}

// ProposeKnowledge is the Review-stage Draft tool for folding a durable
// learning into the Knowledge layer (data/knowledge/) — offered alongside
// ProposeReview, not instead of it (docs/milestones/done/milestone9.md): a
// review conversation can propose any number of knowledge concepts before
// (or instead of) proposing its review verdict. Unlike ProposeReview's
// three-way decision, this is two-way (accept/reject) — there is no prior
// execution branch for a "needs_changes" state to continue from, so a
// rejected proposal is just more conversation, not a special decision
// value. Always carries the concept's full resulting content (never a
// diff), covering both a brand-new concept_id and an edit to an existing
// one with the same tool call shape.
var ProposeKnowledge = Definition{
	Name:        ProposeKnowledgeName,
	Description: "Propose a new knowledge concept, or an edit to an existing one, for the human to accept or reject — always the full resulting content (concept_id, type, frontmatter, body), never a diff.",
	Schema:      proposeKnowledgeSchema,
}

// AskQuestion is the interview-discipline tool offered alongside
// ProposeContext (Requirements/GrillMe) and ProposePlan (Planning Mode) —
// see stageTool in internal/api/stage_conversation.go. Composes with the
// existing "one question per turn, with a recommended answer" interview
// rule (grillMeSystemPrompt/planningModeSystemPrompt) rather than replacing
// it: the rule still governs pacing and content, this just routes the
// options/recommendation through a structured tool call instead of prose,
// so the frontend can render clickable choices (StageConversationPanel.tsx)
// instead of the human having to read and retype an answer. Unlike every
// other Definition in this package, a call to this tool is never persisted
// as something Finalize can write to task.yaml/context.yaml/plan.yaml — the
// human's answer (typed or clicked) is just the next plain chat message,
// so this tool's payload never round-trips beyond the one turn that
// proposed it.
var AskQuestion = Definition{
	Name:        AskQuestionName,
	Description: "Ask the human one interview question with a fixed set of selectable options and a recommended answer, instead of writing the options into your message text. The question text itself still goes in your normal reply content — this tool call only carries the options, the recommended one, and why.",
	Schema:      askQuestionSchema,
}

// All returns every known Draft tool definition, in a stable order — used
// by cmd/draftmcp to build its static tools/list response.
func All() []Definition {
	return []Definition{ProposeTask, ProposeContext, ProposePlan, ProposeReview, ProposeKnowledge, AskQuestion}
}
