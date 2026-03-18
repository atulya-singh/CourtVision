package main

import (
	"log"
	"os"
	"time"

	"github.com/atulya-singh/CourtVision/internal/metrics"
	"github.com/atulya-singh/CourtVision/internal/types"
)

func main() {
	log.SetFlags(log.Ltime | log.Lmsgprefix)
	log.SetPrefix("[CourtVision] ")

	log.Println("Starting Agentic Infrastructure Monitor")
	log.Println("---")
	//constructors
	provider := metrics.NewMockProvider()
	engine := decision.newRuleBasedEngine()

	//decision log
	var allDecisions []types.Decision

	// Main monitoring loop
	interval := 5 * time.Second // slow interval
	if len(os.Args) > 1 && os.Args[1] == "--fast" {
		interval = 1 * time.Second // fast interval
	}

}
