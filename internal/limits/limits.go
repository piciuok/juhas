// Package limits keeps the agents' plan usage (how much of the 5-hour and
// weekly windows is used) for one line in the tray menu. Claude reports it in
// its stream during a review; Codex writes it into its own session files.
package limits

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Window is one usage window.
type Window struct {
	Used     float64   `json:"used"` // percent, 0–100
	ResetsAt time.Time `json:"resets_at"`
}

// Usage is what an agent last reported.
type Usage struct {
	FiveHour *Window   `json:"five_hour,omitempty"`
	Week     *Window   `json:"week,omitempty"`
	At       time.Time `json:"at"` // when it was reported
}

// ClaudePath is where SaveClaude keeps the last report (set by the app).
var ClaudePath string

// SaveClaude stores a stream-json rate_limit_event's rate_limit_info.
func SaveClaude(info json.RawMessage) {
	if ClaudePath == "" || len(info) == 0 {
		return
	}
	var in struct {
		Windows map[string]struct {
			Utilization float64 `json:"utilization"`
			ResetsAt    int64   `json:"resetsAt"`
		} `json:"unifiedWindows"`
	}
	if json.Unmarshal(info, &in) != nil || len(in.Windows) == 0 {
		return
	}
	u := Usage{At: time.Now()}
	w := func(key string) *Window {
		x, ok := in.Windows[key]
		if !ok {
			return nil
		}
		return &Window{Used: x.Utilization * 100, ResetsAt: time.Unix(x.ResetsAt, 0)}
	}
	u.FiveHour, u.Week = w("five_hour"), w("seven_day")
	b, _ := json.Marshal(u)
	tmp := ClaudePath + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, ClaudePath)
	}
}

// Claude is the last report saved by SaveClaude.
func Claude() (Usage, bool) {
	var u Usage
	b, err := os.ReadFile(ClaudePath)
	return u, err == nil && json.Unmarshal(b, &u) == nil
}

// CodexHome is $CODEX_HOME or ~/.codex.
func CodexHome() string {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

// Codex reads the newest rate_limits Codex wrote into its session files
// (~/.codex/sessions/YYYY/MM/DD/*.jsonl), from any Codex use.
func Codex() (Usage, bool) {
	files := recentSessions(filepath.Join(CodexHome(), "sessions"), 7)
	for _, f := range files {
		if u, ok := codexFile(f.path); ok {
			if u.At.IsZero() {
				u.At = f.mod
			}
			return u, true
		}
	}
	return Usage{}, false
}

type session struct {
	path string
	mod  time.Time
}

// recentSessions are the session files of the last days, newest first.
func recentSessions(root string, days int) []session {
	var out []session
	now := time.Now()
	for d := 0; d < days; d++ {
		dir := filepath.Join(root, now.AddDate(0, 0, -d).Format("2006/01/02"))
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			if fi, err := e.Info(); err == nil {
				out = append(out, session{filepath.Join(dir, e.Name()), fi.ModTime()})
			}
		}
		if len(out) >= 5 {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].mod.After(out[j].mod) })
	return out
}

// codexFile finds the last rate_limits in the tail of one session file.
func codexFile(path string) (Usage, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Usage{}, false
	}
	defer f.Close()
	const tail = 512 << 10
	if fi, err := f.Stat(); err == nil && fi.Size() > tail {
		_, _ = f.Seek(fi.Size()-tail, io.SeekStart)
	}
	type win struct {
		Used     float64 `json:"used_percent"`
		ResetsAt int64   `json:"resets_at"`
		Minutes  int     `json:"window_minutes"`
	}
	var last struct {
		Timestamp time.Time `json:"timestamp"`
		Payload   struct {
			RateLimits *struct {
				Primary   *win `json:"primary"`
				Secondary *win `json:"secondary"`
			} `json:"rate_limits"`
		} `json:"payload"`
	}
	found := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.Contains(line, []byte(`"rate_limits":{`)) {
			continue
		}
		var ev = last
		ev.Payload.RateLimits = nil
		if json.Unmarshal(line, &ev) == nil && ev.Payload.RateLimits != nil {
			last, found = ev, true
		}
	}
	if !found {
		return Usage{}, false
	}
	u := Usage{At: last.Timestamp}
	for _, w := range []*win{last.Payload.RateLimits.Primary, last.Payload.RateLimits.Secondary} {
		if w == nil {
			continue
		}
		x := &Window{Used: w.Used, ResetsAt: time.Unix(w.ResetsAt, 0)}
		if w.Minutes > 0 && w.Minutes <= 6*60 {
			u.FiveHour = x
		} else {
			u.Week = x
		}
	}
	return u, u.FiveHour != nil || u.Week != nil
}

// Short is "40% 5h · 46% 7d"; a window past its reset shows 0%.
func (u Usage) Short(now time.Time) string {
	var parts []string
	for _, w := range []struct {
		w     *Window
		label string
	}{{u.FiveHour, "5h"}, {u.Week, "7d"}} {
		if w.w == nil {
			continue
		}
		used := w.w.Used
		if !w.w.ResetsAt.IsZero() && now.After(w.w.ResetsAt) {
			used = 0
		}
		parts = append(parts, fmt.Sprintf("%.0f%% %s", used, w.label))
	}
	return strings.Join(parts, " · ")
}

// Detail is the tooltip: reset times and when it was reported.
func (u Usage) Detail(name string) string {
	var parts []string
	if u.FiveHour != nil {
		parts = append(parts, "5h resets "+u.FiveHour.ResetsAt.Format("15:04"))
	}
	if u.Week != nil {
		parts = append(parts, "7d resets "+u.Week.ResetsAt.Format("Mon 15:04"))
	}
	return fmt.Sprintf("%s: %s (as of %s)", name, strings.Join(parts, ", "), u.At.Format("Jan 2 15:04"))
}
