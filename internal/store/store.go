// Package store keeps the service's state in one JSON file: who may sign in, the bot
// account, the channels, commands, timers and settings. Every change is written at once
// (to a temporary file, then renamed), so a crash never leaves half a file.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// User may sign in to the dashboard. ID is the Twitch user id; it is empty until the
// first sign-in, which binds the account. After that only the id counts.
type User struct {
	Login    string    `json:"login"`
	ID       string    `json:"id,omitempty"`
	Name     string    `json:"name,omitempty"`
	Owner    bool      `json:"owner,omitempty"`
	Role     string    `json:"role,omitempty"` // RoleEditor (also when empty) or RoleViewer; the owner is Owner
	Added    time.Time `json:"added"`
	LastSeen time.Time `json:"last_seen,omitempty"`
}

// Roles of dashboard users besides the owner.
const (
	RoleEditor = "editor" // commands, timers, channels, writing in chat
	RoleViewer = "viewer" // reads everything, changes nothing
)

// Level is the user's role: "owner", RoleEditor or RoleViewer.
func (u User) Level() string {
	switch {
	case u.Owner:
		return "owner"
	case u.Role == RoleViewer:
		return RoleViewer
	}
	return RoleEditor
}

// Bot is the Twitch account the bot chats as.
type Bot struct {
	ID      string    `json:"id"`
	Login   string    `json:"login"`
	Name    string    `json:"name"`
	Access  string    `json:"access"`
	Refresh string    `json:"refresh"`
	Expires time.Time `json:"expires"`
	Scopes  []string  `json:"scopes"`
	Linked  time.Time `json:"linked"`
	By      string    `json:"by"`
}

// Channel is a streamer's chat the bot joins while Enabled.
type Channel struct {
	Login   string    `json:"login"`
	ID      string    `json:"id,omitempty"`
	Name    string    `json:"name,omitempty"`
	Enabled bool      `json:"enabled"`
	Website bool      `json:"website"` // shown on kronwerke.com while live
	Note    string    `json:"note,omitempty"`
	Added   time.Time `json:"added"`
}

// Command answers "!name" in chat.
type Command struct {
	Name     string   `json:"name"` // without the "!", lower case
	Aliases  []string `json:"aliases,omitempty"`
	Response string   `json:"response"`
	Cooldown int      `json:"cooldown"` // seconds, per channel
	Enabled  bool     `json:"enabled"`
	ModOnly  bool     `json:"mod_only"`
	Channels []string `json:"channels,omitempty"` // empty is every channel
	Uses     int      `json:"uses"`
}

// Timer posts a text into chat every Minutes, when at least MinLines were written since.
type Timer struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Text     string   `json:"text"`
	Minutes  int      `json:"minutes"`
	MinLines int      `json:"min_lines"`
	Enabled  bool     `json:"enabled"`
	LiveOnly bool     `json:"live_only"`
	Channels []string `json:"channels,omitempty"`
}

// Session is a signed in browser; the key in the map is the hash of its cookie.
type Session struct {
	UserID  string    `json:"user_id"`
	Login   string    `json:"login"`
	CSRF    string    `json:"csrf"`
	Expires time.Time `json:"expires"`
}

// Data is the whole file.
type Data struct {
	Users    []User             `json:"users"`
	Bot      *Bot               `json:"bot,omitempty"`
	Channels []Channel          `json:"channels"`
	Commands []Command          `json:"commands"`
	Timers   []Timer            `json:"timers"`
	Settings map[string]string  `json:"settings"`
	Sessions map[string]Session `json:"sessions"`
	Seeded   bool               `json:"seeded"`
}

// Store guards Data.
type Store struct {
	path string
	mu   sync.RWMutex
	d    Data
}

// Open reads the file, or starts empty when there is none.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, &s.d); err != nil {
			return nil, err
		}
	}
	if s.d.Settings == nil {
		s.d.Settings = map[string]string{}
	}
	if s.d.Sessions == nil {
		s.d.Sessions = map[string]Session{}
	}
	return s, nil
}

// View runs fn with the data for reading.
func (s *Store) View(fn func(d *Data)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	fn(&s.d)
}

// Update runs fn with the data and writes the file when fn returns nil.
func (s *Store) Update(fn func(d *Data) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	before, _ := json.Marshal(s.d)
	if err := fn(&s.d); err != nil {
		var back Data
		json.Unmarshal(before, &back)
		s.d = back
		return err
	}
	return s.save()
}

func (s *Store) save() error {
	b, err := json.MarshalIndent(s.d, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Hash is how session cookies are kept: only their SHA-256.
func Hash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// Lower is a Twitch login as stored.
func Lower(s string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "#")))
}

// Setting returns a setting or its fallback.
func (s *Store) Setting(key, fallback string) string {
	var v string
	s.View(func(d *Data) { v = d.Settings[key] })
	if v == "" {
		return fallback
	}
	return v
}

// EnabledChannels returns the logins the bot should sit in.
func (s *Store) EnabledChannels() (out []string) {
	s.View(func(d *Data) {
		for _, c := range d.Channels {
			if c.Enabled {
				out = append(out, c.Login)
			}
		}
	})
	sort.Strings(out)
	return
}

// SweepSessions drops expired sessions.
func (s *Store) SweepSessions(now time.Time) {
	s.Update(func(d *Data) error {
		for k, v := range d.Sessions {
			if now.After(v.Expires) {
				delete(d.Sessions, k)
			}
		}
		return nil
	})
}
