// Package configui serves a local web page that edits config.json: a form
// over the same JSON, with the builtin -> defaults -> repo layering visible.
// It listens on 127.0.0.1 only and requires a per-session token.
package configui

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/piciuok/juhas/internal/config"
	"github.com/piciuok/juhas/internal/doctor"
	"github.com/piciuok/juhas/internal/forge"
	"github.com/piciuok/juhas/internal/review"
	"github.com/piciuok/juhas/internal/scan"
	"github.com/piciuok/juhas/internal/state"
)

//go:embed index.html
var indexHTML []byte

//go:embed log.html
var logHTML []byte

//go:embed panel.html
var panelHTML []byte

//go:embed review.html
var reviewHTML []byte

// Panel is the tray app behind the panel page.
type Panel interface {
	PanelModel() any
	PanelDo(action, url, agent string, on bool) error
	// PanelProjects lists the projects one can pin, matching q.
	PanelProjects(q string) (any, error)
}

// Detect describes how a repo would be reviewed (set by main to avoid an
// import cycle with the review package).
var Detect = func(path string, r config.Review) any { return nil }

// Server is one editing session.
type Server struct {
	Path   string // config file
	OnSave func() // e.g. tray: re-poll with the new config
	// Persistent servers (the tray's) never idle out.
	Persistent bool
	// Panel, when set, serves /panel (the tray sets it).
	Panel Panel
	// OnReview runs after a proposal was saved or published (tray: redraw).
	OnReview func()
	// Doctor runs the diagnostics (withAI: also call the agents, costs
	// tokens); LastDoctor returns the latest report. Set by the tray and by
	// `juhas config ui`.
	Doctor     func(withAI bool) *doctor.Report
	LastDoctor func() *doctor.Report
	// ChatEnv, when set, runs the chat on the review page (the tray sets it).
	ChatEnv func() review.ChatEnv

	token string
	srv   *http.Server
	ln    net.Listener
	idle  *time.Timer
	done  chan struct{}
	once  sync.Once
}

// IdleTimeout closes the session when the page has been quiet this long.
var IdleTimeout = 30 * time.Minute

// Start listens on a random loopback port and returns the settings URL.
func Start(path string, onSave func()) (*Server, string, error) {
	return start(path, onSave, false)
}

// StartPersistent is Start for the tray: one server for its whole life,
// serving settings and live run logs.
func StartPersistent(path string, onSave func()) (*Server, string, error) {
	return start(path, onSave, true)
}

// ReviewURL is the proposal page of a run (by its log path).
func (s *Server) ReviewURL(logPath string) string {
	return fmt.Sprintf("http://%s/review?t=%s&job=%s", s.ln.Addr(), s.token, strings.TrimSuffix(filepath.Base(logPath), ".log"))
}

