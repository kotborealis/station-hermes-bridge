package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"stationhermes/internal/llm"
)

type stationResolver struct {
	hermes   *llm.HermesClient
	sessions *sessionStore
	prompt   string
	reset    *resetConfirmation
}

func (r stationResolver) ResolveStream(ctx context.Context, text string, onSentence func(string)) (string, error) {
	if answer, handled, err := r.hermes.HandleVoiceApproval(ctx, text); handled {
		return resolveError(answer, err)
	}
	if answer, handled := r.handleReset(text); handled {
		return answer, nil
	}
	if stopIntent(text) {
		stopped, err := r.hermes.StopActiveRun(ctx)
		if err != nil {
			return voiceError(err), nil
		}
		if !stopped {
			return "Сейчас нет активного запроса.", nil
		}
		return "Останавливаю запрос.", nil
	}
	messages := []llm.Message{{Role: "system", Content: r.prompt}, {Role: "user", Content: text}}
	answer, err := r.hermes.CompleteVoiceTurn(ctx, r.sessions.Current(), messages, onSentence)
	return resolveError(answer, err)
}

func resolveError(answer string, err error) (string, error) {
	if err == nil {
		return answer, nil
	}
	slog.Warn("Hermes voice request failed", "error", err)
	return voiceError(err), nil
}

func voiceError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "Hermes не успел ответить. Попробуй ещё раз."
	}
	if errors.Is(err, llm.ErrRunFailed) || errors.Is(err, llm.ErrRunUnknown) {
		return "Hermes не смог выполнить запрос. Попробуй переформулировать."
	}
	if errors.Is(err, llm.ErrRunInterrupted) || errors.Is(err, context.Canceled) {
		return "Запрос остановлен."
	}
	var status *llm.HTTPStatusError
	if errors.As(err, &status) {
		switch {
		case status.StatusCode == 401 || status.StatusCode == 403:
			return "Hermes не принял токен доступа."
		case status.StatusCode == 429:
			return "Hermes временно ограничил запросы. Попробуй чуть позже."
		case status.StatusCode >= 500:
			return "Hermes сейчас недоступен. Попробуй чуть позже."
		default:
			return "Hermes отклонил запрос."
		}
	}
	if errors.Is(err, llm.ErrTransport) {
		return "Не удалось связаться с Hermes. Проверь подключение и попробуй ещё раз."
	}
	return "Не получилось получить ответ от Hermes."
}

type resetConfirmation struct {
	mu      sync.Mutex
	expires time.Time
}

func (r stationResolver) handleReset(text string) (string, bool) {
	if r.reset == nil || r.sessions == nil {
		return "", false
	}
	command := normalizeVoiceCommand(text)
	r.reset.mu.Lock()
	pending := time.Now().Before(r.reset.expires)
	if pending {
		r.reset.expires = time.Time{}
		if isResetConfirmation(command) {
			r.reset.mu.Unlock()
			if err := r.sessions.StartNew(); err != nil {
				slog.Warn("could not start new Hermes session", "error", err)
				return "Не получилось начать новый разговор. Продолжаю прежний.", true
			}
			return "Начинаю новый разговор. Предыдущий останется в истории Hermes.", true
		}
		if command == "нет" || command == "не надо" || command == "отмена" {
			r.reset.mu.Unlock()
			return "Хорошо, оставляю текущий разговор.", true
		}
	}
	if isResetIntent(command) {
		r.reset.expires = time.Now().Add(30 * time.Second)
		r.reset.mu.Unlock()
		return "Предыдущий разговор останется в истории Hermes. Скажи «да, начни новый разговор», чтобы продолжить без его контекста.", true
	}
	r.reset.mu.Unlock()
	return "", false
}

func normalizeVoiceCommand(text string) string {
	return strings.Trim(strings.ToLower(strings.Join(strings.Fields(text), " ")), " .,!?:;\t\n\r")
}

func isResetIntent(command string) bool {
	switch command {
	case "новый разговор", "начни новый разговор", "сбрось контекст":
		return true
	default:
		return false
	}
}

func isResetConfirmation(command string) bool {
	switch command {
	case "да, начни новый разговор", "да начни новый разговор", "подтверждаю, новый разговор":
		return true
	default:
		return false
	}
}

func stopIntent(text string) bool {
	switch normalizeVoiceCommand(text) {
	case "стоп", "останови", "останови ответ", "останови запрос", "прекрати", "хватит":
		return true
	default:
		return false
	}
}
