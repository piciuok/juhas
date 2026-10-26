package tray

import (
	"path/filepath"
	"testing"

	"github.com/piciuok/juhas/internal/forge"
	"github.com/piciuok/juhas/internal/queue"
	"github.com/piciuok/juhas/internal/review"
	"github.com/piciuok/juhas/internal/state"
)

func TestAwaitingDecision(t *testing.T) {
	dir := t.TempDir()
	logA, logB := filepath.Join(dir, "a.log"), filepath.Join(dir, "b.log")
	_ = review.SaveProposal(review.ProposalPath(logA), &review.Proposal{Comments: []review.ProposedComment{
		{ID: "c1", Body: "x", Status: review.CommentPending}, {ID: "c2", Body: "y", Status: review.CommentAccepted}}})
	_ = review.SaveProposal(review.ProposalPath(logB), &review.Proposal{Comments: []review.ProposedComment{
		{ID: "c1", Body: "x", Status: review.CommentPosted}}})
	st := &state.State{Jobs: map[string]*state.Job{}}
	st.Put(&state.Job{URL: "https://gl/x/-/merge_requests/883", Number: 883, Status: state.Done, Log: logA})
	st.Put(&state.Job{URL: "https://gl/x/-/merge_requests/884", Number: 884, Status: state.Done, Log: logB})
	snap := &queue.Snapshot{Entries: []queue.Entry{
		{Item: forge.Item{URL: "https://gl/x/-/merge_requests/883"}},
		{Item: forge.Item{URL: "https://gl/x/-/merge_requests/884"}},
	}}
	w := awaitingDecision(snap, st)
	if len(w) != 1 || w[0].Ref != "!883" || w[0].Pending != 1 || w[0].Accepted != 1 {
		t.Fatalf("%+v", w)
	}
	if l := w[0].label(); l != "1 awaiting decision, 1 to publish" {
		t.Errorf("label %q", l)
	}
}
