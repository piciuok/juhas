package doctor

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"juhas/internal/config"
)

func desktop(ctx context.Context, r *Report, cfg *config.Config) {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" && os.Getenv("XDG_RUNTIME_DIR") == "" {
		r.add(Warn, "Desktop", "no DBus session — the tray icon and notifications will not work (run in a graphical session)", "")
		return
	}
	if hasName(ctx, "org.kde.StatusNotifierWatcher") {
		r.add(OK, "Desktop", "tray: StatusNotifierWatcher available", "")
	} else {
		hint := "KDE/XFCE/Cinnamon have it built in; GNOME: AppIndicator extension (Ubuntu: by default, Fedora: sudo dnf install gnome-shell-extension-appindicator, then enable it in Extensions)"
		r.add(Fail, "Desktop", "tray: no StatusNotifierWatcher — the icon will not be visible", hint)
	}
	switch {
	case has("notify-send"):
		r.add(OK, "Desktop", "review prompts: notify-send with actions (libnotify ≥ 0.7.10), fallback zenity/kdialog", "")
	case has("zenity"), has("kdialog"):
		r.add(OK, "Desktop", "review prompts: dialog window (zenity/kdialog)", "sudo apt install libnotify-bin — notifications with actions")
	default:
		r.add(Warn, "Desktop", "no notify-send/zenity/kdialog — tray menu only", "sudo apt install libnotify-bin  |  sudo dnf install libnotify")
	}
	_ = cfg
}

func hasName(ctx context.Context, name string) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if has("busctl") {
		out, err := exec.CommandContext(ctx, "busctl", "--user", "--list", "--no-pager").Output()
		if err == nil {
			return strings.Contains(string(out), name)
		}
	}
	if has("dbus-send") {
		out, err := exec.CommandContext(ctx, "dbus-send", "--session", "--print-reply", "--dest=org.freedesktop.DBus",
			"/org/freedesktop/DBus", "org.freedesktop.DBus.NameHasOwner", "string:"+name).Output()
		return err == nil && strings.Contains(string(out), "boolean true")
	}
	return false
}

func has(bin string) bool { _, err := exec.LookPath(bin); return err == nil }
