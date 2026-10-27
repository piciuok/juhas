// juhas watches GitHub/GitLab for pull/merge requests that need your
// review, shows them in the system tray and runs a read-only Claude review on demand.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/piciuok/juhas/internal/effects"
	"github.com/piciuok/juhas/internal/limits"
	"github.com/piciuok/juhas/internal/platform"
	"github.com/piciuok/juhas/internal/state"
)

const usage = `juhas — PR/MRs to review + AI review (macOS, Linux)

Usage:
  juhas [tray] [--foreground]  tray icon (default); from a terminal it starts in
                                      the background and returns
  juhas panel                  queue panel in the browser (needs the tray icon running)
  juhas poll [--dry-run]       check the queue once
  juhas review [--agent claude|codex] <url>
                                      run an AI review of a PR/MR
  juhas doctor [--with-ai]     check the CLIs, auth and repos
  juhas config ui              settings in the browser
  juhas config init            create the config (with mappings from scan_dirs)
  juhas config scan [--write] [--dir D]...
                                      find local clones and propose mappings
  juhas config show [<repo>]   show the settings after merging layers
  juhas update [--check] [--pre] [--force]
                                      update to the latest GitHub release
  juhas install | uninstall    start at login
  juhas zap                      show the "zap" effect (test)
  juhas version
`

var version = "dev"

func main() {
	platform.EnsurePath()
	limits.ClaudePath = filepath.Join(filepath.Dir(state.Open().Path()), "limits-claude.json")
	args := os.Args[1:]
	cmd := "tray"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "tray":
		err = cmdTray(args)
	case "install":
		err = cmdInstall(args)
	case "uninstall":
		err = cmdUninstall(args)
	case "config":
		err = cmdConfig(args)
	case "doctor":
		err = cmdDoctor(args)
	case "review":
		err = cmdReview(args)
	case "guard":
		err = cmdGuard(args)
	case "poll":
		err = cmdPoll(args)
	case "update":
		err = cmdUpdate(args)
	case "panel":
		err = cmdPanel(args)
	case "zap":
		if !effects.Demo() {
			fmt.Println("The screen effect is available on macOS; on Linux urgent MRs raise a critical notification.")
		}
	case "version", "--version":
		fmt.Println("juhas", version)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
