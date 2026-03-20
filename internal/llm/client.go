package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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

// Generate sends a prompt to Ollama and returns the raw text response (no parsing yet)
func (c *Client) Generate(prompt string) (string, error) {
	// 1. Build the request body
	reqBody := ollamaRequest{
		Model:  c.model,
		Prompt: prompt,
		Stream: false, // This is for getting complete response, not a token by token response
		Options: ollamaOptions{
			Temperature: 0.1,  // low temp = more consisten answers
			NumPredict:  2048, // enough room for analysis + JSON
		},
	}

	// 2. Serialize to JSON

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("falied to marshal request: %w", err)
	}

	// 3. Make the HTTP POST request
	url := c.baseURL + "/api/generate"
	resp, err := c.httpClient.Post(url, "application/json", bytes.NewReader(jsonData))
	if err != nil {
		return "", fmt.Errorf("ollama request failed: %w", err)
	}
	defer resp.Body.Close()

	// 4. Read the response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	// 5. Check HTTP status
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama returned status %d: %s", resp.StatusCode, string(body))
	}

	// 6. Parse the JSON response
	var ollamaResp ollamaResponse
	if err := json.Unmarshal(body, &ollamaResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}
	return ollamaResp.Response, nil
}
