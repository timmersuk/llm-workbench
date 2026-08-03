// Package draftmcp implements the Workbench-owned MCP tool surface used by
// Codex. It is transport-independent so CodexRunner's private HTTP listener
// can expose the same protocol behavior without coupling it to the main API.
package draftmcp

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/timmersuk/llm-workbench/internal/drafttool"
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

// Server serves Workbench Draft and Knowledge tools over MCP.
type Server struct {
	KnowledgeStore knowledgetool.Store
}

// NewHTTPHandler exposes a streamable-HTTP-compatible MCP endpoint. Codex
// sends one JSON-RPC request per POST; notifications receive 202 and requests
// receive their JSON-RPC response directly.
func NewHTTPHandler(store knowledgetool.Store) http.Handler {
	s := Server{KnowledgeStore: store}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		defer r.Body.Close()
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeHTTPResponse(w, http.StatusBadRequest, errorResponse(nil, fmt.Sprintf("decoding request: %v", err)))
			return
		}
		response, ok := s.handle(req)
		if !ok {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeHTTPResponse(w, http.StatusOK, response)
	})
}

// HandleJSONRPC handles one MCP request. It is exported for the stdio command
// and returns false for notifications, which deliberately have no response.
func (s Server) HandleJSONRPC(raw []byte) ([]byte, bool, error) {
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, false, err
	}
	response, ok := s.handle(req)
	if !ok {
		return nil, false, nil
	}
	b, err := json.Marshal(response)
	return b, true, err
}

func (s Server) handle(req rpcRequest) (map[string]any, bool) {
	switch req.Method {
	case "initialize":
		return resultResponse(req.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "llm-workbench-draftmcp", "version": "0.1.0"},
		}), true
	case "notifications/initialized":
		return nil, false
	case "tools/list":
		tools := make([]map[string]any, 0, len(drafttool.All())+len(knowledgetool.All()))
		for _, d := range drafttool.All() {
			tools = appendTool(tools, d.Name, d.Description, d.Schema)
		}
		if s.KnowledgeStore != nil {
			for _, d := range knowledgetool.All() {
				tools = appendTool(tools, d.Name, d.Description, d.Schema)
			}
		}
		return resultResponse(req.ID, map[string]any{"tools": tools}), true
	case "tools/call":
		var params toolCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return errorResponse(req.ID, fmt.Sprintf("decoding tools/call params: %v", err)), true
		}
		return resultResponse(req.ID, s.callTool(params)), true
	default:
		if len(req.ID) == 0 {
			return nil, false
		}
		return resultResponse(req.ID, map[string]any{}), true
	}
}

func (s Server) callTool(params toolCallParams) map[string]any {
	if s.KnowledgeStore != nil && (params.Name == knowledgetool.ListName || params.Name == knowledgetool.GetName) {
		return s.callKnowledgeTool(params)
	}
	if def, ok := draftDefinitionsByName[params.Name]; ok {
		var args map[string]any
		if len(params.Arguments) > 0 {
			if err := json.Unmarshal(params.Arguments, &args); err != nil {
				return toolError("decoding arguments: " + err.Error())
			}
		}
		if err := def.Validate(args); err != nil {
			return toolError(fmt.Sprintf("%s proposal rejected: %s. Retry %s with a corrected payload matching its schema.", params.Name, err, params.Name))
		}
	}
	return toolSuccess("proposal received: " + params.Name)
}

func (s Server) callKnowledgeTool(params toolCallParams) map[string]any {
	var args struct {
		ConceptID string `json:"concept_id"`
	}
	if len(params.Arguments) > 0 {
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			return toolError(fmt.Sprintf("decoding %s arguments: %v", params.Name, err))
		}
	}
	var text string
	var err error
	switch params.Name {
	case knowledgetool.ListName:
		text, err = knowledgetool.ExecuteList(s.KnowledgeStore)
	case knowledgetool.GetName:
		text, err = knowledgetool.ExecuteGet(s.KnowledgeStore, args.ConceptID)
	}
	if err != nil {
		return toolError(err.Error())
	}
	return toolSuccess(text)
}

var draftDefinitionsByName = func() map[string]drafttool.Definition {
	m := make(map[string]drafttool.Definition, len(drafttool.All()))
	for _, d := range drafttool.All() {
		m[d.Name] = d
	}
	return m
}()

func appendTool(tools []map[string]any, name, description string, schema json.RawMessage) []map[string]any {
	var decoded any
	if err := json.Unmarshal(schema, &decoded); err != nil {
		return tools
	}
	return append(tools, map[string]any{"name": name, "description": description, "inputSchema": decoded})
}

func resultResponse(id json.RawMessage, result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": rawOrNull(id), "result": result}
}
func errorResponse(id json.RawMessage, message string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": rawOrNull(id), "error": map[string]any{"code": -32000, "message": message}}
}
func rawOrNull(id json.RawMessage) any {
	if len(id) == 0 {
		return nil
	}
	return id
}
func toolSuccess(text string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": false}
}
func toolError(text string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": true}
}

func writeHTTPResponse(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("MCP-Protocol-Version", "2024-11-05")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
