package state

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestUpdateConcurrentAndRecover(t *testing.T) {
	s := OpenAt(filepath.Join(t.TempDir(), "state.json"))
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			url := string(rune('a' + i))
			if err := s.Update(func(st *State) error {
				st.Put(&Job{URL: url, Status: Running, PID: 1 << 30}) // no such pid
				return nil
			}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	st, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Jobs) != 20 {
		t.Fatalf("jobs = %d, want 20 (lost update)", len(st.Jobs))
	}
	if err := s.Update(func(st *State) error {
		st.Put(&Job{URL: "live", Status: Running, PID: os.Getpid(), Owner: os.Getppid()}) // another live juhas
		st.Put(&Job{URL: "mine", Status: Running, PID: os.Getpid(), Owner: os.Getpid()})  // we restarted in place
		st.Put(&Job{URL: "queued", Status: Queued, Owner: os.Getppid()})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverInterrupted(); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Read()
	if st.Job("a").Status != Failed || st.Job("live").Status != Running || st.Job("queued").Status != Queued {
		t.Errorf("a=%s live=%s queued=%s", st.Job("a").Status, st.Job("live").Status, st.Job("queued").Status)
	}
	// the test binary is not an agent, so StopOrphan leaves it alone
	if st.Job("mine").Status != Failed {
		t.Errorf("a job of the process we replaced must fail: %s", st.Job("mine").Status)
	}
}

func TestStale(t *testing.T) {
	j := &Job{Status: Done, BriefSHA: "abc"}
	if j.Stale("abc") || !j.Stale("def") || j.Stale("") {
		t.Error("stale logic")
	}
}
