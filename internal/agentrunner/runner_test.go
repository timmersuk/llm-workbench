package agentrunner

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveWorkspace_Success(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "logthing"), 0o755))

	ws, err := ResolveWorkspace(root, []string{"github.com/timmersuk/logthing"})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "logthing"), ws)
}

func TestResolveWorkspace_NoRepository(t *testing.T) {
	_, err := ResolveWorkspace(t.TempDir(), nil)
	assert.True(t, errors.Is(err, ErrNoRepository))
}

func TestResolveWorkspace_MissingCheckout(t *testing.T) {
	_, err := ResolveWorkspace(t.TempDir(), []string{"github.com/timmersuk/does-not-exist"})
	assert.True(t, errors.Is(err, ErrInvalidRepository))
}

func TestResolveWorkspace_NotADirectory(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "logthing"), []byte("x"), 0o644))

	_, err := ResolveWorkspace(root, []string{"github.com/timmersuk/logthing"})
	assert.True(t, errors.Is(err, ErrInvalidRepository))
}

func TestResolveWorkspace_RejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	// A sibling directory outside root that a naive join could escape to.
	require.NoError(t, os.MkdirAll(filepath.Join(root, "..", "escaped"), 0o755))
	defer os.RemoveAll(filepath.Join(root, "..", "escaped"))

	_, err := ResolveWorkspace(root, []string{"github.com/timmersuk/../escaped"})
	assert.True(t, errors.Is(err, ErrInvalidRepository))
}

func TestResolveWorkspace_UsesFirstRepository(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "first"), 0o755))

	ws, err := ResolveWorkspace(root, []string{"github.com/timmersuk/first", "github.com/timmersuk/second"})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "first"), ws)
}
