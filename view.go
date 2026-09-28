package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func spinFrame() string { return spinFrames[int(time.Now().UnixMilli()/90)%len(spinFrames)] }

func (m *model) fg(c lipgloss.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }

// seg is a plain-text span with a style; rows are built from segs so a
// selection background can be applied to every span (nested ANSI resets
// would otherwise punch holes in it).
type seg struct {
	t string
	s lipgloss.Style
}

func segsWidth(ss []seg) int {
	w := 0
	for _, s := range ss {
		w += ansi.StringWidth(s.t)
	}
	return w
}

func (m *model) row(left, right []seg, w int, sel bool) string {
	var b strings.Builder
	bg := func(s lipgloss.Style) lipgloss.Style {
		if sel {
			return s.Background(m.th.SelBg)
		}
		return s
	}
	rw := segsWidth(right)
	if rw > w/2 {
		right, rw = nil, 0
	}
	lw := w - rw
	used := 0
	for _, sg := range left {
		if used >= lw {
			break
		}
		t := sg.t
		if used+ansi.StringWidth(t) > lw {
			t = ansi.Truncate(t, lw-used, "…")
		}
		b.WriteString(bg(sg.s).Render(t))
		used += ansi.StringWidth(t)
	}
	if fill := w - used - rw; fill > 0 {
		b.WriteString(bg(lipgloss.NewStyle()).Render(strings.Repeat(" ", fill)))
	}
	for _, sg := range right {
		b.WriteString(bg(sg.s).Render(sg.t))
	}
	return b.String()
}

// cell pads or truncates plain text to exactly w cells.
func cell(t string, w int) string {
	if w <= 0 {
		return ""
	}
	if ansi.StringWidth(t) > w {
		t = ansi.Truncate(t, w, "…")
	}
	return t + strings.Repeat(" ", w-ansi.StringWidth(t))
}

// fit pads or truncates a styled line to exactly w cells.
func fit(s string, w int) string {
	if sw := ansi.StringWidth(s); sw > w {
		return ansi.Truncate(s, w, "…")
	} else if sw < w {
		return s + strings.Repeat(" ", w-sw)
	}
	return s
}

