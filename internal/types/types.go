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

// CPUPercent returns CPU usage as a percentage of its limit
func (p *PodMetrics) CPUPercent() float64 {
	if p.CPULimitMilli == 0 {
		return 0
	}
	return (p.CPUUsageMilli / p.CPULimitMilli) * 100
}

// MemPercent returns memory usage as a percentage of its limit
func (p *PodMetrics) MemPercent() float64 {
	if p.MemLimitMB == 0 {
		return 0
	}
	return (p.MemUsageMB / p.MemLimitMB) * 100
}

// holds aggregate resource info for a node
type NodeMetrics struct {
	NodeName         string    `json:"node_name"`
	NodeType         string    `json:"node_type"` // eg . comput-optimized, memory-optimized, general
	CPUCapacityMilli float64   `json:"cpu_capacity_milli"`
	CPUUsedMilli     float64   `json:"cpu_used_milli"`
	MemCapacityMb    float64   `json:"mem_capacity_mb"`
	MemUsedMB        float64   `json:"mem_used_mb"`
	PodCount         int       `json:"pod_count"`
	Timestamp        time.Time `json:"timestamp"`
}
