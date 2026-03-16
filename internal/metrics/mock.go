package metrics

import (
	"sync"

	"github.com/atulya-singh/CourtVision/internal/types"
)

// Provider is the interface any metrics source must implement
type Provider interface {
	GetClusterSnapshot() (*types.ClusterSnapshot, error)
}

type MockProvider struct {
	mu    sync.Mutex
	tick  int
	nodes []nodeTemplate
	pods  []podTemplate
}

type nodeTemplate struct {
	name     string
	nodeType string
	cpuCap   float64 //millicores
	memCap   float64 //MB
}

type podTemplate struct {
	name      string
	namespace string
	node      string
	baseCPU   float64 //base CPU usage in millicores
	baseMem   float64 // base mem usage in MB
	cpuLimit  float64
	cpuReq    float64
	memLimit  float64
	memReq    float64
	noisy     bool // will this pode spike ?
}
