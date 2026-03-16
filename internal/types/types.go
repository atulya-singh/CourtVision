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

// CPUPressure returns CPU usage ration from 0.0 - 1.0, 0.8 means 80% of CPU is being used
func (n *NodeMetrics) CPUPressure() float64 {
	if n.CPUCapacityMilli == 0 {
		return 0
	}
	return n.CPUUsedMilli / n.CPUCapacityMilli
}

// Decision represents a single action the agent wants to take
type Decision struct {
	ID          string     `json:"id"`
	Timestamp   time.Time  `json:"timestamp"`
	Severity    Severity   `json:"severity"`
	Action      ActionType `json:"action"`
	TargetPod   string     `json:"target_pod"`
	Namespace   string     `json:"namespace"`
	TargetNode  string     `json:"target_node,omitempty"`   //
	Reasoning   string     `json:"reasoning"`               //LLM's explanation
	NewCPULimit float64    `json:"new_cpu_limit,omitempty"` //patch_limits
	NewMemLimit float64    `json:"new_mem_limit,omitempty"` //for patch_limits
	Executed    bool       `json:"executed"`
	ExecutedAt  *time.Time `json:"executed_at,omitempty"`
	Error       string     `json:"error,omitempty"`
}

// ClusterSnapshot is the full picture sent to the LLM for analysis
type ClusterSnapshot struct {
	Pods      []PodMetrics  `json:"pods"`
	Nodes     []NodeMetrics `json:"nodes"`
	Timestamp time.Time     `json:"timestamp"`
}
