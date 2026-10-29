package configui

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/piciuok/juhas/internal/forge"
	"github.com/piciuok/juhas/internal/review"
	"github.com/piciuok/juhas/internal/state"
)

// The chat next to the review proposal: a local follow-up conversation with
// the agent about the reviewed PR/MR.

type chatMessage struct {
	review.ChatMessage
	Comments []review.ChatComment `json:"comments,omitempty"`
}

func (s *Server) chatEnv() (review.ChatEnv, error) {
	if s.ChatEnv == nil {
		return review.ChatEnv{}, errors.New("the chat only works while the tray icon is running")
	}
	return s.ChatEnv(), nil
}

func (s *Server) chatGet(w http.ResponseWriter, r *http.Request) (any, error) {
	id := r.URL.Query().Get("job")
	c, err := review.LoadChat(id)
	if err != nil {
		return nil, err
	}
	resp := map[string]any{"busy": review.ChatManager.Busy(id), "progress": "", "available": s.ChatEnv != nil}
	if st, err := state.Open().Read(); err == nil {
		if j := review.JobByID(st, id); j != nil {
			ref := ""
			if p, _, n, err := forge.ParseURL(j.URL); err == nil {
				ref = (forge.Item{Platform: p, Number: n}).Ref()
			}
			resp["job"] = map[string]any{"url": j.URL, "repo": j.Repo, "ref": ref, "title": j.Title, "brief": j.Brief != "",
				"method": j.Method, "sha": j.BriefSHA, "resumable": j.Session != nil, "status": j.Status}
		}
	}
	msgs := make([]chatMessage, 0, len(c.Messages))
	for _, m := range c.Messages {
		cm := chatMessage{ChatMessage: m}
		if m.Role == review.RoleAssistant {
			cm.Comments = review.ChatComments(m.Text)
		}
		msgs = append(msgs, cm)
	}
	resp["messages"] = msgs
	resp["agent"] = c.Agent
	resp["from_review"] = c.Review != ""
	if resp["busy"] == true {
		resp["progress"] = tail(review.ChatLogPath(id), 12)
	}
	return resp, nil
}

// tail is the last n lines of a file.
func tail(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(b) > 64<<10 {
		b = b[len(b)-64<<10:]
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

type chatReq struct {
	Job  string `json:"job"`
	Text string `json:"text"`
	File string `json:"file"`
	Line int    `json:"line"`
}

func readChatReq(r *http.Request) (chatReq, error) {
	var req chatReq
	err := json.NewDecoder(io.LimitReader(r.Body, 1<<18)).Decode(&req)
	return req, err
}

func (s *Server) chatSend(w http.ResponseWriter, r *http.Request) (any, error) {
	req, err := readChatReq(r)
	if err != nil {
		return nil, err
	}
	env, err := s.chatEnv()
	if err != nil {
		return nil, err
	}
	if err := review.ChatManager.Send(env, req.Job, req.Text); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, nil
}

func (s *Server) chatCancel(w http.ResponseWriter, r *http.Request) (any, error) {
	req, err := readChatReq(r)
	if err != nil {
		return nil, err
	}
	review.ChatManager.Cancel(req.Job)
	return map[string]bool{"ok": true}, nil
}

func (s *Server) chatReset(w http.ResponseWriter, r *http.Request) (any, error) {
	req, err := readChatReq(r)
	if err != nil {
		return nil, err
	}
	if err := review.ChatManager.Reset(req.Job); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, nil
}

// chatComment adds a comment the chat suggested to the proposal (pending).
func (s *Server) chatComment(w http.ResponseWriter, r *http.Request) (any, error) {
	req, err := readChatReq(r)
	if err != nil {
		return nil, err
	}
	var job *state.Job
	if st, err := state.Open().Read(); err == nil {
		job = review.JobByID(st, req.Job)
	}
	if err := review.AddChatComment(req.Job, review.ChatComment{File: req.File, Line: req.Line, Body: req.Text}, job); err != nil {
		return nil, err
	}
	if s.OnReview != nil {
		s.OnReview()
	}
	return map[string]bool{"ok": true}, nil
}
