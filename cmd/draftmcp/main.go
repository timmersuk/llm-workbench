// Command draftmcp is a minimal MCP stdio server exposing the Draft
// proposal tools (propose_context, propose_plan, propose_review,
// propose_knowledge — internal/drafttool) and, when given a
// --knowledge-root, the read-only knowledge query tools
// (list_knowledge_concepts, get_knowledge_concept — internal/knowledgetool)
// as real MCP tools, so a codex thread (internal/agentrunner.CodexRunner)
// can call one the same way a Claude Code session calls its in-process SDK
// MCP tool. codex-agent-sdk-go has no in-process "SDK MCP server" helper
// (the severity1 SDK's WithSdkMcpServer has no codex equivalent), so an
// actual external MCP server process is the only way to offer a custom
// tool to a codex thread — see docs/milestones/done/milestone5.md's grill
// notes.
//
// The two tool families differ in kind, not just name. Draft tools never
// decide what a proposal *means* — this server just accepts the call and
// acknowledges it; CodexRunner reads the actual proposal payload directly
// off the MCPToolCall event's Arguments field in the codex event stream,
// not from this server's response, keeping that half of this process
// stateless and side-effect-free. The knowledge query tools are genuinely
// executed for real against a *knowledge.FileStore rooted at
// --knowledge-root (docs/milestones/milestone9.md) — there is no event-
// stream side channel for a query result the way there is for a proposal,
// so the real answer has to come back in this server's own response.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/sirupsen/logrus"

	"github.com/timmersuk/llm-workbench/internal/drafttool"
	"github.com/timmersuk/llm-workbench/internal/knowledge"
	"github.com/timmersuk/llm-workbench/internal/knowledgetool"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func main() {
	knowledgeRoot := flag.String("knowledge-root", "", "root directory of the OKF knowledge bundle (data/knowledge/); when set, also serves list_knowledge_concepts/get_knowledge_concept for real")
	flag.Parse()

	if lvl, err := logrus.ParseLevel(os.Getenv("LOG_LEVEL")); err == nil {
		logrus.SetLevel(lvl)
	}

	var store knowledgetool.Store
	if *knowledgeRoot != "" {
		store = knowledge.NewFileStore(*knowledgeRoot)
	}
	run(os.Stdin, os.Stdout, store)
}

// run drives the MCP stdio protocol: one JSON-RPC message per line in,
// one per line out (per MCP's stdio transport — no Content-Length framing).
// store is nil when --knowledge-root wasn't given, in which case the
// knowledge query tools are neither advertised nor callable.
func run(stdin io.Reader, stdout io.Writer, store knowledgetool.Store) {
	scanner := bufio.NewScanner(stdin)
	// MCP tool arguments (a Draft proposal's full context/plan) can be much
	// larger than bufio.Scanner's 64KiB default token limit.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	out := bufio.NewWriter(stdout)

	for scanner.Scan() {
		var req rpcRequest
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			logrus.WithError(err).Warn("draftmcp: malformed request, skipping")
			continue
		}
		handleRequest(req, out, store)
	}
	if err := scanner.Err(); err != nil {
		logrus.WithError(err).Error("draftmcp: reading stdin")
	}
}

func handleRequest(req rpcRequest, out *bufio.Writer, store knowledgetool.Store) {
	switch req.Method {
	case "initialize":
		respond(out, req.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "llm-workbench-draftmcp", "version": "0.1.0"},
		})
	case "notifications/initialized":
		// No response for notifications (no id).
	case "tools/list":
		var tools []map[string]any
		for _, d := range drafttool.All() {
			tools = appendToolListEntry(tools, d.Name, d.Description, d.Schema)
		}
		// Knowledge tools are only advertised when store is configured — a
		// process invoked without --knowledge-root can't actually serve
		// them, and advertising a tool it would fail is worse than not
		// listing it at all.
		if store != nil {
			for _, d := range knowledgetool.All() {
				tools = appendToolListEntry(tools, d.Name, d.Description, d.Schema)
			}
		}
		respond(out, req.ID, map[string]any{"tools": tools})
	case "tools/call":
		var params toolCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			respondError(out, req.ID, fmt.Sprintf("decoding tools/call params: %v", err))
			return
		}
		if store != nil && (params.Name == knowledgetool.ListName || params.Name == knowledgetool.GetName) {
			handleKnowledgeToolCall(out, req.ID, store, params)
			return
		}
		logrus.WithField("tool", params.Name).Info("draftmcp: proposal tool called")
		respond(out, req.ID, map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": "proposal received: " + params.Name},
			},
			"isError": false,
		})
	default:
		logrus.WithField("method", req.Method).Debug("draftmcp: unhandled method")
		if len(req.ID) > 0 {
			respond(out, req.ID, map[string]any{})
		}
	}
}

// appendToolListEntry decodes schema and appends {name, description,
// inputSchema} to tools — the shape a tools/list response entry needs,
// shared by drafttool.Definition and knowledgetool.Definition even though
// the two are distinct (but structurally identical) types with no common
// interface to range over generically.
func appendToolListEntry(tools []map[string]any, name, description string, schema json.RawMessage) []map[string]any {
	var decoded any
	if err := json.Unmarshal(schema, &decoded); err != nil {
		logrus.WithError(err).WithField("tool", name).Error("draftmcp: decoding tool schema")
		return tools
	}
	return append(tools, map[string]any{
		"name":        name,
		"description": description,
		"inputSchema": decoded,
	})
}

// handleKnowledgeToolCall actually executes a list_knowledge_concepts/
// get_knowledge_concept call against store and returns its real result —
// unlike the Draft tools' acknowledge-only path above, since there is no
// event-stream side channel a real answer could travel through instead.
func handleKnowledgeToolCall(out *bufio.Writer, id json.RawMessage, store knowledgetool.Store, params toolCallParams) {
	var args struct {
		ConceptID string `json:"concept_id"`
	}
	if len(params.Arguments) > 0 {
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			respondError(out, id, fmt.Sprintf("decoding %s arguments: %v", params.Name, err))
			return
		}
	}

	var text string
	var err error
	switch params.Name {
	case knowledgetool.ListName:
		text, err = knowledgetool.ExecuteList(store)
	case knowledgetool.GetName:
		text, err = knowledgetool.ExecuteGet(store, args.ConceptID)
	}

	if err != nil {
		respond(out, id, map[string]any{
			"content": []map[string]any{{"type": "text", "text": err.Error()}},
			"isError": true,
		})
		return
	}
	respond(out, id, map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": false,
	})
}

func respond(out *bufio.Writer, id json.RawMessage, result any) {
	writeMessage(out, map[string]any{"jsonrpc": "2.0", "id": rawOrNull(id), "result": result})
}

func respondError(out *bufio.Writer, id json.RawMessage, message string) {
	writeMessage(out, map[string]any{
		"jsonrpc": "2.0",
		"id":      rawOrNull(id),
		"error":   map[string]any{"code": -32000, "message": message},
	})
}

func rawOrNull(id json.RawMessage) any {
	if len(id) == 0 {
		return nil
	}
	return id
}

func writeMessage(out *bufio.Writer, msg map[string]any) {
	b, err := json.Marshal(msg)
	if err != nil {
		logrus.WithError(err).Error("draftmcp: marshaling response")
		return
	}
	out.Write(b)
	out.WriteByte('\n')
	out.Flush()
}
