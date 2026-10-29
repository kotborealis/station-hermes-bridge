package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"stationhermes/internal/llm"
)

func TestVoiceErrorExplainsCommonHermesFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"timeout", context.DeadlineExceeded, "Hermes не успел ответить. Попробуй ещё раз."},
		{"authentication", &llm.HTTPStatusError{StatusCode: 401}, "Hermes не принял токен доступа."},
		{"rate limit", &llm.HTTPStatusError{StatusCode: 429}, "Hermes временно ограничил запросы. Попробуй чуть позже."},
		{"server", &llm.HTTPStatusError{StatusCode: 503}, "Hermes сейчас недоступен. Попробуй чуть позже."},
		{"run failure", llm.ErrRunFailed, "Hermes не смог выполнить запрос. Попробуй переформулировать."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := voiceError(tc.err); got != tc.want {
				t.Fatalf("voiceError() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestVoiceResetRequiresConfirmationAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-id")
	sessions, err := openSessionStore(path, "original-session")
	if err != nil {
		t.Fatal(err)
	}
	resolver := stationResolver{sessions: sessions, reset: &resetConfirmation{}}

	answer, handled := resolver.handleReset("Начни новый разговор.")
	if !handled || answer == "" {
		t.Fatalf("reset intent not handled: %q, %v", answer, handled)
	}
	if got := sessions.Current(); got != "original-session" {
		t.Fatalf("session changed before confirmation: %q", got)
	}
	if answer, handled = resolver.handleReset("нет"); !handled || answer != "Хорошо, оставляю текущий разговор." {
		t.Fatalf("reset rejection = %q, %v", answer, handled)
	}
	if got := sessions.Current(); got != "original-session" {
		t.Fatalf("session changed after rejection: %q", got)
	}

	_, _ = resolver.handleReset("сбрось контекст")
	answer, handled = resolver.handleReset("да, начни новый разговор")
	if !handled || answer != "Начинаю новый разговор. Предыдущий останется в истории Hermes." {
		t.Fatalf("reset confirmation = %q, %v", answer, handled)
	}
	if got := sessions.Current(); got == "original-session" {
		t.Fatal("session ID did not change")
	}
	reloaded, err := openSessionStore(path, "original-session")
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Current(); got != sessions.Current() {
		t.Fatalf("reloaded session ID = %q, want %q", got, sessions.Current())
	}
}

func TestVoiceResetConfirmationExpires(t *testing.T) {
	resolver := stationResolver{reset: &resetConfirmation{}}
	_, _ = resolver.handleReset("новый разговор")
	resolver.reset.mu.Lock()
	resolver.reset.expires = resolver.reset.expires.Add(-31 * time.Second)
	resolver.reset.mu.Unlock()
	if _, handled := resolver.handleReset("да, начни новый разговор"); handled {
		t.Fatal("expired confirmation was accepted")
	}
}

func TestVoiceErrorDoesNotHideUnknownCause(t *testing.T) {
	got := voiceError(errors.New("opaque failure"))
	if got != "Не получилось получить ответ от Hermes." {
		t.Fatalf("voiceError() = %q", got)
	}
}
