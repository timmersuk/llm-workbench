package api

import (
	"net/http"
	"sort"

	"github.com/timmersuk/llm-workbench/internal/agentrunner"
)

// handleListAgentExecutors reports which agentRunners entries
// (internal/agentrunner) actually respond to a live CheckHealth probe
// right now, mirroring handleListModels' shape. The frontend uses this to
// only offer an executor the server will actually accept, rather than
// letting the user pick one that 400s (stage_conversation.go's "unknown
// executor" check) or silently fails.
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
