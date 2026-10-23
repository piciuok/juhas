package platform

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// Choice is the answer to "run AI review?": "run:<agent>", later or skip.
type Choice string

const (
	Later Choice = "later"
	Skip  Choice = "skip"
)

// RunWith is the choice to start the review with an agent.
func RunWith(agent string) Choice { return Choice("run:" + agent) }

// Agent returns the agent of a run choice.
func (c Choice) Agent() (string, bool) {
	a, ok := strings.CutPrefix(string(c), "run:")
	return a, ok
}

// Agent is one "run with" option; the first in Question.Agents is the default.
type Agent struct {
	Name  string // claude | codex
	Label string // Claude | Codex
}

// Question is shown when a new PR/MR appears.
type Question struct {
	Title   string
	Message string
	Agents  []Agent // run options, default first
	RunL    string  // "Run" or "Run all"
	LaterL  string
	SkipL   string
	Style   string // dialog | notification
	Timeout time.Duration
}

// runLabel is "Run — Claude" (marked when it is the default).
func (q *Question) runLabel(i int) string {
	l := q.RunL + " — " + q.Agents[i].Label
	if i == 0 && len(q.Agents) > 1 {
		l += " (default)"
	}
	return l
}

func (q *Question) defaults() {
	if q.RunL == "" {
		q.RunL = "Run"
	}
	if len(q.Agents) == 0 {
		q.Agents = []Agent{{Name: "claude", Label: "Claude"}}
	}
	if q.LaterL == "" {
		q.LaterL = "Later"
	}
	if q.SkipL == "" {
		q.SkipL = "Skip"
	}
	if q.Timeout == 0 {
		q.Timeout = 10 * time.Minute
	}
}

// Notification is a fire-and-forget message; Open (path or URL) is opened
// when it is clicked, where the backend supports that.
type Notification struct {
	Title   string
	Message string
	Open    string
	Urgent  bool // loud: sound on macOS, critical urgency on Linux
}

func has(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

func output(ctx context.Context, name string, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
		err = nil
	}
	return strings.TrimSpace(string(out)), code, err
}

// trunc keeps dialog text readable.
func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
