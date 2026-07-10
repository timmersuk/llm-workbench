package main

import (
	"encoding/json"
	"testing"
)

// TestDumpToolSchema prints what the adapter actually puts on the wire for
// each tool, to compare against the OpenAI tools format LM Studio expects.
func TestDumpToolSchema(t *testing.T) {
	for _, tool := range makeTools(".") {
		b, err := json.MarshalIndent(tool.Schema(), "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s:\n%s", tool.Name(), b)
	}
}
