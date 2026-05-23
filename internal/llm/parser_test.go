package llm

import (
	"fmt"
	"strings"
	"testing"

	"github.com/atulya-singh/CourtVision/internal/types"
)

// ── ParseResponse ─────────────────────────────────────────────────────────────

func TestParseResponse_SingleValidLine(t *testing.T) {
	input := `{"action":"patch_limits","target_pod":"api-server","namespace":"production","severity":"high","reasoning":"CPU at 95%.","new_cpu_limit":650}`

	decisions, err := ParseResponse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(decisions) != 1 {
		t.Fatalf("want 1 decision, got %d", len(decisions))
	}

	d := decisions[0]
	if d.Action != types.ActionPatchLimits {
		t.Errorf("action: want %s, got %s", types.ActionPatchLimits, d.Action)
	}
	if d.Severity != types.SeverityHigh {
		t.Errorf("severity: want %s, got %s", types.SeverityHigh, d.Severity)
	}
	if d.TargetPod != "api-server" {
		t.Errorf("target_pod: want api-server, got %s", d.TargetPod)
	}
	if d.Namespace != "production" {
		t.Errorf("namespace: want production, got %s", d.Namespace)
	}
	if d.NewCPULimit != 650 {
		t.Errorf("new_cpu_limit: want 650, got %f", d.NewCPULimit)
	}
}

func TestParseResponse_MultipleLines(t *testing.T) {
	input := `{"action":"patch_limits","target_pod":"pod-a","namespace":"ns","severity":"high","reasoning":"CPU high."}
{"action":"none","target_pod":"pod-b","namespace":"ns","severity":"low","reasoning":"All good."}`

	decisions, err := ParseResponse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(decisions) != 2 {
		t.Fatalf("want 2 decisions, got %d", len(decisions))
	}
	if decisions[0].TargetPod != "pod-a" || decisions[1].TargetPod != "pod-b" {
		t.Errorf("wrong pods: %s, %s", decisions[0].TargetPod, decisions[1].TargetPod)
	}
}

func TestParseResponse_EmptyInput(t *testing.T) {
	decisions, err := ParseResponse("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(decisions) != 0 {
		t.Errorf("want 0 decisions for empty input, got %d", len(decisions))
	}
}

func TestParseResponse_StripsMarkdownCodeBlock(t *testing.T) {
	input := "```json\n{\"action\":\"none\",\"target_pod\":\"pod-a\",\"namespace\":\"ns\",\"severity\":\"low\",\"reasoning\":\"OK.\"}\n```"

	decisions, err := ParseResponse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(decisions) != 1 {
		t.Fatalf("want 1 decision after stripping markdown, got %d", len(decisions))
	}
}

func TestParseResponse_StripsUnfencedCodeBlock(t *testing.T) {
	input := "```\n{\"action\":\"none\",\"target_pod\":\"pod\",\"namespace\":\"ns\",\"severity\":\"low\",\"reasoning\":\"OK.\"}\n```"

	decisions, err := ParseResponse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(decisions) != 1 {
		t.Fatalf("want 1 decision, got %d", len(decisions))
	}
}

func TestParseResponse_SkipsNonJSONLines(t *testing.T) {
	input := `Here is my analysis of the cluster:
{"action":"none","target_pod":"pod-a","namespace":"ns","severity":"low","reasoning":"Fine."}
All pods look healthy overall.`

	decisions, err := ParseResponse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(decisions) != 1 {
		t.Fatalf("want 1 decision (prose lines skipped), got %d", len(decisions))
	}
}

func TestParseResponse_SkipsMalformedJSON(t *testing.T) {
	input := `{"action":"none","target_pod":"pod-a","namespace":"ns","severity":"low","reasoning":"Fine."}
{this is not valid json at all}`

	decisions, err := ParseResponse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(decisions) != 1 {
		t.Fatalf("want 1 decision (malformed line skipped), got %d", len(decisions))
	}
}

func TestParseResponse_AllActionTypes(t *testing.T) {
	tests := []struct {
		raw  string
		want types.ActionType
	}{
		{"patch_limits", types.ActionPatchLimits},
		{"evict_and_move", types.ActionEvictAndMove},
		{"scale_down", types.ActionScaleDown},
		{"cordon_node", types.ActionCordonNode},
		{"none", types.ActionNone},
		{"unknown_action", types.ActionNone}, // unrecognised string defaults to none
	}

	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			line := fmt.Sprintf(`{"action":%q,"target_pod":"pod","namespace":"ns","severity":"low","reasoning":"test"}`, tc.raw)
			decisions, err := ParseResponse(line)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(decisions) != 1 {
				t.Fatalf("want 1 decision, got %d", len(decisions))
			}
			if decisions[0].Action != tc.want {
				t.Errorf("want %s, got %s", tc.want, decisions[0].Action)
			}
		})
	}
}

func TestParseResponse_AllSeverityLevels(t *testing.T) {
	tests := []struct {
		raw  string
		want types.Severity
	}{
		{"low", types.SeverityLow},
		{"medium", types.SeverityMedium},
		{"high", types.SeverityHigh},
		{"critical", types.SeverityCritical},
		{"bogus", types.SeverityMedium}, // unrecognised string defaults to medium
	}

	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			line := fmt.Sprintf(`{"action":"none","target_pod":"pod","namespace":"ns","severity":%q,"reasoning":"test"}`, tc.raw)
			decisions, err := ParseResponse(line)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(decisions) != 1 {
				t.Fatalf("want 1 decision, got %d", len(decisions))
			}
			if decisions[0].Severity != tc.want {
				t.Errorf("want %s, got %s", tc.want, decisions[0].Severity)
			}
		})
	}
}

func TestParseResponse_OptionalFieldsPopulated(t *testing.T) {
	input := `{"action":"evict_and_move","target_pod":"pod","namespace":"ns","severity":"high","reasoning":"test","target_node":"node-2","new_cpu_limit":400,"new_mem_limit":512}`

	decisions, err := ParseResponse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(decisions) != 1 {
		t.Fatalf("want 1 decision, got %d", len(decisions))
	}

	d := decisions[0]
	if d.TargetNode != "node-2" {
		t.Errorf("target_node: want node-2, got %s", d.TargetNode)
	}
	if d.NewCPULimit != 400 {
		t.Errorf("new_cpu_limit: want 400, got %f", d.NewCPULimit)
	}
	if d.NewMemLimit != 512 {
		t.Errorf("new_mem_limit: want 512, got %f", d.NewMemLimit)
	}
}

func TestParseResponse_DecisionIDsAreUnique(t *testing.T) {
	line := `{"action":"none","target_pod":"pod","namespace":"ns","severity":"low","reasoning":"test"}`
	input := strings.Repeat(line+"\n", 5)

	decisions, err := ParseResponse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	seen := make(map[string]bool)
	for _, d := range decisions {
		if seen[d.ID] {
			t.Errorf("duplicate decision ID: %s", d.ID)
		}
		seen[d.ID] = true
	}
}

func TestParseResponse_ExecutedIsFalse(t *testing.T) {
	input := `{"action":"patch_limits","target_pod":"pod","namespace":"ns","severity":"low","reasoning":"test"}`

	decisions, err := ParseResponse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decisions[0].Executed {
		t.Error("new decision should not be marked executed")
	}
}
