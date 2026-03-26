package main

import (
	"log"
	"os"
	"time"

	"github.com/atulya-singh/CourtVision/internal/api"
	"github.com/atulya-singh/CourtVision/internal/decision"
	"github.com/atulya-singh/CourtVision/internal/llm"
	"github.com/atulya-singh/CourtVision/internal/metrics"
	"github.com/atulya-singh/CourtVision/internal/store"
	"github.com/atulya-singh/CourtVision/internal/types"
)

func main() {
	log.SetFlags(log.Ltime | log.Lmsgprefix)
	log.SetPrefix("[CourtVision] ")

	log.Println("Starting Agentic Infrastructure Monitor")
	log.Println("---")
	//constructors
	st := store.New()
	provider := metrics.NewMockProvider()

	ollamaURL := os.Getenv("OLLAMA_URL")
	if ollamaURL == "" {
		ollamaURL = "https://localhost:11434"
	}

	model := os.Getenv("OLLAMA_MODEL")
	if model == "" {
		model = "llama3"
	}

	log.Printf("Connecting to Ollama at %s (model: %s)", ollamaURL, model)
	llmClient := llm.NewClient(ollamaURL, model)
	engine := llm.NewEngine(llmClient)

	go monitorLoop(provider, engine, st)

	server := api.NewServer(st, "8080")
	log.Fatal(server.Start())
}

type analyzer interface {
	Analyze(snapshot *types.ClusterSnapshot) ([]types.Decision, error)
}

func monitorLoop(provider metrics.Provider, engine decision.Engine, st *store.Store) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	log.Println("Monitor loop started (every 3 seconds)")

	for range ticker.C {
		// 1. Collect metrics snapshot

		snapshot, err := provider.GetClusterSnapshot()
		if err != nil {
			log.Printf("ERROR collecting metrics: %v", err)
			continue
		}
		// 2. Store the snapshot (API can now serve it)
		st.SetSnapshot(snapshot)

		decisions, err := engine.Analyze(snapshot)
		if err != nil {
			log.Printf("ERROR analyzing: %v", err)
			continue
		}

		for _, d := range decisions {
			st.AddDecision(d)
			log.Printf("Decision: [%s] %s -> %s", d.Severity, d.TargetPod, d.Action)
		}
		if len(decisions) == 0 {
			log.Println("Cycle complete - no issues")
		}
	}
}
