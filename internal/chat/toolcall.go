package chat

import "strings"

// toolCallAccumulator merges fragmented streamed tool-call deltas —
// arriving keyed by index, with id/name typically only present on a call's
// first fragment and arguments arriving as incremental string pieces —
// into complete ToolCalls, preserving first-seen order. Kept independent
// of client.go's HTTP/scanner loop so it can be unit-tested with plain Go
// values.
type toolCallAccumulator struct {
	order []int
	byIdx map[int]*accumulatedToolCall
}

type accumulatedToolCall struct {
	id, name string
	args     strings.Builder
}

func newToolCallAccumulator() *toolCallAccumulator {
	return &toolCallAccumulator{byIdx: make(map[int]*accumulatedToolCall)}
}

// add merges one streamed fragment for the call at index. id/name may be
// empty on any fragment after the first; args is appended verbatim.
func (a *toolCallAccumulator) add(index int, id, name, argsFragment string) {
	call, ok := a.byIdx[index]
	if !ok {
		call = &accumulatedToolCall{}
		a.byIdx[index] = call
		a.order = append(a.order, index)
	}
	if id != "" {
		call.id = id
	}
	if name != "" {
		call.name = name
	}
	call.args.WriteString(argsFragment)
}

// flush returns every call accumulated so far, in first-seen order, as
// complete ToolCalls.
func (a *toolCallAccumulator) flush() []ToolCall {
	calls := make([]ToolCall, 0, len(a.order))
	for _, idx := range a.order {
		c := a.byIdx[idx]
		calls = append(calls, ToolCall{
			ID:   c.id,
			Type: "function",
			Function: ToolCallFunction{
				Name:      c.name,
				Arguments: c.args.String(),
			},
		})
	}
	return calls
}
