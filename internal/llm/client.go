package llm

import (
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
