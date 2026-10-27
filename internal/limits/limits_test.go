package limits

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClaude(t *testing.T) {
	ClaudePath = filepath.Join(t.TempDir(), "limits-claude.json")
	SaveClaude(json.RawMessage(`{"status":"allowed","unifiedWindows":{"five_hour":{"utilization":0.4,"resetsAt":1793091600},"seven_day":{"utilization":0.46,"resetsAt":1793242800}}}`))
	u, ok := Claude()
	if !ok || u.Short(time.Unix(1793000000, 0)) != "40% 5h · 46% 7d" {
		t.Fatalf("%v %+v", ok, u)
	}
	if got := u.Short(time.Unix(1793100000, 0)); got != "0% 5h · 46% 7d" {
		t.Errorf("after the 5h reset: %s", got)
	}
}

func TestCodex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	dir := filepath.Join(home, "sessions", time.Now().Format("2006/01/02"))
	os.MkdirAll(dir, 0o755)
	body := `{"timestamp":"2026-10-26T09:00:00Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":10.0,"window_minutes":300,"resets_at":1793019948},"secondary":{"used_percent":5.0,"window_minutes":10080,"resets_at":1793606748}}}}
{"timestamp":"2026-10-26T09:05:00Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":85.0,"window_minutes":300,"resets_at":1793019948},"secondary":{"used_percent":13.0,"window_minutes":10080,"resets_at":1793606748}}}}
{"timestamp":"2026-10-26T09:06:00Z","type":"response_item","payload":{"type":"message"}}
`
	os.WriteFile(filepath.Join(dir, "rollout-x.jsonl"), []byte(body), 0o644)
	u, ok := Codex()
	if !ok || u.Short(time.Unix(1793000000, 0)) != "85% 5h · 13% 7d" {
		t.Fatalf("%v %+v", ok, u)
	}
}
