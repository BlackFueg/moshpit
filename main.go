// Command mp (moshpit) manages tmux sessions for coding agents on remote servers.
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// version is stamped by release builds (-ldflags "-X main.version=…"); `go install
// …@vX` builds fall back to the module version recorded in the binary.
var version = ""

func init() {
	if version != "" {
		return
	}
	version = "dev"
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		version = strings.TrimPrefix(bi.Main.Version, "v")
	}
}

func main() {
	if os.Getenv(askpassEnv) == "1" {
		os.Exit(askpass(os.Args[1:]))
	}
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = func() {
		p := getPaths()
		fmt.Fprintf(os.Stderr, `mp (moshpit %s): keep coding-agent sessions alive on remote servers.

Usage: mp                    open the session manager
       mp <session>          reattach to a session on whichever host has it
       mp <host> <session>   attach to a session on host, creating it if needed
                             (new sessions start in ~ running default_command)
       mp -version           print version

Files:
  %s   hosts, session registry, theme
  %s   ssh aliases (included from ~/.ssh/config)
  %s   moshpit's SSH key
`, version, p.config, p.sshInclude, p.key)
	}
	flag.Parse()
	if *showVersion {
		fmt.Println("moshpit", version)
		return
	}
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "mp:", err)
		os.Exit(1)
	}
	switch args := flag.Args(); len(args) {
	case 0:
	case 1:
		os.Exit(cliAttach(cfg, "", args[0]))
	case 2:
		os.Exit(cliAttach(cfg, args[0], args[1]))
	default:
		flag.Usage()
		os.Exit(2)
	}
	if _, err := tea.NewProgram(newModel(cfg), tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "mp:", err)
		os.Exit(1)
	}
}