func (s *Server) reviewPage(w http.ResponseWriter, r *http.Request) {
	if !s.tokenOK(r.URL.Query().Get("t")) || !jobID.MatchString(r.URL.Query().Get("job")) {
		http.Error(w, "missing or invalid token", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src 'self' data:; connect-src 'self'")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(reviewHTML)
}

func proposalFile(id string) (string, error) {
	if !jobID.MatchString(id) {
		return "", errors.New("invalid identifier")
	}
	return review.ProposalPath(filepath.Join(state.Open().LogDir(), id+".log")), nil
}

// current fetches the PR/MR as it is now (to spot new commits).
func current(r *http.Request, p *review.Proposal) (forge.Item, error) {
	platform, repo, num, err := forge.ParseURL(p.URL)
	if err != nil {
		return forge.Item{}, err
	}
	host, project := forge.SplitKey(repo)
	f := forge.New(forge.AccountFor(platform, host, p.Account))
	it, err := f.Get(r.Context(), project, num)
	it.Repo = repo
	return it, err
}

func (s *Server) reviewGet(w http.ResponseWriter, r *http.Request) (any, error) {
	path, err := proposalFile(r.URL.Query().Get("job"))
	if err != nil {
		return nil, err
	}
	p, err := review.LoadProposal(path)
	if err != nil {
		return nil, errors.New("no proposal for this review (the agent returned no comments block)")
	}
	resp := map[string]any{"proposal": p, "head_now": "", "stale": false, "lookup_error": ""}
	if it, err := current(r, p); err != nil {
		resp["lookup_error"] = err.Error()
	} else {
		resp["head_now"] = it.HeadSHA
		resp["stale"] = it.HeadSHA != "" && p.HeadSHA != "" && it.HeadSHA != p.HeadSHA
	}
	return resp, nil
}

// reviewFile shows one file a comment is about: its diff and its content at
// the reviewed commit. Only files the proposal mentions can be asked for.
func (s *Server) reviewFile(w http.ResponseWriter, r *http.Request) (any, error) {
	path, err := proposalFile(r.URL.Query().Get("job"))
	if err != nil {
		return nil, err
	}
	p, err := review.LoadProposal(path)
	if err != nil {
		return nil, err
	}
	file := r.URL.Query().Get("file")
	known := false
	for _, c := range p.Comments {
		known = known || (file != "" && c.File == file)
	}
	if !known || strings.HasPrefix(file, "/") || slices.Contains(strings.Split(file, "/"), "..") {
		return nil, errors.New("this file is not part of the proposal")
	}
	platform, repo, num, err := forge.ParseURL(p.URL)
	if err != nil {
		return nil, err
	}
	it := forge.Item{Platform: platform, Repo: repo, Number: num, URL: p.URL, HeadSHA: p.HeadSHA, Account: p.Account}
	return forge.File(r.Context(), it, file, p.HeadSHA)
}

type commentEdit struct {
	ID     string  `json:"id"`
	Body   *string `json:"body"` // nil = unchanged; "" is a real (empty) edit
	Status string  `json:"status"`
}

// reviewSave stores edits and accept/reject decisions; posted ones are final.
func (s *Server) reviewSave(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Job      string        `json:"job"`
		Comments []commentEdit `json:"comments"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, err
	}
	path, err := proposalFile(req.Job)
	if err != nil {
		return nil, err
	}
	unlock, err := review.LockProposal(path, true) // waits out a publish, then applies on top of it
	if err != nil {
		return nil, err
	}
	defer unlock()
	p, err := review.LoadProposal(path)
	if err != nil {
		return nil, err
	}
	for _, e := range req.Comments {
		c, err := p.Find(e.ID)
		if err != nil || c.Status == review.CommentPosted {
			continue
		}
		switch e.Status {
		case review.CommentPending, review.CommentAccepted, review.CommentRejected:
			c.Status = e.Status
		}
		if e.Body != nil {
			c.Body = strings.TrimSpace(*e.Body)
		}
	}
	if err := review.SaveProposal(path, p); err != nil {
		return nil, err
	}
	if s.OnReview != nil {
		s.OnReview()
	}
	return map[string]any{"proposal": p}, nil
}

// reviewPublish posts the accepted comments from the user's gh/glab account.
func (s *Server) reviewPublish(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Job   string `json:"job"`
		Force bool   `json:"force"`
		Event string `json:"event"` // comment (default) | approve | request_changes
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		return nil, err
	}
	switch req.Event {
	case "":
		req.Event = forge.EventComment
	case forge.EventComment, forge.EventApprove, forge.EventRequestChanges:
	default:
		return nil, fmt.Errorf("unknown action %q", req.Event)
	}
	path, err := proposalFile(req.Job)
	if err != nil {
		return nil, err
	}
	unlock, err := review.LockProposal(path, false) // a second click or tab must not post again
	if err != nil {
		return nil, err
	}
	defer unlock()
	p, err := review.LoadProposal(path)
	if err != nil {
		return nil, err
	}
	it, err := current(r, p)
	if err != nil {
		return nil, fmt.Errorf("could not fetch the PR/MR: %w", err)
	}
	if it.HeadSHA != p.HeadSHA && p.HeadSHA != "" && !req.Force {
		return nil, errors.New("stale: new commits since the review — lines may have shifted")
	}
	if p.HeadSHA != "" {
		it.HeadSHA = p.HeadSHA // anchor comments to the commit that was reviewed
	}
	summary := ""
	if p.Summary.Status == review.CommentAccepted {
		summary = p.Summary.Body
	}
	if p.Summary.Status == review.CommentAccepted && summary == "" {
		return nil, errors.New("the accepted general comment is empty")
	}
	var comments []forge.PostComment
	for _, c := range p.Comments {
		if c.Status == review.CommentAccepted && c.Body == "" {
			return nil, fmt.Errorf("the accepted comment on %s is empty", c.File)
		}
		if c.Status == review.CommentAccepted {
			comments = append(comments, forge.PostComment{ID: c.ID, File: c.File, Line: c.Line, Body: c.Body})
		}
	}
	if summary == "" && len(comments) == 0 && req.Event == forge.EventComment {
		return nil, errors.New("nothing is accepted")
	}
	if summary == "" && req.Event == forge.EventRequestChanges {
		return nil, errors.New("requesting changes needs an accepted general comment that says why")
	}
	sum, res, dec := forge.Publish(r.Context(), it, summary, comments, req.Event)
	if req.Event != forge.EventComment {
		p.Decision = &review.Decision{Event: req.Event, Status: review.CommentPosted, URL: dec.URL}
		if dec.Err != nil {
			p.Decision.Status, p.Decision.Error = review.CommentFailed, dec.Err.Error()
		} else {
			p.SkipUndecided() // decided without them: nothing is left waiting
		}
	}
	apply := func(pr forge.PostResult) {
		c, err := p.Find(pr.ID)
		if err != nil {
			return
		}
		if pr.Err != nil {
			c.Status, c.Error = review.CommentFailed, pr.Err.Error()
			return
		}
		c.Status, c.Posted, c.Note, c.Error = review.CommentPosted, pr.URL, pr.Note, ""
	}
	if summary != "" {
		apply(sum)
	}
	for _, pr := range res {
		apply(pr)
	}
	if err := review.SaveProposal(path, p); err != nil {
		return nil, err
	}
	if s.OnReview != nil {
		s.OnReview()
	}
	return map[string]any{"proposal": p}, nil
}

// PanelURL is the panel page.
func (s *Server) PanelURL() string {
	return fmt.Sprintf("http://%s/panel?t=%s", s.ln.Addr(), s.token)
}

func (s *Server) panelPage(w http.ResponseWriter, r *http.Request) {
	if !s.tokenOK(r.URL.Query().Get("t")) {
		http.Error(w, "missing or invalid token — open the Panel from the tray icon menu", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src 'self' data:; connect-src 'self'")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(panelHTML)
}

func (s *Server) panelDo(w http.ResponseWriter, r *http.Request) (any, error) {
	if s.Panel == nil {
		return nil, errors.New("the panel only works while the tray icon is running")
	}
	var req struct {
		Action string `json:"action"`
		URL    string `json:"url"`
		Agent  string `json:"agent"`
		On     bool   `json:"on"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		return nil, err
	}
	if err := s.Panel.PanelDo(req.Action, req.URL, req.Agent, req.On); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, nil
}

// LogURL is the live viewer for a run log in the state jobs directory.
func (s *Server) LogURL(logPath string) string {
	return fmt.Sprintf("http://%s/log?t=%s&job=%s", s.ln.Addr(), s.token, strings.TrimSuffix(filepath.Base(logPath), ".log"))
}

func start(path string, onSave func(), persistent bool) (*Server, string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, "", err
	}
	s := &Server{Path: path, OnSave: onSave, Persistent: persistent, token: hex.EncodeToString(b), done: make(chan struct{})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", err
	}
	s.ln = ln
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.page)
	mux.HandleFunc("GET /log", s.logPage)
	mux.HandleFunc("GET /panel", s.panelPage)
	mux.HandleFunc("GET /review", s.reviewPage)
	mux.HandleFunc("GET /doctor", s.doctorPage)
	mux.HandleFunc("GET /api/doctor", s.api(s.doctorGet))
	mux.HandleFunc("POST /api/doctor/run", s.api(s.doctorRun))
	mux.HandleFunc("GET /api/review", s.api(s.reviewGet))
	mux.HandleFunc("GET /api/review/file", s.api(s.reviewFile))
	mux.HandleFunc("POST /api/review/save", s.api(s.reviewSave))
	mux.HandleFunc("POST /api/review/publish", s.api(s.reviewPublish))
	mux.HandleFunc("GET /api/chat", s.api(s.chatGet))
	mux.HandleFunc("POST /api/chat/send", s.api(s.chatSend))
	mux.HandleFunc("POST /api/chat/cancel", s.api(s.chatCancel))
	mux.HandleFunc("POST /api/chat/reset", s.api(s.chatReset))
	mux.HandleFunc("POST /api/chat/comment", s.api(s.chatComment))
	mux.HandleFunc("GET /api/panel", s.api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		if s.Panel == nil {
			return nil, errors.New("the panel only works while the tray icon is running (juhas tray)")
		}
		return s.Panel.PanelModel(), nil
	}))
	mux.HandleFunc("POST /api/panel/do", s.api(s.panelDo))
	mux.HandleFunc("GET /api/panel/projects", s.api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		if s.Panel == nil {
			return nil, errors.New("the panel only works while the tray icon is running (juhas tray)")
		}
		return s.Panel.PanelProjects(r.URL.Query().Get("q"))
	}))
	mux.HandleFunc("GET /api/log", s.api(s.logChunk))
	mux.HandleFunc("GET /api/config", s.api(s.getConfig))
	mux.HandleFunc("POST /api/validate", s.api(s.validate))
	mux.HandleFunc("POST /api/save", s.api(s.save))
	mux.HandleFunc("POST /api/scan", s.api(s.scan))
	mux.HandleFunc("GET /api/ls", s.api(s.ls))
	mux.HandleFunc("POST /api/accounts", s.api(s.accounts))
	mux.HandleFunc("POST /api/close", s.api(func(w http.ResponseWriter, r *http.Request) (any, error) {
		if s.Persistent {
			return map[string]bool{"ok": true}, nil // the tray owns this server
		}
		go s.Close()
		return map[string]bool{"ok": true}, nil
	}))
	s.srv = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// every page carries the token in its URL: never frame it, never send it on
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		mux.ServeHTTP(w, r)
	}), ReadHeaderTimeout: 10 * time.Second}
	s.idle = time.AfterFunc(IdleTimeout, func() {
		if !s.Persistent {
			s.Close()
		}
	})
	go func() { _ = s.srv.Serve(ln) }()
	return s, fmt.Sprintf("http://%s/?t=%s", ln.Addr(), s.token), nil
}

