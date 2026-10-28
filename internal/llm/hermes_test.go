package llm

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
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

func TestCompleteDialogueStreamUsesConfiguredHermesRun(t *testing.T) {
	t.Parallel()
	client := &HermesClient{
		Model: "hermes-agent", BaseURL: "https://hermes.test/v1", APIKey: "test-key", SessionID: "station-session",
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
	answer, err := client.CompleteDialogueStream(t.Context(), []Message{{Role: "system", Content: "Отвечай кратко."}, {Role: "user", Content: "Сколько будет два плюс два?"}}, func(sentence string) { sentences <- sentence })
	if err != nil {
		t.Fatalf("CompleteDialogueStream() error = %v", err)
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
