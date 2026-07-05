package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Completer creates chat completions.
type Completer interface {
	CreateChatCompletion(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
}

// Client talks to an OpenAI-compatible chat completions endpoint.
type Client struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

// NewClient returns a Client pointed at baseURL (e.g.
// "http://localhost:11434/v1"). apiKey may be empty for servers that don't
// require authentication.
func NewClient(baseURL, apiKey string, timeout time.Duration) *Client {
	return &Client{
		BaseURL:    baseURL,
		APIKey:     apiKey,
		HTTPClient: &http.Client{Timeout: timeout},
	}
}

// CreateChatCompletion posts req to {BaseURL}/chat/completions and decodes the
// response.
func (c *Client) CreateChatCompletion(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("encoding chat completion request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("building chat completion request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("calling chat completions endpoint: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("reading chat completions response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return CompletionResponse{}, fmt.Errorf("chat completions endpoint returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var out CompletionResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return CompletionResponse{}, fmt.Errorf("decoding chat completions response: %w", err)
	}
	return out, nil
}

// CheckHealth requests the models list from the provider and returns nil when
// the endpoint responds successfully.
func (c *Client) CheckHealth(ctx context.Context) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/models", nil)
	if err != nil {
		return fmt.Errorf("building healthcheck request: %w", err)
	}
	if c.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("calling models endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("models endpoint returned status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}
