package store

import (
	"sync"

	"github.com/atulya-singh/CourtVision/internal/types"
)

type Store struct {
	mu        sync.RWMutex
	snapshot  *types.ClusterSnapshot
	decisions []types.Decision
	listeners []chan types.Decision // sse subscribers
}
