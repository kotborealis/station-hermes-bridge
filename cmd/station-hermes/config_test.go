package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigRequiresExplicitHermesEndpointAndPrompt(t *testing.T) {
	t.Parallel()
	base := `{"listen":"127.0.0.1:8443","upstream":"wss://station.example/uni.ws","hermes_base_url":"https://hermes.example/v1","model":"hermes-agent","session_id":"voice","api_key_file":"/run/key","voice_prompt":"Отвечай кратко."}`
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err != nil {
		t.Fatalf("valid configuration rejected: %v", err)
	}

	for _, missing := range []string{`"hermes_base_url":"https://hermes.example/v1",`, `"voice_prompt":"Отвечай кратко."`} {
		config := strings.Replace(base, missing, "", 1)
		if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadConfig(path); err == nil {
			t.Fatalf("configuration without %s accepted", missing)
		}
	}
}
