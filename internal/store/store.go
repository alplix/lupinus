// Package store persists Lupinus's connection list and settings to a JSON
// file in the OS config directory, and keeps VNC passwords out of that file
// entirely — they live in the OS credential store (Windows Credential
// Manager / macOS Keychain / Linux Secret Service) via go-keyring, unlike
// the plaintext-file approach used by this author's earlier Wails apps.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/zalando/go-keyring"
)

const keyringService = "Lupinus"

// Connection is a saved connection's non-secret metadata. The password (if
// any) is stored separately in the OS keyring, keyed by ID.
type Connection struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol,omitempty"` // "vnc" | "rdp" — empty means "vnc" (pre-v0.2.0 saves)
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username,omitempty"` // RDP only
	ColorTag string `json:"colorTag,omitempty"`
	LastUsed string `json:"lastUsed,omitempty"` // RFC 3339, empty if never connected
}

type fileData struct {
	Connections []Connection `json:"connections"`
	Theme       string       `json:"theme"` // "light" | "dark" | "system"

	// TrustedCerts implements trust-on-first-use for RDP's TLS layer,
	// which almost always presents a self-signed certificate — there's no
	// CA chain to verify against, so instead the first certificate seen
	// for a given "host:port" is pinned, and every later connection must
	// match it exactly (same idea as SSH's known_hosts). Keyed by address,
	// value is the SHA-256 fingerprint of the leaf certificate, hex-encoded.
	TrustedCerts map[string]string `json:"trustedCerts,omitempty"`
}

// Store is safe for concurrent use.
type Store struct {
	mu   sync.Mutex
	path string
	data fileData
}

// Open loads (or initializes) the config file at
// "<UserConfigDir>/lupinus/config.json".
func Open() (*Store, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	dir = filepath.Join(dir, "lupinus")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	s := &Store{
		path: filepath.Join(dir, "config.json"),
		data: fileData{Theme: "system"},
	}

	if b, err := os.ReadFile(s.path); err == nil {
		if err := json.Unmarshal(b, &s.data); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	return s, nil
}

func (s *Store) persistLocked() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// List returns saved connections, most-recently-used first.
func (s *Store) List() []Connection {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Connection, len(s.data.Connections))
	copy(out, s.data.Connections)
	return out
}

// Save inserts or updates a connection. If conn.ID is empty a new UUID is
// assigned. If password is non-empty it replaces the stored credential;
// pass an empty password to leave an existing credential untouched.
func (s *Store) Save(conn Connection, password string) (Connection, error) {
	if conn.ID == "" {
		conn.ID = uuid.NewString()
	}

	s.mu.Lock()
	found := false
	for i, existing := range s.data.Connections {
		if existing.ID == conn.ID {
			s.data.Connections[i] = conn
			found = true
			break
		}
	}
	if !found {
		s.data.Connections = append(s.data.Connections, conn)
	}
	err := s.persistLocked()
	s.mu.Unlock()
	if err != nil {
		return Connection{}, err
	}

	if password != "" {
		if err := keyring.Set(keyringService, conn.ID, password); err != nil {
			return Connection{}, err
		}
	}

	return conn, nil
}

// TouchLastUsed stamps a connection's LastUsed time to now.
func (s *Store) TouchLastUsed(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range s.data.Connections {
		if c.ID == id {
			s.data.Connections[i].LastUsed = time.Now().UTC().Format(time.RFC3339)
			return s.persistLocked()
		}
	}
	return nil
}

// Delete removes a connection and its stored credential, if any.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	for i, c := range s.data.Connections {
		if c.ID == id {
			s.data.Connections = append(s.data.Connections[:i], s.data.Connections[i+1:]...)
			break
		}
	}
	err := s.persistLocked()
	s.mu.Unlock()
	if err != nil {
		return err
	}

	if err := keyring.Delete(keyringService, id); err != nil && err != keyring.ErrNotFound {
		return err
	}
	return nil
}

// Password returns the stored credential for a connection, or "" if none
// was set.
func (s *Store) Password(id string) (string, error) {
	pw, err := keyring.Get(keyringService, id)
	if err == keyring.ErrNotFound {
		return "", nil
	}
	return pw, err
}

func (s *Store) Theme() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Theme
}

func (s *Store) SetTheme(theme string) error {
	s.mu.Lock()
	s.data.Theme = theme
	err := s.persistLocked()
	s.mu.Unlock()
	return err
}

// VerifyOrTrustCertificate implements trust-on-first-use: the first
// fingerprint seen for addr is pinned and persisted; every later call for
// the same addr must match it exactly, or this returns an error explaining
// the mismatch (the server's certificate changed — could be a reinstall,
// could be a man-in-the-middle attack) rather than silently accepting it.
func (s *Store) VerifyOrTrustCertificate(addr, fingerprint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.TrustedCerts == nil {
		s.data.TrustedCerts = map[string]string{}
	}
	existing, known := s.data.TrustedCerts[addr]
	if !known {
		s.data.TrustedCerts[addr] = fingerprint
		return s.persistLocked()
	}
	if existing != fingerprint {
		return fmt.Errorf(
			"the TLS certificate for %s does not match the one trusted on first connect (expected %s…, got %s…) — "+
				"this happens if the server was reinstalled, but can also mean a man-in-the-middle attack. "+
				"If you're certain the change is expected, forget the old certificate in Settings and reconnect",
			addr, existing[:16], fingerprint[:16],
		)
	}
	return nil
}

// TrustedCertificates lists every "host:port" address with a pinned
// certificate, for display/management in Settings.
func (s *Store) TrustedCertificates() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.data.TrustedCerts))
	for addr := range s.data.TrustedCerts {
		out = append(out, addr)
	}
	return out
}

// ForgetCertificate removes a pinned certificate, so the next connection
// to addr re-pins whatever certificate it presents (trust-on-first-use
// again) instead of being rejected as a mismatch.
func (s *Store) ForgetCertificate(addr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data.TrustedCerts, addr)
	return s.persistLocked()
}
