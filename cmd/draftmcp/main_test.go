package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/drafttool"
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
	run(in, &out)

	msgs := decodeLines(t, &out)
	require.Len(t, msgs, 1)
	result, ok := msgs[0]["result"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "2024-11-05", result["protocolVersion"])
}

func TestRun_ToolsList(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n")
	var out bytes.Buffer
	run(in, &out)

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
	in := strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"propose_context","arguments":{"objective":"do the thing"}}}` + "\n")
	var out bytes.Buffer
	run(in, &out)

	msgs := decodeLines(t, &out)
	require.Len(t, msgs, 1)
	result := msgs[0]["result"].(map[string]any)
	require.Equal(t, false, result["isError"])
	content := result["content"].([]any)
	require.Len(t, content, 1)
}

func TestRun_NotificationsInitialized_NoResponse(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n")
	var out bytes.Buffer
	run(in, &out)

	require.Empty(t, out.String())
}

func TestRun_MalformedLine_Skipped(t *testing.T) {
	in := strings.NewReader("not json\n" + `{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}` + "\n")
	var out bytes.Buffer
	run(in, &out)

	msgs := decodeLines(t, &out)
	require.Len(t, msgs, 1)
}

func TestRun_UnknownMethodWithID_RespondsEmpty(t *testing.T) {
	in := strings.NewReader(`{"jsonrpc":"2.0","id":4,"method":"something/unknown","params":{}}` + "\n")
	var out bytes.Buffer
	run(in, &out)

	msgs := decodeLines(t, &out)
	require.Len(t, msgs, 1)
	require.Equal(t, float64(4), msgs[0]["id"])
}
