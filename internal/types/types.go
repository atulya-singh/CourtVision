package types

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
