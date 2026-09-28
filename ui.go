package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type pane int

const (
	paneHosts pane = iota
	paneSessions
)

type mode int

const (
	modeMain mode = iota
	modeForm
	modeConfirm
	modeBoot
	modeHelp
)

type statusKind int

const (
	skInfo statusKind = iota
	skOK
	skWarn
	skErr
)

type hostRT struct {
	syncing bool
	known   bool // at least one sync attempt finished this run
	online  bool
	err     string
}

type syncRound struct {
	pending, total, failed int
	notes                  []string
}

type model struct {
	cfg *Config
	th  Theme
	w   int
	h   int

	focus      pane
	hi, si     int
	hoff, soff int

	rt       map[string]*hostRT
	round    *syncRound
	lastSync time.Time
	ops      int // remote operations in flight
	spinning bool

	mode    mode
	form    *form
	confirm *confirm
	boot    *bootView

	status     string
	statusKind statusKind
	statusAt   time.Time

	localMosh bool
}

type (
	clockMsg   time.Time
	spinMsg    struct{}
	syncMsg    struct {
		host  string
		res   *SyncResult
		err   error
		round bool
	}
	opKind int
	opMsg  struct {
		kind       opKind
		host, sess string
		to         string // rename target / command
		dir        string
		err        error
	}
	attachDoneMsg struct {
		host, sess, transport string
		err                   error
		stderr                string
	}
	bootLineMsg bootLine
	bootDoneMsg struct {
		host *Host
		err  error
	}
)

const (
	opCreate opKind = iota
	opRename
	opKill
	opRevoke
)

func newModel(cfg *Config) *model {
	return &model{
		cfg:       cfg,
		th:        themes[themeIndex(cfg.Theme)],
		rt:        map[string]*hostRT{},
		localMosh: haveMosh(),
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.syncAll(), clockTick())
}

func clockTick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return clockMsg(t) })
}

func spinTick() tea.Cmd {
	return tea.Tick(90*time.Millisecond, func(time.Time) tea.Msg { return spinMsg{} })
}

func (m *model) busy() bool {
	if m.ops > 0 || (m.boot != nil && !m.boot.done) {
		return true
	}
	for _, r := range m.rt {
		if r.syncing {
			return true
		}
	}
	return false
}

// startSpin (re)starts the spinner clock only while something is in flight.
func (m *model) startSpin() tea.Cmd {
	if m.spinning || !m.busy() {
		return nil
	}
	m.spinning = true
	return spinTick()
}

func (m *model) rtFor(name string) *hostRT {
	r, ok := m.rt[name]
	if !ok {
		r = &hostRT{}
		m.rt[name] = r
	}
	return r
}

func (m *model) setStatus(k statusKind, format string, a ...any) {
	m.status, m.statusKind, m.statusAt = fmt.Sprintf(format, a...), k, time.Now()
}

func (m *model) save() {
	if err := m.cfg.save(); err != nil {
		m.setStatus(skErr, "Saving config failed: %v", err)
	}
}

func (m *model) curHost() *Host {
	if m.hi < 0 || m.hi >= len(m.cfg.Hosts) {
		return nil
	}
	return m.cfg.Hosts[m.hi]
}

func (m *model) curSess() *Session {
	h := m.curHost()
	if h == nil || m.si < 0 || m.si >= len(h.Sessions) {
		return nil
	}
	return h.Sessions[m.si]
}

func (m *model) clamp() {
	m.hi = max(0, min(m.hi, len(m.cfg.Hosts)-1))
	if h := m.curHost(); h != nil {
		m.si = max(0, min(m.si, len(h.Sessions)-1))
	} else {
		m.si = 0
	}
}

func (m *model) selectSession(name string) {
	if h := m.curHost(); h != nil {
		for i, s := range h.Sessions {
			if s.Name == name {
				m.si = i
				return
			}
		}
	}
}

func (m *model) transportFor(h *Host) string { return transportFor(h, m.localMosh) }

// ---- sync ------------------------------------------------------------------

func syncCmd(h *Host, round bool) tea.Cmd {
	name, c := h.Name, h.conn()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		res, err := fetchState(ctx, c)
		return syncMsg{host: name, res: res, err: err, round: round}
	}
}

