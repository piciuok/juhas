package review

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// A session that prints its result and then just sits there must be closed.
func TestWatchStreamEndsHungSession(t *testing.T) {
	old := HangAfterResult
	HangAfterResult = 1 * time.Second
	defer func() { HangAfterResult = old }()

	cmd := exec.Command("sh", "-c", `echo '{"type":"result","subtype":"success","result":"# Raport","num_turns":1}'; sleep 60`)
	setProcessGroup(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	start := time.Now()
	sr := watchStream(stdout, &syncWriter{w: &log}, cmd)
	_ = cmd.Wait()
	if !sr.Got || sr.Text != "# Raport" {
		t.Errorf("result: %+v", sr)
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("took %s", d)
	}
	if !strings.Contains(log.String(), "did not end") {
		t.Errorf("log:\n%s", log.String())
	}
}
