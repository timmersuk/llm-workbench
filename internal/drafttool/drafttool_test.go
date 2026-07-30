package drafttool

import "testing"

func TestProposePlanValidate(t *testing.T) {
	valid := map[string]any{
		"approach":             "do the thing",
		"steps":                []any{"step 1"},
		"estimated_complexity": "medium",
	}
	if err := ProposePlan.Validate(valid); err != nil {
		t.Fatalf("expected valid proposal to pass, got %v", err)
	}

	// The exact corruption seen in production: steps never landed as its
	// own key.
	corrupted := map[string]any{
		"approach":             "do the thing</approach><parameter name=\"steps\">[\"a\"]",
		"estimated_complexity": "medium",
	}
	err := ProposePlan.Validate(corrupted)
	if err == nil {
		t.Fatal("expected corrupted proposal (missing steps) to fail validation")
	}
}
