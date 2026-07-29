package main

// State is the tray icon's 3-state health indicator, driven by polling
// GET /healthcheck every pollInterval (see main.go's poll loop).
type State int

const (
	// StateHealthy means the last poll's /healthcheck responded 200 with
	// {"status": "ok"} — every subsystem is up.
	StateHealthy State = iota
	// StateDegraded means the last poll's /healthcheck responded (any
	// status code) with a decodable body reporting {"status": "error"} —
	// the server is up and answering, but at least one subsystem (e.g. the
	// configured LLM backend) is down. Detail comes from the response's
	// Error field.
	StateDegraded
	// StateUnreachable means the last poll didn't produce a usable
	// /healthcheck response at all: a transport-level failure (connection
	// refused, timeout, non-OK/non-ServiceUnavailable status, malformed
	// body), or the in-process server hasn't started yet, or it has
	// already exited (gracefully via Quit, or crashed). Any single missed
	// poll reverts immediately to this state — there is no debounce.
	StateUnreachable
)

// String is used for logging and as the fallback tooltip/menu label.
func (s State) String() string {
	switch s {
	case StateHealthy:
		return "Healthy"
	case StateDegraded:
		return "Degraded"
	default:
		return "Unreachable"
	}
}

// healthcheckPayload mirrors the wire shape of internal/api's
// healthcheckResponse (docs/engineering conventions.md's Healthchecks
// section) — just the fields the tray cares about. Kept as the tray's own
// local type rather than importing internal/api, since that package's type
// is unexported and the tray only ever needs to read this JSON, never
// build the server's response.
type healthcheckPayload struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// stateFromHealthcheck maps a successfully decoded /healthcheck response
// to StateHealthy or StateDegraded. It is only ever called once the HTTP
// request and JSON decode have both already succeeded — the poll loop in
// main.go treats any failure to get this far (network error, malformed
// body, an HTTP status that isn't 200 or 503) as StateUnreachable without
// consulting this function at all.
func stateFromHealthcheck(resp healthcheckPayload) State {
	if resp.Status == "ok" {
		return StateHealthy
	}
	return StateDegraded
}
