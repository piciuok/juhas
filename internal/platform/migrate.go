package platform

import (
	"bytes"
	"os"
	"path/filepath"
)

// legacyNames are what the app was called before it became Juhas, newest
// first: Kopaś, and the review-agent prototype.
var legacyNames = []string{"kopas", "review-agent"}

// MigrateLegacy moves ~/.config/<old> and ~/.local/state/<old> to their
// juhas names (only when the new one does not exist yet), removes the old
// autostart entries and binaries, and returns what it did. Stopping a
// still-running old tray is platform specific (stopLegacy).
func MigrateLegacy() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var done []string
	if stopLegacyFn() {
		done = append(done, "stopped the previous version (old LaunchAgent)")
	}
	var moves [][2]string
	for _, old := range legacyNames {
		moves = append(moves,
			[2]string{filepath.Join(home, ".config", old), filepath.Join(home, ".config", "juhas")},
			[2]string{filepath.Join(home, ".local", "state", old), filepath.Join(home, ".local", "state", "juhas")})
	}
	for _, d := range moves {
		if _, err := os.Stat(d[0]); err != nil {
			continue
		}
		if _, err := os.Stat(d[1]); err == nil {
			continue // already migrated (or both exist): leave the old one alone
		}
		if err := os.Rename(d[0], d[1]); err == nil {
			done = append(done, "moved "+d[0]+" → "+d[1])
			// state.json stores absolute run-log paths
			if sj := filepath.Join(d[1], "state.json"); fileExists(sj) {
				if b, err := os.ReadFile(sj); err == nil {
					_ = os.WriteFile(sj, bytes.ReplaceAll(b, []byte(d[0]+"/"), []byte(d[1]+"/")), 0o600)
				}
			}
		}
	}
	old := legacyAutostart(home)
	for _, n := range legacyNames {
		old = append(old, filepath.Join(home, ".local", "bin", n))
	}
	for _, f := range old {
		if err := os.Remove(f); err == nil {
			done = append(done, "removed "+f)
		}
	}
	return done
}

// stopLegacyFn is swapped out in tests: launchctl acts on the real user
// session whatever HOME says.
var stopLegacyFn = stopLegacy

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
