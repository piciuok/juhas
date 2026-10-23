// Package platform hides the macOS/Linux differences: PATH, opening files,
// notifications, dialogs and autostart.
package platform

import (
	"os"
	"path/filepath"
	"strings"
)

// EnsurePath appends the usual user tool locations to PATH. Apps started from
// launchd, the Dock or an XDG autostart entry get a minimal PATH that misses
// Homebrew and ~/.local/bin, where gh, glab and claude live.
func EnsurePath() {
	home, _ := os.UserHomeDir()
	extra := []string{
		"/opt/homebrew/bin", "/usr/local/bin",
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".claude", "local"),
		filepath.Join(home, "go", "bin"),
		"/home/linuxbrew/.linuxbrew/bin",
		"/snap/bin",
		"/usr/bin", "/bin", "/usr/sbin", "/sbin",
	}
	cur := filepath.SplitList(os.Getenv("PATH"))
	have := map[string]bool{}
	for _, p := range cur {
		have[p] = true
	}
	for _, p := range extra {
		if !have[p] {
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				cur = append(cur, p)
				have[p] = true
			}
		}
	}
	os.Setenv("PATH", strings.Join(cur, string(os.PathListSeparator)))
}
