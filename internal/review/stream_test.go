package review

import (
	"encoding/json"
	"strings"
	"testing"
)

const sampleStream = `{"type":"system","subtype":"init","model":"claude-opus-5-5"}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"glab mr view 883 --comments"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"title: x\nstate: open\n"}]}}
{"type":"assistant","parent_tool_use_id":"t9","message":{"content":[{"type":"tool_use","id":"t2","name":"Read","input":{"file_path":"/r/a.ts"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t3","is_error":true,"content":[{"type":"text","text":"PreToolUse:Bash hook error: Blocked by Juhas (review is read-only): glab mr note"}]}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"# Review\n\nOK"}]}}
{"type":"result","subtype":"success","is_error":false,"num_turns":2,"duration_ms":5000,"total_cost_usd":0.1,"result":"stage"}
{"type":"result","subtype":"success","is_error":false,"num_turns":7,"duration_ms":65000,"total_cost_usd":0.42,"result":"# Review\n\nOK"}
`

func TestConsumeStream(t *testing.T) {
	var log strings.Builder
	res := ConsumeStream(strings.NewReader(sampleStream), &log)
	if strings.Count(log.String(), "✓ done") != 1 {
		t.Errorf("want one final summary:\n%s", log.String())
	}
	if !res.Got || res.Text != "# Review\n\nOK" || res.Turns != 7 || res.IsError {
		t.Errorf("result: %+v", res)
	}
	out := log.String()
	for _, want := range []string{
		"● Claude session started (model claude-opus-5-5)",
		"▶ Bash glab mr view 883 --comments",
		"↳ 2 lines · title: x state: open",
		"    │ ▶ Read /r/a.ts",
		"⛔ blocked:",
		"💬 # Review\n",
		"◦ stage finished: 2 turns, 5s",
		"✓ done: 7 turns, 1m5s, $0.42",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
}

func TestFullTextKeepsWholeMessage(t *testing.T) {
	long := strings.Repeat("word ", 120) + "\nSecond line of the report"
	var log strings.Builder
	ConsumeStream(strings.NewReader(`{"type":"assistant","message":{"content":[{"type":"text","text":`+jsonString(long)+`}]}}`+"\n"), &log)
	out := log.String()
	if strings.Contains(out, "…") || !strings.Contains(out, "Second line of the report") || strings.Count(out, "word") != 120 {
		t.Errorf("message truncated:\n%s", out)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
