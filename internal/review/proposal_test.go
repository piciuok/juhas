package review

import (
	"path/filepath"
	"strings"
	"testing"
)

const answer = "# Review\n\nBrief body.\n\n```juhas-review\n" +
	`{"verdict": "request changes", "summary": "Thanks! Two things to fix.", "comments": [
	  {"file": "./apps/web/a.ts", "line": 12, "severity": "WARN", "body": "Missing an undefined guard here."},
	  {"file": "b.ts", "line": 0, "severity": "INFO", "body": "  "}
	]}` + "\n```\n"

func TestExtractProposal(t *testing.T) {
	brief, p, err := ExtractProposal(answer)
	if err != nil || p == nil {
		t.Fatalf("%v %v", p, err)
	}
	if strings.Contains(brief, "juhas-review") || !strings.HasPrefix(brief, "# Review") {
		t.Errorf("brief: %q", brief)
	}
	if p.Verdict != "request changes" || p.Summary.Body == "" || len(p.Comments) != 1 {
		t.Fatalf("proposal: %+v", p)
	}
	c := p.Comments[0]
	if c.File != "apps/web/a.ts" || c.Line != 12 || c.Status != CommentPending || c.ID != "c1" {
		t.Errorf("comment: %+v", c)
	}
	if md := p.Markdown(); !strings.Contains(md, "`apps/web/a.ts:12`") || !strings.Contains(md, "General comment") {
		t.Errorf("markdown:\n%s", md)
	}
	path := filepath.Join(t.TempDir(), "x.review.json")
	if err := SaveProposal(path, p); err != nil {
		t.Fatal(err)
	}
	q, err := LoadProposal(path)
	if err != nil || len(q.All()) != 2 {
		t.Fatalf("%+v %v", q, err)
	}
	pend, acc, posted := q.Counts()
	if pend != 2 || acc != 0 || posted != 0 {
		t.Errorf("counts %d %d %d", pend, acc, posted)
	}
}

func TestExtractProposalAbsentOrBroken(t *testing.T) {
	if b, p, err := ExtractProposal("# brief only"); p != nil || err != nil || b != "# brief only" {
		t.Error("no block")
	}
	if _, p, err := ExtractProposal("x\n```juhas-review\n{nope\n```"); p != nil || err == nil {
		t.Error("broken block must error and keep the brief")
	}
}

func TestResolved(t *testing.T) {
	p := &Proposal{Summary: ProposedComment{ID: "summary", Body: "x", Status: CommentPosted},
		Comments: []ProposedComment{{ID: "c1", Body: "a", Status: CommentRejected}, {ID: "c2", Body: "b", Status: CommentAccepted}}}
	if p.Resolved() {
		t.Error("accepted but unpublished comment must keep it open")
	}
	p.Comments[1].Status = CommentPosted
	if !p.Resolved() {
		t.Error("all posted/rejected = resolved")
	}
	if !ProposalResolved("") || !ProposalResolved("/nope/x.log") {
		t.Error("no proposal = nothing to decide")
	}
}

func TestLockProposal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.review.json")
	unlock, err := LockProposal(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LockProposal(path, false); err != ErrBusy {
		t.Errorf("second publish: %v, want ErrBusy", err)
	}
	unlock()
	u2, err := LockProposal(path, false)
	if err != nil {
		t.Errorf("after unlock: %v", err)
	} else {
		u2()
	}
}