// syncAll is the only way hosts get polled: on launch and when the user asks.
func (m *model) syncAll() tea.Cmd {
	var cmds []tea.Cmd
	for _, h := range m.cfg.Hosts {
		r := m.rtFor(h.Name)
		if r.syncing {
			continue
		}
		r.syncing = true
		cmds = append(cmds, syncCmd(h, true))
	}
	if len(cmds) == 0 {
		return nil
	}
	if m.round == nil {
		m.round = &syncRound{}
	}
	m.round.pending += len(cmds)
	m.round.total += len(cmds)
	return tea.Batch(append(cmds, m.startSpin())...)
}

func (m *model) syncHost(h *Host) tea.Cmd {
	r := m.rtFor(h.Name)
	if r.syncing {
		return nil
	}
	r.syncing = true
	return tea.Batch(syncCmd(h, false), m.startSpin())
}

func (m *model) onSync(msg syncMsg) {
	r := m.rtFor(msg.host)
	r.syncing, r.known = false, true
	var note string
	isErr := false
	if h := m.cfg.host(msg.host); h != nil {
		cur := m.curSess()
		if msg.err != nil {
			r.online, r.err = false, msg.err.Error()
			note, isErr = h.Name+": "+r.err, true
		} else {
			r.online, r.err = true, ""
			notes := h.Reconcile(msg.res)
			if !msg.res.HasTmux {
				r.err = "tmux is not installed on this host"
				notes = append(notes, "tmux missing")
			}
			m.save()
			if len(notes) > 0 {
				note = h.Name + ": " + strings.Join(notes, ", ")
			}
		}
		if cur != nil && m.curHost() == h {
			m.selectSession(cur.Name)
		}
		m.clamp()
	}

	if !msg.round || m.round == nil {
		if note != "" {
			m.setStatus(skWarn, "%s", note)
		}
		return
	}
	rd := m.round
	rd.pending--
	if isErr {
		rd.failed++
	}
	if note != "" {
		rd.notes = append(rd.notes, note)
	}
	if rd.pending > 0 {
		return
	}
	m.round, m.lastSync = nil, time.Now()
	ok := rd.total - rd.failed
	switch {
	case len(rd.notes) == 0:
		m.setStatus(skOK, "All %s in sync with the registry", plural(rd.total, "host"))
	case rd.failed > 0:
		m.setStatus(skWarn, "Synced %d/%d · %s", ok, rd.total, strings.Join(rd.notes, " · "))
	default:
		m.setStatus(skInfo, "Synced · %s", strings.Join(rd.notes, " · "))
	}
}

// ---- remote operations -----------------------------------------------------

func opCmd(kind opKind, h *Host, sess, to, dir string, run func(context.Context, Conn) error) tea.Cmd {
	name, c := h.Name, h.conn()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		return opMsg{kind: kind, host: name, sess: sess, to: to, dir: dir, err: run(ctx, c)}
	}
}

func (m *model) runOp(cmd tea.Cmd) tea.Cmd {
	m.ops++
	return tea.Batch(cmd, m.startSpin())
}

func (m *model) onOp(msg opMsg) tea.Cmd {
	m.ops--
	h := m.cfg.host(msg.host)
	if msg.err != nil {
		verb := map[opKind]string{opCreate: "create", opRename: "rename", opKill: "kill", opRevoke: "revoke key on"}[msg.kind]
		target := msg.sess
		if msg.kind == opRevoke {
			target = msg.host
		}
		m.setStatus(skErr, "Couldn't %s %s: %v", verb, target, msg.err)
		if h != nil {
			return m.syncHost(h)
		}
		return nil
	}
	switch msg.kind {
	case opRevoke:
		m.setStatus(skOK, "Revoked moshpit key on %s", msg.host)
		return nil
	}
	if h == nil {
		return nil
	}
	switch msg.kind {
	case opCreate:
		s := h.session(msg.sess)
		if s == nil {
			s = &Session{Name: msg.sess, Origin: originApp, Created: time.Now()}
			h.Sessions = append(h.Sessions, s)
		}
		s.Gone, s.Command, s.Origin = false, msg.to, originApp
		s.Path = msg.dir
		s.live = &Live{Created: time.Now(), Activity: time.Now(), Windows: 1}
		h.sortSessions()
		m.save()
		if m.curHost() == h {
			m.selectSession(s.Name)
		}
		return m.attach(h, s)
	case opRename:
		if s := h.session(msg.sess); s != nil {
			s.Name = msg.to
		}
		h.sortSessions()
		m.save()
		m.selectSession(msg.to)
		m.setStatus(skOK, "Renamed %s → %s", msg.sess, msg.to)
	case opKill:
		h.removeSession(msg.sess)
		m.save()
		m.clamp()
		m.setStatus(skOK, "Killed %s on %s", msg.sess, h.Name)
	}
	return m.syncHost(h)
}

