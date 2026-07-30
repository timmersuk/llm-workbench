package jsonschema

import "testing"

var planSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"approach": map[string]any{"type": "string"},
		"steps":    map[string]any{"type": "array"},
	},
	"required": []any{"approach", "steps"},
}

func TestRequiredFieldsPresent_Valid(t *testing.T) {
	args := map[string]any{"approach": "do the thing", "steps": []any{"step 1"}}
	if err := RequiredFieldsPresent(planSchema, args); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestRequiredFieldsPresent_MissingField(t *testing.T) {
	// The exact corruption seen in production: steps never landed as its
	// own key, only approach is present.
	args := map[string]any{"approach": "do the thing</approach><parameter name=\"steps\">[\"a\"]"}
	err := RequiredFieldsPresent(planSchema, args)
	if err == nil {
		t.Fatal("expected an error for missing \"steps\"")
	}
	if got := err.Error(); got != `missing required field "steps"` {
		t.Fatalf("unexpected message: %s", got)
	}
}

func TestRequiredFieldsPresent_WrongType(t *testing.T) {
	args := map[string]any{"approach": "do the thing", "steps": "not an array"}
	err := RequiredFieldsPresent(planSchema, args)
	if err == nil {
		t.Fatal("expected an error for wrong-typed \"steps\"")
	}
	if got := err.Error(); got != `field "steps" should be array, got string` {
		t.Fatalf("unexpected message: %s", got)
	}
}

func TestRequiredFieldsPresent_NoRequiredList(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{}}
	if err := RequiredFieldsPresent(schema, map[string]any{}); err != nil {
		t.Fatalf("expected no error when schema has no required list, got %v", err)
	}
}

func TestRequiredFieldsPresent_MultipleProblemsSorted(t *testing.T) {
	args := map[string]any{}
	err := RequiredFieldsPresent(planSchema, args)
	if err == nil {
		t.Fatal("expected an error")
	}
	want := `missing required field "approach"; missing required field "steps"`
	if got := err.Error(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
