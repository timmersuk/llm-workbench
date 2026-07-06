package task

import "strings"

// trimmedList trims whitespace from each item of items. gopkg.in/yaml.v3
// (still v3.0.1 as of writing, no fix available) fails to round-trip any
// string — scalar or list item — whose first character is a newline: it
// marshals without error but errors on Unmarshal ("did not find expected
// key"/"did not find expected '-' indicator"). LLM-authored Draft content
// (CONTEXT.md) commonly starts with a blank line, so every free-text field
// coming from a Draft is trimmed before it's written, rather than writing a
// file this same package can't read back.
func trimmedList(items []string) []string {
	trimmed := make([]string, len(items))
	for i, s := range items {
		trimmed[i] = strings.TrimSpace(s)
	}
	return trimmed
}
