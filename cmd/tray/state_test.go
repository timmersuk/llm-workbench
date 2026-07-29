package main

import "testing"

func TestStateFromHealthcheck(t *testing.T) {
	cases := []struct {
		name string
		resp healthcheckPayload
		want State
	}{
		{"ok status", healthcheckPayload{Status: "ok"}, StateHealthy},
		{"error status", healthcheckPayload{Status: "error", Error: "agent:local: connection refused"}, StateDegraded},
		{"unrecognized status treated as degraded, not healthy", healthcheckPayload{Status: "weird"}, StateDegraded},
		{"empty status treated as degraded, not healthy", healthcheckPayload{}, StateDegraded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stateFromHealthcheck(tc.resp); got != tc.want {
				t.Errorf("stateFromHealthcheck(%+v) = %v, want %v", tc.resp, got, tc.want)
			}
		})
	}
}

func TestState_String(t *testing.T) {
	cases := []struct {
		state State
		want  string
	}{
		{StateHealthy, "Healthy"},
		{StateDegraded, "Degraded"},
		{StateUnreachable, "Unreachable"},
		{State(99), "Unreachable"}, // unknown values fail safe to Unreachable's label
	}
	for _, tc := range cases {
		if got := tc.state.String(); got != tc.want {
			t.Errorf("State(%d).String() = %q, want %q", tc.state, got, tc.want)
		}
	}
}
