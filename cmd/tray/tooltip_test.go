package main

import (
	"strings"
	"testing"
)

func TestFormatTooltip(t *testing.T) {
	cases := []struct {
		name   string
		state  State
		detail string
		want   string
	}{
		{"healthy has no detail", StateHealthy, "", "llm-workbench: Healthy"},
		{"degraded with error detail", StateDegraded, "agent:local: connection refused", "llm-workbench: Degraded (agent:local: connection refused)"},
		{"unreachable with transport detail", StateUnreachable, "connect: connection refused", "llm-workbench: Unreachable (connect: connection refused)"},
		{"unreachable with no detail yet", StateUnreachable, "", "llm-workbench: Unreachable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatTooltip(tc.state, tc.detail); got != tc.want {
				t.Errorf("formatTooltip(%v, %q) = %q, want %q", tc.state, tc.detail, got, tc.want)
			}
		})
	}
}

func TestFormatTooltip_TruncatesLongDetail(t *testing.T) {
	longDetail := strings.Repeat("x", 300)
	got := formatTooltip(StateDegraded, longDetail)

	runeLen := len([]rune(got))
	if runeLen > maxTooltipLen {
		t.Fatalf("formatTooltip result is %d runes, want <= %d", runeLen, maxTooltipLen)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("formatTooltip(long detail) = %q, want a truncated string ending in an ellipsis", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("truncate(%q, 10) = %q, want unchanged", "short", got)
	}

	got := truncate("abcdefghij", 5)
	if want := "abcd…"; got != want {
		t.Errorf("truncate(%q, 5) = %q, want %q", "abcdefghij", got, want)
	}
	if runeLen := len([]rune(got)); runeLen != 5 {
		t.Errorf("truncate result has %d runes, want 5", runeLen)
	}
}

func TestTruncate_MultiByteRunesNotSplit(t *testing.T) {
	// Each of these runes is multi-byte in UTF-8 (e.g. "é" = 2 bytes) — a
	// byte-based truncation would corrupt one, producing invalid UTF-8.
	s := strings.Repeat("é", 10)
	got := truncate(s, 5)
	for _, r := range got {
		if r == '�' {
			t.Fatalf("truncate(%q, 5) = %q contains the UTF-8 replacement character, a multi-byte rune was split", s, got)
		}
	}
}
