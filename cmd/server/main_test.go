package main

import (
	"context"
	"net"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testConfig returns a config sufficient to exercise run() without touching
// real environment variables (loadConfig/utils.MustGetEnv is deliberately
// bypassed here — this test is about run()'s shutdown behavior, not env
// parsing). httpAddr binds an OS-assigned loopback port since no test here
// needs to know which one. dataRepoURL points at a fresh empty bare
// repository under t.TempDir() — the same subprocess-boot provisioning
// pattern documented on run() itself (an e2e harness driving the real
// binary must do the equivalent via `git init --bare` before launch; this
// in-process test does it directly via the `git` CLI, exactly like
// internal/gitstore's own tests).
func testConfig(t *testing.T) config {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "data-remote.git")
	require.NoError(t, exec.Command("git", "init", "--bare", remote).Run())

	return config{
		httpAddr:              "127.0.0.1:0",
		workspaceRoot:         filepath.Join(t.TempDir(), "workspace"),
		dataRepoURL:           remote,
		pushInterval:          time.Hour,
		logLevel:              "error",
		logFormat:             "text",
		llmBaseURL:            "http://127.0.0.1:1",
		llmModel:              "test-model",
		llmTimeout:            time.Second,
		agentTimeout:          time.Second,
		agentExecutionTimeout: time.Second,
		reposRoot:             t.TempDir(),
		draftMCPPath:          "draftmcp",
		shutdownTimeout:       5 * time.Second,
	}
}

// TestRun_ShutsDownGracefullyOnContextCancel exercises the exact code path
// main() drives on Ctrl+C (signal.NotifyContext cancelling ctx): run() must
// notice ctx.Done(), call http.Server.Shutdown, invoke every agent runner's
// CloseAll (via the interface{ CloseAll() } type assertion) and return nil
// — all well within cfg.shutdownTimeout, since nothing here is slow to
// close.
func TestRun_ShutsDownGracefullyOnContextCancel(t *testing.T) {
	cfg := testConfig(t)
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() { errCh <- run(ctx, cfg) }()

	// Give the HTTP listener a moment to actually bind before triggering
	// shutdown, so this exercises the same "serving, then cancelled"
	// sequence Ctrl+C produces in real use rather than racing startup.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(cfg.shutdownTimeout + 2*time.Second):
		t.Fatal("run() did not return within shutdownTimeout after ctx cancellation")
	}
}

// TestRun_ReportsListenError ensures a real listen failure (address already
// in use) still surfaces as an error return from run() rather than a
// process-exiting Fatal call from inside run() itself — main() must stay
// the sole os.Exit/logrus.Fatal point.
func TestRun_ReportsListenError(t *testing.T) {
	cfg := testConfig(t)

	// Occupy a real port first so run()'s own ListenAndServe fails.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	cfg.httpAddr = ln.Addr().String()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = run(ctx, cfg)
	require.Error(t, err)
}
