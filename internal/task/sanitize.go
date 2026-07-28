package task

import "strings"

// trimmedList trims whitespace from each item of items. Originally
// required to work around a gopkg.in/yaml.v3 bug (fixed by the migration
// to github.com/goccy/go-yaml, internal/yamlutil): any string — scalar or
// list item — whose first character was a newline marshaled without
// error but errored on Unmarshal ("did not find expected key"/"did not
// find expected '-' indicator"), and LLM-authored Draft content
// (CONTEXT.md) commonly starts with a blank line. Kept now as deliberate
// data hygiene for LLM-authored free text, not correctness.
func trimmedList(items []string) []string {
	trimmed := make([]string, len(items))
	for i, s := range items {
		trimmed[i] = strings.TrimSpace(s)
	}
	return trimmed
}

// trimmedVerification trims each VerificationStep's Description (same
// hygiene trimmedList applies, since Description is LLM-authored Draft
// free text) and drops any entry whose Description is
// empty after trimming — an empty verification step carries no meaning and
// would only clutter Review's per-step walk. Kind is trimmed but otherwise
// preserved as-is; validation of its value belongs to the Draft schema, not
// here.
func trimmedVerification(items []VerificationStep) []VerificationStep {
	trimmed := make([]VerificationStep, 0, len(items))
	for _, s := range items {
		desc := strings.TrimSpace(s.Description)
		if desc == "" {
			continue
		}
		trimmed = append(trimmed, VerificationStep{Description: desc, Kind: strings.TrimSpace(s.Kind)})
	}
	return trimmed
}

// trimmedContextFiles trims each ContextFile's Path/Role (same hygiene
// trimmedList applies, since both are LLM-authored Draft free text) and
// drops any entry whose Path is empty
// after trimming — a file entry with no path carries no meaning.
func trimmedContextFiles(items []ContextFile) []ContextFile {
	trimmed := make([]ContextFile, 0, len(items))
	for _, f := range items {
		path := strings.TrimSpace(f.Path)
		if path == "" {
			continue
		}
		trimmed = append(trimmed, ContextFile{Path: path, Role: strings.TrimSpace(f.Role)})
	}
	return trimmed
}
