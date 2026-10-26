package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/piciuok/juhas/internal/config"
	"github.com/piciuok/juhas/internal/forge"
	"github.com/piciuok/juhas/internal/state"
)

func TestQueuedLogNamesWhatItWaitsFor(t *testing.T) {
	store := state.OpenAt(filepath.Join(t.TempDir(), "state.json"))
	r := &Runner{Cfg: config.Default(), Store: store}
	r.init()
	first := forge.Item{Repo: "gl/a/b", Number: 922, URL: "u922", Platform: forge.GitLab}
	second := forge.Item{Repo: "gl/a/b", Number: 342, URL: "u342", Platform: forge.GitLab, Title: "second"}
	r.running[first.URL] = first
	r.waiting = []forge.Item{second}
	p := LogPath(store, second.URL)
	r.writeQueued(second, p)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "⏳ queued (position 1") || !strings.Contains(string(b), "waiting for: gl/a/b !922 (running)") {
		t.Errorf("queued log:\n%s", b)
	}
	if LogPath(store, first.URL) == p {
		t.Error("two PR/MRs share a log file")
	}
}
