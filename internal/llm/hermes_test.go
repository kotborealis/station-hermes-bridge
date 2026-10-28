package llm

import (
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
	client := &HermesClient{BaseURL: "https://hermes.test/v1", APIKey: "test-key", HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
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
