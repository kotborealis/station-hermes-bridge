package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSessionStoreRotatesAndRestoresID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session-id")
	store, err := openSessionStore(path, "configured-session")
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Current(); got != "configured-session" {
		t.Fatalf("initial session ID = %q", got)
	}
	if err := store.StartNew(); err != nil {
		t.Fatal(err)
	}
	rotated := store.Current()
	if rotated == "configured-session" || len(rotated) != len("station-")+32 {
		t.Fatalf("new session ID = %q", rotated)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("session ID file mode = %04o, want 0600", info.Mode().Perm())
	}
	reloaded, err := openSessionStore(path, "configured-session")
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Current(); got != rotated {
		t.Fatalf("restored session ID = %q, want %q", got, rotated)
	}
}
