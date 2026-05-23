package decision

import (
	"log"

	"github.com/atulya-singh/CourtVision/internal/types"
)

// FallbackEngine tries the primary engine and transparently falls back to
// the secondary if the primary returns an error (e.g. Ollama is unreachable).
type FallbackEngine struct {
	primary  Engine
	fallback Engine
}

func NewFallbackEngine(primary, fallback Engine) *FallbackEngine {
	return &FallbackEngine{primary: primary, fallback: fallback}
}

func (f *FallbackEngine) Analyze(snapshot *types.ClusterSnapshot) ([]types.Decision, error) {
	decisions, err := f.primary.Analyze(snapshot)
	if err != nil {
		log.Printf("primary engine failed (%v), using rule-based fallback", err)
		return f.fallback.Analyze(snapshot)
	}
	return decisions, nil
}
