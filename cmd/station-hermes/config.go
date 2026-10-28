package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strings"
)

type config struct {
	Listen      string `json:"listen"`
	Upstream    string `json:"upstream"`
	HermesURL   string `json:"hermes_base_url"`
	Model       string `json:"model"`
	SessionID   string `json:"session_id"`
	APIKeyFile  string `json:"api_key_file"`
	VoicePrompt string `json:"voice_prompt"`
}

func loadConfig(path string) (config, error) {
	cfg := config{Listen: "127.0.0.1:8443", Upstream: "wss://uniproxy.alice.yandex.net/uni.ws"}
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Listen == "" || cfg.Upstream == "" || cfg.HermesURL == "" || cfg.Model == "" || cfg.SessionID == "" || cfg.APIKeyFile == "" || strings.TrimSpace(cfg.VoicePrompt) == "" {
		return cfg, errors.New("listen, upstream, hermes_base_url, model, session_id, api_key_file and voice_prompt are required")
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
