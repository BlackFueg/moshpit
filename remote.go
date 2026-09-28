package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// Conn is an immutable snapshot of how to reach a host; safe to hand to goroutines.
type Conn struct {
	Address  string
	User     string
	Port     int
	Identity string
}

func (h *Host) conn() Conn {
	return Conn{Address: h.Address, User: h.User, Port: h.Port, Identity: h.Identity}
}

func (c Conn) dest() string {
	if c.User != "" {
		return c.User + "@" + c.Address
	}
	return c.Address
}

// sshOpts returns ssh options (no destination). multiplex reuses one TCP
// connection for the short-lived control commands, which keeps sync snappy.
func (c Conn) sshOpts(multiplex bool) []string {
	var a []string
	if c.Port != 0 {
		a = append(a, "-p", strconv.Itoa(c.Port))
	}
	if c.Identity != "" {
		a = append(a, "-i", c.Identity, "-o", "IdentitiesOnly=yes")
	}
	if c.User != "" {
		a = append(a, "-l", c.User)
	}
	a = append(a, "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3")
	if multiplex {
		a = append(a,
			"-o", "ControlMaster=auto",
			"-o", "ControlPath="+filepath.Join(getPaths().cache, "cm-%C"),
			"-o", "ControlPersist=10") // covers an action and its follow-up sync; no idle keepalives
	}
	return a
}

// sq quotes s for POSIX sh.
func sq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// wrapScript makes the whole script parse before it runs and detaches its
// stdin, so nothing inside can swallow the rest of the script from `sh -s`.
func wrapScript(body string) string {
	return "__mp() {\n" + body + "\n}\n__mp </dev/null\n"
}

// runRemote pipes a POSIX sh script to the host over non-interactive ssh.
func runRemote(ctx context.Context, c Conn, body string) (string, error) {
	if err := os.MkdirAll(getPaths().cache, 0o700); err != nil {
		return "", err
	}
	args := append(c.sshOpts(true),
		"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=8", "-o", "LogLevel=ERROR",
		c.Address, "sh -s")
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = strings.NewReader(wrapScript(body))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // never let ssh touch our TTY
	cmd.WaitDelay = 2 * time.Second
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return out.String(), errors.New("timed out")
		}
		return out.String(), sshError(errb.String(), err)
	}
	return out.String(), nil
}

// sshError turns ssh/remote stderr into one short, readable line.
func sshError(stderr string, err error) error {
	lines := strings.Split(strings.TrimSpace(ansi.Strip(stderr)), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	low := strings.ToLower(stderr)
	switch {
	case strings.Contains(low, "permission denied"):
		return errors.New("authentication failed")
	case strings.Contains(low, "could not resolve hostname"):
		return errors.New("unknown hostname")
	case strings.Contains(low, "connection refused"):
		return errors.New("connection refused")
	case strings.Contains(low, "timed out"):
		return errors.New("connection timed out")
	case strings.Contains(low, "no route to host"), strings.Contains(low, "network is unreachable"):
		return errors.New("host unreachable")
	case strings.Contains(low, "host key verification failed"), strings.Contains(low, "remote host identification has changed"):
		return errors.New("host key mismatch (see ~/.ssh/known_hosts)")
	case last != "":
		return errors.New(strings.TrimPrefix(last, "ssh: "))
	}
	return err
}

type LiveSession struct {
	Name string
	Live
}

type SyncResult struct {
	BootID   string
	Home     string
	Tmux     string
	HasTmux  bool
	HasMosh  bool
	Sessions []LiveSession
}

const syncScript = `printf 'B\t%s\n' "$(cat /proc/sys/kernel/random/boot_id 2>/dev/null)"
printf 'H\t%s\n' "$HOME"
command -v mosh-server >/dev/null 2>&1 && printf 'M\t1\n'
command -v tmux >/dev/null 2>&1 || return 0
printf 'T\t%s\n' "$(tmux -V)"
tmux list-sessions -F 'S	#{session_name}	#{session_created}	#{session_activity}	#{session_attached}	#{session_windows}	#{session_path}' 2>/dev/null
tmux list-panes -a -F 'P	#{session_name}	#{window_active}#{pane_active}	#{pane_current_command}' 2>/dev/null
return 0`

func fetchState(ctx context.Context, c Conn) (*SyncResult, error) {
	out, err := runRemote(ctx, c, syncScript)
	if err != nil {
		return nil, err
	}
	return parseState(out), nil
}

func parseState(out string) *SyncResult {
	r := &SyncResult{}
	cmds := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		switch {
		case f[0] == "B" && len(f) > 1:
			r.BootID = strings.TrimSpace(f[1])
		case f[0] == "H" && len(f) > 1:
			r.Home = f[1]
		case f[0] == "M":
			r.HasMosh = true
		case f[0] == "T" && len(f) > 1:
			r.HasTmux = true
			r.Tmux = strings.TrimPrefix(f[1], "tmux ")
		case f[0] == "S" && len(f) >= 7:
			r.Sessions = append(r.Sessions, LiveSession{Name: f[1], Live: Live{
				Created:  unix(f[2]),
				Activity: unix(f[3]),
				Attached: atoi(f[4]),
				Windows:  atoi(f[5]),
				Path:     strings.Join(f[6:], "\t"),
			}})
		case f[0] == "P" && len(f) >= 4 && f[2] == "11":
			cmds[f[1]] = f[3]
		}
	}
	for i := range r.Sessions {
		r.Sessions[i].Cmd = cmds[r.Sessions[i].Name]
	}
	return r
}

