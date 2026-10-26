package tray

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/piciuok/juhas/internal/config"
	"github.com/piciuok/juhas/internal/doctor"
	"github.com/piciuok/juhas/internal/queue"
	"github.com/piciuok/juhas/internal/state"
)

func saveSnapshot(store *state.Store, snap *queue.Snapshot) {
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return
	}
	tmp := store.SnapshotPath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, store.SnapshotPath())
	}
}

func reportPath(store *state.Store) string {
	return filepath.Join(filepath.Dir(store.Path()), "doctor.txt")
}

// writeReport saves the doctor report as text for "Diagnostics" to open.
func writeReport(store *state.Store, rep *doctor.Report) {
	var b strings.Builder
	fmt.Fprintf(&b, "Juhas — diagnostics %s\n", rep.At.Format("2006-01-02 15:04"))
	section := ""
	for _, c := range rep.Checks {
		if c.Section != section {
			section = c.Section
			fmt.Fprintf(&b, "\n%s\n", section)
		}
		fmt.Fprintf(&b, "  %s %s\n", c.Level.Symbol(), c.Msg)
		if c.Hint != "" && c.Level != doctor.OK {
			fmt.Fprintf(&b, "      → %s\n", c.Hint)
		}
	}
	fmt.Fprintf(&b, "\n%d ok, %d warnings, %d errors\n", rep.Count(doctor.OK), rep.Count(doctor.Warn), rep.Count(doctor.Fail))
	_ = os.WriteFile(reportPath(store), []byte(b.String()), 0o644)
}

// InstallBinary copies the running executable to ~/.local/bin/juhas
// (unless it already runs from there) and returns that path, so autostart
// and the guard hook never point into a build directory.
func InstallBinary() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	dst := filepath.Join(config.Home(), ".local", "bin", "juhas")
	if exe == dst {
		return dst, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	src, err := os.Open(exe)
	if err != nil {
		return "", err
	}
	defer src.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	return dst, os.Rename(tmp, dst)
}
