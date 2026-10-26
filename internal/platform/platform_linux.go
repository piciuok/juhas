package platform

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

func Open(target string) error {
	return exec.Command("xdg-open", target).Start()
}

var (
	actionsOnce sync.Once
	actionsOK   bool
)

// notifySendActions: --action and --wait arrived in libnotify 0.7.10.
func notifySendActions() bool {
	actionsOnce.Do(func() {
		if !has("notify-send") {
			return
		}
		out, _ := exec.Command("notify-send", "--help").CombinedOutput()
		actionsOK = strings.Contains(string(out), "--action")
	})
	return actionsOK
}

func AskBackend(style string) string {
	switch {
	case style == "notification" && notifySendActions():
		return "notify-send"
	case style == "notification":
		return "notification"
	case has("zenity"):
		return "zenity"
	case has("kdialog"):
		return "kdialog"
	case notifySendActions():
		return "notify-send"
	}
	return "notification"
}

func Ask(q Question) Choice {
	q.defaults()
	ctx, cancel := context.WithTimeout(context.Background(), q.Timeout)
	defer cancel()
	msg := trunc(q.Message, 900)
	def := RunWith(q.Agents[0].Name)
	switch AskBackend(q.Style) {
	case "notify-send":
		args := []string{"-a", "juhas", "-u", "normal", "--wait"}
		for i, a := range q.Agents {
			args = append(args, "--action=run:"+a.Name+"="+q.runLabel(i))
		}
		args = append(args, "--action=skip="+q.SkipL, "--action=later="+q.LaterL, q.Title, trunc(msg, 300))
		out, _, err := output(ctx, "notify-send", args...)
		switch {
		case err != nil:
		case out == "default":
			return def
		case strings.HasPrefix(out, "run:"):
			return Choice(out)
		case out == "skip":
			return Skip
		}
		return Later
	case "zenity":
		args := []string{"--question", "--title=" + q.Title, "--text=" + msg, "--no-markup",
			"--ok-label=" + q.runLabel(0), "--cancel-label=" + q.LaterL}
		for i := range q.Agents[1:] {
			args = append(args, "--extra-button="+q.runLabel(i+1))
		}
		args = append(args, "--extra-button="+q.SkipL, "--timeout="+strconv.Itoa(int(q.Timeout.Seconds())))
		out, code, err := output(ctx, "zenity", args...)
		switch {
		case err != nil:
			return Later
		case code == 0:
			return def
		case out == q.SkipL:
			return Skip
		}
		for i, a := range q.Agents {
			if out == q.runLabel(i) {
				return RunWith(a.Name)
			}
		}
		return Later
	case "kdialog":
		args := []string{"--title", q.Title, "--menu", msg}
		for i, a := range q.Agents {
			args = append(args, "run:"+a.Name, q.runLabel(i))
		}
		args = append(args, "later", q.LaterL, "skip", q.SkipL)
		out, code, err := output(ctx, "kdialog", args...)
		switch {
		case err != nil || code != 0:
			return Later
		case strings.HasPrefix(out, "run:"):
			return Choice(out)
		case out == "skip":
			return Skip
		}
		return Later
	}
	Notify(Notification{Title: q.Title, Message: q.Message + " — choose in the tray icon menu"})
	return Later
}

// Info shows a message the user asked for (e.g. a manual update check) as a
// dialog when zenity or kdialog is there, else as a notification.
func Info(title, msg string) {
	msg = trunc(msg, 900)
	switch {
	case has("zenity"):
		_ = exec.Command("zenity", "--info", "--title", title, "--text", msg, "--timeout", "60").Run()
	case has("kdialog"):
		_ = exec.Command("kdialog", "--title", title, "--msgbox", msg).Run()
	default:
		Notify(Notification{Title: title, Message: msg})
	}
}

func Notify(n Notification) {
	if !has("notify-send") {
		return
	}
	base := []string{"-a", "juhas"}
	if n.Urgent {
		base = append(base, "-u", "critical", "-i", "weather-storm")
	}
	if n.Open != "" && notifySendActions() {
		go func() {
			// --wait blocks until the notification closes; servers that keep
			// notifications around would pile up one process per notification
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
			defer cancel()
			out, err := exec.CommandContext(ctx, "notify-send", append(base, "--wait", "--action=open=Open", "--", n.Title, trunc(n.Message, 300))...).Output()
			if a := strings.TrimSpace(string(out)); err == nil && (a == "open" || a == "default") {
				_ = Open(n.Open)
			}
		}()
		return
	}
	// "--": a PR title starting with "-" must not be read as an option
	_ = exec.Command("notify-send", append(base, "--", n.Title, trunc(n.Message, 300))...).Start()
}

func desktopPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "autostart", "juhas.desktop")
}

func AutostartEnabled() bool {
	_, err := os.Stat(desktopPath())
	return err == nil
}

// AutostartInstall writes an XDG autostart entry, which every desktop
// (GNOME, KDE, XFCE, Cinnamon) starts inside the graphical session.
func AutostartInstall(exe string) error {
	entry := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=Juhas
Comment=PR/MRs to review + AI review
Exec=%s tray
Icon=dialog-information
Terminal=false
X-GNOME-Autostart-enabled=true
X-GNOME-Autostart-Delay=10
`, desktopQuote(exe))
	if err := os.MkdirAll(filepath.Dir(desktopPath()), 0o755); err != nil {
		return err
	}
	return os.WriteFile(desktopPath(), []byte(entry), 0o644)
}

func AutostartUninstall() error {
	err := os.Remove(desktopPath())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func desktopQuote(s string) string {
	if !strings.ContainsAny(s, " \"'\\$`") {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\\\`, `"`, `\"`, "`", "\\`", "$", `\$`).Replace(s) + `"`
}

func legacyAutostart(home string) []string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		dir = filepath.Join(home, ".config")
	}
	var out []string
	for _, n := range legacyNames {
		out = append(out, filepath.Join(dir, "autostart", n+".desktop"))
	}
	return out
}

// stopLegacy ends a running old tray (started from XDG autostart).
func stopLegacy() bool {
	stopped := false
	for _, n := range legacyNames {
		if exec.Command("pkill", "-f", "/"+n+" tray").Run() == nil || exec.Command("pkill", "-x", n).Run() == nil {
			stopped = true
		}
	}
	return stopped
}
