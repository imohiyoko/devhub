package storage

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const agentTokenFile = "ai-api-token"

// AgentToken returns the persistent credential used by same-user local agents.
// The token is separate from the browser session token: agents read it from a
// owner-private settings file, while child processes never inherit either
// credential (0600 on Unix; the user profile ACL is the Windows boundary).
func (s *Store) AgentToken() (string, error) {
	path := filepath.Join(s.settingsDir, agentTokenFile)
	if token, err := readAgentToken(path); err == nil {
		_ = os.Chmod(path, 0o600)
		return token, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate ai-api token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) { // another server using this home won the creation race
		return readAgentToken(path)
	}
	if err != nil {
		return "", fmt.Errorf("create ai-api token: %w", err)
	}
	if _, err := f.WriteString(token + "\n"); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("write ai-api token: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close ai-api token: %w", err)
	}
	return token, nil
}

// ReadAgentToken reads the credential for an already-running server without
// opening its database. CLI status/stop use this to authenticate /ai-api/info.
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
		return "", fmt.Errorf("invalid ai-api token file %s", path)
	}
	return token, nil
}