// Done is closed when the session ends.
func (s *Server) Done() <-chan struct{} { return s.done }

func (s *Server) Close() {
	s.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(ctx)
		close(s.done)
	})
}

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" || !s.tokenOK(r.URL.Query().Get("t")) {
		http.Error(w, "missing or invalid token — reopen Settings from the menu", http.StatusForbidden)
		return
	}
	s.idle.Reset(IdleTimeout)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src 'self' data:; connect-src 'self'")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	_, _ = w.Write(indexHTML)
}

func (s *Server) logPage(w http.ResponseWriter, r *http.Request) {
	if !s.tokenOK(r.URL.Query().Get("t")) || !jobID.MatchString(r.URL.Query().Get("job")) {
		http.Error(w, "missing or invalid token", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src 'self' data:; connect-src 'self'")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(logHTML)
}

var jobID = regexp.MustCompile(`^[0-9a-f]{12}$`)

// logChunk returns the log from offset on, plus the job's status.
func (s *Server) logChunk(w http.ResponseWriter, r *http.Request) (any, error) {
	id := r.URL.Query().Get("job")
	if !jobID.MatchString(id) {
		return nil, errors.New("invalid identifier")
	}
	store := state.Open()
	path := filepath.Join(store.LogDir(), id+".log")
	offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	resp := map[string]any{"offset": offset, "text": "", "status": "", "title": "", "url": "", "brief": "", "error": "", "run": ""}
	if st, err := store.Read(); err == nil {
		for _, j := range st.Jobs {
			if j.Log == path {
				resp["status"], resp["title"], resp["url"], resp["brief"], resp["error"] = j.Status, j.Repo+" "+j.Title, j.URL, j.Brief, j.Error
				resp["run"] = strconv.FormatInt(j.StartedAt.UnixNano(), 36)
			}
		}
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return resp, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// a new run truncates and rewrites the same file: start over when the
	// run changed (or the file got shorter)
	if fi, err := f.Stat(); err == nil && (offset > fi.Size() || (r.URL.Query().Get("run") != "" && r.URL.Query().Get("run") != resp["run"])) {
		offset = 0
		resp["reset"] = true
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return nil, err
	}
	resp["text"], resp["offset"] = string(b), offset+int64(len(b))
	return resp, nil
}

func (s *Server) tokenOK(t string) bool {
	return subtle.ConstantTimeCompare([]byte(t), []byte(s.token)) == 1
}

// api wraps a JSON handler: token header (no cookies, so no CSRF), loopback
// Host (no DNS rebinding), JSON out.
func (s *Server) api(fn func(http.ResponseWriter, *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.Host)
		if host != "127.0.0.1" || !s.tokenOK(r.Header.Get("X-Token")) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		s.idle.Reset(IdleTimeout)
		v, err := fn(w, r)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if err != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(v)
	}
}

type configResp struct {
	Path      string          `json:"path"`
	Raw       json.RawMessage `json:"raw"`
	Builtin   json.RawMessage `json:"builtin"`
	Effective map[string]any  `json:"effective"`
	Clones    []cloneInfo     `json:"clones"`
	Exists    bool            `json:"exists"`
	Version   string          `json:"version"` // of the file on disk; sent back on save
}

type cloneInfo struct {
	Key        string `json:"key"`
	Dir        string `json:"dir"`
	Configured bool   `json:"configured"`
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) (any, error) {
	b, err := os.ReadFile(s.Path)
	exists := err == nil
	if errors.Is(err, os.ErrNotExist) {
		d := config.Default()
		b, err = json.Marshal(d)
	}
	if err != nil {
		return nil, err
	}
	resp := configResp{Path: s.Path, Raw: b, Builtin: json.RawMessage(config.BuiltinSettings()), Exists: exists}
	if exists {
		resp.Version = config.Version(b)
	}
	if cfg, err := config.Parse(b, s.Path); err == nil {
		resp.Effective = effective(cfg)
		resp.Clones = clones(r.Context(), cfg)
	}
	return resp, nil
}

func effective(cfg *config.Config) map[string]any {
	out := map[string]any{}
	keys := append(cfg.RepoKeys(), "")
	for _, k := range keys {
		s, err := cfg.Effective(k)
		if err != nil {
			continue
		}
		out[k] = map[string]any{"settings": s, "method": Detect(s.Path, s.Review)}
	}
	return out
}

func clones(ctx context.Context, cfg *config.Config) []cloneInfo {
	var roots, hosts []string
	for _, d := range cfg.ScanDirs {
		roots = append(roots, config.Expand(d))
	}
	hosts = cfg.HostNames()
	var out []cloneInfo
	seen := map[string]bool{}
	for _, c := range scan.OnHosts(scan.Clones(ctx, roots), hosts) {
		if seen[strings.ToLower(c.Key)] {
			continue
		}
		seen[strings.ToLower(c.Key)] = true
		_, ok := cfg.RepoKey(c.Key)
		out = append(out, cloneInfo{Key: c.Key, Dir: config.Contract(c.Dir), Configured: ok})
	}
	return out
}

type rawReq struct {
	Raw json.RawMessage `json:"raw"`
}

func readRaw(r *http.Request) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var req rawReq
	if err := json.Unmarshal(b, &req); err != nil {
		return nil, err
	}
	return req.Raw, nil
}

func (s *Server) validate(w http.ResponseWriter, r *http.Request) (any, error) {
	raw, err := readRaw(r)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Parse(raw, s.Path)
	if err != nil {
		return nil, cleanErr(err, s.Path)
	}
	return map[string]any{"effective": effective(cfg)}, nil
}

type dirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"` // ~-contracted
	Git  bool   `json:"git"`
	Key  string `json:"key,omitempty"` // host/path of origin, for clones
}