func (m *model) panel(title string, body []string, w, h int, focused bool) string {
	bc := m.fg(m.th.Border)
	if focused {
		bc = m.fg(m.th.Accent)
	}
	inner := w - 2
	title = ansi.Truncate(title, max(0, inner-4), "…")
	top := bc.Render("╭─") + " " + title + " " + bc.Render(strings.Repeat("─", max(0, inner-3-ansi.StringWidth(title)))+"╮")
	var b strings.Builder
	b.WriteString(top)
	for i := range h - 2 {
		l := ""
		if i < len(body) {
			l = body[i]
		}
		b.WriteString("\n" + bc.Render("│") + fit(l, inner) + bc.Render("│"))
	}
	b.WriteString("\n" + bc.Render("╰"+strings.Repeat("─", inner)+"╯"))
	return b.String()
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < 5*time.Second:
		return "now"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func (m *model) shortPath(h *Host, p string) string {
	if h.Home != "" && (p == h.Home || strings.HasPrefix(p, h.Home+"/")) {
		return "~" + p[len(h.Home):]
	}
	return p
}

// ---- screen ----------------------------------------------------------------

func (m *model) View() string {
	if m.w == 0 {
		return ""
	}
	if m.w < 60 || m.h < 14 {
		return lipgloss.Place(m.w, m.h, lipgloss.Center, lipgloss.Center,
			m.fg(m.th.Warn).Render(fmt.Sprintf("Terminal too small (%d×%d); need 60×14", m.w, m.h)))
	}
	if m.mode == modeMain {
		return m.viewMain()
	}
	th := m.th
	m.th = th.dimmed()
	base := m.viewMain()
	m.th = th
	var dlg string
	switch m.mode {
	case modeForm:
		dlg = m.viewForm()
	case modeConfirm:
		dlg = m.viewConfirm()
	case modeBoot:
		dlg = m.viewBoot()
	case modeHelp:
		dlg = m.viewHelp()
	}
	return overlay(base, dlg, m.w, m.h)
}

func (m *model) viewMain() string {
	bodyH := m.h - 3
	var body string
	if len(m.cfg.Hosts) == 0 {
		body = m.viewEmpty(bodyH)
	} else {
		hw := min(34, max(24, m.w/3))
		body = lipgloss.JoinHorizontal(lipgloss.Top,
			m.viewHosts(hw, bodyH),
			m.viewSessions(m.w-hw, bodyH))
	}
	return strings.Join([]string{m.viewHeader(), body, m.viewStatus(), m.viewFooter()}, "\n")
}

func (m *model) viewHeader() string {
	brand := lipgloss.NewStyle().Bold(true).Foreground(m.th.OnAccent).Background(m.th.Accent).Render(" ◆ MOSHPIT ")
	tag := m.fg(m.th.Faint).Render("  agent sessions · tmux + mosh")
	var right string
	syncing := 0
	for _, r := range m.rt {
		if r.syncing {
			syncing++
		}
	}
	switch {
	case syncing > 0:
		right = m.fg(m.th.Accent).Render(spinFrame()) + m.fg(m.th.Muted).Render(" syncing "+plural(syncing, "host"))
	case m.ops > 0:
		right = m.fg(m.th.Accent).Render(spinFrame()) + m.fg(m.th.Muted).Render(" working")
	case !m.lastSync.IsZero():
		right = m.fg(m.th.Good).Render("✓") + m.fg(m.th.Faint).Render(" synced "+m.lastSync.Format("15:04:05"))
	}
	right += " "
	gap := m.w - ansi.StringWidth(brand) - ansi.StringWidth(tag) - ansi.StringWidth(right)
	if gap < 1 {
		return fit(brand+tag, m.w)
	}
	return brand + tag + strings.Repeat(" ", gap) + right
}

func (m *model) viewEmpty(h int) string {
	msg := lipgloss.JoinVertical(lipgloss.Center,
		m.fg(m.th.Accent).Bold(true).Render("No hosts yet"),
		"",
		m.fg(m.th.Muted).Render("Add a server with just its address and a password."),
		m.fg(m.th.Muted).Render("moshpit installs a key, tmux and mosh for you."),
		"",
		m.fg(m.th.Faint).Render("press ")+m.fg(m.th.Accent).Bold(true).Render("a")+m.fg(m.th.Faint).Render(" to add a host"),
	)
	return m.panel(m.fg(m.th.Text).Bold(true).Render("Hosts"),
		strings.Split(lipgloss.Place(m.w-2, h-2, lipgloss.Center, lipgloss.Center, msg), "\n"), m.w, h, true)
}

func (m *model) hostDot(h *Host) seg {
	r := m.rt[h.Name]
	switch {
	case r != nil && r.syncing:
		return seg{spinFrame(), m.fg(m.th.Accent)}
	case r == nil || !r.known:
		return seg{"◌", m.fg(m.th.Faint)}
	case !r.online:
		return seg{"●", m.fg(m.th.Bad)}
	case r.err != "":
		return seg{"▲", m.fg(m.th.Warn)}
	}
	return seg{"●", m.fg(m.th.Good)}
}

func (m *model) viewHosts(w, h int) string {
	inner := w - 2
	focused := m.focus == paneHosts
	per := 2
	visible := max(1, (h-2)/per)
	m.hoff = scrollOffset(m.hi, m.hoff, visible, len(m.cfg.Hosts))
	var lines []string
	for i := m.hoff; i < len(m.cfg.Hosts) && i < m.hoff+visible; i++ {
		ho := m.cfg.Hosts[i]
		sel := i == m.hi
		bar := seg{" ", lipgloss.NewStyle()}
		if sel {
			c := m.th.Faint
			if focused {
				c = m.th.Accent
			}
			bar = seg{"▌", m.fg(c)}
		}
		count := ""
		if r := m.rt[ho.Name]; r != nil && r.known && !r.online {
			count = "offline"
		} else if n := ho.liveCount(); n > 0 {
			count = fmt.Sprint(n)
		}
		name := m.fg(m.th.Text).Bold(true)
		if sel && focused {
			name = name.Foreground(m.th.Accent)
		}
		lines = append(lines,
			m.row([]seg{bar, m.hostDot(ho), {" ", lipgloss.NewStyle()}, {ho.Name, name}},
				[]seg{{count + " ", m.fg(m.th.Muted)}}, inner, sel),
			m.row([]seg{bar, {"  " + ho.conn().dest() + " · " + m.transportFor(ho), m.fg(m.th.Faint)}}, nil, inner, sel))
	}
	title := m.fg(m.th.Text).Bold(true).Render("Hosts") + m.fg(m.th.Faint).Render(fmt.Sprintf(" %d", len(m.cfg.Hosts)))
	return m.panel(title, lines, w, h, focused)
}

func scrollOffset(sel, off, visible, n int) int {
	if sel < off {
		off = sel
	}
	if sel >= off+visible {
		off = sel - visible + 1
	}
	return max(0, min(off, max(0, n-visible)))
}

func (m *model) viewSessions(w, h int) string {
	ho := m.curHost()
	inner := w - 2
	focused := m.focus == paneSessions
	r := m.rtFor(ho.Name)

	tr := m.transportFor(ho)
	title := m.fg(m.th.Text).Bold(true).Render(ho.Name) +
		m.fg(m.th.Faint).Render(" · "+ho.conn().dest()+" · ") + m.fg(m.th.Accent2).Render(tr)
	if ho.Transport == transportAuto {
		title += m.fg(m.th.Faint).Render(" (auto)")
	}

	var lines []string
	center := func(parts ...string) []string {
		return strings.Split(lipgloss.Place(inner, h-2, lipgloss.Center, lipgloss.Center, lipgloss.JoinVertical(lipgloss.Center, parts...)), "\n")
	}
	switch {
	case r.known && !r.online && len(ho.Sessions) == 0:
		return m.panel(title, center(
			m.fg(m.th.Bad).Render("✕ Can't reach "+ho.Name),
			m.fg(m.th.Muted).Render(r.err), "",
			m.fg(m.th.Faint).Render("s retry · t switch transport · x remove host")), w, h, focused)
	case len(ho.Sessions) == 0 && (r.syncing || !r.known):
		return m.panel(title, center(m.fg(m.th.Accent).Render(spinFrame())+m.fg(m.th.Muted).Render(" Checking "+ho.Name+"…")), w, h, focused)
	case len(ho.Sessions) == 0:
		return m.panel(title, center(
			m.fg(m.th.Muted).Render("No tmux sessions on "+ho.Name), "",
			m.fg(m.th.Faint).Render("press ")+m.fg(m.th.Accent).Bold(true).Render("n")+m.fg(m.th.Faint).Render(" to start one running `"+m.cfg.DefaultCommand+"`")), w, h, focused)
	}

	// Columns collapse right-to-left as the pane narrows.
	const wMark, wState, wWin, wAct = 3, 11, 4, 7
	showCmd, showDir := inner >= 58, inner >= 74
	wCmd, wDir := 0, 0
	if showCmd {
		wCmd = 10
	}
	flex := inner - wMark - wState - wWin - wAct - wCmd
	wName := flex
	if showDir {
		wName = max(12, flex*2/5)
		wDir = flex - wName
	}
	head := m.fg(m.th.Faint).Bold(true)
	hdr := []seg{{"   ", head}, {cell("SESSION", wName), head}, {cell("STATE", wState), head}, {cell("WIN", wWin), head}, {cell("ACTIVE", wAct), head}}
	if showCmd {
		hdr = append(hdr, seg{cell("RUNNING", wCmd), head})
	}
	if showDir {
		hdr = append(hdr, seg{cell("DIR", wDir), head})
	}
	lines = append(lines, m.row(hdr, nil, inner, false))

	listH := h - 2 - 1 - 2 // header row + detail footer
	m.soff = scrollOffset(m.si, m.soff, listH, len(ho.Sessions))
	for i := m.soff; i < len(ho.Sessions) && i < m.soff+listH; i++ {
		s := ho.Sessions[i]
		sel := i == m.si && focused
		bar := seg{" ", lipgloss.NewStyle()}
		if i == m.si {
			c := m.th.Faint
			if focused {
				c = m.th.Accent
			}
			bar = seg{"▌", m.fg(c)}
		}
		mark := seg{"◆ ", m.fg(m.th.Accent2)}
		if s.Origin != originApp {
			mark = seg{"◇ ", m.fg(m.th.Faint)}
		}
		nameSt := m.fg(m.th.Text)
		var state seg
		win, act, cmd, dir := "—", "—", "", s.Path
		switch {
		case s.Gone:
			state = seg{cell("✕ gone", wState), m.fg(m.th.Bad)}
			nameSt = m.fg(m.th.Muted).Strikethrough(true)
			cmd = s.Command
		case s.live != nil && s.live.Attached > 0:
			state = seg{cell("● attached", wState), m.fg(m.th.Good)}
		case s.live != nil:
			state = seg{cell("○ detached", wState), m.fg(m.th.Muted)}
		default:
			state = seg{cell("◌ unknown", wState), m.fg(m.th.Faint)}
		}
		if s.live != nil {
			win, act, cmd = fmt.Sprint(s.live.Windows), ago(s.live.Activity), s.live.Cmd
		}
		if sel {
			nameSt = nameSt.Bold(true)
		}
		cells := []seg{bar, mark, {cell(s.Name, wName), nameSt}, state,
			{cell(win, wWin), m.fg(m.th.Muted)}, {cell(act, wAct), m.fg(m.th.Muted)}}
		if showCmd {
			cs := m.fg(m.th.Muted)
			if cmd != "" && cmd != "bash" && cmd != "zsh" && cmd != "sh" && cmd != "fish" && !s.Gone {
				cs = m.fg(m.th.Accent)
			}
			cells = append(cells, seg{cell(cmd, wCmd), cs})
		}
		if showDir {
			cells = append(cells, seg{cell(m.shortPath(ho, dir), wDir), m.fg(m.th.Faint)})
		}
		lines = append(lines, m.row(cells, nil, inner, sel))
	}

	// Detail footer pinned to the bottom of the panel.
	for len(lines) < h-4 {
		lines = append(lines, "")
	}
	lines = append(lines, m.fg(m.th.Border).Render(strings.Repeat("─", inner)), " "+m.sessionDetail(ho, m.curSess()))
	return m.panel(title, lines, w, h, focused)
}

func (m *model) sessionDetail(h *Host, s *Session) string {
	f, mu := m.fg(m.th.Faint), m.fg(m.th.Muted)
	if s == nil {
		return f.Render(h.OS)
	}
	if s.Gone {
		what := "shell"
		if s.Command != "" {
			what = "`" + s.Command + "`"
		}
		return m.fg(m.th.Bad).Render("gone") + f.Render(" · ") + mu.Render("⏎ recreates it in "+m.shortPath(h, orHome(s.Path))+" running "+what) + f.Render(" · x forget")
	}
	origin := "created with moshpit"
	if s.Origin != originApp {
		origin = "found on server"
	}
	parts := []string{origin}
	if s.live != nil && !s.live.Created.IsZero() {
		parts = append(parts, "started "+ago(s.live.Created)+" ago")
	}
	if s.live != nil && s.live.Attached > 0 {
		parts = append(parts, plural(s.live.Attached, "client")+" attached")
	}
	return mu.Render(strings.Join(parts, " · "))
}

func orHome(p string) string {
	if p == "" {
		return "~"
	}
	return p
}

func (m *model) viewStatus() string {
	if m.status == "" {
		return ""
	}
	icon, c := "•", m.th.Accent
	switch m.statusKind {
	case skOK:
		icon, c = "✓", m.th.Good
	case skWarn:
		icon, c = "▲", m.th.Warn
	case skErr:
		icon, c = "✕", m.th.Bad
	}
	return fit(" "+m.fg(c).Render(icon)+" "+m.fg(m.th.Text).Render(m.status), m.w)
}

func (m *model) keys(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, m.fg(m.th.Accent).Bold(true).Render(pairs[i])+" "+m.fg(m.th.Faint).Render(pairs[i+1]))
	}
	return strings.Join(parts, m.fg(m.th.Border).Render("  "))
}