func (m *model) attach(h *Host, s *Session) tea.Cmd {
	tr := m.transportFor(h)
	if tr == transportMosh && !m.localMosh {
		m.setStatus(skErr, "mosh isn't installed locally (brew install mosh / apt install mosh), or press t to use ssh")
		return nil
	}
	cmd := attachCmd(h.conn(), tr, s.Name)
	tail := &tailBuf{}
	cmd.Stderr = io.MultiWriter(os.Stderr, tail)
	host, sess := h.Name, s.Name
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return attachDoneMsg{host: host, sess: sess, transport: tr, err: err, stderr: tail.lastLine()}
	})
}

func (m *model) onAttachDone(msg attachDoneMsg) tea.Cmd {
	h := m.cfg.host(msg.host)
	if msg.err != nil {
		detail := msg.stderr
		if detail == "" {
			detail = msg.err.Error()
		}
		hint := ""
		if msg.transport == transportMosh {
			hint = " (press t to switch this host to ssh)"
		}
		m.setStatus(skErr, "%s/%s: %s%s", msg.host, msg.sess, detail, hint)
	} else {
		m.setStatus(skOK, "Detached from %s/%s; it keeps running", msg.host, msg.sess)
	}
	if h != nil {
		return m.syncHost(h)
	}
	return nil
}

// ---- update ----------------------------------------------------------------

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		if m.form != nil {
			m.form.setWidth(m.dialogWidth() - 8)
		}
		return m, nil

	case clockMsg: // local only: refreshes relative times and expires the status line
		if m.status != "" && m.statusKind != skErr && time.Time(msg).Sub(m.statusAt) > 12*time.Second {
			m.status = ""
		}
		return m, clockTick()

	case spinMsg:
		if !m.busy() {
			m.spinning = false
			return m, nil
		}
		return m, spinTick()

	case syncMsg:
		m.onSync(msg)
		return m, nil

	case opMsg:
		return m, m.onOp(msg)

	case attachDoneMsg:
		return m, m.onAttachDone(msg)

	case bootLineMsg:
		if m.boot != nil {
			m.boot.lines = append(m.boot.lines, bootLine(msg))
			return m, waitBoot(m.boot.ch)
		}
		return m, nil

	case bootDoneMsg:
		return m, m.onBootDone(msg)

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" && m.mode != modeBoot {
			return m, tea.Quit
		}
		switch m.mode {
		case modeForm:
			return m, m.keyForm(msg)
		case modeConfirm:
			return m, m.keyConfirm(msg)
		case modeBoot:
			return m, m.keyBoot(msg)
		case modeHelp:
			m.mode = modeMain
			return m, nil
		}
		return m, m.keyMain(msg)
	}
	return m, nil
}

func (m *model) move(d int) {
	if m.focus == paneHosts {
		old := m.hi
		m.hi = max(0, min(len(m.cfg.Hosts)-1, m.hi+d))
		if m.hi != old {
			m.si, m.soff = 0, 0
		}
		return
	}
	if h := m.curHost(); h != nil {
		m.si = max(0, min(len(h.Sessions)-1, m.si+d))
	}
}

