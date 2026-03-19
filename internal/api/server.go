package api

import (
	"github.com/atulya-singh/CourtVision/internal/store"
)

// Server holds the HTTP server and its dependencies
type Server struct {
	store *store.Store
	port  string
}
