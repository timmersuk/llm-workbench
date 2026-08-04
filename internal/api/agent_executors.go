package api

import (
	"net/http"
	"sort"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
)

// handleListAgentExecutors reports every configured agentRunners entry's
// (internal/agentrunner) capabilities — name, models, efforts, and defaults
// — regardless of transient health; CheckHealth/the /healthcheck endpoint
// remains the one place operational status is surfaced. This is the single
// consolidated capability-discovery endpoint (GET /api/v1/chat/models was
// retired in its favor): the frontend uses it both to populate per-turn
// executor/model/effort pickers and to validate a persisted or submitted
// task default against the executor's actually-advertised choices.
func (s *Server) handleListAgentExecutors() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		executors := make([]agentrunner.ExecutorCapabilities, 0, len(s.AgentRunners))
		for name, runner := range s.AgentRunners {
			capability, err := runner.Capabilities(r.Context())
			if err != nil {
				continue
			}
			capability.Name = name
			executors = append(executors, capability)
		}
		sort.Slice(executors, func(i, j int) bool { return executors[i].Name < executors[j].Name })
		writeJSON(w, http.StatusOK, map[string][]agentrunner.ExecutorCapabilities{"executors": executors})
	}
}
