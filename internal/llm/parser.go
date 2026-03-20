package llm

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/atulya-singh/CourtVision/internal/types"
)

// llmDecision is the JSON structure we expect the LLM to output
// It maps to our types.Decision but with simpler field names for the LLM.

type llmDecision struct {
	Action      string  `json:"action"`
	TargetPod   string  `json:"target_pod"`
	Namespace   string  `json:"namespace"`
	Severity    string  `json:"severity"`
	Reasoning   string  `json:"reasoning"`
	TargetNode  string  `json:"target_node,omitempty"`
	NewCPULimit float64 `json:"new_cpu_limit,omitempty"`
	NewMemLimit float64 `json:"new_mem_limit,omitempty"`
}

var parseCount int // tracks number of decisions we have parsed

func ParseResponse(raw string) ([]types.Decision, error) {
	var decisions []types.Decision

	cleaned := cleanLLMOutput(raw)

	lines := strings.Split(cleaned, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		// skip empty lines

		if line == "" {
			continue
		}

		// skip lines that aren't JSON
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var ld llmDecision
		if err := json.Unmarshal([]byte(line), &ld); err != nil {
			// BAD JSON - skip this line, try the next line\
			continue
		}
		// convert to our internal Decision type
		d := convertToDecision(ld)
		decisions = append(decisions, d)
	}

	return decisions, nil
}

// cleanLLMOutput handles common formatting issues in LLM responses
func cleanLLMOutput(raw string) string {
	s := raw

	// Remove markdown code block wrappers if present
	// LLMs often wrap JSON in ```json ... ``` despite being told not to
	s = strings.ReplaceAll(s, "```json", "")
	s = strings.ReplaceAll(s, "```", "")

	// Remove any leading/trailing whitespace
	s = strings.TrimSpace(s)

	return s
}

// convertToDecision maps the LLM's JSON format to our internal Decision type
func convertToDecision(ld llmDecision) types.Decision {
	parseCount++

	// Map string action to our ActionType
	action := types.ActionNone
	switch ld.Action {
	case "patch_limits":
		action = types.ActionPatchLimits
	case "evict_and_move":
		action = types.ActionEvictAndMove
	case "scale_down":
		action = types.ActionScaleDown
	case "cordon_node":
		action = types.ActionCordonNode
	}

	// Map string severity to our Severity type
	severity := types.SeverityMedium
	switch ld.Severity {
	case "low":
		severity = types.SeverityLow
	case "medium":
		severity = types.SeverityMedium
	case "high":
		severity = types.SeverityHigh
	case "critical":
		severity = types.SeverityCritical
	}

	return types.Decision{
		ID:          fmt.Sprintf("llm-decision-%d-%d", time.Now().Unix(), parseCount),
		Timestamp:   time.Now(),
		Severity:    severity,
		Action:      action,
		TargetPod:   ld.TargetPod,
		Namespace:   ld.Namespace,
		TargetNode:  ld.TargetNode,
		Reasoning:   ld.Reasoning,
		NewCPULimit: ld.NewCPULimit,
		NewMemLimit: ld.NewMemLimit,
		Executed:    false,
	}
}
