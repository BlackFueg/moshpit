package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	originApp      = "app"
	originExternal = "external"

	transportAuto = "auto"
	transportMosh = "mosh"
	transportSSH  = "ssh"
)

var (
	hostNameRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,47}$`)
	sessionNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,39}$`)
)

// Live is the server-side state of a tmux session as of the last sync. Never persisted.
type Live struct {
	Created  time.Time
	Activity time.Time
	Attached int
	Windows  int
	Path     string
	Cmd      string
}

// Session is a registry entry. Registry entries survive the session itself
// (Gone) so sessions lost to a reboot can be recreated in place.
type Session struct {
	Name    string    `json:"name"`
	Path    string    `json:"path,omitempty"`
	Command string    `json:"command,omitempty"`
	Origin  string    `json:"origin"`
	Created time.Time `json:"created"`
	Gone    bool      `json:"gone,omitempty"`

	live *Live
}

type Host struct {
	Name      string     `json:"name"`
	Address   string     `json:"address"`
	User      string     `json:"user,omitempty"`
	Port      int        `json:"port,omitempty"`
	Identity  string     `json:"identity,omitempty"`
	Transport string     `json:"transport,omitempty"`
	OS        string     `json:"os,omitempty"`
	Home      string     `json:"home,omitempty"`
	BootID    string     `json:"boot_id,omitempty"`
	HasMosh   bool       `json:"has_mosh,omitempty"`
	Tmux      string     `json:"tmux,omitempty"`
	LastSync  time.Time  `json:"last_sync"`
	Sessions  []*Session `json:"sessions,omitempty"`
}

type Config struct {
	Theme          string  `json:"theme"`
	DefaultCommand string  `json:"default_command"`
	Hosts          []*Host `json:"hosts"`
}

type appPaths struct {
	home, dir, config, sshInclude, cache, key string
}

func getPaths() appPaths {
	home, _ := os.UserHomeDir()
	cfgBase := os.Getenv("XDG_CONFIG_HOME")
	if cfgBase == "" {
		cfgBase = filepath.Join(home, ".config")
	}
	cacheBase := os.Getenv("XDG_CACHE_HOME")
	if cacheBase == "" {
		cacheBase = filepath.Join(home, ".cache")
	}
	dir := filepath.Join(cfgBase, "moshpit")
	return appPaths{
		home:       home,
		dir:        dir,
		config:     filepath.Join(dir, "config.json"),
		sshInclude: filepath.Join(dir, "ssh_config"),
		cache:      filepath.Join(cacheBase, "moshpit"),
		key:        filepath.Join(home, ".ssh", "moshpit_ed25519"),
	}
}

func loadConfig() (*Config, error) {
	c := &Config{}
	b, err := os.ReadFile(getPaths().config)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, c); err != nil {
			return nil, fmt.Errorf("parse %s: %w", getPaths().config, err)
		}
	}
	if c.Theme == "" {
		c.Theme = themes[0].Name
	}
	if c.DefaultCommand == "" {
		c.DefaultCommand = "omp"
	}
	for _, h := range c.Hosts {
		if h.Transport == "" {
			h.Transport = transportAuto
		}
	}
	return c, nil
}

func (c *Config) save() error {
	p := getPaths()
	if err := os.MkdirAll(p.dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(p.config, append(b, '\n'), 0o600)
}

func writeAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (c *Config) host(name string) *Host {
	for _, h := range c.Hosts {
		if h.Name == name {
			return h
		}
	}
	return nil
}

func (c *Config) removeHost(name string) {
	out := c.Hosts[:0]
	for _, h := range c.Hosts {
		if h.Name != name {
			out = append(out, h)
		}
	}
	c.Hosts = out
}

func (h *Host) session(name string) *Session {
	for _, s := range h.Sessions {
		if s.Name == name {
			return s
		}
	}
	return nil
}

func (h *Host) removeSession(name string) {
	out := h.Sessions[:0]
	for _, s := range h.Sessions {
		if s.Name != name {
			out = append(out, s)
		}
	}
	h.Sessions = out
}

func (h *Host) sortSessions() {
	sort.SliceStable(h.Sessions, func(i, j int) bool {
		a, b := h.Sessions[i], h.Sessions[j]
		if a.Gone != b.Gone {
			return !a.Gone
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
}

func (h *Host) liveCount() int {
	n := 0
	for _, s := range h.Sessions {
		if s.live != nil {
			n++
		}
	}
	return n
}

// Reconcile merges a fresh server snapshot into the registry and returns
// human-readable notes describing every drift that was found.
//
// Rules: sessions on the server but not in the registry are adopted as
// external; registry sessions missing from the server are kept as Gone when
// they were created by moshpit or the host rebooted (so they can be recreated),
// otherwise they are dropped.
func (h *Host) Reconcile(r *SyncResult) []string {
	rebooted := h.BootID != "" && r.BootID != "" && h.BootID != r.BootID
	live := make(map[string]*Live, len(r.Sessions))
	for i := range r.Sessions {
		live[r.Sessions[i].Name] = &r.Sessions[i].Live
	}

	var kept []*Session
	seen := map[string]bool{}
	lost, ended, back := 0, 0, 0
	for _, s := range h.Sessions {
		seen[s.Name] = true
		if l, ok := live[s.Name]; ok {
			if s.Gone {
				back++
			}
			s.Gone, s.live = false, l
			if l.Path != "" {
				s.Path = l.Path
			}
			kept = append(kept, s)
			continue
		}
		wasLive := !s.Gone
		s.live = nil
		switch {
		case s.Gone:
			kept = append(kept, s)
		case rebooted:
			s.Gone = true
			kept = append(kept, s)
			lost++
		case s.Origin == originApp:
			s.Gone = true
			kept = append(kept, s)
			ended++
		default:
			if wasLive {
				ended++
			}
		}
	}
	found := 0
	for i := range r.Sessions {
		ls := &r.Sessions[i]
		if seen[ls.Name] {
			continue
		}
		kept = append(kept, &Session{
			Name: ls.Name, Path: ls.Path, Origin: originExternal,
			Created: ls.Created, live: &ls.Live,
		})
		found++
	}
	h.Sessions = kept
	h.sortSessions()

	h.BootID = r.BootID
	h.HasMosh = r.HasMosh
	h.Tmux = r.Tmux
	if r.Home != "" {
		h.Home = r.Home
	}
	h.LastSync = time.Now()

	var notes []string
	if rebooted {
		notes = append(notes, "rebooted")
	}
	if lost > 0 {
		notes = append(notes, plural(lost, "session")+" lost")
	}
	if ended > 0 {
		notes = append(notes, plural(ended, "session")+" ended")
	}
	if found > 0 {
		notes = append(notes, plural(found, "new session")+" found")
	}
	if back > 0 {
		notes = append(notes, plural(back, "session")+" back")
	}
	return notes
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
