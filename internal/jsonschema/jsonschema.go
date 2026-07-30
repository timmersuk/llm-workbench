// Package jsonschema implements the one narrow slice of JSON Schema this
// codebase actually needs — checking that a tool call's decoded arguments
// satisfy a schema's top-level "required" list, with the right JSON kind —
// not a general validator. A model can emit syntactically valid JSON that
// still doesn't match the schema it was given (a required key silently
// missing, or holding the wrong kind of value); this catches that class of
// error so a caller can reject the call and let the model retry, instead of
// persisting malformed data. Deliberately no external dependency: the repo
// convention is no new runtime dependency without a stated reason, and full
// JSON Schema semantics (nested validation, $ref, oneOf, ...) aren't needed.
package jsonschema

import (
	"fmt"
	"sort"
	"strings"
)

// RequiredFieldsPresent checks that every name in schema's top-level
// "required" array is present in args and, when schema["properties"][name]
// declares a "type", that args[name]'s decoded JSON kind matches it. schema
// and args are both already-decoded JSON objects (e.g. from
// json.Unmarshal(..., &map[string]any{})). Returns a single error listing
// every field that's missing or the wrong kind, or nil if schema declares
// no "required" list at all.
func RequiredFieldsPresent(schema, args map[string]any) error {
	required, _ := schema["required"].([]any)
	if len(required) == 0 {
		return nil
	}
	properties, _ := schema["properties"].(map[string]any)

	var problems []string
	for _, r := range required {
		name, ok := r.(string)
		if !ok {
			continue
		}
		value, present := args[name]
		if !present {
			problems = append(problems, fmt.Sprintf("missing required field %q", name))
			continue
		}
		if wantType := declaredType(properties, name); wantType != "" {
			if gotType := kindOf(value); gotType != "" && gotType != wantType {
				problems = append(problems, fmt.Sprintf("field %q should be %s, got %s", name, wantType, gotType))
			}
		}
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("%s", strings.Join(problems, "; "))
}

// declaredType returns properties[name]'s "type" string, or "" if
// unspecified.
func declaredType(properties map[string]any, name string) string {
	prop, ok := properties[name].(map[string]any)
	if !ok {
		return ""
	}
	t, _ := prop["type"].(string)
	return t
}

// kindOf maps a decoded JSON value to its JSON Schema type name ("string",
// "array", "object", "boolean", "number"), or "" for a kind
// RequiredFieldsPresent doesn't check (e.g. null).
func kindOf(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	case bool:
		return "boolean"
	case float64:
		return "number"
	default:
		return ""
	}
}
