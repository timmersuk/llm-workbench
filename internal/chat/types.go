// Package chat provides an OpenAI-compatible chat completion client, used to
// talk to local/self-hosted OpenAI-compatible servers (e.g. Ollama, LM Studio).
package chat

// Message is a single message in a chat completion request or response.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// CompletionRequest is an OpenAI-compatible non-streaming chat completion
// request body.
type CompletionRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
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

// ModelsResponse is the OpenAI-compatible response body for GET
// {base}/models.
type ModelsResponse struct {
	Data []ModelInfo `json:"data"`
}

// ModelInfo describes a single model the provider makes available.
type ModelInfo struct {
	ID string `json:"id"`
}
