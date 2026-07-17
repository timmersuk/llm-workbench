package agentrunner

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestMergePRComments_SortsChronologicallyAcrossAllThreeSources(t *testing.T) {
	viewJSON := []byte(`{
		"comments": [
			{"author": {"login": "carol"}, "body": "general comment", "createdAt": "2026-07-01T10:00:00Z"}
		],
		"reviews": [
			{"author": {"login": "bob"}, "body": "please fix this", "state": "CHANGES_REQUESTED", "submittedAt": "2026-07-01T12:00:00Z"}
		]
	}`)
	inlineJSON := []byte(`[
		{"user": {"login": "bob"}, "body": "inline nit", "path": "foo.go", "line": 42, "diff_hunk": "@@ -1,2 +1,2 @@", "created_at": "2026-07-01T11:00:00Z"}
	]`)

	out, err := mergePRComments(viewJSON, inlineJSON)
	require.NoError(t, err)

	var entries []prCommentEntry
	require.NoError(t, yaml.Unmarshal([]byte(out), &entries))
	require.Len(t, entries, 3)

	assert.Equal(t, "comment", entries[0].Kind)
	assert.Equal(t, "carol", entries[0].Author)
	assert.Equal(t, "general comment", entries[0].Body)

	assert.Equal(t, "inline_comment", entries[1].Kind)
	assert.Equal(t, "bob", entries[1].Author)
	assert.Equal(t, "foo.go", entries[1].Path)
	assert.Equal(t, 42, entries[1].Line)
	assert.Equal(t, "@@ -1,2 +1,2 @@", entries[1].DiffHunk)

	assert.Equal(t, "review", entries[2].Kind)
	assert.Equal(t, "bob", entries[2].Author)
	assert.Equal(t, "CHANGES_REQUESTED", entries[2].State)
	assert.Equal(t, "please fix this", entries[2].Body)
}

func TestMergePRComments_EmptySources_ReturnsEmptyList(t *testing.T) {
	out, err := mergePRComments([]byte(`{"comments":[],"reviews":[]}`), []byte(`[]`))
	require.NoError(t, err)

	var entries []prCommentEntry
	require.NoError(t, yaml.Unmarshal([]byte(out), &entries))
	assert.Empty(t, entries)
}

func TestMergePRComments_InvalidViewJSON_ReturnsError(t *testing.T) {
	_, err := mergePRComments([]byte(`not json`), []byte(`[]`))
	assert.Error(t, err)
}

func TestMergePRComments_InvalidInlineJSON_ReturnsError(t *testing.T) {
	_, err := mergePRComments([]byte(`{"comments":[],"reviews":[]}`), []byte(`not json`))
	assert.Error(t, err)
}
