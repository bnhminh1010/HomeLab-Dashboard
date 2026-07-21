package nodeagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/bnhminh1010/homelab-dashboard/internal/nodes"
)

const credentialFileLimit = 64 << 10

// Credentials are issued once by the dashboard enrollment endpoint. The
// credential itself is never logged and is stored only in the agent state file.
type Credentials struct {
	ServerURL       string `json:"serverUrl"`
	WebsocketURL    string `json:"websocketUrl"`
	NodeID          string `json:"nodeId"`
	Credential      string `json:"credential"`
	ProtocolVersion int    `json:"protocolVersion"`
}

func (credentials Credentials) Validate() error {
	server, err := validateServerURL(credentials.ServerURL)
	if err != nil {
		return err
	}
	_, err = resolveWebsocketURL(server, credentials.WebsocketURL)
	if err != nil {
		return err
	}
	if strings.TrimSpace(credentials.NodeID) == "" || len(credentials.NodeID) > 128 {
		return errors.New("node agent: node ID is required")
	}
	if strings.TrimSpace(credentials.Credential) == "" || len(credentials.Credential) > 512 {
		return errors.New("node agent: credential is required")
	}
	if credentials.ProtocolVersion != nodes.ProtocolVersion {
		return fmt.Errorf("node agent: unsupported protocol version %d", credentials.ProtocolVersion)
	}
	return nil
}

// SaveCredentials writes a complete file to the target directory and renames
// it into place. A crash cannot leave a partially written credential file.
func SaveCredentials(path string, credentials Credentials) error {
	if err := credentials.Validate(); err != nil {
		return err
	}
	if !filepath.IsAbs(path) {
		return errors.New("node agent: credential path must be absolute")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("node agent: create credential directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("node agent: protect credential directory: %w", err)
	}

	temporary, err := os.CreateTemp(directory, ".credentials-*")
	if err != nil {
		return fmt.Errorf("node agent: create credential file: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("node agent: protect credential file: %w", err)
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(credentials); err != nil {
		return fmt.Errorf("node agent: encode credentials: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("node agent: sync credentials: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("node agent: close credentials: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("node agent: install credentials: %w", err)
	}
	removeTemporary = false
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("node agent: protect installed credentials: %w", err)
	}
	if directoryHandle, err := os.Open(directory); err == nil {
		_ = directoryHandle.Sync()
		_ = directoryHandle.Close()
	}
	return nil
}

func LoadCredentials(path string) (Credentials, error) {
	if !filepath.IsAbs(path) {
		return Credentials{}, errors.New("node agent: credential path must be absolute")
	}
	file, err := os.Open(path)
	if err != nil {
		return Credentials{}, fmt.Errorf("node agent: open credentials: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Credentials{}, fmt.Errorf("node agent: inspect credentials: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Credentials{}, errors.New("node agent: credential path must be a regular file")
	}
	if info.Size() > credentialFileLimit {
		return Credentials{}, errors.New("node agent: credential file exceeds 64 KiB")
	}
	if info.Mode().Perm() != 0o600 {
		return Credentials{}, fmt.Errorf("node agent: credential file permissions must be 0600, got %04o", info.Mode().Perm())
	}
	decoder := json.NewDecoder(io.LimitReader(file, credentialFileLimit+1))
	decoder.DisallowUnknownFields()
	var credentials Credentials
	if err := decoder.Decode(&credentials); err != nil {
		return Credentials{}, fmt.Errorf("node agent: decode credentials: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Credentials{}, errors.New("node agent: credential file must contain one JSON value")
	}
	if err := credentials.Validate(); err != nil {
		return Credentials{}, err
	}
	return credentials, nil
}

func validateServerURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("node agent: server URL must be an absolute URL without credentials, query, or fragment")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	switch parsed.Scheme {
	case "https":
		return parsed, nil
	case "http":
		if isLoopbackHost(parsed.Hostname()) {
			return parsed, nil
		}
	}
	return nil, errors.New("node agent: HTTPS is required except for loopback development")
}

func resolveWebsocketURL(server *url.URL, raw string) (*url.URL, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		value = "/ws/v1/agents/connect"
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, errors.New("node agent: invalid websocket URL")
	}
	if !parsed.IsAbs() {
		parsed = server.ResolveReference(parsed)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Host == "" {
		return nil, errors.New("node agent: websocket URL must not contain credentials, query, or fragment")
	}
	if !strings.EqualFold(parsed.Host, server.Host) {
		return nil, errors.New("node agent: websocket URL must use the dashboard host")
	}
	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	}
	if parsed.Scheme == "wss" {
		return parsed, nil
	}
	if parsed.Scheme == "ws" && isLoopbackHost(parsed.Hostname()) {
		return parsed, nil
	}
	return nil, errors.New("node agent: secure WSS is required except for loopback development")
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
