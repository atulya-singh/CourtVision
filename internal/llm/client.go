package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"time"
)

type Client struct {
	baseURL    string
	model      string
	httpClient *http.Client
}

// NewClient creates an Ollama client
func NewClient(baseURL string, model string) *Client {
	return &Client{
		baseURL: baseURL,
		model:   model,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// ollamaRequest is what we send to Ollama's /api/generate endpoint
type ollamaRequest struct {
	Model   string        `json:"model"`
	Prompt  string        `json:"prompt"`
	Stream  bool          `json:"stream"` // false = wait for full response
	Options ollamaOptions `json:"options"`
}

// struct to control LLM behavior
type ollamaOptions struct {
	Temperature float64 `json:"temperature"` // 0.0 = deterministic, 1.0 = creative
	NumPredict  int     `json:"num_predict"` // max tokens in response
}

// ollamaResponse is what Ollama sends back
type ollamaResponse struct {
	Model    string `json:"model"`
	Response string `json:"response"` // The actual LLM output
	Done     bool   `json:"done"`
}

// Generate sends a prompt to Ollama and returns the raw text response.
// It retries up to 3 times on transient network errors with exponential
// backoff and jitter. Non-2xx Ollama responses are not retried.
func (c *Client) Generate(prompt string) (string, error) {
	reqBody := ollamaRequest{
		Model:  c.model,
		Prompt: prompt,
		Stream: false,
		Options: ollamaOptions{
			Temperature: 0.1,
			NumPredict:  2048,
		},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	const maxAttempts = 3
	var lastErr error

	for i := 0; i < maxAttempts; i++ {
		if i > 0 {
			// Exponential backoff: 100ms, 200ms — plus ±20% jitter to avoid
			// thundering herd if multiple agents restart simultaneously.
			base := time.Duration(100*(1<<uint(i-1))) * time.Millisecond
			jitter := time.Duration(rand.Int63n(int64(base) / 5))
			time.Sleep(base + jitter)
		}

		resp, err := c.httpClient.Post(c.baseURL+"/api/generate", "application/json", bytes.NewReader(jsonData))
		if err != nil {
			lastErr = fmt.Errorf("connection failed: %w", err)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("failed to read response: %w", err)
			continue
		}

		// Non-2xx responses are Ollama application errors (bad model name,
		// malformed request) — they won't recover on retry.
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("ollama returned status %d: %s", resp.StatusCode, string(body))
		}

		var ollamaResp ollamaResponse
		if err := json.Unmarshal(body, &ollamaResp); err != nil {
			return "", fmt.Errorf("failed to parse response: %w", err)
		}
		return ollamaResp.Response, nil
	}

	return "", fmt.Errorf("ollama unreachable after %d attempts: %w", maxAttempts, lastErr)
}
