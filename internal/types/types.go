package types

import "time"

// ActionType represents what the agent can do
type ActionType string

const (
	ActionNone         ActionType = "none"
	ActionPatchLimits  ActionType = "patch_limits"   // Rewrite resource limits on the fly
	ActionEvictAndMove ActionType = "evict_and_move" //Move pod to a differnet node
	ActionScaleDown    ActionType = "scale_down"     // Reduce replicas of the pod causing problems
	ActionCordonNode   ActionType = "cordon_node"    // Mark node as unschedulable
)

// Severity of the detected issue
type Severity string

const (
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

// PodMetrics holds realt-time resource usage for a single pod
type PodMetrics struct {
	PodName         string    `json:"pod_name"`
	Namespace       string    `json:"namespace"`
	NodeName        string    `json:"node_name"`
	CPUUsageMilli   float64   `json:"cpu_usage_milli"`   //millicores currently used
	CPULimitMilli   float64   `json:"cpu_limit_milli"`   //millicores limit
	CPURequestMilli float64   `json:"cpu_request_milli"` //millicores requested
	MemUsageMB      float64   `json:"mem_usage_mb"`      //MB currently used
	MemLimitMB      float64   `json:"mem_limit_mb"`      //MB limit
	MemRequestMB    float64   `json:"mem_request_mb"`    //MB requested
	RestartCount    int       `json:"restart_count"`
	Timestamp       time.Time `json:"timestamp"`
}
