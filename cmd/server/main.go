// Command server runs the llm-workbench HTTP server: an API over the
// task/project repositories, a chat completions proxy to a local
// OpenAI-compatible LLM server, and the embedded frontend dashboard.
//
// This binary is a thin wrapper around internal/serverapp, which owns every
// bit of config loading and server wiring (see serverapp.LoadConfig,
// serverapp.Run) so cmd/tray can host the exact same server in-process
// alongside a systray icon with zero duplicated logic.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/sirupsen/logrus"

	"github.com/timmersuk/llm-workbench/internal/serverapp"
)

// BuildID is set via -ldflags at build time (see Makefile).
var BuildID = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cfg := serverapp.LoadConfig()
	cfg.BuildID = BuildID

	if err := serverapp.Run(ctx, cfg); err != nil {
		logrus.WithError(err).Fatal("server exited")
	}
}
