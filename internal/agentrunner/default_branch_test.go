package agentrunner

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGithubOwnerRepo_ExtractsOwnerRepo(t *testing.T) {
	got, err := githubOwnerRepo("github.com/timmersuk/llm-workbench")
	require.NoError(t, err)
	assert.Equal(t, "timmersuk/llm-workbench", got)
}

func TestGithubOwnerRepo_RejectsTooFewSegments(t *testing.T) {
	_, err := githubOwnerRepo("github.com/timmersuk")
	assert.Error(t, err)
}
