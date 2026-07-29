package main

import "fmt"

// maxTooltipLen matches Windows' NOTIFYICONDATA::szTip limit (128 WCHARs
// including the terminator, per the Win32 shell API this package's Windows
// backend ultimately calls through) — formatTooltip truncates to it so a
// long subsystem error can never silently fail to display at all.
const maxTooltipLen = 127

// formatTooltip builds the tray icon's hover tooltip text for the given
// state. detail is extra context appended after the state label — the
// Degraded case's healthcheckPayload.Error, the Unreachable case's
// transport error or (if the in-process server has already exited
// unexpectedly) crash error text. An empty detail omits the separator
// entirely, so the Healthy case (which never has one) reads as a plain
// "llm-workbench: Healthy".
func formatTooltip(state State, detail string) string {
	text := "llm-workbench: " + state.String()
	if detail != "" {
		text = fmt.Sprintf("%s (%s)", text, detail)
	}
	return truncate(text, maxTooltipLen)
}

// truncate shortens s to at most maxLen runes, replacing the tail with an
// ellipsis so it's visually obvious the text was cut rather than complete.
// Operates on runes, not bytes, so it never splits a multi-byte UTF-8
// sequence (subsystem error text can come from arbitrary upstream error
// messages).
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 1 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-1]) + "…"
}
