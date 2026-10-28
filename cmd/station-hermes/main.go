package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"stationhermes/internal/bridge"
	"stationhermes/internal/llm"
)

type config struct {
	Listen     string `json:"listen"`
	Upstream   string `json:"upstream"`
	HermesURL  string `json:"hermes_base_url"`
	Model      string `json:"model"`
	SessionID  string `json:"session_id"`
	APIKeyFile string `json:"api_key_file"`
}

type stationResolver struct {
	engine *bridge.Engine
}

func (r stationResolver) Resolve(ctx context.Context, text string) (bridge.Resolution, error) {
	return r.engine.Resolve(ctx, text)
}

func (r stationResolver) Decide(ctx context.Context, text string) bridge.RouteDecision {
	switch strings.Trim(strings.ToLower(strings.TrimSpace(text)), " .,!?:;\t\n\r") {
	case "разрешаю", "подтверждаю", "разреши", "да разрешаю", "отмена", "отменяю", "не разрешаю", "отклоняю", "нет отмена":
		return bridge.RouteDecision{Route: "llm", Source: "voice_approval"}
	default:
		return r.engine.Decide(ctx, text)
	}
}

func (r stationResolver) ResolveDecision(ctx context.Context, text string, decision bridge.RouteDecision) (bridge.Resolution, error) {
	return r.engine.ResolveDecision(ctx, text, decision)
}

func main() {
	configPath := flag.String("config", "/data/opt/station-hermes/config.json", "configuration file")
	tlsCheck := flag.String("check-hermes-tls", "", "verify HTTPS and certificate trust for a URL, without sending credentials")
	apiCheck := flag.Bool("check-hermes-api", false, "send a harmless one-word diagnostic request to Hermes")
	streamCheck := flag.Bool("check-hermes-stream", false, "check a Hermes Runs reply and its sentence SSE events")
	endpointAction := flag.String("speaker-endpoint", "", "apply or rollback the loopback UniWS endpoint")
	endpointConfig := flag.String("speaker-config", "/system/vendor/quasar/quasar.cfg", "Quasar configuration path")
	endpointBackup := flag.String("speaker-backup", "/data/opt/station-hermes/endpoint-backup.json", "endpoint backup path")
	flag.Parse()
	if *endpointAction != "" {
		if err := setSpeakerEndpoint(*endpointConfig, *endpointBackup, "ws://127.0.0.1:8443/uni.ws", *endpointAction); err != nil {
			slog.Error("speaker endpoint update rejected", "error", err)
			os.Exit(1)
		}
		fmt.Println("speaker endpoint updated; restart the voice service to apply")
		return
	}
	if *tlsCheck != "" {
		if err := checkHTTPS(*tlsCheck); err != nil {
			slog.Error("Hermes HTTPS check failed", "error", err)
			os.Exit(1)
		}
		return
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		slog.Error("configuration rejected", "error", err)
		os.Exit(2)
	}
	key, err := os.ReadFile(cfg.APIKeyFile)
	if err != nil || strings.TrimSpace(string(key)) == "" {
		slog.Error("Hermes API key is missing")
		os.Exit(2)
	}
	api := &llm.Client{BaseURL: cfg.HermesURL, Model: cfg.Model, APIKey: strings.TrimSpace(string(key)), API: "chat_completions", MaxOutputTokens: 512}
	hermes := &llm.HermesClient{API: api, BaseURL: cfg.HermesURL, APIKey: api.APIKey, SessionID: cfg.SessionID}
	if *apiCheck {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if _, err := api.Complete(ctx, []llm.Message{{Role: "user", Content: "Ответь одним словом: связь"}}, false); err != nil {
			slog.Error("Hermes API check failed", "error", err)
			os.Exit(1)
		}
		fmt.Println("Hermes API OK")
		return
	}
	if *streamCheck {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		// Keep the smoke-check turn outside the user's persistent conversation.
		hermes.SessionID = cfg.SessionID + "-stream-check"
		streamed := false
		messages := []llm.Message{
			{Role: "system", Content: "Ты голосовой помощник пользователя. Отвечай коротко и по-русски."},
			{Role: "user", Content: "Ответь одной короткой фразой с точкой: проверка потоковой передачи работает."},
		}
		if _, _, err := hermes.CompleteDialogueStream(ctx, messages, false, func(string) { streamed = true }); err != nil {
			slog.Error("Hermes Runs stream check failed", "error", err)
			os.Exit(1)
		}
		if !streamed {
			slog.Error("Hermes run completed without a sentence SSE event")
			os.Exit(1)
		}
		fmt.Println("Hermes Runs API and sentence SSE OK")
		return
	}
	engine := &bridge.Engine{LLM: hermes}
	routing := bridge.DefaultRoutingConfig()
	// This device build replaces the conversational assistant: every recognized
	// text request goes to Hermes instead of being classified back to Yandex.
	routing.DefaultRoute = "llm"
	routing.Rules = nil
	if err := engine.SetRouting(routing); err != nil {
		slog.Error("routing configuration rejected", "error", err)
		os.Exit(2)
	}
	p := &bridge.Proxy{Upstream: cfg.Upstream}
	voice := stationResolver{engine: engine}
	p.NewSession = func(ctx context.Context, up, down bridge.Sender) bridge.Session {
		n := bridge.NewNative(ctx, up, down, voice)
		n.GateText = false
		n.ShouldListen = true
		n.ProgressAfter = 2 * time.Second
		n.ProgressInterval = 4 * time.Second
		return n
	}
	mux := http.NewServeMux()
	mux.Handle("/uni.ws", p)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "mode": "station-to-hermes", "upstream": cfg.Upstream})
	})
	srv := &http.Server{Addr: cfg.Listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	slog.Info("station-hermes bridge starting", "listen", cfg.Listen, "upstream", cfg.Upstream, "hermes", cfg.HermesURL, "model", cfg.Model)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("HTTP server stopped", "error", err)
		os.Exit(1)
	}
}

func checkHTTPS(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return errors.New("URL must be an HTTPS URL with a hostname and no user info")
	}
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(u.String())
	if err != nil {
		return fmt.Errorf("HTTPS connection or certificate validation failed: %w", err)
	}
	defer resp.Body.Close()
	fmt.Printf("TLS OK; HTTP status %s\n", resp.Status)
	return nil
}

func loadConfig(path string) (config, error) {
	cfg := config{Listen: "127.0.0.1:8443", Upstream: "wss://uniproxy.alice.yandex.net/uni.ws", HermesURL: "https://hermes.home.awooo.ru/v1", Model: "hermes-agent"}
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Listen == "" || cfg.Upstream == "" || cfg.HermesURL == "" || cfg.Model == "" || cfg.SessionID == "" || cfg.APIKeyFile == "" {
		return cfg, errors.New("listen, upstream, hermes_base_url, model, session_id and api_key_file are required")
	}
	host, _, err := net.SplitHostPort(cfg.Listen)
	ip, parseErr := netip.ParseAddr(host)
	if err != nil || parseErr != nil || !ip.IsLoopback() {
		return cfg, errors.New("listen must be a loopback IP and port")
	}
	upstream, err := url.Parse(cfg.Upstream)
	if err != nil || upstream.Scheme != "wss" || upstream.Hostname() == "" || upstream.User != nil {
		return cfg, errors.New("upstream must be a wss URL without user info")
	}
	hermes, err := url.Parse(cfg.HermesURL)
	if err != nil || hermes.Scheme != "https" || hermes.Hostname() == "" || hermes.User != nil {
		return cfg, errors.New("hermes_base_url must be an HTTPS URL without user info")
	}
	return cfg, nil
}