func (m *model) viewFooter() string {
	var k string
	switch {
	case len(m.cfg.Hosts) == 0:
		k = m.keys("a", "add host", "T", "theme", "q", "quit")
	case m.focus == paneHosts:
		k = m.keys("↑↓", "select", "⏎", "sessions", "n", "new", "a", "add host", "r", "rename", "x", "remove", "t", "transport", "s", "sync", "?", "help", "q", "quit")
	default:
		enter := "attach"
		if s := m.curSess(); s != nil && s.Gone {
			enter = "recreate"
		}
		k = m.keys("↑↓", "select", "⏎", enter, "n", "new", "r", "rename", "x", "kill", "←", "hosts", "s", "sync", "?", "help", "q", "quit")
	}
	return fit(" "+k, m.w)
}

// ---- dialogs ---------------------------------------------------------------

func (m *model) dialog(title string, body string, accent lipgloss.Color) string {
	w := m.dialogWidth()
	head := m.fg(accent).Bold(true).Render(title)
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).BorderForeground(accent).
		Padding(1, 2).Width(w).
		Render(head + "\n" + body)
}

func (m *model) viewForm() string {
	f := m.form
	var b strings.Builder
	if f.sub != "" {
		b.WriteString(m.fg(m.th.Muted).Width(m.dialogWidth() - 6).Render(f.sub))
		b.WriteString("\n")
	}
	for i, fd := range f.fields {
		focused := i == f.idx
		lab, prompt := m.fg(m.th.Muted), m.fg(m.th.Border).Render("  ")
		if focused {
			lab, prompt = m.fg(m.th.Accent).Bold(true), m.fg(m.th.Accent).Render("› ")
		}
		b.WriteString("\n" + lab.Render(fd.label))
		if fd.hint != "" && focused {
			b.WriteString(m.fg(m.th.Faint).Render("  " + ansi.Truncate(fd.hint, m.dialogWidth()-10-ansi.StringWidth(fd.label), "…")))
		}
		b.WriteString("\n" + prompt + fd.in.View())
	}
	b.WriteString("\n")
	if f.err != "" {
		b.WriteString("\n" + m.fg(m.th.Bad).Render("✕ "+f.err))
	}
	submit := "next"
	if f.idx == len(f.fields)-1 {
		submit = "submit"
	}
	b.WriteString("\n" + m.keys("⏎", submit, "⇥", "field", "ctrl+s", "submit", "esc", "cancel"))
	return m.dialog(f.title, b.String(), m.th.Accent)
}

