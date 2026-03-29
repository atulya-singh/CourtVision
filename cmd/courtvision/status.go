package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/atulya-singh/CourtVision/internal/ui"
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
			var lines []string

			lines = append(lines, ui.BrandStyle.Render("CourtVision Status"))
			lines = append(lines, "")

			// Check Ollama
			ollamaLines := checkOllamaStyled(ollamaURL)
			lines = append(lines, ollamaLines...)

			// Check Kubernetes
			lines = append(lines, "")
			lines = append(lines,
				fmt.Sprintf("%s  Kubernetes: Not configured %s",
					ui.Dot,
					ui.DimStyle.Render("(use --metrics k8s to enable)")))

			content := strings.Join(lines, "\n")
			fmt.Println()
			fmt.Println(ui.BorderBox.Render(content))
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

func checkOllamaStyled(baseURL string) []string {
	var lines []string
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(baseURL + "/api/tags")

	if err != nil {
		lines = append(lines,
			fmt.Sprintf("%s  Ollama: Not reachable %s",
				ui.CrossMark,
				ui.DimStyle.Render(fmt.Sprintf("(%s)", baseURL))))
		lines = append(lines,
			fmt.Sprintf("   %s %v", ui.DimStyle.Render("Error:"), err))
		lines = append(lines,
			fmt.Sprintf("   %s", ui.DimStyle.Render("Is Ollama running? Start it with: ollama serve")))
		return lines
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		lines = append(lines,
			fmt.Sprintf("%s  Ollama: Returned status %d %s",
				ui.CrossMark,
				resp.StatusCode,
				ui.DimStyle.Render(fmt.Sprintf("(%s)", baseURL))))
		return lines
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		lines = append(lines,
			fmt.Sprintf("%s  Ollama: Connected %s",
				ui.CheckMark,
				ui.DimStyle.Render(fmt.Sprintf("(%s)", baseURL))))
		lines = append(lines,
			fmt.Sprintf("   %s", ui.DimStyle.Render("Could not read model list")))
		return lines
	}

	var tags ollamaTagsResponse
	if err := json.Unmarshal(body, &tags); err != nil {
		lines = append(lines,
			fmt.Sprintf("%s  Ollama: Connected %s",
				ui.CheckMark,
				ui.DimStyle.Render(fmt.Sprintf("(%s)", baseURL))))
		lines = append(lines,
			fmt.Sprintf("   %s", ui.DimStyle.Render("Could not parse model list")))
		return lines
	}

	lines = append(lines,
		fmt.Sprintf("%s  Ollama: Connected %s",
			ui.CheckMark,
			ui.DimStyle.Render(fmt.Sprintf("(%s)", baseURL))))

	if len(tags.Models) == 0 {
		lines = append(lines,
			fmt.Sprintf("   %s %s",
				ui.DimStyle.Render("Models:"),
				ui.DimStyle.Render("(none installed)")))
		lines = append(lines,
			fmt.Sprintf("   %s", ui.DimStyle.Render("Run: ollama pull llama3")))
	} else {
		names := make([]string, len(tags.Models))
		for i, m := range tags.Models {
			names[i] = ui.CyanStyle.Render(m.Name)
		}
		lines = append(lines,
			fmt.Sprintf("   %s %s",
				ui.DimStyle.Render("Models:"),
				strings.Join(names, ui.DimStyle.Render(", "))))
	}

	return lines
}
