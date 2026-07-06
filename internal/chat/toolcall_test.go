package chat

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestToolCallAccumulator_SingleCallSingleFragment(t *testing.T) {
	acc := newToolCallAccumulator()
	acc.add(0, "call-1", "get_weather", `{"city":"Paris"}`)

	assert.Equal(t, []ToolCall{
		{ID: "call-1", Type: "function", Function: ToolCallFunction{Name: "get_weather", Arguments: `{"city":"Paris"}`}},
	}, acc.flush())
}

func TestToolCallAccumulator_NameAndIDOnlyOnFirstFragment(t *testing.T) {
	acc := newToolCallAccumulator()
	acc.add(0, "call-1", "get_weather", "")
	acc.add(0, "", "", `{"city":`)
	acc.add(0, "", "", `"Paris"}`)

	assert.Equal(t, []ToolCall{
		{ID: "call-1", Type: "function", Function: ToolCallFunction{Name: "get_weather", Arguments: `{"city":"Paris"}`}},
	}, acc.flush())
}

func TestToolCallAccumulator_MultipleParallelCallsPreserveFirstSeenOrder(t *testing.T) {
	acc := newToolCallAccumulator()
	acc.add(1, "call-2", "second", `{}`)
	acc.add(0, "call-1", "first", `{}`)
	acc.add(1, "", "", "") // more fragments for the already-seen index-1 call
	acc.add(0, "", "", "")

	got := acc.flush()
	assert.Len(t, got, 2)
	assert.Equal(t, "call-2", got[0].ID)
	assert.Equal(t, "second", got[0].Function.Name)
	assert.Equal(t, "call-1", got[1].ID)
	assert.Equal(t, "first", got[1].Function.Name)
}

func TestToolCallAccumulator_OutOfOrderIndicesInterleaved(t *testing.T) {
	acc := newToolCallAccumulator()
	acc.add(0, "call-1", "a", `{"x":`)
	acc.add(1, "call-2", "b", `{"y":`)
	acc.add(0, "", "", `1}`)
	acc.add(1, "", "", `2}`)

	got := acc.flush()
	assert.Len(t, got, 2)
	assert.Equal(t, `{"x":1}`, got[0].Function.Arguments)
	assert.Equal(t, `{"y":2}`, got[1].Function.Arguments)
}

func TestToolCallAccumulator_FlushWithNoCallsIsEmpty(t *testing.T) {
	acc := newToolCallAccumulator()
	assert.Empty(t, acc.flush())
}
