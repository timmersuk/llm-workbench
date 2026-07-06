// Package chat provides an OpenAI-compatible chat completion client, used to
// talk to local/self-hosted OpenAI-compatible servers (e.g. Ollama, LM Studio).
package chat

// Message is a single message in a chat completion request or response.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// CompletionRequest is an OpenAI-compatible chat completion request body.
// Stream is set internally by ChatClient's two completion methods
// (CreateChatCompletion forces it false, StreamChatCompletion forces it
// true) — callers don't need to set it themselves.
type CompletionRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
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
// ChatClient.StreamChatCompletion. Content and ReasoningContent are never
// both populated on the same Delta — reasoning arrives as its own complete
// stream of Deltas before the final-answer Deltas start, matching how
// reasoning-capable models (e.g. DeepSeek-R1-style, via llama.cpp/LM
// Studio's reasoning_content extension) actually emit it.
type Delta struct {
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content"`
}

// streamChunk is the wire shape of a single OpenAI-compatible
// "chat.completion.chunk" Server-Sent Event, decoded internally by
// StreamChatCompletion — only the fields this workbench reads.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
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
