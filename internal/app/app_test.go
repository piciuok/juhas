package app

import (
	"strings"
	"testing"

	"github.com/piciuok/juhas/internal/config"
	"github.com/piciuok/juhas/internal/forge"
	"github.com/piciuok/juhas/internal/queue"
	"github.com/piciuok/juhas/internal/state"
)

func entry(n int, onNew, sha, path string) queue.Entry {
	return queue.Entry{
		Item:  forge.Item{Platform: forge.GitLab, Repo: "gl/team/app", Number: n, URL: "u" + string(rune('0'+n)), HeadSHA: sha, Title: "t"},
		Path:  path,
		OnNew: onNew,
	}
}

func TestDecide(t *testing.T) {
	st := &state.State{Jobs: map[string]*state.Job{}}
	first := &queue.Snapshot{Entries: []queue.Entry{entry(1, "ask", "a", "/r")}}
	if p := Decide(st, first, 2, false); !p.Baseline || len(p.Ask) != 0 || st.Job("u1").Status != state.Pending {
		t.Fatalf("first poll must be a silent baseline: %+v", p)
	}
	// a manual check must not re-ask baseline items (never asked)
	if p := Decide(st, first, 2, true); len(p.Ask) != 0 {
		t.Error("manual check re-asked a baseline item")
	}

	snap := &queue.Snapshot{Entries: []queue.Entry{
		entry(1, "ask", "a", "/r"),    // known: nothing
		entry(2, "ask", "b", "/r"),    // new: ask
		entry(3, "auto", "c", "/r"),   // auto
		entry(4, "auto", "d", "/r"),   // auto
		entry(5, "auto", "e", "/r"),   // auto over limit 2: pending
		entry(6, "silent", "f", "/r"), // silent
		entry(7, "ask", "g", ""),      // no clone
	}}
	p := Decide(st, snap, 2, false)
	if len(p.Ask) != 1 || p.Ask[0].Number != 2 || p.Reasons["u2"] != ReasonNew {
		t.Errorf("ask = %+v %v", p.Ask, p.Reasons)
	}
	if len(p.Auto) != 2 || len(p.Silent) != 2 || len(p.NoClone) != 1 {
		t.Errorf("auto=%d silent=%d noclone=%d", len(p.Auto), len(p.Silent), len(p.NoClone))
	}
	if st.Job("u2").Status != state.Asked || st.Job("u2").AskedSHA != "b" || st.Job("u3").Status != state.Queued {
		t.Errorf("statuses: %+v %+v", st.Job("u2"), st.Job("u3"))
	}
	if p := Decide(st, snap, 2, false); len(p.Ask)+len(p.Auto)+len(p.Silent)+len(p.NoClone) != 0 {
		t.Errorf("re-asked: %+v", p)
	}

	// "Skip": quiet on automatic polls, asked again on a manual check
	st.Job("u2").Status = state.Skipped
	if p := Decide(st, snap, 2, false); len(p.Ask) != 0 {
		t.Error("skipped with same head asked by an automatic poll")
	}
	if p := Decide(st, snap, 2, true); len(p.Ask) != 1 || p.Reasons["u2"] != ReasonAgain {
		t.Errorf("manual check must re-ask a skipped item: %+v", p.Reasons)
	}
	st.Job("u2").Status = state.Skipped
	snap.Entries[1].HeadSHA = "b2"
	if p := Decide(st, snap, 2, false); len(p.Ask) != 1 || p.Reasons["u2"] != ReasonChanged {
		t.Error("skipped with new head must be asked again")
	}

	// reviewed, then new commits: one recheck question per head, status kept
	j := st.Job("u1")
	j.Status, j.Brief, j.BriefSHA = state.Done, "/b.md", "a"
	if !j.Reviewed("a") {
		t.Fatal("Reviewed")
	}
	snap.Entries[0].HeadSHA = "a2"
	p = Decide(st, snap, 2, false)
	if p.Reasons["u1"] != ReasonChanged || st.Job("u1").Status != state.Done || st.Job("u1").AskedSHA != "a2" {
		t.Errorf("recheck: %v %+v", p.Reasons, st.Job("u1"))
	}
	if p := Decide(st, snap, 2, false); p.Reasons["u1"] != "" {
		t.Error("recheck asked twice for the same head")
	}
}

func TestQuestion(t *testing.T) {
	cfg := config.Default()
	q := Question(cfg, []queue.Entry{entry(2, "ask", "b", "")}, nil)
	if !strings.Contains(q.Title, "team/app !2") || !strings.Contains(q.Message, "Run AI review?") {
		t.Errorf("single: %+v", q)
	}
	q = Question(cfg, []queue.Entry{entry(2, "ask", "b", ""), entry(3, "ask", "c", "")}, nil)
	if q.RunL != "Run all" || !strings.Contains(q.Title, "2 new") {
		t.Errorf("batch: %+v", q)
	}
}

func TestQuestionAgents(t *testing.T) {
	cfg, err := config.Parse([]byte(`{"repos": {"gl/team/app": {"agent": "codex"}}}`), "t")
	if err != nil {
		t.Fatal(err)
	}
	q := Question(cfg, []queue.Entry{entry(2, "ask", "b", "")}, nil)
	if len(q.Agents) != 2 || q.Agents[0].Name != "codex" || q.Agents[1].Name != "claude" {
		t.Errorf("repo default agent must come first: %+v", q.Agents)
	}
}

func TestQuestionRecheck(t *testing.T) {
	e := entry(2, "ask", "b", "")
	q := Question(config.Default(), []queue.Entry{e}, map[string]string{e.URL: ReasonChanged})
	if !strings.Contains(q.Title, "Changes in") || !strings.Contains(q.Message, "Review again?") || q.RunL != "Re-review" {
		t.Errorf("%+v", q)
	}
}

func TestDecideLaterThenPush(t *testing.T) {
	st := &state.State{Initialized: true, Jobs: map[string]*state.Job{}}
	snap := &queue.Snapshot{Entries: []queue.Entry{entry(1, "ask", "a", "/r")}}
	if p := Decide(st, snap, 2, false); len(p.Ask) != 1 {
		t.Fatal("new item not asked")
	}
	st.Job("u1").Status = state.Pending // "Later"
	if p := Decide(st, snap, 2, false); len(p.Ask) != 0 {
		t.Error("postponed item re-asked without new commits")
	}
	snap.Entries[0].HeadSHA = "a2"
	if p := Decide(st, snap, 2, false); len(p.Ask) != 1 || p.Reasons["u1"] != ReasonChanged {
		t.Errorf("postponed item with new commits must be asked again: %+v", p.Reasons)
	}
}

func TestDecideBaselineWaitsForCleanPoll(t *testing.T) {
	st := &state.State{Jobs: map[string]*state.Job{}}
	offline := &queue.Snapshot{Errors: []string{"no network"}}
	if p := Decide(st, offline, 2, false); !p.Baseline || st.Initialized {
		t.Fatalf("failed first poll must not finish the baseline: %+v", st)
	}
	online := &queue.Snapshot{Entries: []queue.Entry{entry(1, "ask", "a", "/r")}}
	if p := Decide(st, online, 2, false); !p.Baseline || len(p.Ask) != 0 || !st.Initialized {
		t.Errorf("first clean poll is the baseline: %+v", p)
	}
	st2 := &state.State{Jobs: map[string]*state.Job{}}
	for i := 0; i <= maxBaselineTries; i++ {
		Decide(st2, offline, 2, false)
	}
	if !st2.Initialized {
		t.Error("a persistent error must not keep the baseline open forever")
	}
}
