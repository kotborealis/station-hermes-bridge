package llm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	stdjson "encoding/json"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// HermesClient talks to Hermes Runs, which owns the persistent conversation.
type HermesClient struct {
	Model     string
	BaseURL   string
	APIKey    string
	SessionID string
	HTTP      *http.Client

	mu         sync.Mutex
	approvalMu sync.Mutex
	pending    *hermesApproval
}

type hermesApproval struct {
	RunID      string
	RequestID  string
	Generation int
	Label      string
	Expires    time.Time
}

type hermesRun struct {
	RunID           string          `json:"run_id"`
	Status          string          `json:"status"`
	Output          string          `json:"output"`
	PendingControls []hermesControl `json:"pending_controls"`
}

type hermesControl struct {
	RequestID           string `json:"request_id"`
	PromptID            string `json:"prompt_id"`
	ExecutionGeneration int    `json:"execution_generation"`
	Tool                string `json:"tool"`
	ToolName            string `json:"tool_name"`
	Title               string `json:"title"`
	Description         string `json:"description"`
}

// CompleteDialogueStream preserves Runs API status as the source of truth while
// forwarding complete assistant sentences from the resumable events stream.
func (c *HermesClient) CompleteDialogueStream(ctx context.Context, messages []Message, onSentence func(string)) (string, error) {
	if c.Model == "" {
		return "", errors.New("Hermes model is not configured")
	}
	return c.completeVoice(ctx, messages, onSentence)
}

func (c *HermesClient) completeVoice(ctx context.Context, messages []Message, onSentence func(string)) (string, error) {
	userText, instructions := lastUserAndInstructions(messages)
	if userText == "" {
		return "", errors.New("Hermes request has no user text")
	}
	if c.SessionID == "" {
		return "", errors.New("Hermes session ID is not configured")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	var created struct {
		RunID string `json:"run_id"`
	}
	key, err := hermesIdempotencyKey()
	if err != nil {
		return "", errors.New("cannot create Hermes request identity")
	}
	body := map[string]any{
		"input": userText, "session_id": c.SessionID,
		"instructions": instructions, "model": c.Model,
	}
	if err := c.request(ctx, http.MethodPost, "/runs", body, &created, "Idempotency-Key", key); err != nil {
		return "", err
	}
	if created.RunID == "" {
		return "", errors.New("Hermes did not return a run ID")
	}
	if onSentence != nil {
		eventCtx, stopEvents := context.WithCancel(ctx)
		eventsDone := make(chan struct{})
		go func() {
			defer close(eventsDone)
			c.readRunEvents(eventCtx, created.RunID, onSentence)
		}()
		answer, waitErr := c.waitForRun(ctx, created.RunID)
		stopEvents()
		<-eventsDone
		return answer, waitErr
	}
	return c.waitForRun(ctx, created.RunID)
}

func (c *HermesClient) readRunEvents(ctx context.Context, runID string, onSentence func(string)) {
	path := strings.TrimRight(c.BaseURL, "/") + "/runs/" + url.PathEscape(runID) + "/events"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Accept", "text/event-stream")
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	event := ""
	var data strings.Builder
	var pendingText string
	var eventNames []string
	textEvents := 0
	sentences := 0
	flush := func() {
		if data.Len() > 0 {
			if delta := runEventText(event, []byte(data.String())); delta != "" {
				textEvents++
				for _, sentence := range completeSentences(&pendingText, delta) {
					sentences++
					onSentence(sentence)
				}
			}
		}
		event = ""
		data.Reset()
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, ":") { // SSE keepalive comment
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			event = value
			if slices.Contains([]string{"message.delta", "assistant.delta", "message.complete", "assistant.completed"}, value) && !slices.Contains(eventNames, value) {
				eventNames = append(eventNames, value)
			}
		case "data":
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(value)
		}
	}
	flush()
	if sentences == 0 {
		slog.Info("hermes_run_sse_summary", "message_delta_seen", slices.Contains(eventNames, "message.delta"), "assistant_text_events", textEvents, "sentences", sentences)
	}
}

func runEventText(event string, raw []byte) string {
	var payload map[string]stdjson.RawMessage
	if stdjson.Unmarshal(raw, &payload) != nil {
		return ""
	}
	if event == "" {
		for _, key := range []string{"type", "event", "kind"} {
			if value, ok := payload[key]; ok {
				_ = stdjson.Unmarshal(value, &event)
				if event != "" {
					break
				}
			}
		}
	}
	switch event {
	case "message.delta", "assistant.delta", "message.complete", "assistant.completed":
	default:
		return ""
	}
	for _, key := range []string{"text", "delta", "content"} {
		if text := eventTextValue(payload[key]); text != "" {
			return text
		}
	}
	for _, key := range []string{"payload", "data", "message"} {
		var nested map[string]stdjson.RawMessage
		if stdjson.Unmarshal(payload[key], &nested) != nil {
			continue
		}
		for _, nestedKey := range []string{"text", "delta", "content"} {
			if text := eventTextValue(nested[nestedKey]); text != "" {
				return text
			}
		}
	}
	return ""
}

func eventTextValue(raw stdjson.RawMessage) string {
	var value string
	if stdjson.Unmarshal(raw, &value) == nil {
		return value
	}
	var nested map[string]stdjson.RawMessage
	if stdjson.Unmarshal(raw, &nested) == nil {
		for _, key := range []string{"text", "content"} {
			if text := eventTextValue(nested[key]); text != "" {
				return text
			}
		}
	}
	return ""
}

