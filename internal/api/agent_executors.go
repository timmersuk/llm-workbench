package api

import (
	"net/http"
	"sort"
)

// handleListAgentExecutors reports which agentRunners entries
// (internal/agentrunner) actually respond to a live CheckHealth probe
// right now, mirroring handleListModels' shape. The frontend uses this to
// only offer an executor the server will actually accept, rather than
// letting the user pick one that 400s (stage_conversation.go's "unknown
// executor" check) or silently fails.
func (s *Server) handleListAgentExecutors() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		executors := make([]string, 0, len(s.AgentRunners))
		for name, runner := range s.AgentRunners {
			if err := runner.CheckHealth(r.Context()); err != nil {
				continue
			}
			executors = append(executors, name)
		}
		sort.Strings(executors)
		writeJSON(w, http.StatusOK, map[string][]string{"executors": executors})
	}
}
