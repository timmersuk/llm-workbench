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
	ProposeContextName = "propose_context"
	ProposePlanName    = "propose_plan"
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
        "verification": {"type": "array", "items": {"type": "string"}},
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

// All returns every known Draft tool definition, in a stable order — used
// by cmd/draftmcp to build its static tools/list response.
func All() []Definition {
	return []Definition{ProposeContext, ProposePlan}
}
