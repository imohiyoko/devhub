package storage

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
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

func TestAgentTokenRepairsPersistentlyMalformedFile(t *testing.T) {
	home := t.TempDir()
	st, err := Open(home, devhub.Assets)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	path := filepath.Join(home, "settings", agentTokenFile)
	if err := os.WriteFile(path, []byte("incomplete"), 0o600); err != nil {
		t.Fatal(err)
	}
	token, err := st.AgentToken()
	if err != nil {
		t.Fatal(err)
	}
	fromDisk, err := ReadAgentToken(home)
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || fromDisk != token {
		t.Fatalf("repaired token mismatch: returned=%q disk=%q", token, fromDisk)
	}
}

func TestConcurrentAgentTokenCreationConverges(t *testing.T) {
	home := t.TempDir()
	st, err := Open(home, devhub.Assets)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	const callers = 20
	tokens := make(chan string, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := st.AgentToken()
			if err != nil {
				errs <- err
				return
			}
			tokens <- token
		}()
	}
	wg.Wait()
	close(tokens)
	close(errs)
	for err := range errs {
		t.Errorf("AgentToken: %v", err)
	}
	var want string
	for token := range tokens {
		if want == "" {
			want = token
		}
		if token != want {
			t.Errorf("concurrent tokens differ: got %q want %q", token, want)
		}
	}
	if want == "" {
		t.Fatal("no token returned")
	}
	matches, err := filepath.Glob(filepath.Join(home, "settings", ".ai-api-token-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary token files remain: %v", matches)
	}
}