func (m *model) keyMain(k tea.KeyMsg) tea.Cmd {
	h, s := m.curHost(), m.curSess()
	switch k.String() {
	case "q":
		return tea.Quit
	case "?":
		m.mode = modeHelp
	case "tab":
		if m.focus == paneHosts && h != nil {
			m.focus = paneSessions
		} else {
			m.focus = paneHosts
		}
	case "left", "h":
		m.focus = paneHosts
	case "right", "l":
		if h != nil {
			m.focus = paneSessions
		}
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "pgup", "home", "g":
		m.move(-1 << 20)
	case "pgdown", "end", "G":
		m.move(1 << 20)
	case "enter":
		if h == nil {
			return m.openAddHost(nil)
		}
		if m.focus == paneHosts {
			m.focus = paneSessions
			return nil
		}
		if s == nil {
			return m.openNewSession(h)
		}
		if s.Gone {
			m.setStatus(skInfo, "Recreating %s on %s…", s.Name, h.Name)
			return m.runOp(opCmd(opCreate, h, s.Name, s.Command, s.Path, func(ctx context.Context, c Conn) error {
				_, err := ensureSession(ctx, c, s.Name, s.Path, s.Command)
				return err
			}))
		}
		return m.attach(h, s)
	case "a":
		return m.openAddHost(nil)
	case "n":
		if h != nil {
			return m.openNewSession(h)
		}
	case "r":
		if m.focus == paneHosts && h != nil {
			return m.openForm(&form{kind: fkRenameHost, title: "Rename host", sub: "Local name only; the server is not touched.", host: h.Name,
				fields: []field{newField(m.th, "Name", "", h.Name, h.Name, false)}})
		}
		if s != nil && !s.Gone {
			return m.openForm(&form{kind: fkRenameSession, title: "Rename session", sub: h.Name + " / " + s.Name, host: h.Name, sess: s.Name,
				fields: []field{newField(m.th, "New name", "letters, digits, - and _", s.Name, s.Name, false)}})
		}
	case "x", "d", "delete":
		if m.focus == paneHosts && h != nil {
			m.confirm = &confirm{kind: ckDeleteHost, host: h.Name, danger: true,
				title: "Remove host " + h.Name + "?",
				body:  "Sessions on the server keep running. Revoking also removes moshpit's key from the server's authorized_keys.",
				opts:  []confirmOpt{{"y", "remove + revoke key"}, {"l", "remove locally"}, {"esc", "cancel"}}}
			m.mode = modeConfirm
			return nil
		}
		if s != nil && s.Gone {
			h.removeSession(s.Name)
			m.save()
			m.clamp()
			m.setStatus(skOK, "Forgot %s", s.Name)
			return nil
		}
		if s != nil {
			m.confirm = &confirm{kind: ckKillSession, host: h.Name, sess: s.Name, danger: true,
				title: "Kill " + s.Name + "?",
				body:  "Ends the tmux session on " + h.Name + " and every process in it, including a running agent.",
				opts:  []confirmOpt{{"y", "kill session"}, {"esc", "cancel"}}}
			m.mode = modeConfirm
		}
	case "s", "ctrl+r":
		cmd := m.syncAll()
		if cmd != nil {
			m.setStatus(skInfo, "Syncing %s…", plural(len(m.cfg.Hosts), "host"))
		}
		return cmd
	case "t":
		if h != nil {
			h.Transport = map[string]string{transportAuto: transportMosh, transportMosh: transportSSH, transportSSH: transportAuto}[h.Transport]
			if h.Transport == "" {
				h.Transport = transportMosh
			}
			m.save()
			m.setStatus(skInfo, "%s: transport %s → connects with %s", h.Name, h.Transport, m.transportFor(h))
		}
	case "T":
		i := (themeIndex(m.th.Name) + 1) % len(themes)
		m.th = themes[i]
		m.cfg.Theme = m.th.Name
		m.save()
		m.setStatus(skInfo, "Theme: %s", m.th.Name)
	}
	return nil
}

// ---- forms -----------------------------------------------------------------

func (m *model) dialogWidth() int { return min(64, max(40, m.w-6)) }

func (m *model) openForm(f *form) tea.Cmd {
	f.setWidth(m.dialogWidth() - 8)
	f.focus(0)
	m.form, m.mode = f, modeForm
	return nil
}

