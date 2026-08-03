package serverapp

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

// testConfig returns a config sufficient to exercise Run() without touching
// real environment variables (LoadConfig/utils.MustGetEnv is deliberately
// bypassed here — this test is about Run()'s shutdown behavior, not env
// parsing). HTTPAddr binds an OS-assigned loopback port since no test here
// needs to know which one. DataRepoURL points at a fresh empty bare
// repository under t.TempDir() — the same subprocess-boot provisioning
// pattern documented on Run() itself (an e2e harness driving the real
// binary must do the equivalent via `git init --bare` before launch; this
// in-process test does it directly via the `git` CLI, exactly like
// internal/gitstore's own tests).
func testConfig(t *testing.T) Config {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "data-remote.git")
	require.NoError(t, exec.Command("git", "init", "--bare", remote).Run())

	return Config{
		HTTPAddr:              "127.0.0.1:0",
		WorkspaceRoot:         filepath.Join(t.TempDir(), "workspace"),
		DataRepoURL:           remote,
		PushInterval:          time.Hour,
		LogLevel:              "error",
		LogFormat:             "text",
		LLMBaseURL:            "http://127.0.0.1:1",
		LLMModel:              "test-model",
		LLMTimeout:            time.Second,
		AgentTimeout:          time.Second,
		AgentExecutionTimeout: time.Second,
		ReposRoot:             t.TempDir(),
		ShutdownTimeout:       5 * time.Second,
		BuildID:               "test",
	}
}

// TestRun_ShutsDownGracefullyOnContextCancel exercises the exact code path
// main() drives on Ctrl+C (signal.NotifyContext cancelling ctx), and the
// exact code path cmd/tray's Quit action drives: Run() must notice
// ctx.Done(), call http.Server.Shutdown, invoke every agent runner's
// CloseAll (via the interface{ CloseAll() } type assertion) and return nil
// — all well within cfg.ShutdownTimeout, since nothing here is slow to
// close.
func TestRun_ShutsDownGracefullyOnContextCancel(t *testing.T) {
	cfg := testConfig(t)
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, cfg) }()

	// Give the HTTP listener a moment to actually bind before triggering
	// shutdown, so this exercises the same "serving, then cancelled"
	// sequence Ctrl+C produces in real use rather than racing startup.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(cfg.ShutdownTimeout + 2*time.Second):
		t.Fatal("Run() did not return within ShutdownTimeout after ctx cancellation")
	}
}

// TestRun_ReportsListenError ensures a real listen failure (address already
// in use) still surfaces as an error return from Run() rather than a
// process-exiting Fatal call from inside Run() itself — main() must stay
// the sole os.Exit/logrus.Fatal point.
func TestRun_ReportsListenError(t *testing.T) {
	cfg := testConfig(t)

	// Occupy a real port first so Run()'s own ListenAndServe fails.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	cfg.HTTPAddr = ln.Addr().String()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = Run(ctx, cfg)
	require.Error(t, err)
}
