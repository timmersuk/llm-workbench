package draftmcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/drafttool"
)

func TestHTTPHandler_ListsDraftTools(t *testing.T) {
	h := NewHTTPHandler(nil)
	req := httptest.NewRequest(http.MethodPost, "/internal/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "2024-11-05", rec.Header().Get("MCP-Protocol-Version"))
	var response struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	require.Len(t, response.Result.Tools, len(drafttool.All()))
	for _, tool := range response.Result.Tools {
		require.NotEmpty(t, tool.Name)
	}
}

func TestHTTPHandler_AcknowledgesNotifications(t *testing.T) {
	h := NewHTTPHandler(nil)
	req := httptest.NewRequest(http.MethodPost, "/internal/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusAccepted, rec.Code)
}

func TestHTTPHandler_RejectsNonPost(t *testing.T) {
	h := NewHTTPHandler(nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/internal/mcp", nil))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}
