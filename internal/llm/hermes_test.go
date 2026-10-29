package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCompleteSentences(t *testing.T) {
	var pending string
	got := completeSentences(&pending, "Ответ: 3.5 метра. Следующая фраза")
	if want := []string{"Ответ: 3.5 метра."}; !reflect.DeepEqual(got, want) {
		t.Fatalf("completeSentences() = %#v, want %#v", got, want)
	}
	if pending != " Следующая фраза" {
		t.Fatalf("pending = %q, want remaining text", pending)
	}
	got = completeSentences(&pending, " готова!")
	if want := []string{"Следующая фраза готова!"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("completeSentences() = %#v, want %#v", got, want)
	}
}

func TestRunEventTextAcceptsTypedNestedDelta(t *testing.T) {
	got := runEventText("", []byte(`{"type":"message.delta","payload":{"delta":{"text":"Готово."}}}`))
	if got != "Готово." {
		t.Fatalf("runEventText() = %q, want sentence delta", got)
	}
	if got := runEventText("tool.completed", []byte(`{"text":"do not speak this"}`)); got != "" {
		t.Fatalf("tool event text = %q, want it ignored", got)
	}
}

func TestReadRunEventsForwardsAssistantSentenceDeltasOnly(t *testing.T) {
	client := &HermesClient{Model: "hermes-agent", BaseURL: "https://hermes.test/v1", APIKey: "test-key", HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/runs/run_test/events" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("unexpected authorization header")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(": keepalive\n\nevent: tool.started\ndata: {\"tool\":\"search\"}\n\nevent: message.interim\ndata: {\"text\":\"thinking\"}\n\nevent: message.delta\ndata: {\"text\":\"Первая фраза.\"}\n\n")),
			Request:    r,
		}, nil
	})}}
	var got []string
	client.readRunEvents(t.Context(), "run_test", func(sentence string) { got = append(got, sentence) })
	if want := []string{"Первая фраза."}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sentences = %#v, want %#v", got, want)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCompleteVoiceTurnUsesConfiguredHermesRun(t *testing.T) {
	t.Parallel()
	client := &HermesClient{
		Model: "hermes-agent", BaseURL: "https://hermes.test/v1", APIKey: "test-key",
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
				t.Errorf("unexpected authorization header")
			}
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
				var body struct {
					Input        string `json:"input"`
					Instructions string `json:"instructions"`
					Model        string `json:"model"`
					SessionID    string `json:"session_id"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode create run: %v", err)
				}
				if body.Input != "Сколько будет два плюс два?" || body.Instructions != "Отвечай кратко." || body.Model != "hermes-agent" || body.SessionID != "station-session" {
					t.Errorf("unexpected run request: %+v", body)
				}
				return jsonResponse(r, `{"run_id":"run_test"}`), nil
			case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/run_test/events":
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("event: message.delta\ndata: {\"text\":\"Четыре.\"}\n\n")), Request: r}, nil
			case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/run_test":
				return jsonResponse(r, `{"status":"completed","output":"Четыре."}`), nil
			default:
				t.Errorf("unexpected Hermes request: %s %s", r.Method, r.URL.Path)
				return nil, io.EOF
			}
		})},
	}
	sentences := make(chan string, 2)
	answer, err := client.CompleteVoiceTurn(t.Context(), "station-session", []Message{{Role: "system", Content: "Отвечай кратко."}, {Role: "user", Content: "Сколько будет два плюс два?"}}, func(sentence string) { sentences <- sentence })
	if err != nil {
		t.Fatalf("CompleteVoiceTurn() error = %v", err)
	}
	if answer != "Четыре." {
		t.Fatalf("answer = %q, want Hermes final output", answer)
	}
	select {
	case sentence := <-sentences:
		if sentence != "Четыре." {
			t.Fatalf("streamed sentence = %q", sentence)
		}
	default:
		t.Fatal("Hermes sentence delta was not forwarded")
	}
}

func jsonResponse(r *http.Request, body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

func TestStopActiveRunInterruptsHermesRun(t *testing.T) {
	var stopped atomic.Bool
	pollStarted := make(chan struct{}, 1)
	stopCalled := make(chan struct{}, 1)
	client := &HermesClient{
		Model: "hermes-agent", BaseURL: "https://hermes.test/v1", APIKey: "test-key",
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
				return jsonResponse(r, `{"run_id":"run_stop"}`), nil
			case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/run_stop":
				select {
				case pollStarted <- struct{}{}:
				default:
				}
				if stopped.Load() {
					return jsonResponse(r, `{"status":"cancelled"}`), nil
				}
				return jsonResponse(r, `{"status":"running"}`), nil
			case r.Method == http.MethodPost && r.URL.Path == "/v1/runs/run_stop/stop":
				stopped.Store(true)
				stopCalled <- struct{}{}
				return jsonResponse(r, `{"status":"stopping"}`), nil
			default:
				t.Errorf("unexpected Hermes request: %s %s", r.Method, r.URL.Path)
				return nil, io.EOF
			}
		})},
	}
	done := make(chan error, 1)
	go func() {
		_, err := client.CompleteVoiceTurn(t.Context(), "station-session", []Message{{Role: "system", Content: "short"}, {Role: "user", Content: "long task"}}, nil)
		done <- err
	}()
	<-pollStarted
	stoppedRun, err := client.StopActiveRun(t.Context())
	if err != nil || !stoppedRun {
		t.Fatalf("StopActiveRun() = %v, %v", stoppedRun, err)
	}
	select {
	case <-stopCalled:
	case <-time.After(time.Second):
		t.Fatal("Hermes stop endpoint was not called")
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrRunInterrupted) {
			t.Fatalf("run error = %v, want ErrRunInterrupted", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not reach cancelled status")
	}
}

func TestContextCancellationStopsHermesRun(t *testing.T) {
	stopCalled := make(chan struct{}, 1)
	pollStarted := make(chan struct{}, 1)
	client := &HermesClient{
		Model: "hermes-agent", BaseURL: "https://hermes.test/v1", APIKey: "test-key",
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
				return jsonResponse(r, `{"run_id":"run_cancel"}`), nil
			case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/run_cancel":
				select {
				case pollStarted <- struct{}{}:
				default:
				}
				<-r.Context().Done()
				return nil, r.Context().Err()
			case r.Method == http.MethodPost && r.URL.Path == "/v1/runs/run_cancel/stop":
				stopCalled <- struct{}{}
				return jsonResponse(r, `{"status":"stopping"}`), nil
			default:
				t.Errorf("unexpected Hermes request: %s %s", r.Method, r.URL.Path)
				return nil, io.EOF
			}
		})},
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := client.CompleteVoiceTurn(ctx, "station-session", []Message{{Role: "system", Content: "short"}, {Role: "user", Content: "long task"}}, nil)
		done <- err
	}()
	<-pollStarted
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled run did not return")
	}
	select {
	case <-stopCalled:
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not call Hermes stop endpoint")
	}
}
