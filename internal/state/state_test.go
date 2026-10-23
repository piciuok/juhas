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
		st.Put(&Job{URL: "live", Status: Running, PID: os.Getpid()})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverInterrupted(); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Read()
	if st.Job("a").Status != Failed || st.Job("live").Status != Running {
		t.Errorf("a=%s live=%s", st.Job("a").Status, st.Job("live").Status)
	}
}

func TestStale(t *testing.T) {
	j := &Job{Status: Done, BriefSHA: "abc"}
	if j.Stale("abc") || !j.Stale("def") || j.Stale("") {
		t.Error("stale logic")
	}
}