func completeSentences(pending *string, delta string) []string {
	*pending += delta
	var out []string
	start := 0
	for i, r := range *pending {
		if r != '.' && r != '!' && r != '?' && r != '…' && r != '\n' {
			continue
		}
		// Avoid treating a decimal point as a sentence boundary.
		if r == '.' && i > 0 && i+1 < len(*pending) && (*pending)[i-1] >= '0' && (*pending)[i-1] <= '9' && (*pending)[i+1] >= '0' && (*pending)[i+1] <= '9' {
			continue
		}
		end := i + len(string(r))
		part := strings.TrimSpace((*pending)[start:end])
		if part != "" {
			out = append(out, part)
		}
		start = end
	}
	if start > 0 {
		*pending = (*pending)[start:]
	}
	return out
}

func lastUserAndInstructions(messages []Message) (string, string) {
	var user string
	var instructions strings.Builder
	for _, message := range messages {
		if message.Role == "system" && message.Content != "" {
			if instructions.Len() > 0 {
				instructions.WriteString("\n\n")
			}
			instructions.WriteString(message.Content)
		}
		if message.Role == "user" {
			user = strings.TrimSpace(message.Content)
		}
	}
	return user, instructions.String()
}

func hermesIdempotencyKey() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "quasar-" + hex.EncodeToString(raw[:]), nil
}

func (c *HermesClient) waitForRun(ctx context.Context, runID string) (string, error) {
	path := "/runs/" + url.PathEscape(runID)
	for {
		var run hermesRun
		if err := c.request(ctx, http.MethodGet, path, nil, &run); err != nil {
			return "", err
		}
		switch run.Status {
		case "completed":
			answer := strings.TrimSpace(run.Output)
			if answer == "" {
				return "", errors.New("Hermes completed without a text answer")
			}
			return answer, nil
		case "waiting_for_approval":
			if len(run.PendingControls) == 0 {
				return "", errors.New("Hermes is waiting for approval but returned no approval identity")
			}
			control := run.PendingControls[0]
			requestID := control.RequestID
			if requestID == "" {
				requestID = control.PromptID
			}
			if requestID == "" || control.ExecutionGeneration < 1 {
				return "", errors.New("Hermes returned an incomplete approval identity")
			}
			label := strings.TrimSpace(control.Tool)
			if label == "" {
				label = strings.TrimSpace(control.ToolName)
			}
			if label == "" {
				label = strings.TrimSpace(control.Title)
			}
			if label == "" {
				label = "запрос инструмента"
			}
			label = shortApprovalLabel(label)
			c.approvalMu.Lock()
			c.pending = &hermesApproval{RunID: runID, RequestID: requestID, Generation: control.ExecutionGeneration, Label: label, Expires: time.Now().Add(60 * time.Second)}
			c.approvalMu.Unlock()
			return "Hermes хочет выполнить действие: " + label + ". Скажи «разрешаю» для одного запуска или «отмена».", nil
		case "failed", "cancelled", "interrupted", "unknown":
			return "", fmt.Errorf("Hermes run ended with status %s", run.Status)
		case "", "started", "queued", "running", "stopping":
		default:
			return "", errors.New("Hermes returned an unknown run status")
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("Hermes run timed out: %w", ctx.Err())
		case <-time.After(400 * time.Millisecond):
		}
	}
}

func shortApprovalLabel(value string) string {
	value = strings.Join(strings.Fields(strings.ReplaceAll(strings.ReplaceAll(value, "\n", " "), "\r", " ")), " ")
	runes := []rune(value)
	if len(runes) > 100 {
		value = string(runes[:100])
	}
	return value
}

func (c *HermesClient) HandleVoiceApproval(ctx context.Context, text string) (string, bool, error) {
	c.approvalMu.Lock()
	pending := c.pending
	if pending == nil {
		c.approvalMu.Unlock()
		return "", false, nil
	}
	if time.Now().After(pending.Expires) {
		c.pending = nil
		c.approvalMu.Unlock()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = c.replyApproval(ctx, pending, "deny")
		}()
		return "Срок подтверждения истёк, запрос отклонён.", true, nil
	}
	decision := approvalDecision(text)
	if decision == "" {
		c.approvalMu.Unlock()
		return "Есть ожидающий запрос Hermes: " + pending.Label + ". Скажи «разрешаю» для одного запуска или «отмена».", true, nil
	}
	c.pending = nil
	c.approvalMu.Unlock()

	choice := "deny"
	if decision == "allow" {
		choice = "once"
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := c.replyApproval(ctx, pending, choice); err != nil {
		return "Не удалось отправить решение Hermes.", true, err
	}
	answer, err := c.waitForRun(ctx, pending.RunID)
	if err != nil {
		return "Решение отправлено, но Hermes не вернул итоговый ответ.", true, err
	}
	return answer, true, nil
}

func (c *HermesClient) replyApproval(ctx context.Context, pending *hermesApproval, choice string) error {
	path := "/runs/" + url.PathEscape(pending.RunID) + "/approval"
	body := map[string]any{
		"request_id":           pending.RequestID,
		"execution_generation": pending.Generation,
		"choice":               choice,
	}
	return c.request(ctx, http.MethodPost, path, body, nil)
}

func (c *HermesClient) request(ctx context.Context, method, path string, body any, out any, headers ...string) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return errors.New("cannot encode Hermes request")
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, reader)
	if err != nil {
		return errors.New("invalid Hermes endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if len(headers) == 2 {
		req.Header.Set(headers[0], headers[1])
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("Hermes API transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Hermes API returned HTTP %d", resp.StatusCode)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		return errors.New("cannot read Hermes API response")
	}
	if err := json.Unmarshal(data, out); err != nil {
		return errors.New("invalid Hermes API response")
	}
	return nil
}
