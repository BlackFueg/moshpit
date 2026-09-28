package main

import "github.com/charmbracelet/lipgloss"

type Theme struct {
	Name     string
	Accent   lipgloss.Color // focus, keys, brand
	Accent2  lipgloss.Color // moshpit-created marker, secondary highlights
	Text     lipgloss.Color
	Muted    lipgloss.Color
	Faint    lipgloss.Color
	Border   lipgloss.Color
	Good     lipgloss.Color
	Warn     lipgloss.Color
	Bad      lipgloss.Color
	SelBg    lipgloss.Color
	OnAccent lipgloss.Color // text drawn on an Accent background
}

var themes = []Theme{
	{"tokyo-night", "#7aa2f7", "#bb9af7", "#c0caf5", "#a9b1d6", "#565f89", "#3b4261", "#9ece6a", "#e0af68", "#f7768e", "#292e42", "#1a1b26"},
	{"catppuccin", "#89b4fa", "#cba6f7", "#cdd6f4", "#bac2de", "#6c7086", "#45475a", "#a6e3a1", "#f9e2af", "#f38ba8", "#313244", "#1e1e2e"},
	{"gruvbox", "#fe8019", "#d3869b", "#ebdbb2", "#d5c4a1", "#7c6f64", "#504945", "#b8bb26", "#fabd2f", "#fb4934", "#3c3836", "#282828"},
	{"nord", "#88c0d0", "#b48ead", "#eceff4", "#d8dee9", "#616e88", "#434c5e", "#a3be8c", "#ebcb8b", "#bf616a", "#3b4252", "#2e3440"},
	{"rose-pine", "#ebbcba", "#c4a7e7", "#e0def4", "#bfbacf", "#6e6a86", "#403d52", "#9ccfd8", "#f6c177", "#eb6f92", "#26233a", "#191724"},
}

func themeIndex(name string) int {
	for i, t := range themes {
		if t.Name == name {
			return i
		}
	}
	return 0
}

// dimmed flattens the palette so an open dialog stands out from the screen behind it.
func (t Theme) dimmed() Theme {
	d := t
	d.Accent, d.Accent2, d.Text, d.Muted, d.Good, d.Warn, d.Bad = t.Faint, t.Faint, t.Faint, t.Faint, t.Faint, t.Faint, t.Faint
	d.Border = t.SelBg
	return d
}
