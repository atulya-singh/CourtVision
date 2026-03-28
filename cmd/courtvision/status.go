package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func statusCmd() *cobra.Command {
	var ollamaURL string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Check connectivity to Ollama and Kubernetes",
		Long: `Verify that CourtVision can reach its dependencies.
 
Checks Ollama connectivity and lists installed models.
In future versions, also checks Kubernetes cluster access
and metrics-server availability.`,

		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println()
			fmt.Println("  CourtVision Status")
			fmt.Println(" ------------------")
			fmt.Println()

			// Check Ollama
			checkOllama(ollamaURL)

			// Check Kubernetes (placeholder for when you build K8s integration)
			fmt.Println()
			fmt.Println("  Kubernetes: ○ Not configured (use --metrics k8s to enable)")
			fmt.Println()

			return nil
		},
	}

	cmd.Flags().StringVar(&ollamaURL, "ollama-url", "http://localhost:11434", "Ollama server URL")

	return cmd
}

type ollamaTagsResponse struct {
	Models []ollamaModel `json:"models"`
}

type ollamaModel struct {
	Name string `json:"name"`
}

func checkOllama(baseURL string) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(baseURL + "/api/tags")

	if err != nil {
		fmt.Printf("  Ollama:    ✗ Not reachable (%s)\n", baseURL)
		fmt.Printf("             Error: %v\n", err)
		fmt.Println("             Is Ollama running? Start it with: ollama serve")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Printf("  Ollama:    ✗ Returned status %d (%s)\n", resp.StatusCode, baseURL)
		return
	}

	// parse the response to list installed models
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Printf("  Ollama:    ✓ Connected (%s)\n", baseURL)
		fmt.Println("             Could not read model list")
		return
	}
	var tags ollamaTagsResponse
	if err := json.Unmarshal(body, &tags); err != nil {
		fmt.Printf("  Ollama:    ✓ Connected (%s)\n", baseURL)
		fmt.Println("             Could not parse model list")
		return
	}

	fmt.Printf("  Ollama:    ✓ Connected (%s)\n", baseURL)

	if len(tags.Models) == 0 {
		fmt.Println("  Models:    (none installed)")
		fmt.Println("             Run: ollama pull llama3")
	} else {
		names := make([]string, len(tags.Models))
		for i, m := range tags.Models {
			names[i] = m.Name
		}
		fmt.Printf("  Models:    %s\n", strings.Join(names, ", "))
	}
}
