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
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/x/ansi"
)

const (
	askpassEnv = "MOSHPIT_ASKPASS"
	secretEnv  = "MOSHPIT_SECRET"
)

// askpass runs when ssh invokes this binary as SSH_ASKPASS: it answers
// password prompts with the secret from the environment and refuses anything else.
func askpass(args []string) int {
	p := strings.ToLower(strings.Join(args, " "))
	if strings.Contains(p, "yes/no") || strings.Contains(p, "passphrase") || strings.Contains(p, "fingerprint") {
		return 1
	}
	fmt.Println(os.Getenv(secretEnv))
	return 0
}

type addReq struct {
	Name, Address, User, Password string
	Port                          int
}

type bootLine struct{ kind, text string } // kind: step ok warn fail info raw

// ensureKey returns moshpit's public key, generating the keypair on first use.
func ensureKey(path string) (pub string, created bool, err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", false, err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		host, _ := os.Hostname()
		cmd := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "moshpit@"+host, "-f", path)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", false, fmt.Errorf("ssh-keygen: %s", strings.TrimSpace(string(out)))
		}
		created = true
	}
	b, err := os.ReadFile(path + ".pub")
	if errors.Is(err, os.ErrNotExist) {
		b, err = exec.Command("ssh-keygen", "-y", "-f", path).Output()
		if err == nil {
			err = os.WriteFile(path+".pub", b, 0o644)
		}
	}
	if err != nil {
		return "", created, err
	}
	return strings.TrimSpace(string(b)), created, nil
}

func readPubKey() string {
	b, _ := os.ReadFile(getPaths().key + ".pub")
	return strings.TrimSpace(string(b))
}

// bootstrapScript installs the key, tmux and mosh on a bare Linux box. It only
// assumes POSIX sh. Progress is reported as "::<kind> <text>" lines.
const bootstrapScript = `
say() { printf '::%s\n' "$*"; }
as_root() {
	if [ "$(id -u)" = 0 ]; then "$@"
	elif command -v sudo >/dev/null 2>&1; then
		if sudo -n true 2>/dev/null; then sudo -n "$@"
		elif [ -n "$PW" ]; then printf '%s\n' "$PW" | sudo -S -p '' "$@"
		else return 97; fi
	else return 97; fi
}
umask 077
say "step Authorizing moshpit key"
mkdir -p "$HOME/.ssh" && touch "$HOME/.ssh/authorized_keys" || { say "fail cannot write ~/.ssh/authorized_keys"; exit 1; }
chmod 700 "$HOME/.ssh"; chmod 600 "$HOME/.ssh/authorized_keys"
if grep -qxF "$PUBKEY" "$HOME/.ssh/authorized_keys"; then say "ok Key was already authorized"
else printf '%s\n' "$PUBKEY" >> "$HOME/.ssh/authorized_keys" && say "ok Key added to ~/.ssh/authorized_keys"; fi
command -v restorecon >/dev/null 2>&1 && restorecon -R "$HOME/.ssh" 2>/dev/null
os=$(. /etc/os-release 2>/dev/null && echo "$PRETTY_NAME"); say "fact os ${os:-$(uname -sr)}"
say "fact home $HOME"

need=""
command -v tmux >/dev/null 2>&1 || need="$need tmux"
command -v mosh-server >/dev/null 2>&1 || need="$need mosh"
if [ -n "$need" ]; then
	say "step Installing$need"
	if as_root true 2>/dev/null; then
		pm=""
		for p in apt-get dnf yum apk pacman zypper; do command -v $p >/dev/null 2>&1 && { pm=$p; break; }; done
		pkg() {
			case $pm in
			apt-get) as_root env DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=300 -y -qq install "$@" ;;
			dnf) as_root dnf -y -q install "$@" ;;
			yum) as_root yum -y -q install "$@" ;;
			apk) as_root apk add -q "$@" ;;
			pacman) as_root pacman -Sy --noconfirm --needed "$@" ;;
			zypper) as_root zypper -n -q install "$@" ;;
			*) return 1 ;;
			esac
		}
		if [ -z "$pm" ]; then say "warn No supported package manager found"
		else
			say "info using $pm"
			[ "$pm" = apt-get ] && as_root env DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=300 -qq update 2>&1
			case "$need" in *tmux*) pkg tmux 2>&1 ;; esac
			case "$need" in *mosh*) pkg mosh 2>&1 || { case $pm in dnf|yum) pkg epel-release 2>&1 && pkg mosh 2>&1 ;; esac; } ;; esac
		fi
	else
		say "warn No root or sudo access; install manually:$need"
	fi
fi

if command -v tmux >/dev/null 2>&1; then say "ok $(tmux -V) ready"; say "fact tmux $(tmux -V | cut -d' ' -f2)"
else say "fail tmux is not available"; exit 1; fi

if command -v mosh-server >/dev/null 2>&1; then
	say "ok mosh-server ready"; say "fact mosh 1"
	if command -v ufw >/dev/null 2>&1 && as_root ufw status 2>/dev/null | grep -q 'Status: active'; then
		as_root ufw allow 60000:61000/udp >/dev/null 2>&1 && say "ok Opened UDP 60000-61000 in ufw"
	fi
	if command -v firewall-cmd >/dev/null 2>&1 && as_root firewall-cmd --state >/dev/null 2>&1; then
		as_root firewall-cmd -q --permanent --add-port=60000-61000/udp && as_root firewall-cmd -q --reload && say "ok Opened UDP 60000-61000 in firewalld"
	fi
else
	say "warn mosh unavailable, sessions will use plain ssh"
fi

if [ ! -e "$HOME/.tmux.conf" ] && [ ! -e "${XDG_CONFIG_HOME:-$HOME/.config}/tmux/tmux.conf" ]; then
	term=screen-256color
	infocmp tmux-256color >/dev/null 2>&1 && term=tmux-256color
	cat > "$HOME/.tmux.conf" <<EOF
# Written by moshpit: tuned for terminal UIs such as coding agents. Safe to edit.
set -sg escape-time 0
set -g default-terminal "$term"
set -asq terminal-features ",*:RGB"
set -sq extended-keys on
set -asq terminal-features ",*:extkeys"
set -gq allow-passthrough on
set -g set-clipboard on
set -g focus-events on
set -g history-limit 50000
set -g mouse on
EOF
	say "ok Wrote ~/.tmux.conf tuned for TUIs"
fi
`

