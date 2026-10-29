package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type sessionStore struct {
	mu   sync.RWMutex
	path string
	id   string
}

func openSessionStore(path, configuredID string) (*sessionStore, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		id := strings.TrimSpace(string(data))
		if id == "" || len(id) > 256 {
			return nil, errors.New("stored Hermes session ID is invalid")
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("protect Hermes session ID file: %w", err)
		}
		return &sessionStore{path: path, id: id}, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read Hermes session ID: %w", err)
	}
	if configuredID == "" {
		return nil, errors.New("Hermes session ID is not configured")
	}
	return &sessionStore{path: path, id: configuredID}, nil
}

func (s *sessionStore) Current() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.id
}

func (s *sessionStore) StartNew() error {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return errors.New("cannot generate Hermes session ID")
	}
	id := "station-" + hex.EncodeToString(raw[:])

	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".session-id-*")
	if err != nil {
		return fmt.Errorf("create Hermes session ID file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect Hermes session ID file: %w", err)
	}
	if _, err := fmt.Fprintln(tmp, id); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write Hermes session ID file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync Hermes session ID file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close Hermes session ID file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("install Hermes session ID: %w", err)
	}
	s.id = id
	return nil
}
