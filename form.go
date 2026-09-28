package main

import (
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type formKind int

const (
	fkAddHost formKind = iota
	fkNewSession
	fkRenameSession
	fkRenameHost
)

type field struct {
	label, hint string
	in          textinput.Model
}

type form struct {
	kind        formKind
	title, sub  string
	fields      []field
	idx         int
	err         string
	host, sess  string // context the form acts on
}

func newField(th Theme, label, hint, placeholder, value string, secret bool) field {
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = placeholder
	in.SetValue(value)
	in.CharLimit = 256
	in.Cursor.SetMode(cursor.CursorStatic)
	if secret {
		in.EchoMode = textinput.EchoPassword
		in.EchoCharacter = '•'
	}
	in.TextStyle = lipgloss.NewStyle().Foreground(th.Text)
	in.PlaceholderStyle = lipgloss.NewStyle().Foreground(th.Faint)
	in.Cursor.Style = lipgloss.NewStyle().Foreground(th.Accent)
	in.Cursor.TextStyle = lipgloss.NewStyle().Foreground(th.Text)
	return field{label: label, hint: hint, in: in}
}

func (f *form) focus(i int) {
	for j := range f.fields {
		f.fields[j].in.Blur()
	}
	f.idx = (i + len(f.fields)) % len(f.fields)
	f.fields[f.idx].in.Focus()
	f.fields[f.idx].in.CursorEnd()
}

func (f *form) value(i int) string { return strings.TrimSpace(f.fields[i].in.Value()) }

func (f *form) setWidth(w int) {
	for i := range f.fields {
		f.fields[i].in.Width = w
	}
}

// update routes a key to the form; submit reports that the user confirmed the last field.
func (f *form) update(k tea.KeyMsg) (cmd tea.Cmd, submit bool) {
	switch k.String() {
	case "tab", "down":
		f.focus(f.idx + 1)
		return nil, false
	case "shift+tab", "up":
		f.focus(f.idx - 1)
		return nil, false
	case "enter":
		if f.idx == len(f.fields)-1 {
			return nil, true
		}
		f.focus(f.idx + 1)
		return nil, false
	case "ctrl+s":
		return nil, true
	}
	f.err = ""
	f.fields[f.idx].in, cmd = f.fields[f.idx].in.Update(k)
	return cmd, false
}

type confirmOpt struct{ key, label string }

type confirmKind int

const (
	ckKillSession confirmKind = iota
	ckDeleteHost
)

type confirm struct {
	kind        confirmKind
	title, body string
	opts        []confirmOpt
	danger      bool
	host, sess  string
}

type bootView struct {
	name   string
	lines  []bootLine
	done   bool
	err    error
	cancel func()
	form   *form // reopened on failure so the user can fix a typo
	ch     chan tea.Msg
}
