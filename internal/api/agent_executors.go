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
func handleListAgentExecutors(agentRunners map[string]agentrunner.AgentRunner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		executors := make([]string, 0, len(agentRunners))
		for name, runner := range agentRunners {
			if err := runner.CheckHealth(r.Context()); err != nil {
				continue
			}
			executors = append(executors, name)
		}
		sort.Strings(executors)
		writeJSON(w, http.StatusOK, map[string][]string{"executors": executors})
	}
}