func (m *model) openAddHost(prev *form) tea.Cmd {
	if prev != nil {
		return m.openForm(prev)
	}
	return m.openForm(&form{kind: fkAddHost, title: "Add host",
		sub: "Installs a moshpit SSH key, tmux and mosh. Only ssh is needed on the server.",
		fields: []field{
			newField(m.th, "Host", "IP, hostname, or an alias from ~/.ssh/config", "203.0.113.7", "", false),
			newField(m.th, "User", "", "root", "", false),
			newField(m.th, "Password", "used once to install the key and for sudo; never stored. Empty = use existing key", "", "", true),
			newField(m.th, "Port", "", "22", "", false),
			newField(m.th, "Name", "how it shows up here and in `ssh <name>`", "defaults to host", "", false),
		}})
}

func (m *model) openNewSession(h *Host) tea.Cmd {
	return m.openForm(&form{kind: fkNewSession, title: "New session on " + h.Name, host: h.Name,
		sub: "Starts a tmux session, runs the command, then attaches.",
		fields: []field{
			newField(m.th, "Name", "letters, digits, - and _", m.nextSessionName(h), m.nextSessionName(h), false),
			newField(m.th, "Directory", "on the server", "~", "~", false),
			newField(m.th, "Command", "empty = plain shell", m.cfg.DefaultCommand, m.cfg.DefaultCommand, false),
		}})
}

func (m *model) nextSessionName(h *Host) string {
	for i := 1; ; i++ {
		n := "agent-" + strconv.Itoa(i)
		if h.session(n) == nil {
			return n
		}
	}
}

func (m *model) keyForm(k tea.KeyMsg) tea.Cmd {
	f := m.form
	if k.String() == "esc" {
		m.form, m.mode = nil, modeMain
		return nil
	}
	cmd, submit := f.update(k)
	if !submit {
		return cmd
	}
	h := m.cfg.host(f.host)
	switch f.kind {
	case fkAddHost:
		return m.submitAddHost(f)
	case fkNewSession:
		if h == nil {
			break
		}
		name, dir, command := f.value(0), f.value(1), f.value(2)
		if dir == "" {
			dir = "~"
		}
		if !sessionNameRe.MatchString(name) {
			f.err = "Name: use letters, digits, - and _ (max 40)"
			return nil
		}
		if s := h.session(name); s != nil && !s.Gone {
			f.err = "A session named " + name + " already exists"
			return nil
		}
		m.form, m.mode = nil, modeMain
		m.setStatus(skInfo, "Starting %s on %s…", name, h.Name)
		return m.runOp(opCmd(opCreate, h, name, command, dir, func(ctx context.Context, c Conn) error {
			_, err := ensureSession(ctx, c, name, dir, command)
			return err
		}))
	case fkRenameSession:
		if h == nil {
			break
		}
		to := f.value(0)
		if !sessionNameRe.MatchString(to) {
			f.err = "Use letters, digits, - and _ (max 40)"
			return nil
		}
		if to == f.sess {
			break
		}
		if h.session(to) != nil {
			f.err = to + " already exists"
			return nil
		}
		from := f.sess
		m.form, m.mode = nil, modeMain
		return m.runOp(opCmd(opRename, h, from, to, "", func(ctx context.Context, c Conn) error {
			return renameSession(ctx, c, from, to)
		}))
	case fkRenameHost:
		if h == nil {
			break
		}
		to := f.value(0)
		if !hostNameRe.MatchString(to) {
			f.err = "Use letters, digits, ., - and _"
			return nil
		}
		if to != h.Name && m.cfg.host(to) != nil {
			f.err = to + " already exists"
			return nil
		}
		m.rt[to] = m.rtFor(h.Name)
		delete(m.rt, h.Name)
		h.Name = to
		m.save()
		m.refreshSSHConfig()
	}
	m.form, m.mode = nil, modeMain
	return nil
}

