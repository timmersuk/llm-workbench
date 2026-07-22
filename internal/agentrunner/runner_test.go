package agentrunner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubClone temporarily replaces cloneRepository with fn for the test's
// duration, restoring the real gitutil.Clone-backed implementation on
// cleanup — the same seam claude_runner.go's lookPath/newClient provide,
// so these tests never spawn a real git subprocess or hit the network.
func stubClone(t *testing.T, fn func(ctx context.Context, url, dest string) error) {
	t.Helper()
	original := cloneRepository
	cloneRepository = fn
	t.Cleanup(func() { cloneRepository = original })
}

func TestResolveWorkspace_Success(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "logthing"), 0o755))

	var cloneCalled bool
	stubClone(t, func(ctx context.Context, url, dest string) error {
		cloneCalled = true
		return nil
	})

	ws, err := ResolveWorkspace(context.Background(), root, []string{"github.com/timmersuk/logthing"})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "logthing"), ws)
	assert.False(t, cloneCalled, "clone must not be attempted when the workspace already exists")
}

func TestResolveWorkspace_NoRepository(t *testing.T) {
	_, err := ResolveWorkspace(context.Background(), t.TempDir(), nil)
	assert.True(t, errors.Is(err, ErrNoRepository))
}

func TestResolveWorkspace_ClonesWhenMissing(t *testing.T) {
	root := t.TempDir()

	var gotURL, gotDest string
	stubClone(t, func(ctx context.Context, url, dest string) error {
		gotURL, gotDest = url, dest
		return os.MkdirAll(dest, 0o755)
	})

	ws, err := ResolveWorkspace(context.Background(), root, []string{"github.com/timmersuk/logthing"})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "logthing"), ws)
	assert.Equal(t, "https://github.com/timmersuk/logthing", gotURL)
	assert.Equal(t, filepath.Join(root, "logthing"), gotDest)
}

func TestResolveWorkspace_CloneFailureIsError(t *testing.T) {
	root := t.TempDir()

	stubClone(t, func(ctx context.Context, url, dest string) error {
		return errors.New("network unreachable")
	})

	_, err := ResolveWorkspace(context.Background(), root, []string{"github.com/timmersuk/does-not-exist"})
	assert.True(t, errors.Is(err, ErrCloneFailed))
}

func TestResolveWorkspace_ConcurrentCallsCloneOnlyOnce(t *testing.T) {
	root := t.TempDir()

	var cloneCount atomic.Int32
	stubClone(t, func(ctx context.Context, url, dest string) error {
		cloneCount.Add(1)
		return os.MkdirAll(dest, 0o755)
	})

	const concurrency = 8
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			_, err := ResolveWorkspace(context.Background(), root, []string{"github.com/timmersuk/logthing"})
			assert.NoError(t, err)
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(1), cloneCount.Load(), "concurrent resolves of the same missing workspace must clone exactly once")
}

func TestResolveWorkspace_NotADirectory(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "logthing"), []byte("x"), 0o644))

	_, err := ResolveWorkspace(context.Background(), root, []string{"github.com/timmersuk/logthing"})
	assert.True(t, errors.Is(err, ErrInvalidRepository))
}

func TestResolveWorkspace_RejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	// A sibling directory outside root that a naive join could escape to.
	require.NoError(t, os.MkdirAll(filepath.Join(root, "..", "escaped"), 0o755))
	defer os.RemoveAll(filepath.Join(root, "..", "escaped"))

	_, err := ResolveWorkspace(context.Background(), root, []string{"github.com/timmersuk/../escaped"})
	assert.True(t, errors.Is(err, ErrInvalidRepository))
}

func TestResolveWorkspace_UsesFirstRepository(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "first"), 0o755))

	ws, err := ResolveWorkspace(context.Background(), root, []string{"github.com/timmersuk/first", "github.com/timmersuk/second"})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "first"), ws)
}
