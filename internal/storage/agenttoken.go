package storage

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const agentTokenFile = "ai-api-token"

var errInvalidAgentToken = errors.New("invalid ai-api token")

// AgentToken returns the persistent credential used by same-user local agents.
// The token is separate from the browser session token: agents read it from a
// owner-private settings file, while child processes never inherit either
// credential (0600 on Unix; the user profile ACL is the Windows boundary).
func (s *Store) AgentToken() (string, error) {
	path := filepath.Join(s.settingsDir, agentTokenFile)
	if token, err := readAgentToken(path); err == nil {
		_ = os.Chmod(path, 0o600)
		return token, nil
	} else if !os.IsNotExist(err) && !errors.Is(err, errInvalidAgentToken) {
		return "", err
	}

	unlock, err := lockAgentTokenFile(filepath.Join(s.settingsDir, ".ai-api-token.lock"))
	if err != nil {
		return "", fmt.Errorf("lock ai-api token: %w", err)
	}
	defer unlock()

	// A competing process may have repaired or created the token while this
	// caller waited for the lock, so always re-check before generating one.
	if token, err := readAgentToken(path); err == nil {
		_ = os.Chmod(path, 0o600)
		return token, nil
	} else if !os.IsNotExist(err) && !errors.Is(err, errInvalidAgentToken) {
		return "", err
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate ai-api token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	// Write and close the complete credential under a private temporary name,
	// then atomically rename it while holding the cross-process token lock.
	// Competitors can never observe an empty or partial final file. Rename also
	// works on filesystems which do not support hard links.
	f, err := os.CreateTemp(s.settingsDir, ".ai-api-token-*")
	if err != nil {
		return "", fmt.Errorf("create temporary ai-api token: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("protect temporary ai-api token: %w", err)
	}
	if _, err := f.WriteString(token + "\n"); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("write ai-api token: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("sync ai-api token: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close ai-api token: %w", err)
	}
	// Windows cannot rename over an existing file. At this point any existing
	// token is known to be malformed and no competing writer can touch it while
	// the lock is held, so removing it is safe and enables self-repair.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("remove invalid ai-api token: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", fmt.Errorf("publish ai-api token: %w", err)
	}
	return token, nil
}

// ReadAgentToken reads the credential for an already-running server without
// opening its database. CLI status/stop use it only to verify signed probe
// responses; they never send it to the listener.
func ReadAgentToken(home string) (string, error) {
	return readAgentToken(filepath.Join(home, "settings", agentTokenFile))
}

func readAgentToken(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		return "", fmt.Errorf("%w file %s", errInvalidAgentToken, path)
	}
	return token, nil
}
