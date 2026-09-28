package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// cliAttach implements `mp <session>` and `mp <host> <session>`: resolve the
// target, make sure the tmux session exists (creating or recreating it), then
// hand the terminal over to mosh/ssh. Returns the process exit code.
func cliAttach(cfg *Config, hostName, name string) int {
	h, s, err := resolveTarget(cfg, hostName, name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mp:", err)
		return 1
	}

	dir, command := "~", cfg.DefaultCommand
	if s != nil {
		dir, command = orHome(s.Path), s.Command
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	created, err := ensureSession(ctx, h.conn(), name, dir, command)
	cancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mp: %s: %v\n", h.Name, err)
		return 1
	}

	switch {
	case created:
		if s == nil {
			s = &Session{Name: name, Origin: originApp, Created: time.Now()}
			h.Sessions = append(h.Sessions, s)
		}
		s.Gone, s.Origin, s.Command, s.Path = false, originApp, command, dir
	case s == nil:
		h.Sessions = append(h.Sessions, &Session{Name: name, Origin: originExternal, Created: time.Now()})
	default:
		s.Gone = false
	}
	h.sortSessions()
	if err := cfg.save(); err != nil {
		fmt.Fprintln(os.Stderr, "mp: saving config:", err)
	}

	tr := transportFor(h, haveMosh())
	if tr == transportMosh && !haveMosh() {
		fmt.Fprintf(os.Stderr, "mp: %s is set to mosh but mosh isn't installed locally\n", h.Name)
		return 1
	}
	verb := "attaching to"
	if created {
		verb = "started"
		if command != "" {
			verb += " `" + command + "` in"
		}
	}
	fmt.Fprintf(os.Stderr, "mp: %s %s/%s via %s\n", verb, h.Name, name, tr)

	cmd := attachCmd(h.conn(), tr, name)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "mp:", err)
		return 1
	}
	return 0
}

// resolveTarget finds the host (and the registry entry, if any) for a session.
func resolveTarget(cfg *Config, hostName, name string) (*Host, *Session, error) {
	if len(cfg.Hosts) == 0 {
		return nil, nil, errors.New("no hosts registered yet; run `mp` and press a to add one")
	}
	if hostName != "" {
		h := cfg.host(hostName)
		if h == nil {
			return nil, nil, fmt.Errorf("unknown host %q (registered: %s)", hostName, hostNames(cfg))
		}
		if !sessionNameRe.MatchString(name) && h.session(name) == nil {
			return nil, nil, fmt.Errorf("invalid session name %q: use letters, digits, - and _", name)
		}
		return h, h.session(name), nil
	}

	matches := findInRegistry(cfg, name)
	if len(matches) == 0 && cfg.host(name) != nil {
		return nil, nil, fmt.Errorf("%q is a host; to create a session on it run: mp %s <session>", name, name)
	}
	if len(matches) == 0 {
		// Unknown locally: it may have been started elsewhere, so ask the servers.
		fmt.Fprintf(os.Stderr, "mp: %q isn't in the registry, checking %s…\n", name, plural(len(cfg.Hosts), "host"))
		syncBlocking(cfg)
		matches = findInRegistry(cfg, name)
	}
	switch len(matches) {
	case 0:
		return nil, nil, fmt.Errorf("no session %q on any host; create it with: mp <host> %s  (hosts: %s)", name, name, hostNames(cfg))
	case 1:
		return matches[0], matches[0].session(name), nil
	}
	var where []string
	for _, h := range matches {
		where = append(where, "mp "+h.Name+" "+name)
	}
	return nil, nil, fmt.Errorf("%q exists on several hosts; pick one:\n  %s", name, strings.Join(where, "\n  "))
}

// findInRegistry returns hosts holding a session with this name, preferring
// running sessions over ones known to be gone.
func findInRegistry(cfg *Config, name string) []*Host {
	var live, gone []*Host
	for _, h := range cfg.Hosts {
		if s := h.session(name); s != nil {
			if s.Gone {
				gone = append(gone, h)
			} else {
				live = append(live, h)
			}
		}
	}
	if len(live) > 0 {
		return live
	}
	return gone
}

// syncBlocking refreshes every host's registry in parallel and saves it.
func syncBlocking(cfg *Config) {
	type result struct {
		h   *Host
		res *SyncResult
		err error
	}
	results := make([]result, len(cfg.Hosts))
	var wg sync.WaitGroup
	for i, h := range cfg.Hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			res, err := fetchState(ctx, h.conn())
			results[i] = result{h, res, err}
		}()
	}
	wg.Wait()
	for _, r := range results {
		if r.err != nil {
			fmt.Fprintf(os.Stderr, "mp: %s: %v\n", r.h.Name, r.err)
			continue
		}
		r.h.Reconcile(r.res)
	}
	if err := cfg.save(); err != nil {
		fmt.Fprintln(os.Stderr, "mp: saving config:", err)
	}
}

func hostNames(cfg *Config) string {
	var n []string
	for _, h := range cfg.Hosts {
		n = append(n, h.Name)
	}
	sort.Strings(n)
	return strings.Join(n, ", ")
}