func (m *model) viewConfirm() string {
	c := m.confirm
	accent := m.th.Accent
	if c.danger {
		accent = m.th.Bad
	}
	var opts []string
	for _, o := range c.opts {
		opts = append(opts, o.key, o.label)
	}
	body := "\n" + m.fg(m.th.Muted).Width(m.dialogWidth()-6).Render(c.body) + "\n\n" + m.keys(opts...)
	return m.dialog(c.title, body, accent)
}

func (m *model) viewBoot() string {
	b := m.boot
	var out []string
	var raw []string
	lastStep := -1
	for i, l := range b.lines {
		if l.kind == "step" {
			lastStep = i
		}
	}
	for i, l := range b.lines {
		last := i == lastStep
		switch l.kind {
		case "step":
			icon := m.fg(m.th.Good).Render("✓")
			switch {
			case last && !b.done:
				icon = m.fg(m.th.Accent).Render(spinFrame())
			case last && b.err != nil:
				icon = m.fg(m.th.Bad).Render("✕")
			}
			out = append(out, icon+" "+m.fg(m.th.Text).Render(l.text))
			raw = nil
		case "ok":
			out = append(out, m.fg(m.th.Faint).Render("  └ ")+m.fg(m.th.Muted).Render(l.text))
		case "info":
			out = append(out, m.fg(m.th.Faint).Render("  └ "+l.text))
		case "warn":
			out = append(out, m.fg(m.th.Warn).Render("  ▲ "+l.text))
		case "fail":
			out = append(out, m.fg(m.th.Bad).Render("✕ "+l.text))
		case "raw":
			raw = append(raw, l.text)
			if len(raw) > 3 {
				raw = raw[1:]
			}
		}
	}
	if !b.done {
		for _, r := range raw {
			out = append(out, m.fg(m.th.Faint).Render("    "+ansi.Truncate(r, m.dialogWidth()-12, "…")))
		}
	}
	// Keep the tail visible if the log outgrows the screen.
	if maxL := m.h - 10; len(out) > maxL {
		out = out[len(out)-maxL:]
	}
	body := "\n" + strings.Join(out, "\n") + "\n\n"
	title, accent := "Adding "+b.name, m.th.Accent
	switch {
	case !b.done:
		body += m.keys("esc", "cancel")
	case b.err != nil:
		title, accent = "Couldn't add "+b.name, m.th.Bad
		body += m.keys("⏎", "edit & retry", "esc", "close")
	default:
		title, accent = b.name+" is ready", m.th.Good
		body += m.keys("⏎", "continue")
	}
	return m.dialog(title, body, accent)
}

