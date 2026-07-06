// Package chat provides an OpenAI-compatible chat completion client, used to
// talk to local/self-hosted OpenAI-compatible servers (e.g. Ollama, LM Studio).
package chat

import "encoding/json"

// Message is a single message in a chat completion request or response.
// ToolCalls is set on an assistant message that called one or more tools;
// ToolCallID is set on a "tool" role message replying to one of those calls
// (matched by ID) — both are only relevant once Tools is used on the
// request.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// Tool declares one function the model may call, in the OpenAI-compatible
// "tools" request field shape.
type Tool struct {
	Type     string     `json:"type"` // always "function"
	Function ToolSchema `json:"function"`
}

// ToolSchema names a callable function and its JSON Schema parameters.
type ToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ToolCall is a single function call the model made: either fully
// accumulated from streamed Deltas (see Delta.ToolCall) or replayed back
// into Message.ToolCalls when reconstructing prior conversation history
// that included one.
type ToolCall struct {
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type"` // always "function"
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction is the called function's name and arguments. Arguments
// is a JSON-encoded string (per the OpenAI-compatible wire format), not a
// nested object — callers must json.Unmarshal it themselves.
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// CompletionRequest is an OpenAI-compatible chat completion request body.
// Stream is set internally by ChatClient's two completion methods
// (CreateChatCompletion forces it false, StreamChatCompletion forces it
// true) — callers don't need to set it themselves. Tools is optional; when
// empty, this is exactly the request shape that existed before tool-calling
// support was added.
type CompletionRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Tools    []Tool    `json:"tools,omitempty"`
	Stream   bool      `json:"stream,omitempty"`
}

// CompletionResponse is an OpenAI-compatible non-streaming chat completion
// response body (only the fields this workbench uses).
type CompletionResponse struct {
	ID      string   `json:"id"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
}

// Choice is a single completion choice.
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Delta is one incremental piece of a streamed chat completion, emitted by
// ChatClient.StreamChatCompletion. Content, ReasoningContent, and ToolCall
// are never more than one populated on the same Delta — reasoning arrives
// as its own complete stream of Deltas before the final-answer Deltas start
// (matching how reasoning-capable models, e.g. DeepSeek-R1-style, via
// llama.cpp/LM Studio's reasoning_content extension, actually emit it), and
// ToolCall is only ever populated once per call, after StreamChatCompletion
// has fully accumulated that call's streamed argument fragments — never
// emitted as partial JSON.
type Delta struct {
	Content          string    `json:"content"`
	ReasoningContent string    `json:"reasoning_content"`
	ToolCall         *ToolCall `json:"tool_call,omitempty"`
}

// streamChunk is the wire shape of a single OpenAI-compatible
// "chat.completion.chunk" Server-Sent Event, decoded internally by
// StreamChatCompletion — only the fields this workbench reads. ToolCalls
// mirrors how upstream servers actually stream them: one fragment per
// chunk, keyed by Index (stable across chunks for the same call); ID/Name
// are typically only present on a call's first fragment, and Arguments
// arrives as incremental string pieces of one JSON object.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

// ModelsResponse is the OpenAI-compatible response body for GET
// {base}/models.
type ModelsResponse struct {
	Data []ModelInfo `json:"data"`
}

// ModelInfo describes a single model the provider makes available.
type ModelInfo struct {
	ID string `json:"id"`
}
