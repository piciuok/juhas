package doctor

import (
	"context"
	"os/exec"

	"github.com/piciuok/juhas/internal/config"
)

func desktop(_ context.Context, r *Report, cfg *config.Config) {
	switch {
	case has("alerter"):
		r.add(OK, "Desktop", "review prompts: alerter (actions in the notification)", "")
	case cfg.AskStyle == "notification":
		r.add(OK, "Desktop", "review prompts: plain notification + menu (ask_style=notification)", "")
	default:
		r.add(OK, "Desktop", "review prompts: dialog window (osascript)", "brew install vjeantet/tap/alerter — actions directly in the notification")
	}
	if has("terminal-notifier") {
		r.add(OK, "Desktop", "notifications: terminal-notifier (click opens the brief)", "")
	} else {
		r.add(Warn, "Desktop", "notifications: osascript (click does not open the brief)", "brew install terminal-notifier")
	}
}

func has(bin string) bool { _, err := exec.LookPath(bin); return err == nil }