// protected: folders macOS guards with a consent prompt (TCC). The picker
// lists them but does not look inside until you open one.
func protected(dir string) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	home := config.Home()
	switch filepath.Dir(dir) {
	case home:
		switch filepath.Base(dir) {
		case "Desktop", "Documents", "Downloads", "Music", "Movies", "Pictures", "Library", "Public":
			return true
		}
	case "/Volumes", "/Network":
		return true
	}
	return dir == "/Volumes" || dir == "/Network"
}

// ls lists the subdirectories of path (default ~) for the folder picker:
// a browser cannot hand a real filesystem path to the page, so the page
// walks the tree through the app instead.
func (s *Server) ls(w http.ResponseWriter, r *http.Request) (any, error) {
	p := config.Expand(r.URL.Query().Get("path"))
	if p == "" {
		p = config.Home()
	}
	p, err := filepath.Abs(p)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	hidden := r.URL.Query().Get("hidden") == "1"
	dirs := []dirEntry{}
	for _, e := range entries {
		name := e.Name()
		if !hidden && strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(p, name)
		if !e.IsDir() {
			// follow symlinks to directories
			if st, err := os.Stat(full); err != nil || !st.IsDir() {
				continue
			}
		}
		d := dirEntry{Name: name, Path: config.Contract(full)}
		if protected(full) {
			// looking inside asks macOS for consent in Juhas's name (Music,
			// Photos, Documents…, network volumes): only when you open it
			dirs = append(dirs, d)
			continue
		}
		if _, err := os.Stat(filepath.Join(full, ".git")); err == nil {
			d.Git = true
			if out, err := exec.CommandContext(r.Context(), "git", "-C", full, "remote", "get-url", "origin").Output(); err == nil {
				d.Key, _ = forge.RemoteKey(strings.TrimSpace(string(out)))
			}
		}
		dirs = append(dirs, d)
	}
	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name) })
	parent := filepath.Dir(p)
	if parent == p {
		parent = ""
	}
	return map[string]any{"path": config.Contract(p), "abs": p, "parent": config.Contract(parent), "home": config.Contract(config.Home()), "dirs": dirs}, nil
}

