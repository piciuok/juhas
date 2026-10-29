package review

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/piciuok/juhas/internal/limits"
)

// streamEvent is the subset of `claude -p --output-format stream-json
// --verbose` we render.
type streamEvent struct {
	Type            string `json:"type"`
	Subtype         string `json:"subtype"`
	ParentToolUseID string `json:"parent_tool_use_id"`
	Message         struct {
		Content []struct {
			Type    string          `json:"type"`
			Text    string          `json:"text"`
			Name    string          `json:"name"`
			ID      string          `json:"id"`
			Input   json.RawMessage `json:"input"`
			Content json.RawMessage `json:"content"`
			IsError bool            `json:"is_error"`
		} `json:"content"`
	} `json:"message"`
	Result     string  `json:"result"`
	IsError    bool    `json:"is_error"`
	NumTurns   int     `json:"num_turns"`
	DurationMS int64   `json:"duration_ms"`
	CostUSD    float64 `json:"total_cost_usd"`
	Model      string  `json:"model"`
	SessionID  string  `json:"session_id"`
	// rate_limit_event: plan usage, kept for the tray's limits line
	RateLimitInfo json.RawMessage `json:"rate_limit_info"`
}

// StreamResult is what the final "result" event said.
type StreamResult struct {
	Text     string
	Subtype  string // success | error_max_turns | error_during_execution ...
	IsError  bool
	Turns    int
	Duration time.Duration
	CostUSD  float64
	Got      bool   // a result event arrived
	Session  string // session id, for --resume
}

// ConsumeStream renders claude's stream-json into a human-readable run log,
// one line per step, and returns the final result.
func ConsumeStream(r io.Reader, log io.Writer) StreamResult { return consumeStream(r, log, nil) }

// consumeStream also reports every event to onEvent (true for a result),
// which the hang watchdog uses.
func consumeStream(r io.Reader, log io.Writer, onEvent func(result bool)) StreamResult {
	var res StreamResult
	session := ""
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		var ev streamEvent
		if json.Unmarshal(line, &ev) != nil {
			fmt.Fprintf(log, "%s   %s\n", stamp(), oneLine(string(line), 200))
			continue
		}
		if onEvent != nil {
			onEvent(ev.Type == "result")
		}
		if ev.SessionID != "" {
			session = ev.SessionID
		}
		ind := ""
		if ev.ParentToolUseID != "" {
			ind = "    │ " // inside a subagent
		}
		switch ev.Type {
		case "rate_limit_event":
			limits.SaveClaude(ev.RateLimitInfo)
		case "system":
			if ev.Subtype == "init" {
				fmt.Fprintf(log, "%s ● Claude session started%s\n", stamp(), optional(" (model "+ev.Model+")", ev.Model != ""))
			}
		case "assistant":
			for _, c := range ev.Message.Content {
				switch c.Type {
				case "text":
					if t := strings.TrimSpace(c.Text); t != "" {
						fmt.Fprintf(log, "%s %s💬 %s\n", stamp(), ind, fullText(t, ind))
					}
				case "tool_use":
					fmt.Fprintf(log, "%s %s▶ %s %s\n", stamp(), ind, c.Name, toolSummary(c.Name, c.Input))
				}
			}
		case "user":
			for _, c := range ev.Message.Content {
				if c.Type != "tool_result" {
					continue
				}
				text := resultText(c.Content)
				mark := "↳"
				switch {
				case strings.Contains(text, "Blocked by Juhas"):
					mark = "⛔ blocked:"
				case c.IsError:
					mark = "✗"
				}
				fmt.Fprintf(log, "%s %s  %s %s\n", stamp(), ind, mark, resultSummary(text))
			}
		case "result":
			// A session can report several results (one per stage of an
			// orchestrating skill); the last one is the answer.
			if res.Got {
				fmt.Fprintf(log, "%s ◦ stage finished: %d turns, %s\n", stamp(), res.Turns, res.Duration.Round(time.Second))
			}
			res = StreamResult{Text: ev.Result, Subtype: ev.Subtype, IsError: ev.IsError, Turns: ev.NumTurns,
				Duration: ev.Duration(), CostUSD: ev.CostUSD, Got: true}
		}
	}
	if res.Got {
		status := "✓ done"
		if res.IsError || res.Subtype != "success" {
			status = "✗ done with error (" + res.Subtype + ")"
		}
		fmt.Fprintf(log, "%s %s: %d turns, %s, $%.2f\n", stamp(), status, res.Turns, res.Duration.Round(time.Second), res.CostUSD)
	}
	res.Session = session
	return res
}

func (e streamEvent) Duration() time.Duration { return time.Duration(e.DurationMS) * time.Millisecond }

func stamp() string { return time.Now().Format("15:04:05") }

func optional(s string, ok bool) string {
	if ok {
		return s
	}
	return ""
}

// toolSummary shows the interesting argument of a tool call.
func toolSummary(name string, input json.RawMessage) string {
	var m map[string]any
	_ = json.Unmarshal(input, &m)
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
	switch name {
	case "Bash":
		return oneLine(pick("command"), 200)
	case "Read", "Write", "Edit":
		return pick("file_path")
	case "Grep":
		return oneLine(pick("pattern")+"  "+pick("path", "glob"), 160)
	case "Glob":
		return pick("pattern")
	case "Skill":
		return oneLine(pick("skill", "command")+" "+pick("args"), 160)
	case "Task", "Agent":
		return oneLine(pick("description", "subagent_type"), 160)
	case "TodoWrite":
		return ""
	}
	return oneLine(string(input), 160)
}

func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return string(raw)
}

func resultSummary(text string) string {
	t := strings.TrimSpace(text)
	if t == "" {
		return "(empty)"
	}
	n := strings.Count(t, "\n") + 1
	if n > 1 {
		return fmt.Sprintf("%d lines · %s", n, oneLine(t, 140))
	}
	return oneLine(t, 200)
}

// fullText keeps an agent message whole: continuation lines are indented
// under the timestamp so the log stays readable.
func fullText(s, ind string) string {
	pad := strings.Repeat(" ", len("15:04:05 ")) + ind + "   "
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			lines[i] = ""
		} else {
			lines[i] = pad + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}