// runBootstrap provisions a host and proves key-only login works afterwards.
func runBootstrap(ctx context.Context, r addReq, emit func(bootLine)) (*Host, error) {
	p := getPaths()
	emit(bootLine{"step", "Preparing local SSH key"})
	pub, created, err := ensureKey(p.key)
	if err != nil {
		return nil, err
	}
	if created {
		emit(bootLine{"ok", "Generated ~/.ssh/moshpit_ed25519"})
	} else {
		emit(bootLine{"ok", "Using ~/.ssh/moshpit_ed25519"})
	}

	c := Conn{Address: r.Address, User: r.User, Port: r.Port}
	emit(bootLine{"step", "Connecting to " + c.dest()})
	args := append(c.sshOpts(false),
		"-T", "-o", "StrictHostKeyChecking=accept-new", "-o", "ConnectTimeout=10",
		"-o", "LogLevel=ERROR", "-o", "ControlPath=none")
	env := os.Environ()
	if r.Password != "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		args = append(args,
			"-o", "PreferredAuthentications=keyboard-interactive,password",
			"-o", "PubkeyAuthentication=no", "-o", "NumberOfPasswordPrompts=1")
		env = append(env, "SSH_ASKPASS="+exe, "SSH_ASKPASS_REQUIRE=force", askpassEnv+"=1", secretEnv+"="+r.Password)
	} else {
		args = append(args, "-o", "BatchMode=yes")
	}
	args = append(args, c.Address, "sh -s")

	script := fmt.Sprintf("PUBKEY=%s\nPW=%s\n%s", sq(pub), sq(r.Password), bootstrapScript)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(wrapScript(script))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.WaitDelay = 2 * time.Second
	var errb bytes.Buffer
	cmd.Stderr = &errb
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	h := &Host{Name: r.Name, Address: r.Address, User: r.User, Port: r.Port, Identity: p.key, Transport: transportAuto}
	var failed string
	connected := false
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		line := sc.Text()
		if !connected {
			connected = true
			emit(bootLine{"ok", "Connected"})
		}
		if !strings.HasPrefix(line, "::") {
			// Package managers redraw progress with \r; keep only the final state.
			if i := strings.LastIndexByte(line, '\r'); i >= 0 {
				line = line[i+1:]
			}
			if t := strings.TrimSpace(strings.Map(dropControl, ansi.Strip(line))); t != "" {
				emit(bootLine{"raw", t})
			}
			continue
		}
		kind, text, _ := strings.Cut(line[2:], " ")
		if kind == "fact" {
			k, v, _ := strings.Cut(text, " ")
			switch k {
			case "os":
				h.OS = v
			case "home":
				h.Home = v
			case "tmux":
				h.Tmux = v
			case "mosh":
				h.HasMosh = true
			}
			continue
		}
		if kind == "fail" {
			failed = text
		}
		emit(bootLine{kind, text})
	}
	if err := cmd.Wait(); err != nil || failed != "" {
		if failed != "" {
			return nil, errors.New(failed)
		}
		if ctx.Err() != nil {
			return nil, errors.New("cancelled")
		}
		e := sshError(errb.String(), err)
		if e.Error() == "authentication failed" && r.Password == "" {
			e = errors.New("authentication failed: enter a password, or add a key for this host to ssh-agent")
		}
		return nil, e
	}
	if h.Tmux == "" {
		return nil, errors.New("setup did not finish (connection dropped?)")
	}

	emit(bootLine{"step", "Verifying passwordless login"})
	vctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, err := runRemote(vctx, h.conn(), "true"); err != nil {
		return nil, fmt.Errorf("key login failed: %w", err)
	}
	emit(bootLine{"ok", "Passwordless login works"})
	return h, nil
}

func dropControl(r rune) rune {
	if r < 0x20 || r == 0x7f {
		return -1
	}
	return r
}