// scan lists clones for the submitted (maybe unsaved) scan_dirs and hosts.
func (s *Server) scan(w http.ResponseWriter, r *http.Request) (any, error) {
	raw, err := readRaw(r)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Parse(raw, s.Path)
	if err != nil {
		return nil, cleanErr(err, s.Path)
	}
	return map[string]any{"clones": clones(r.Context(), cfg)}, nil
}

// save validates, keeps a .bak of the previous file and writes the JSON
// as submitted (key order preserved), re-indented.
func (s *Server) save(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Raw  json.RawMessage `json:"raw"`
		Base string          `json:"base"` // version the page loaded
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		return nil, err
	}
	raw := []byte(req.Raw)
	if _, err := config.Parse(raw, s.Path); err != nil {
		return nil, cleanErr(err, s.Path)
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	version, err := config.WriteRaw(s.Path, buf.Bytes(), req.Base)
	if errors.Is(err, config.ErrChanged) {
		return nil, errors.New("conflict: Juhas (or another Settings tab) changed the config since this page loaded — e.g. it auto-mapped a clone. Reload to see it, then make your change again")
	}
	if err != nil {
		return nil, err
	}
	if s.OnSave != nil {
		go s.OnSave()
	}
	return map[string]any{"ok": true, "saved": time.Now().Format("15:04:05"), "version": version}, nil
}

func cleanErr(err error, path string) error {
	return errors.New(strings.TrimPrefix(err.Error(), path+": "))
}
