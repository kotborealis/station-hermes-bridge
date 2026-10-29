package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"stationhermes/internal/bridge"
	"stationhermes/internal/llm"
)

func main() {
	configPath := flag.String("config", "/data/opt/station-hermes/config.json", "configuration file")
	tlsCheck := flag.String("check-hermes-tls", "", "verify HTTPS and certificate trust for a URL, without sending credentials")
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
	apiKey := strings.TrimSpace(string(key))
	statePath := *configPath + ".session-id"
	sessions, err := openSessionStore(statePath, cfg.SessionID)
	if err != nil {
		slog.Error("Hermes session state rejected", "error", err)
		os.Exit(2)
	}
	hermes := &llm.HermesClient{Model: cfg.Model, BaseURL: cfg.HermesURL, APIKey: apiKey}
	p := &bridge.Proxy{Upstream: cfg.Upstream}
	voice := stationResolver{hermes: hermes, sessions: sessions, prompt: cfg.VoicePrompt, reset: &resetConfirmation{}}
	p.NewSession = func(ctx context.Context, up, down bridge.Sender) bridge.Session {
		n := bridge.NewNative(ctx, up, down, voice)
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