func (m *model) viewHelp() string {
	rows := [][2]string{
		{"↑↓ / j k", "move"}, {"⇥ / ← →", "switch pane"},
		{"⏎", "attach · recreate a gone session"}, {"n", "new session"},
		{"r", "rename session / host"}, {"x", "kill session · forget gone · remove host"},
		{"a", "add host"}, {"t", "transport: auto → mosh → ssh"},
		{"s", "sync all hosts now"}, {"T", "cycle theme"}, {"q", "quit"},
	}
	var b strings.Builder
	b.WriteString("\n")
	for _, r := range rows {
		b.WriteString(m.fg(m.th.Accent).Bold(true).Render(cell(r[0], 12)) + m.fg(m.th.Muted).Render(r[1]) + "\n")
	}
	b.WriteString("\n" + m.fg(m.th.Accent2).Render("◆") + m.fg(m.th.Faint).Render(" created with moshpit   ") +
		m.fg(m.th.Faint).Render("◇ found on server (another machine, or started by hand)"))
	b.WriteString("\n" + m.fg(m.th.Faint).Render("Detach from a session with ctrl-b d. Sessions keep running."))
	b.WriteString("\n" + m.fg(m.th.Faint).Render("Hosts sync on launch and when you press s; drift is reported below the panes."))
	b.WriteString("\n" + m.fg(m.th.Faint).Render("From a shell: mp <session> reattaches · mp <host> <session> creates or attaches."))
	b.WriteString("\n\n" + m.fg(m.th.Faint).Render("any key to close · theme "+m.th.Name+" · moshpit "+version))
	return m.dialog("Keys", b.String(), m.th.Accent)
}

// overlay draws a dialog centered over the base screen.
func overlay(base, top string, w, h int) string {
	bl := strings.Split(base, "\n")
	for len(bl) < h {
		bl = append(bl, "")
	}
	tl := strings.Split(top, "\n")
	tw := 0
	for _, l := range tl {
		tw = max(tw, ansi.StringWidth(l))
	}
	x, y := max(0, (w-tw)/2), max(0, (h-len(tl))/2)
	for i, line := range tl {
		r := y + i
		if r >= len(bl) {
			break
		}
		b := bl[r]
		left := ansi.Truncate(b, x, "")
		if lw := ansi.StringWidth(left); lw < x {
			left += strings.Repeat(" ", x-lw)
		}
		right := ansi.TruncateLeft(b, x+tw, "")
		bl[r] = left + "\x1b[0m" + fit(line, tw) + "\x1b[0m" + right
	}
	return strings.Join(bl, "\n")
}
