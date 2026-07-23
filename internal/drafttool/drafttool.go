// Package drafttool holds the Draft-proposing tool definitions
// (propose_context, propose_plan) shared between internal/api's stage
// conversations (which register them per-conversation against
// ClaudeRunner/ChatClientRunner) and cmd/draftmcp (which exposes them as a
// static MCP server for CodexRunner, since codex-agent-sdk-go has no
// in-process "SDK MCP server" equivalent — a real external MCP server is
// the only way to offer a custom tool to a codex thread). Defining the
// name/description/schema once here means both call sites see the same
// tool shape by construction, not by convention.
package drafttool

import "encoding/json"

const (
	ProposeContextName   = "propose_context"
	ProposePlanName      = "propose_plan"
	ProposeReviewName    = "propose_review"
	ProposeKnowledgeName = "propose_knowledge"
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
        "files": {"type": "array", "items": {"type": "string"}},
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
// ProposeReview, not instead of it (docs/milestones/milestone9.md): a
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

// All returns every known Draft tool definition, in a stable order — used
// by cmd/draftmcp to build its static tools/list response.
func All() []Definition {
	return []Definition{ProposeContext, ProposePlan, ProposeReview, ProposeKnowledge}
}
