package main

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// User-facing ssh integration: moshpit keeps its own Host blocks in a managed
// file and Includes it from ~/.ssh/config, so `ssh <name>` / `mosh <name>`
// work from any terminal. Hosts whose name the user already defines are skipped.

func userSSHConfig() string { return filepath.Join(getPaths().home, ".ssh", "config") }

// userHostAliases lists literal Host names in ~/.ssh/config (not following Includes).
func userHostAliases() map[string]bool {
	out := map[string]bool{}
	f, err := os.Open(userSSHConfig())
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) > 1 && strings.EqualFold(fields[0], "host") {
			for _, a := range fields[1:] {
				out[a] = true
			}
		}
	}
	return out
}

// syncSSHConfig rewrites the managed file and makes sure ~/.ssh/config includes it.
// Returns the host names that got an ssh alias.
func syncSSHConfig(hosts []*Host) ([]string, error) {
	p := getPaths()
	user := userHostAliases()
	var b strings.Builder
	b.WriteString("# Managed by moshpit (mp). Regenerated on every change; edit hosts in mp instead.\n")
	var named []string
	for _, h := range hosts {
		if h.Identity == "" || h.Name == h.Address || user[h.Name] || user[h.Address] {
			continue
		}
		fmt.Fprintf(&b, "\nHost %s\n    HostName %s\n", h.Name, h.Address)
		if h.User != "" {
			fmt.Fprintf(&b, "    User %s\n", h.User)
		}
		if h.Port != 0 {
			fmt.Fprintf(&b, "    Port %d\n", h.Port)
		}
		fmt.Fprintf(&b, "    IdentityFile %s\n    IdentitiesOnly yes\n", h.Identity)
		named = append(named, h.Name)
	}
	if err := os.MkdirAll(p.dir, 0o700); err != nil {
		return nil, err
	}
	if err := writeAtomic(p.sshInclude, []byte(b.String()), 0o600); err != nil {
		return nil, err
	}
	return named, ensureInclude(p.sshInclude)
}

// ensureInclude prepends one Include line to ~/.ssh/config (it must precede
// any Host block to apply globally). A one-time backup is kept.
func ensureInclude(path string) error {
	cfg := userSSHConfig()
	if real, err := filepath.EvalSymlinks(cfg); err == nil {
		cfg = real // keep dotfile-manager symlinks intact
	}
	line := "Include " + strings.Replace(path, getPaths().home, "~", 1)
	b, err := os.ReadFile(cfg)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) == 2 && strings.EqualFold(f[0], "include") && (f[1] == path || f[1] == strings.Fields(line)[1]) {
			return nil
		}
	}
	if len(b) > 0 {
		if _, err := os.Stat(cfg + ".moshpit.bak"); errors.Is(err, fs.ErrNotExist) {
			if err := os.WriteFile(cfg+".moshpit.bak", b, 0o600); err != nil {
				return err
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		return err
	}
	out := "# Added by moshpit: ssh aliases for hosts registered in mp\n" + line + "\n\n" + string(b)
	return writeAtomic(cfg, []byte(out), 0o600)
}