func unix(s string) time.Time {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n == 0 {
		return time.Time{}
	}
	return time.Unix(n, 0)
}

func atoi(s string) int { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }

// shPath renders a user-supplied remote directory for sh, expanding a leading ~.
func shPath(p string) string {
	switch {
	case p == "" || p == "~":
		return `"$HOME"`
	case strings.HasPrefix(p, "~/"):
		return `"$HOME"/` + sq(p[2:])
	}
	return sq(p)
}

// ensureSession starts the named session (cd'ing to dir and typing command)
// unless it already exists. created reports which of the two happened.
func ensureSession(ctx context.Context, c Conn, name, dir, command string) (created bool, err error) {
	var b strings.Builder
	fmt.Fprintf(&b, "tmux has-session -t %s 2>/dev/null && { echo exists; exit 0; }\n", sq("="+name))
	fmt.Fprintf(&b, "d=%s\n", shPath(dir))
	b.WriteString(`[ -d "$d" ] || { echo "no such directory: $d" >&2; exit 3; }` + "\n")
	fmt.Fprintf(&b, "tmux -u new-session -d -s %s -c \"$d\" || exit 1\n", sq(name))
	if command != "" {
		fmt.Fprintf(&b, "tmux send-keys -t %s %s Enter\n", sq("="+name+":"), sq(command))
	}
	b.WriteString("echo created\n")
	out, err := runRemote(ctx, c, b.String())
	return strings.TrimSpace(out) == "created", err
}

func haveMosh() bool {
	_, err := exec.LookPath("mosh")
	return err == nil
}

// transportFor resolves a host's transport setting; auto picks mosh only when
// both ends have it.
func transportFor(h *Host, localMosh bool) string {
	switch h.Transport {
	case transportSSH, transportMosh:
		return h.Transport
	}
	if localMosh && h.HasMosh {
		return transportMosh
	}
	return transportSSH
}

func renameSession(ctx context.Context, c Conn, from, to string) error {
	_, err := runRemote(ctx, c, fmt.Sprintf("tmux rename-session -t %s %s", sq("="+from), sq(to)))
	return err
}

func killSession(ctx context.Context, c Conn, name string) error {
	_, err := runRemote(ctx, c, fmt.Sprintf("tmux kill-session -t %s", sq("="+name)))
	return err
}

// revokeKey removes moshpit's public key from the host's authorized_keys.
func revokeKey(ctx context.Context, c Conn, pub string) error {
	_, err := runRemote(ctx, c, fmt.Sprintf(`k=%s; f="$HOME/.ssh/authorized_keys"
[ -f "$f" ] || exit 0
grep -vxF "$k" "$f" > "$f.mp" ; cat "$f.mp" > "$f" && rm -f "$f.mp"`, sq(pub)))
	return err
}

// attachCmd builds the interactive command that lands the user inside the tmux
// session. `new-session -A` attaches, or recreates the name if it vanished.
func attachCmd(c Conn, transport, session string) *exec.Cmd {
	tmux := []string{"tmux", "-u", "new-session", "-A", "-s", session}
	if transport == transportMosh {
		// No multiplexing: mosh learns the server IP through its own ProxyCommand.
		sshCmd := "ssh"
		for _, o := range append(c.sshOpts(false), "-o", "ConnectTimeout=10") {
			sshCmd += " " + sq(o)
		}
		args := []string{"--ssh=" + sshCmd, "--server=env LC_ALL=C.UTF-8 mosh-server", c.dest(), "--"}
		return exec.Command("mosh", append(args, tmux...)...)
	}
	remote := strings.Join([]string{"tmux", "-u", "new-session", "-A", "-s", sq(session)}, " ")
	args := append(c.sshOpts(false), "-t", "-o", "ConnectTimeout=10", c.Address, remote)
	return exec.Command("ssh", args...)
}

// tailBuf keeps the last few KB written to it (stderr of attached sessions).
type tailBuf struct {
	mu sync.Mutex
	b  []byte
}

func (t *tailBuf) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > 4096 {
		t.b = t.b[len(t.b)-4096:]
	}
	return len(p), nil
}

func (t *tailBuf) lastLine() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(ansi.Strip(string(t.b))), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(strings.ReplaceAll(lines[i], "\r", "")); l != "" {
			return l
		}
	}
	return ""
}
