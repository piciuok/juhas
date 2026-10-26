package platform

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const launchLabel = "pl.piciuok.juhas"

func Open(target string) error {
	return exec.Command("/usr/bin/open", target).Start()
}

// AskBackend names what Ask will use.
func AskBackend(style string) string {
	switch {
	case has("alerter"):
		return "alerter"
	case style == "notification":
		return "notification"
	}
	return "osascript"
}

// Ask blocks until the user answers or the timeout passes (Later).
func Ask(q Question) Choice {
	q.defaults()
	ctx, cancel := context.WithTimeout(context.Background(), q.Timeout)
	defer cancel()
	switch AskBackend(q.Style) {
	case "alerter":
		var actions []string
		for i := range q.Agents {
			actions = append(actions, q.runLabel(i))
		}
		actions = append(actions, q.SkipL)
		out, _, err := output(ctx, "alerter", "-title", q.Title, "-message", trunc(q.Message, 240),
			"-actions", strings.Join(actions, ","), "-dropdownLabel", q.RunL, "-closeLabel", q.LaterL,
			"-timeout", strconv.Itoa(int(q.Timeout.Seconds())), "-group", "juhas")
		if err == nil {
			if out == "@CONTENTCLICKED" {
				return RunWith(q.Agents[0].Name)
			}
			for i, a := range q.Agents {
				if out == q.runLabel(i) {
					return RunWith(a.Name)
				}
			}
			if out == q.SkipL {
				return Skip
			}
		}
		return Later
	case "notification":
		Notify(Notification{Title: q.Title, Message: q.Message + " — choose in the menu bar icon"})
		return Later
	}
	if len(q.Agents) == 1 {
		script := fmt.Sprintf(`display dialog %s with title %s buttons {%s, %s, %s} default button %s cancel button %s with icon note giving up after %d`,
			asq(trunc(q.Message, 900)), asq(q.Title), asq(q.SkipL), asq(q.LaterL), asq(q.RunL), asq(q.RunL), asq(q.LaterL), int(q.Timeout.Seconds()))
		out, _, err := output(ctx, "/usr/bin/osascript", "-e", script)
		switch {
		case err != nil, strings.Contains(out, "gave up:true"):
			return Later
		case strings.Contains(out, "button returned:"+q.RunL):
			return RunWith(q.Agents[0].Name)
		case strings.Contains(out, "button returned:"+q.SkipL):
			return Skip
		}
		return Later
	}
	// More than three answers: a list, the default agent preselected.
	var items []string
	for i := range q.Agents {
		items = append(items, asq("▶ "+q.runLabel(i)))
	}
	items = append(items, asq("✕ "+q.SkipL))
	script := fmt.Sprintf(`choose from list {%s} with title %s with prompt %s default items {%s} OK button name "Choose" cancel button name %s`,
		strings.Join(items, ", "), asq(q.Title), asq(trunc(q.Message, 900)), items[0], asq(q.LaterL))
	out, _, err := output(ctx, "/usr/bin/osascript", "-e", script)
	if err != nil || out == "false" {
		return Later
	}
	for i, a := range q.Agents {
		if out == "▶ "+q.runLabel(i) {
			return RunWith(a.Name)
		}
	}
	if out == "✕ "+q.SkipL {
		return Skip
	}
	return Later
}

// Info shows a message the user asked for (e.g. a manual update check) as a
// dialog, so it is seen even when notifications are muted. It blocks until
// dismissed or after a minute.
func Info(title, msg string) {
	_ = exec.Command("/usr/bin/osascript", "-e", fmt.Sprintf(`display dialog %s with title %s buttons {"OK"} default button "OK" with icon note giving up after 60`,
		asq(trunc(msg, 900)), asq(title))).Run()
}

func Notify(n Notification) {
	if has("terminal-notifier") {
		args := []string{"-title", n.Title, "-message", trunc(n.Message, 240), "-group", "juhas-" + n.Title}
		if n.Urgent {
			args = append(args, "-sound", "Funk")
		}
		if n.Open != "" {
			target := n.Open
			if strings.HasPrefix(target, "/") {
				target = "file://" + target
			}
			args = append(args, "-open", target)
		}
		_ = exec.Command("terminal-notifier", args...).Start()
		return
	}
	script := fmt.Sprintf("display notification %s with title %s", asq(trunc(n.Message, 240)), asq(n.Title))
	if n.Urgent {
		script += ` sound name "Funk"`
	}
	_ = exec.Command("/usr/bin/osascript", "-e", script).Start()
}

// asq quotes s as an AppleScript string literal.
func asq(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchLabel+".plist")
}

func AutostartEnabled() bool {
	_, err := os.Stat(plistPath())
	return err == nil
}

// AutostartInstall registers exe as a LaunchAgent in the GUI session (so it
// can reach the Keychain and show a status item). It does not start a second
// copy now; the next login does.
func AutostartInstall(exe string) error {
	home, _ := os.UserHomeDir()
	logDir := filepath.Join(home, ".local", "state", "juhas")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array><string>%s</string><string>tray</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>LimitLoadToSessionType</key><string>Aqua</string>
  <key>ProcessType</key><string>Interactive</string>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, launchLabel, xmlEsc(exe), xmlEsc(filepath.Join(logDir, "tray.out.log")), xmlEsc(filepath.Join(logDir, "tray.err.log")))
	if err := os.MkdirAll(filepath.Dir(plistPath()), 0o755); err != nil {
		return err
	}
	return os.WriteFile(plistPath(), []byte(plist), 0o644)
}

// AutostartUninstall removes the LaunchAgent; a running tray keeps running.
func AutostartUninstall() error {
	err := os.Remove(plistPath())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func xmlEsc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// legacyLabels are LaunchAgents of earlier names of the app.
var legacyLabels = []string{"pl.piciuok.kopas"}

func legacyAutostart(home string) []string {
	var out []string
	for _, l := range legacyLabels {
		out = append(out, filepath.Join(home, "Library", "LaunchAgents", l+".plist"))
	}
	return out
}

// stopLegacy unloads old LaunchAgents, which also stops their tray. It is
// skipped when this process is that agent: bootout would kill it mid-way.
func stopLegacy() bool {
	stopped := false
	for _, l := range legacyLabels {
		if os.Getenv("XPC_SERVICE_NAME") == l {
			continue
		}
		target := fmt.Sprintf("gui/%d/%s", os.Getuid(), l)
		if exec.Command("/bin/launchctl", "print", target).Run() != nil {
			continue
		}
		if exec.Command("/bin/launchctl", "bootout", target).Run() == nil {
			stopped = true
		}
	}
	return stopped
}
