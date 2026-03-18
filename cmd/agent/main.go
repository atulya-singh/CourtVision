package main

import (
	"log"

	"github.com/atulya-singh/CourtVision/internal/metrics"
)

func main() {
	log.SetFlags(log.Ltime | log.Lmsgprefix)
	log.SetPrefix("[CourtVision] ")

	log.Println("Starting Agentic Infrastructure Monitor")
	log.Println("---")
	//constructors
	provider := metrics.NewMockProvider()
	engine := decision.newRuleBasedEngine()
}
