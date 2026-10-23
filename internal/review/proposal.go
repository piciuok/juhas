package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// A review ends with a proposal: comments the agent suggests, which the
// user accepts, edits or rejects before the app publishes the accepted ones.

const (
	CommentPending  = "pending"
	CommentAccepted = "accepted"
	CommentRejected = "rejected"
	CommentPosted   = "posted"
	CommentFailed   = "failed"
)

type ProposedComment struct {
	ID       string `json:"id"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Severity string `json:"severity"` // e.g. BLOCK/WARN/INFO or high/medium/low
	Body     string `json:"body"`
	Status   string `json:"status"`
	Posted   string `json:"posted,omitempty"` // URL of the published comment
	Note     string `json:"note,omitempty"`   // e.g. "outside the diff — posted as a general comment"
	Error    string `json:"error,omitempty"`
}

type Proposal struct {
	URL       string            `json:"url"`
	Repo      string            `json:"repo"`
	Number    int               `json:"number"`
	Title     string            `json:"title"`
	Platform  string            `json:"platform"`
	Account   string            `json:"account,omitempty"` // forge account the PR/MR was found with; publishing uses it
	HeadSHA   string            `json:"head_sha"`          // what the review saw
	Agent     string            `json:"agent"`
	Verdict   string            `json:"verdict"`
	Summary   ProposedComment   `json:"summary"` // the general comment (File empty)
	Comments  []ProposedComment `json:"comments"`
	CreatedAt time.Time         `json:"created_at"`
}

// proposalBlock matches the fenced ```juhas-review … ``` JSON block.
var proposalBlock = regexp.MustCompile("(?s)```(?:juhas|kopas)-review\\s*\\n(.*?)\\n```")

// ProposalPromptRules tells the agent how to hand over its comments.
func ProposalPromptRules(commentsLang string) string {
	return fmt.Sprintf(`Proposed comments (mandatory, at the very end of the answer): a code block tagged juhas-review with JSON of this shape:
`+"```juhas-review"+`
{"verdict": "approve | request changes | needs discussion", "summary": "general comment on the PR/MR", "comments": [{"file": "path/relative/to/repo.ts", "line": 123, "severity": "BLOCK|WARN|INFO", "body": "comment text"}]}
`+"```"+`
- "line" is the line number in the NEW version of the file (right side of the diff), within the changed hunks; if a remark is not about a specific line, leave it out of "comments" and put it in "summary".
- Write "summary" and every "body" in %s, ready to post on the PR/MR (specific, polite, no internal markers like [BLOCK] in the text).
- Do not repeat these comments in the markdown part — the app shows them under the brief itself. You publish nothing: this is only a proposal that a human will approve.`, LangName(commentsLang))
}

// ExtractProposal splits the agent's answer into the brief and the proposal.
// When there is no (valid) block the brief is returned unchanged.
func ExtractProposal(text string) (string, *Proposal, error) {
	m := proposalBlock.FindStringSubmatchIndex(text)
	if m == nil {
		return text, nil, nil
	}
	raw := text[m[2]:m[3]]
	brief := strings.TrimSpace(text[:m[0]] + text[m[1]:])
	var in struct {
		Verdict  string `json:"verdict"`
		Summary  string `json:"summary"`
		Comments []struct {
			File     string `json:"file"`
			Line     int    `json:"line"`
			Severity string `json:"severity"`
			Body     string `json:"body"`
		} `json:"comments"`
	}
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return text, nil, fmt.Errorf("juhas-review block: %w", err)
	}
	p := &Proposal{Verdict: in.Verdict, Summary: ProposedComment{ID: "summary", Body: strings.TrimSpace(in.Summary), Status: CommentPending}}
	for i, c := range in.Comments {
		if strings.TrimSpace(c.Body) == "" {
			continue
		}
		p.Comments = append(p.Comments, ProposedComment{ID: fmt.Sprintf("c%d", i+1), File: strings.TrimPrefix(c.File, "./"),
			Line: c.Line, Severity: c.Severity, Body: strings.TrimSpace(c.Body), Status: CommentPending})
	}
	return brief, p, nil
}

// Markdown renders the proposal for the brief file.
func (p *Proposal) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Proposed comments on the PR/MR\n\n_Awaiting decision in the Juhas Panel: accept, edit or reject, then publish._\n\n")
	if p.Verdict != "" {
		fmt.Fprintf(&b, "**Verdict:** %s\n\n", p.Verdict)
	}
	if p.Summary.Body != "" {
		fmt.Fprintf(&b, "**General comment**\n> %s\n\n", strings.ReplaceAll(p.Summary.Body, "\n", "\n> "))
	}
	for _, c := range p.Comments {
		loc := c.File
		if c.Line > 0 {
			loc = fmt.Sprintf("%s:%d", c.File, c.Line)
		}
		sev := ""
		if c.Severity != "" {
			sev = " · " + c.Severity
		}
		fmt.Fprintf(&b, "**`%s`**%s\n> %s\n\n", loc, sev, strings.ReplaceAll(c.Body, "\n", "\n> "))
	}
	return b.String()
}

// ProposalPath is next to the run log: jobs/<id>.review.json.
func ProposalPath(logPath string) string {
	return strings.TrimSuffix(logPath, ".log") + ".review.json"
}

// ErrBusy means another publish of the same proposal is in progress.
var ErrBusy = errors.New("this proposal is being published right now")

// LockProposal serialises edits and publishing of one proposal, also across
// processes (the tray and \`juhas config ui\` both serve the page). With wait
// unset it fails with ErrBusy instead of waiting. Call the returned func to unlock.
func LockProposal(path string, wait bool) (func(), error) {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	how := syscall.LOCK_EX
	if !wait {
		how |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

func SaveProposal(path string, p *Proposal) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func LoadProposal(path string) (*Proposal, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Proposal
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Counts returns pending, accepted and posted comment numbers (summary included).
func (p *Proposal) Counts() (pending, accepted, posted int) {
	for _, c := range p.All() {
		switch c.Status {
		case CommentPending:
			pending++
		case CommentAccepted:
			accepted++
		case CommentPosted:
			posted++
		}
	}
	return
}

// All lists the summary (if any) and the inline comments.
func (p *Proposal) All() []*ProposedComment {
	var out []*ProposedComment
	if p.Summary.Body != "" {
		out = append(out, &p.Summary)
	}
	for i := range p.Comments {
		out = append(out, &p.Comments[i])
	}
	return out
}

// Find returns the comment with id.
func (p *Proposal) Find(id string) (*ProposedComment, error) {
	for _, c := range p.All() {
		if c.ID == id {
			return c, nil
		}
	}
	return nil, errors.New("no comment " + id)
}

// Resolved: nothing waits for the user — every comment is posted or
// rejected (an accepted but unpublished or failed one still needs action).
func (p *Proposal) Resolved() bool {
	for _, c := range p.All() {
		if c.Status != CommentPosted && c.Status != CommentRejected {
			return false
		}
	}
	return true
}

// ProposalResolved reports whether the run behind logPath left nothing to
// decide: no proposal file, or a resolved one.
func ProposalResolved(logPath string) bool {
	if logPath == "" {
		return true
	}
	p, err := LoadProposal(ProposalPath(logPath))
	if err != nil {
		return true
	}
	return p.Resolved()
}
