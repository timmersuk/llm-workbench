package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/drafttool"
	"github.com/timmersuk/llm-workbench/internal/knowledge"
	"github.com/timmersuk/llm-workbench/internal/knowledgetool"
)

// decodeLines splits out's newline-delimited JSON-RPC responses.
func decodeLines(t *testing.T, out *bytes.Buffer) []map[string]any {
	t.Helper()
	var msgs []map[string]any
	scanner := bufio.NewScanner(out)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal(line, &m))
		msgs = append(msgs, m)
	}
	require.NoError(t, scanner.Err())
	return msgs
}

func TestRun_Initialize(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{}}` + "\n")
	var out bytes.Buffer
	run(in, &out, nil)

	msgs := decodeLines(t, &out)
	require.Len(t, msgs, 1)
	result, ok := msgs[0]["result"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "2024-11-05", result["protocolVersion"])
}

func TestRun_ToolsList(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n")
	var out bytes.Buffer
	run(in, &out, nil)

	msgs := decodeLines(t, &out)
	require.Len(t, msgs, 1)
	result := msgs[0]["result"].(map[string]any)
	tools := result["tools"].([]any)
	require.Len(t, tools, len(drafttool.All()))

	names := make(map[string]bool)
	for _, raw := range tools {
		tool := raw.(map[string]any)
		names[tool["name"].(string)] = true
		require.NotEmpty(t, tool["description"])
		require.NotNil(t, tool["inputSchema"])
	}
	require.True(t, names[drafttool.ProposeContextName])
	require.True(t, names[drafttool.ProposePlanName])
	require.True(t, names[drafttool.ProposeReviewName])
}

func TestRun_ToolsCall_Acknowledges(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"propose_context","arguments":{"objective":"do the thing","context":{"summary":"s"}}}}` + "\n")
	var out bytes.Buffer
	run(in, &out, nil)

	msgs := decodeLines(t, &out)
	require.Len(t, msgs, 1)
	result := msgs[0]["result"].(map[string]any)
	require.Equal(t, false, result["isError"])
	content := result["content"].([]any)
	require.Len(t, content, 1)
}

// TestRun_ToolsCall_RejectsSchemaInvalidArgs covers the corruption class
// that motivated validating proposals at all: a required field (here
// propose_plan's "steps") silently missing from otherwise-valid JSON.
func TestRun_ToolsCall_RejectsSchemaInvalidArgs(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"propose_plan","arguments":{"approach":"do it","estimated_complexity":"low"}}}` + "\n")
	var out bytes.Buffer
	run(in, &out, nil)

	msgs := decodeLines(t, &out)
	require.Len(t, msgs, 1)
	result := msgs[0]["result"].(map[string]any)
	require.Equal(t, true, result["isError"])
}

func TestRun_ToolsList_NoKnowledgeRoot_OmitsKnowledgeTools(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n")
	var out bytes.Buffer
	run(in, &out, nil)

	msgs := decodeLines(t, &out)
	result := msgs[0]["result"].(map[string]any)
	tools := result["tools"].([]any)
	require.Len(t, tools, len(drafttool.All()))

	for _, raw := range tools {
		tool := raw.(map[string]any)
		require.NotEqual(t, knowledgetool.ListName, tool["name"])
		require.NotEqual(t, knowledgetool.GetName, tool["name"])
	}
}

func TestRun_ToolsList_WithKnowledgeRoot_IncludesKnowledgeTools(t *testing.T) {
	store := knowledge.NewFileStore(t.TempDir())
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n")
	var out bytes.Buffer
	run(in, &out, store)

	msgs := decodeLines(t, &out)
	result := msgs[0]["result"].(map[string]any)
	tools := result["tools"].([]any)
	require.Len(t, tools, len(drafttool.All())+len(knowledgetool.All()))

	names := make(map[string]bool)
	for _, raw := range tools {
		tool := raw.(map[string]any)
		names[tool["name"].(string)] = true
	}
	require.True(t, names[knowledgetool.ListName])
	require.True(t, names[knowledgetool.GetName])
}

func TestRun_ToolsCall_KnowledgeList_ReturnsRealContent(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "coding-standards"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "coding-standards", "logging.md"),
		[]byte("---\ntype: Coding Standard\ntitle: Logging\n---\nUse structured logging.\n"), 0o644))
	store := knowledge.NewFileStore(root)

	in := strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_knowledge_concepts","arguments":{}}}` + "\n")
	var out bytes.Buffer
	run(in, &out, store)

	msgs := decodeLines(t, &out)
	require.Len(t, msgs, 1)
	result := msgs[0]["result"].(map[string]any)
	require.Equal(t, false, result["isError"])
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	require.Contains(t, text, "coding-standards/logging")
	require.Contains(t, text, "Logging")
}

func TestRun_ToolsCall_KnowledgeGet_ReturnsRealContent(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "note.md"), []byte("---\ntype: Reference\n---\nhello world\n"), 0o644))
	store := knowledge.NewFileStore(root)

	in := strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_knowledge_concept","arguments":{"concept_id":"note"}}}` + "\n")
	var out bytes.Buffer
	run(in, &out, store)

	msgs := decodeLines(t, &out)
	result := msgs[0]["result"].(map[string]any)
	require.Equal(t, false, result["isError"])
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	require.Contains(t, text, "hello world")
}

func TestRun_ToolsCall_KnowledgeGet_MissingConceptIsError(t *testing.T) {
	store := knowledge.NewFileStore(t.TempDir())

	in := strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_knowledge_concept","arguments":{"concept_id":"does-not-exist"}}}` + "\n")
	var out bytes.Buffer
	run(in, &out, store)

	msgs := decodeLines(t, &out)
	result := msgs[0]["result"].(map[string]any)
	require.Equal(t, true, result["isError"])
}

func TestRun_ToolsCall_KnowledgeTool_WithoutKnowledgeRoot_FallsThroughToAck(t *testing.T) {
	// No store configured (nil) — a knowledge-tool-shaped call name is
	// treated the same as any other unrecognized tools/call: acknowledged,
	// not executed, matching the pre-Milestone-9 behavior for any tool this
	// process wasn't told about.
	in := strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_knowledge_concepts","arguments":{}}}` + "\n")
	var out bytes.Buffer
	run(in, &out, nil)

	msgs := decodeLines(t, &out)
	result := msgs[0]["result"].(map[string]any)
	require.Equal(t, false, result["isError"])
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	require.Equal(t, "proposal received: list_knowledge_concepts", text)
}

func TestRun_NotificationsInitialized_NoResponse(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n")
	var out bytes.Buffer
	run(in, &out, nil)

	require.Empty(t, out.String())
}

func TestRun_MalformedLine_Skipped(t *testing.T) {
	in := strings.NewReader("not json\n" + `{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}` + "\n")
	var out bytes.Buffer
	run(in, &out, nil)

	msgs := decodeLines(t, &out)
	require.Len(t, msgs, 1)
}

func TestRun_UnknownMethodWithID_RespondsEmpty(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":4,"method":"something/unknown","params":{}}` + "\n")
	var out bytes.Buffer
	run(in, &out, nil)

	msgs := decodeLines(t, &out)
	require.Len(t, msgs, 1)
	require.Equal(t, float64(4), msgs[0]["id"])
}