func (m *model) submitAddHost(f *form) tea.Cmd {
	addr, user, pw, portS, name := f.value(0), f.value(1), f.fields[2].in.Value(), f.value(3), f.value(4)
	if addr == "" || strings.HasPrefix(addr, "-") || strings.ContainsAny(addr, " \t@") {
		f.err = "Host: enter an IP, hostname or ssh alias (put the user in User)"
		f.focus(0)
		return nil
	}
	port := 0
	if portS != "" {
		p, err := strconv.Atoi(portS)
		if err != nil || p < 1 || p > 65535 {
			f.err = "Port: 1-65535"
			f.focus(3)
			return nil
		}
		if p != 22 {
			port = p
		}
	}
	isAlias := userHostAliases()[addr]
	if user == "" && !isAlias {
		user = "root"
	}
	if name == "" {
		name = addr
	}
	if !hostNameRe.MatchString(name) {
		f.err = "Name: letters, digits, ., - and _"
		f.focus(4)
		return nil
	}
	if m.cfg.host(name) != nil {
		f.err = name + " is already registered"
		f.focus(4)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan tea.Msg, 64)
	m.boot = &bootView{name: name, cancel: cancel, form: f, ch: ch}
	m.form, m.mode = nil, modeBoot
	req := addReq{Name: name, Address: addr, User: user, Password: pw, Port: port}
	go func() {
		h, err := runBootstrap(ctx, req, func(l bootLine) { ch <- bootLineMsg(l) })
		ch <- bootDoneMsg{host: h, err: err}
	}()
	return tea.Batch(waitBoot(ch), m.startSpin())
}

func waitBoot(ch chan tea.Msg) tea.Cmd { return func() tea.Msg { return <-ch } }

func (m *model) onBootDone(msg bootDoneMsg) tea.Cmd {
	b := m.boot
	if b == nil {
		return nil
	}
	b.done, b.err = true, msg.err
	b.cancel()
	if msg.err != nil {
		b.lines = append(b.lines, bootLine{"fail", msg.err.Error()})
		return nil
	}
	h := msg.host
	m.cfg.Hosts = append(m.cfg.Hosts, h)
	m.save()
	if m.refreshSSHConfig() {
		b.lines = append(b.lines, bootLine{"ok", "`ssh " + h.Name + "` now works from any terminal"})
	}
	b.form.fields[2].in.SetValue("") // drop the password as soon as it's no longer needed
	m.hi, m.si, m.focus = len(m.cfg.Hosts)-1, 0, paneSessions
	return m.syncHost(h)
}

// refreshSSHConfig regenerates the managed ssh aliases; reports whether the
// current host got one.
func (m *model) refreshSSHConfig() bool {
	named, err := syncSSHConfig(m.cfg.Hosts)
	if err != nil {
		m.setStatus(skWarn, "Couldn't update ssh config: %v", err)
		return false
	}
	h := m.curHost()
	for _, n := range named {
		if h != nil && n == h.Name {
			return true
		}
	}
	return false
}

func (m *model) keyBoot(k tea.KeyMsg) tea.Cmd {
	b := m.boot
	if !b.done {
		if k.String() == "ctrl+c" || k.String() == "esc" {
			b.cancel()
		}
		return nil
	}
	switch k.String() {
	case "enter", "esc", "q":
		m.boot, m.mode = nil, modeMain
		if b.err != nil && k.String() == "enter" {
			return m.openAddHost(b.form)
		}
	}
	return nil
}

func (m *model) keyConfirm(k tea.KeyMsg) tea.Cmd {
	c := m.confirm
	key := k.String()
	if key == "esc" || key == "n" || key == "q" {
		m.confirm, m.mode = nil, modeMain
		return nil
	}
	h := m.cfg.host(c.host)
	if h == nil {
		m.confirm, m.mode = nil, modeMain
		return nil
	}
	switch {
	case c.kind == ckKillSession && key == "y":
		m.confirm, m.mode = nil, modeMain
		name := c.sess
		return m.runOp(opCmd(opKill, h, name, "", "", func(ctx context.Context, cn Conn) error {
			return killSession(ctx, cn, name)
		}))
	case c.kind == ckDeleteHost && (key == "y" || key == "l"):
		m.confirm, m.mode = nil, modeMain
		var cmd tea.Cmd
		if key == "y" && h.Identity == getPaths().key {
			pub := readPubKey()
			cmd = m.runOp(opCmd(opRevoke, h, "", "", "", func(ctx context.Context, cn Conn) error {
				return revokeKey(ctx, cn, pub)
			}))
		}
		m.cfg.removeHost(h.Name)
		delete(m.rt, h.Name)
		m.save()
		m.refreshSSHConfig()
		m.clamp()
		m.focus = paneHosts
		m.setStatus(skOK, "Removed %s", c.host)
		return cmd
	}
	return nil
}
