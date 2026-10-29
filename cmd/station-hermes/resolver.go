package main

import (
	"context"

	"stationhermes/internal/llm"
)

type stationResolver struct {
	hermes *llm.HermesClient
	prompt string
}

func (r stationResolver) ResolveStream(ctx context.Context, text string, onSentence func(string)) (string, error) {
	if answer, handled, err := r.hermes.HandleVoiceApproval(ctx, text); handled {
		return answer, err
	}
	messages := []llm.Message{{Role: "system", Content: r.prompt}, {Role: "user", Content: text}}
	answer, err := r.hermes.CompleteVoiceTurn(ctx, messages, onSentence)
	return answer, err
}
