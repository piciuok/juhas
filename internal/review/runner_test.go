package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// A paused queue lets the running review finish and starts nothing else
// until it resumes; Waiting lists what is held back, as it was asked for.
func TestPauseHoldsTheQueue(t *testing.T) {
	home := isolate(t)
	repo := filepath.Join(home, "repo")
	_ = os.MkdirAll(repo, 0o755)
	started := filepath.Join(home, "started.log")
	bin := filepath.Join(home, "fake-claude")
	result := filepath.Join(home, "result.jsonl")
	_ = os.WriteFile(result, []byte(`{"type":"result","subtype":"success","result":"# Brief","num_turns":1}`+"\n"), 0o644)
	_ = os.WriteFile(bin, []byte("#!/bin/sh\ncat > /dev/null\necho x >> "+started+"\nsleep 1\ncat "+result+"\n"), 0o755)
	cfg, err := config.Parse([]byte(`{"claude": {"bin": "`+bin+`"}, "briefs_dir": "`+filepath.Join(home, "briefs")+`", "concurrency": 1, "defaults": {"review": {"skill": "none", "fallback": "builtin"}}}`), filepath.Join(home, "c.json"))
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{Cfg: cfg, Store: state.Open()}
	a := forge.Item{Platform: forge.GitHub, Repo: "github.com/acme/widgets", Number: 1, URL: "https://github.com/acme/widgets/pull/1"}
	b := a
	b.Number, b.URL = 2, "https://github.com/acme/widgets/pull/2"
	r.Enqueue(a, repo, "")
	r.Enqueue(b, repo, "claude")
	for i := 0; i < 100; i++ { // a is running
		if run, _ := r.Queue(); len(run) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.Pause(true)
	for i := 0; i < 200; i++ { // a finishes
		if run, _ := r.Queue(); len(run) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	run, wait := r.Queue()
	if len(run) != 0 || len(wait) != 1 {
		t.Fatalf("paused: running %v waiting %v", run, wait)
	}
	agent := map[int]string{1: "", 2: "claude"}[wait[0].Number] // either may have got the slot first
	if q := r.Waiting(); len(q) != 1 || q[0].Dir != repo || q[0].Agent != agent || q[0].Item.URL != wait[0].URL {
		t.Fatalf("waiting: %+v", q)
	}
	if n, _ := os.ReadFile(started); strings.Count(string(n), "x") != 1 {
		t.Fatalf("started while paused: %q", n)
	}
	r.Pause(false)
	r.Wait()
	if n, _ := os.ReadFile(started); strings.Count(string(n), "x") != 2 {
		t.Fatalf("not resumed: %q", n)
	}
}
