package storage

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	devhub "github.com/imohiyoko/devhub"
)

func TestAgentTokenPersistsAndIsPrivate(t *testing.T) {
	home := t.TempDir()
	st, err := Open(home, devhub.Assets)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	first, err := st.AgentToken()
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.AgentToken()
	if err != nil {
		t.Fatal(err)
	}
	fromDisk, err := ReadAgentToken(home)
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || first != second || first != fromDisk {
		t.Fatalf("tokens differ: first=%q second=%q disk=%q", first, second, fromDisk)
	}
	if info, err := os.Stat(filepath.Join(home, "settings", agentTokenFile)); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("token mode = %o, want 600", info.Mode().Perm())
	}
}

func TestReadAgentTokenRejectsMalformedFile(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "settings")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, agentTokenFile), []byte("not-a-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAgentToken(home); err == nil {
		t.Fatal("malformed token was accepted")
	}
}
