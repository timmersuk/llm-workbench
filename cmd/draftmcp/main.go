// Command draftmcp is a minimal MCP stdio server exposing the Draft
// proposal tools (propose_context, propose_plan — internal/drafttool) as
// real MCP tools, so a codex thread (internal/agentrunner.CodexRunner) can
// call one the same way a Claude Code session calls its in-process SDK MCP
// tool. codex-agent-sdk-go has no in-process "SDK MCP server" helper (the
// severity1 SDK's WithSdkMcpServer has no codex equivalent), so an actual
// external MCP server process is the only way to offer a custom tool to a
// codex thread — see docs/milestones/done/milestone5.md's grill notes.
//
// This server never decides what a proposal *means* — it just accepts the
// call and acknowledges it. CodexRunner reads the actual proposal payload
// directly off the MCPToolCall event's Arguments field in the codex event
// stream, not from this server's response. That keeps this process
// stateless and side-effect-free: it exists purely so codex's tool-calling
// machinery has a real tool to call.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/sirupsen/logrus"

	"github.com/timmersuk/llm-workbench/internal/drafttool"
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
	if lvl, err := logrus.ParseLevel(os.Getenv("LOG_LEVEL")); err == nil {
		logrus.SetLevel(lvl)
	}
	run(os.Stdin, os.Stdout)
}

// run drives the MCP stdio protocol: one JSON-RPC message per line in,
// one per line out (per MCP's stdio transport — no Content-Length framing).
func run(stdin io.Reader, stdout io.Writer) {
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
		handleRequest(req, out)
	}
	if err := scanner.Err(); err != nil {
		logrus.WithError(err).Error("draftmcp: reading stdin")
	}
}

func handleRequest(req rpcRequest, out *bufio.Writer) {
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
		defs := drafttool.All()
		tools := make([]map[string]any, 0, len(defs))
		for _, d := range defs {
			var schema any
			if err := json.Unmarshal(d.Schema, &schema); err != nil {
				logrus.WithError(err).WithField("tool", d.Name).Error("draftmcp: decoding tool schema")
				continue
			}
			tools = append(tools, map[string]any{
				"name":        d.Name,
				"description": d.Description,
				"inputSchema": schema,
			})
		}
		respond(out, req.ID, map[string]any{"tools": tools})
	case "tools/call":
		var params toolCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			respondError(out, req.ID, fmt.Sprintf("decoding tools/call params: %v", err))
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
