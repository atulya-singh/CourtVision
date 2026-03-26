package llm

import (
	"log"

	"github.com/atulya-singh/CourtVision/internal/types"
)

type Generatable interface {
	Generate(prompt string) (string, error)
}

type Engine struct {
	client Generatable
}

func NewEngine(client Generatable) *Engine {
	return &Engine{client: client}
}

func (e *Engine) Analyze(snapshot *types.ClusterSnapshot) ([]types.Decision, error) {
	// Convert current system state into a prompt for LLM

	prompt := BuildPrompt(snapshot)

	// send prompt to LLM and get raw output

	response, err := e.client.Generate(prompt)

	if err != nil {
		log.Printf("LLM generation failed : %v", err)
		return nil, err
	}

	// parse raw output into structured response

	decisions, err := ParseResponse(response)

	if err != nil {
		log.Printf("Failed to parse LLM response: %v", err)
		return nil, err
	}

	log.Printf("LLM produced %d decision(s)", len(decisions))

	return decisions, nil
}
