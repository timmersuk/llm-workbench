package agentrunner

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/timmersuk/llm-workbench/internal/gitutil"
)

// stubBehindOriginChecker/stubDirtyChecker/stubNow mirror stubClone
// (runner_test.go): temporarily replace the indirected package var,
// restoring the real implementation on cleanup.
func stubBehindOriginChecker(t *testing.T, fn func(ctx context.Context, dir string) gitutil.BehindOriginStatus) {
	t.Helper()
	original := behindOriginChecker
	behindOriginChecker = fn
	t.Cleanup(func() { behindOriginChecker = original })
}

func stubDirtyChecker(t *testing.T, fn func(ctx context.Context, dir string) gitutil.DirtyStatus) {
	t.Helper()
	original := dirtyChecker
	dirtyChecker = fn
	t.Cleanup(func() { dirtyChecker = original })
}

func stubNow(t *testing.T, fn func() time.Time) {
	t.Helper()
	original := nowFunc
	nowFunc = fn
	t.Cleanup(func() { nowFunc = original })
}

func TestGetWorkspaceStatus_NoRepository(t *testing.T) {
	_, err := GetWorkspaceStatus(context.Background(), t.TempDir(), nil)
	assert.ErrorIs(t, err, ErrNoRepository)
}

func TestGetWorkspaceStatus_NeverClonesMissingWorkspace(t *testing.T) {
	root := t.TempDir() // no "logthing" checkout under here at all

	stubClone(t, func(ctx context.Context, url, dest string) error {
		t.Fatal("GetWorkspaceStatus must never clone")
		return nil
	})

	status, err := GetWorkspaceStatus(context.Background(), root, []string{"github.com/timmersuk/logthing"})
	require.NoError(t, err)
	assert.False(t, status.BehindOrigin.Known)
	assert.False(t, status.Dirty.Known)
}

func TestGetWorkspaceStatus_ReflectsRealCheckout(t *testing.T) {
	root := t.TempDir()
	initTestRepo(t, root, "logthing")

	status, err := GetWorkspaceStatus(context.Background(), root, []string{"github.com/timmersuk/logthing"})
	require.NoError(t, err)
	assert.True(t, status.Dirty.Known)
	assert.False(t, status.Dirty.Dirty)
}

func TestBehindOriginCached_ReusesResultWithinTTL(t *testing.T) {
	root := t.TempDir()
	initTestRepo(t, root, "logthing")

	var calls int
	stubBehindOriginChecker(t, func(ctx context.Context, dir string) gitutil.BehindOriginStatus {
		calls++
		return gitutil.BehindOriginStatus{Known: true, Behind: calls}
	})
	stubDirtyChecker(t, func(ctx context.Context, dir string) gitutil.DirtyStatus {
		return gitutil.DirtyStatus{Known: true}
	})

	now := time.Now()
	stubNow(t, func() time.Time { return now })

	status1, err := GetWorkspaceStatus(context.Background(), root, []string{"github.com/timmersuk/logthing"})
	require.NoError(t, err)
	status2, err := GetWorkspaceStatus(context.Background(), root, []string{"github.com/timmersuk/logthing"})
	require.NoError(t, err)
	assert.Equal(t, 1, calls, "second call within the TTL must reuse the cached result")
	assert.Equal(t, status1.BehindOrigin, status2.BehindOrigin)

	now = now.Add(behindOriginCacheTTL + time.Second)
	status3, err := GetWorkspaceStatus(context.Background(), root, []string{"github.com/timmersuk/logthing"})
	require.NoError(t, err)
	assert.Equal(t, 2, calls, "a call past the TTL must re-check")
	assert.Equal(t, 2, status3.BehindOrigin.Behind)
}

func TestGetWorkspaceStatus_DirtyIsNeverCached(t *testing.T) {
	root := t.TempDir()
	initTestRepo(t, root, "logthing")

	var calls int
	stubDirtyChecker(t, func(ctx context.Context, dir string) gitutil.DirtyStatus {
		calls++
		return gitutil.DirtyStatus{Known: true}
	})
	stubBehindOriginChecker(t, func(ctx context.Context, dir string) gitutil.BehindOriginStatus {
		return gitutil.BehindOriginStatus{Known: true}
	})
	now := time.Now()
	stubNow(t, func() time.Time { return now })

	_, err := GetWorkspaceStatus(context.Background(), root, []string{"github.com/timmersuk/logthing"})
	require.NoError(t, err)
	_, err = GetWorkspaceStatus(context.Background(), root, []string{"github.com/timmersuk/logthing"})
	require.NoError(t, err)
	assert.Equal(t, 2, calls, "the dirty check must run fresh every call, unlike the TTL-cached behind-origin check")
}
