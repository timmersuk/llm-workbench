// Package knowledgetool holds the shared name/description/schema and
// execution logic for the read-only knowledge query tools — Milestone 9's
// "discovery mechanism" (docs/milestones/done/milestone9.md): list_knowledge_concepts
// and get_knowledge_concept, an always-available, unfiltered, whole-bundle
// query surface over data/knowledge/, distinct from internal/drafttool's
// proposal tools in kind, not just name. A Draft tool ends a stage
// conversation's turn for human review and is never actually executed by
// this codebase (only acknowledged — the real handling is Finalize); these
// tools are genuinely executed for real, every time, and never end a turn.
// Because of that difference they carry their own execution logic here too
// (ExecuteList/ExecuteGet), shared by every caller that can run a tool for
// real: internal/toolloop (ChatClientRunner), ClaudeRunner's in-process MCP
// handler, and cmd/draftmcp's static server (CodexRunner) — so the exact
// same query, against the exact same Store interface, produces the exact
// same result text regardless of which executor asked.
package knowledgetool

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/timmersuk/llm-workbench/internal/knowledge"
)

const (
	ListName = "list_knowledge_concepts"
	GetName  = "get_knowledge_concept"
)

// Definition is one query tool's name, human-readable description, and JSON
// Schema for its arguments — enough to build a chat.Tool
// (internal/toolloop), an SDK MCP tool (ClaudeRunner), or an MCP
// tools/list entry (cmd/draftmcp).
type Definition struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

var listSchema = json.RawMessage(`{"type":"object","properties":{}}`)

var getSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "concept_id": {"type": "string", "description": "The OKF concept id (its path under data/knowledge/ with the .md suffix removed), e.g. coding-standards/logging."}
  },
  "required": ["concept_id"]
}`)

// List is the list_knowledge_concepts tool definition: every concept's
// browsable summary, unfiltered across the whole bundle — not limited to
// the current Project's curated knowledge[] list — so a task can discover
// concepts nobody has curated into its own project yet
// (docs/milestones/done/milestone9.md's motivating agent-shell/llm-workbench
// example).
var List = Definition{
	Name:        ListName,
	Description: "List every knowledge concept in the workspace-wide data/knowledge/ bundle (id, type, title, description, tags) — unfiltered, not limited to this project's own curated knowledge list. Use this to discover what's available before calling get_knowledge_concept.",
	Schema:      listSchema,
}

// Get is the get_knowledge_concept tool definition: full content by id.
var Get = Definition{
	Name:        GetName,
	Description: "Fetch a knowledge concept's full content (type, frontmatter, and markdown body) by its concept id, as returned by list_knowledge_concepts.",
	Schema:      getSchema,
}

// All returns every known query tool definition, in a stable order.
func All() []Definition {
	return []Definition{List, Get}
}

// Store is the narrow read surface these tools need — satisfied by
// *knowledge.FileStore (and by the wider api.KnowledgeStore, a superset).
// Deliberately Get/List only: the write side (Put) is reached exclusively
// through the propose_knowledge Draft tool and its human accept/reject
// decision (internal/drafttool, internal/api/knowledge_draft.go), never
// through a tool an executor can call unattended.
type Store interface {
	Get(conceptID string) (knowledge.Concept, error)
	List() ([]knowledge.ConceptSummary, error)
}

// ExecuteList runs the list_knowledge_concepts query against store,
// rendering the result as plain text a model can read directly — one line
// per concept, sorted by id for stable output across calls.
func ExecuteList(store Store) (string, error) {
	summaries, err := store.List()
	if err != nil {
		return "", fmt.Errorf("listing knowledge concepts: %w", err)
	}
	if len(summaries) == 0 {
		return "no knowledge concepts exist yet", nil
	}

	sort.Slice(summaries, func(i, j int) bool { return summaries[i].ConceptID < summaries[j].ConceptID })

	var b strings.Builder
	for _, s := range summaries {
		fmt.Fprintf(&b, "- %s [%s]", s.ConceptID, s.Type)
		if s.Title != "" {
			fmt.Fprintf(&b, " — %s", s.Title)
		}
		if s.Description != "" {
			fmt.Fprintf(&b, ": %s", s.Description)
		}
		if len(s.Tags) > 0 {
			fmt.Fprintf(&b, " (tags: %s)", strings.Join(s.Tags, ", "))
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// ExecuteGet runs the get_knowledge_concept query against store, rendering
// the full concept (type, every frontmatter field, and the markdown body)
// as plain text.
func ExecuteGet(store Store, conceptID string) (string, error) {
	if conceptID == "" {
		return "", fmt.Errorf("concept_id is required")
	}
	c, err := store.Get(conceptID)
	if err != nil {
		return "", fmt.Errorf("fetching knowledge concept %q: %w", conceptID, err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "concept_id: %s\ntype: %s\n", conceptID, c.Type)
	keys := make([]string, 0, len(c.Frontmatter))
	for k := range c.Frontmatter {
		if k == "type" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s: %v\n", k, c.Frontmatter[k])
	}
	b.WriteString("\n")
	b.WriteString(c.Body)
	return b.String(), nil
}
