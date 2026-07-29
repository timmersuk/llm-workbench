package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteAPIError_EmitsTheDocumentedJSONEnvelope(t *testing.T) {
	w := httptest.NewRecorder()
	writeAPIError(w, http.StatusBadRequest, "message index 5 out of range")

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var body apiErrorBody
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "bad_request", body.Error.Code)
	assert.Equal(t, "message index 5 out of range", body.Error.Message)
}

func TestErrorCodeForStatus_MapsEveryStatusThisPackageActuallyUses(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{http.StatusBadRequest, "bad_request"},
		{http.StatusConflict, "conflict"},
		{http.StatusNotFound, "not_found"},
		{http.StatusBadGateway, "bad_gateway"},
		{http.StatusInternalServerError, "internal_error"},
		// Anything unmapped falls back to internal_error rather than an
		// empty/unknown code — errorCodeForStatus's default case.
		{http.StatusTeapot, "internal_error"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, errorCodeForStatus(c.status), "status %d", c.status)
	}
}
