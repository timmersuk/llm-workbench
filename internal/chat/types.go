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
	Model           string    `json:"model"`
	ReasoningEffort string    `json:"reasoning_effort,omitempty"`
	Messages        []Message `json:"messages"`
	Tools           []Tool    `json:"tools,omitempty"`
	// MaxTokens caps the tokens generated per response. Zero omits the field
	// (provider default). The tool loop sets it so a misbehaving local model
	// — one that spirals or emits the same tool call unbounded — cannot
	// generate without limit and wedge the endpoint (Milestone 8 Phase 0).
	MaxTokens int  `json:"max_tokens,omitempty"`
	Stream    bool `json:"stream,omitempty"`
	// StreamOptions is forced non-nil (IncludeUsage: true) by
	// StreamChatCompletion — callers don't set it themselves, mirroring how
	// Stream itself is forced — so the loop can read a final usage chunk.
	StreamOptions *StreamOptions `json:"stream_options,omitempty"`
}

// StreamOptions is the OpenAI-compatible "stream_options" request field.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
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
	// Usage is populated exactly once, on the final usage-only chunk a
	// stream_options.include_usage request gets after the last content/tool
	// chunk (empty Choices, so it is otherwise skipped) — never combined
	// with Content/ReasoningContent/ToolCall on the same Delta.
	Usage *Usage `json:"usage,omitempty"`
}

// Usage is OpenAI-compatible token accounting for one completion, requested
// via CompletionRequest.StreamOptions and delivered as a Delta.Usage.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// streamChunk is the wire shape of a single OpenAI-compatible
// "chat.completion.chunk" Server-Sent Event, decoded internally by
// StreamChatCompletion — only the fields this workbench reads. ToolCalls
// mirrors how upstream servers actually stream them: one fragment per
// chunk, keyed by Index (stable across chunks for the same call); ID/Name
// are typically only present on a call's first fragment, and Arguments
// arrives as incremental string pieces of one JSON object.
type streamChunk struct {
	// Usage is only present on the final usage-only chunk (see Delta.Usage);
	// absent on every content/tool-call chunk.
	Usage   *Usage `json:"usage,omitempty"`
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
			// Reasoning-capable models expose their thinking under one of two
			// keys depending on family: reasoning_content (qwen, deepseek via
			// llama.cpp/LM Studio) or reasoning (gpt-oss). Both are decoded so
			// the loop sees reasoning regardless of which the model uses
			// (Milestone 8 Phase 0 found gpt-oss uses `reasoning`).
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
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
