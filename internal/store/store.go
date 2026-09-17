// Package store persists Lupinus's connection list and settings to a JSON
// file in the OS config directory, and keeps VNC passwords out of that file
// entirely — they live in the OS credential store (Windows Credential
// Manager / macOS Keychain / Linux Secret Service) via go-keyring, unlike
// the plaintext-file approach used by this author's earlier Wails apps.
package store

import (
	"encoding/json"
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
	Host     string `json:"host"`
	Port     int    `json:"port"`
	ColorTag string `json:"colorTag,omitempty"`
	LastUsed string `json:"lastUsed,omitempty"` // RFC 3339, empty if never connected
}

type fileData struct {
	Connections []Connection `json:"connections"`
	Theme       string       `json:"theme"` // "light" | "dark" | "system"
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
