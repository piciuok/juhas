// Package doctor checks that the CLIs, their auth and the repo mapping work in
// the environment the app actually runs in.
package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"juhas/internal/config"
	"juhas/internal/forge"
)

type Level int

const (
	OK Level = iota
	Warn
	Fail
)

func (l Level) Symbol() string {
	return [...]string{"✓", "!", "✗"}[l]
}

type Check struct {
	Level   Level  `json:"level"`
	Section string `json:"section"`
	Msg     string `json:"msg"`
	Hint    string `json:"hint,omitempty"`
}

type Report struct {
	At     time.Time `json:"at"`
	Checks []Check   `json:"checks"`
}

// Worst returns the highest level in the report.
func (r *Report) Worst() Level {
	w := OK
	for _, c := range r.Checks {
		if c.Level > w {
			w = c.Level
		}
	}
	return w
}

// Count returns how many checks have level l.
func (r *Report) Count(l Level) int {
	n := 0
	for _, c := range r.Checks {
		if c.Level == l {
			n++
		}
	}
	return n
}

type Options struct {
	WithClaude bool // run a tiny `claude -p` (costs tokens)
	// SkillInfo, when set, describes which review skill a repo will use.
	SkillInfo func(key string, s config.Settings) (string, Level)
}

func (r *Report) add(l Level, section, msg, hint string) {
	r.Checks = append(r.Checks, Check{Level: l, Section: section, Msg: msg, Hint: hint})
}

// Run executes every check. It never fails as a whole; problems are checks.
func Run(ctx context.Context, cfg *config.Config, opt Options) *Report {
	r := &Report{At: time.Now()}
	tools(ctx, r, cfg)
	github(ctx, r, cfg)
	gitlab(ctx, r, cfg)
	claude(ctx, r, cfg, opt)
	codex(ctx, r, cfg, opt)
	repos(ctx, r, cfg, opt)
	desktop(ctx, r, cfg)
	return r
}

// installHint returns how to install bin on this OS.
func installHint(bin string) string {
	linux := map[string]string{
		"git":    "sudo apt install git  |  sudo dnf install git",
		"gh":     "https://github.com/cli/cli/blob/trunk/docs/install_linux.md (apt/dnf repo) or: brew install gh",
		"glab":   "https://gitlab.com/gitlab-org/cli/-/releases (.deb/.rpm) or: brew install glab",
		"claude": "curl -fsSL https://claude.ai/install.sh | bash",
		"codex":  "npm install -g @openai/codex or: brew install codex",
	}
	mac := map[string]string{
		"git":    "xcode-select --install or: brew install git",
		"gh":     "brew install gh",
		"glab":   "brew install glab",
		"claude": "curl -fsSL https://claude.ai/install.sh | bash",
		"codex":  "brew install codex or: npm install -g @openai/codex",
	}
	if runtime.GOOS == "darwin" {
		return mac[bin]
	}
	return linux[bin]
}

func version(ctx context.Context, bin string) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return "?"
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return line
}

func tools(ctx context.Context, r *Report, cfg *config.Config) {
	need := []string{"git", "gh", "glab", cfg.Claude.Bin}
	if len(cfg.GitLab) == 0 {
		need = []string{"git", "gh", cfg.Claude.Bin}
	}
	// codex is checked in its own section: it is optional
	for _, bin := range need {
		p, err := exec.LookPath(bin)
		if err != nil {
			r.add(Fail, "CLI", bin+": not in PATH", installHint(filepath.Base(bin)))
			continue
		}
		v := version(ctx, p)
		lvl, note, hint := checkVersion(filepath.Base(bin), v)
		r.add(lvl, "CLI", fmt.Sprintf("%s: %s (%s)%s", bin, v, p, note), hint)
	}
}

func github(ctx context.Context, r *Report, cfg *config.Config) {
	if !forge.Available("gh") || len(cfg.GitHub) == 0 {
		return
	}
	st, err := forge.AuthStatus(ctx, "gh")
	if err != nil {
		r.add(Fail, "GitHub", "gh auth status: "+err.Error(), "gh auth login")
		return
	}
	for _, h := range cfg.GitHub {
		a := h.Account(forge.GitHub)
		var found *forge.HostAuth
		for i := range st {
			if !strings.EqualFold(st[i].Host, h.Host) {
				continue
			}
			if h.User == "" && (st[i].Active || found == nil) || h.User != "" && strings.EqualFold(st[i].Account, h.User) {
				found = &st[i]
			}
		}
		login := "gh auth login --hostname " + h.Host
		who := a.Label()
		if h.User == "" {
			who += " (active gh account)"
		}
		if !h.Watched() {
			who += " — not watched"
		}
		if found == nil || !found.LoggedIn {
			if h.User != "" {
				login += "  (log in as " + h.User + "; gh keeps the other accounts)"
			}
			r.add(Fail, "GitHub", who+": not logged in", login)
			continue
		}
		r.add(OK, "GitHub", fmt.Sprintf("%s: logged in as %s", who, found.Account), "")
		for _, scope := range []string{"repo", "read:org"} {
			if !strings.Contains(found.Detail, "'"+scope+"'") && strings.Contains(found.Detail, "Token scopes") {
				hint := "gh auth refresh --hostname " + h.Host + " --scopes " + scope
				if h.User != "" {
					hint = "gh auth switch --hostname " + h.Host + " --user " + h.User + " && " + hint
				}
				msg := a.Label() + ": token lacks scope " + scope
				if scope == "read:org" {
					msg += " (team review requests are not visible)"
				}
				r.add(Warn, "GitHub", msg, hint)
			}
		}
	}
	// accounts gh knows that Juhas does not use
	for _, s := range st {
		if !s.LoggedIn || s.Account == "" {
			continue
		}
		used := false
		for _, h := range cfg.GitHub {
			if strings.EqualFold(h.Host, s.Host) && (h.User == "" && s.Active || strings.EqualFold(h.User, s.Account)) {
				used = true
			}
		}
		if !used {
			r.add(OK, "GitHub", fmt.Sprintf("%s@%s: logged in in gh, not used (Settings → Accounts)", s.Account, s.Host), "")
		}
	}
}

func gitlab(ctx context.Context, r *Report, cfg *config.Config) {
	if !forge.Available("glab") {
		return
	}
	for _, h := range cfg.GitLab {
		a := h.Account(forge.GitLab)
		login := "glab auth login --hostname " + h.Host
		if a.ConfigDir != "" {
			login = "GLAB_CONFIG_DIR=" + config.Contract(a.ConfigDir) + " " + login
		}
		me, err := forge.NewGitLab(a).Me(ctx)
		if err != nil {
			r.add(Fail, "GitLab", a.Label()+": "+err.Error(), login)
			continue
		}
		if h.User != "" && !strings.EqualFold(me, h.User) {
			r.add(Warn, "GitLab", fmt.Sprintf("%s: glab is logged in as %s", a.Label(), me), login)
			continue
		}
		msg := fmt.Sprintf("%s: logged in as %s", h.Host, me)
		if !h.Watched() {
			msg += " — not watched"
		}
		r.add(OK, "GitLab", msg, "")
	}
}

func claude(ctx context.Context, r *Report, cfg *config.Config, opt Options) {
	bin := cfg.Claude.Bin
	if _, err := exec.LookPath(bin); err != nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, bin, "auth", "status", "--json").Output()
	var st struct {
		LoggedIn   bool   `json:"loggedIn"`
		AuthMethod string `json:"authMethod"`
		Email      string `json:"email"`
	}
	if err != nil || json.Unmarshal(out, &st) != nil {
		r.add(Warn, "Claude", "could not read `claude auth status`", "run `claude` once in a terminal and log in")
	} else if !st.LoggedIn {
		r.add(Fail, "Claude", "not logged in", "run `claude` in a terminal and log in")
	} else {
		r.add(OK, "Claude", fmt.Sprintf("logged in (%s, %s)", st.AuthMethod, st.Email), "")
	}
	m := cfg.Claude.Model
	if m == "" {
		m = "default"
	}
	e := cfg.Claude.Effort
	if e == "" {
		e = "default"
	}
	r.add(OK, "Claude", fmt.Sprintf("model: %s · effort: %s · protection: hook guard + dontAsk", m, e), "")
	if opt.WithClaude {
		cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		cmd := exec.CommandContext(cctx, bin, "-p", "--max-turns", "1", "--output-format", "text")
		cmd.Stdin = strings.NewReader("Reply with one word: OK")
		out, err := cmd.CombinedOutput()
		if err != nil {
			r.add(Fail, "Claude", "claude -p: "+strings.TrimSpace(string(out)), "")
		} else {
			r.add(OK, "Claude", "claude -p replies: "+strings.TrimSpace(string(out)), "")
		}
	}
}

// usesCodex: some repo (or the default) reviews with Codex.
func usesCodex(cfg *config.Config) bool {
	for _, k := range append(cfg.RepoKeys(), "") {
		if s, err := cfg.Effective(k); err == nil && s.Agent == config.AgentCodex {
			return true
		}
	}
	return false
}

func codex(ctx context.Context, r *Report, cfg *config.Config, opt Options) {
	bin := cfg.Codex.Bin
	p, err := exec.LookPath(bin)
	if err != nil {
		lvl := Warn
		if usesCodex(cfg) {
			lvl = Fail
		}
		r.add(lvl, "Codex", bin+": not in PATH (only needed if you want reviews with Codex)", installHint("codex"))
		return
	}
	v := version(ctx, p)
	lvl, note, hint := checkVersion("codex", v)
	r.add(lvl, "Codex", fmt.Sprintf("%s: %s (%s)%s", bin, v, p, note), hint)
	shadowed(ctx, r, "Codex", bin, p, v)
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, bin, "login", "status").CombinedOutput()
	if st := strings.TrimSpace(string(out)); err != nil || !strings.Contains(strings.ToLower(st), "logged in") {
		r.add(Fail, "Codex", "not logged in: "+oneLine(st), "codex login")
	} else {
		r.add(OK, "Codex", st, "")
	}
	m := cfg.Codex.Model
	if m == "" {
		m = "from ~/.codex/config.toml"
	}
	e := cfg.Codex.Effort
	if e == "" {
		e = "default"
	}
	r.add(OK, "Codex", fmt.Sprintf("model: %s · effort: %s · sandbox: read-only, no network (the app fetches PR data)", m, e), "")
	if opt.WithClaude {
		cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
		defer cancel()
		args := []string{"exec", "--sandbox", "read-only", "--ephemeral", "--skip-git-repo-check", "--color", "never"}
		if cfg.Codex.Model != "" {
			args = append(args, "--model", cfg.Codex.Model)
		}
		cmd := exec.CommandContext(cctx, bin, append(args, "Reply with one word: OK")...)
		cmd.Stdin = strings.NewReader("")
		out, err := cmd.CombinedOutput()
		if err != nil {
			r.add(Fail, "Codex", "codex exec: "+lastLines(string(out), 2), "check the model in Settings (codex.model) or update: codex update")
		} else {
			r.add(OK, "Codex", "codex exec replies: "+lastLines(string(out), 1), "")
		}
	}
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		s = s[:159] + "…"
	}
	return s
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return oneLine(strings.Join(lines, " "))
}

func repos(ctx context.Context, r *Report, cfg *config.Config, opt Options) {
	briefs := config.Expand(cfg.BriefsDir)
	if err := os.MkdirAll(briefs, 0o755); err != nil {
		r.add(Fail, "Repo", "briefs_dir: "+err.Error(), "")
	}
	if len(cfg.Repos) == 0 {
		r.add(Warn, "Repo", "no mappings in repos", "juhas config scan --write")
	}
	gl := map[string]*forge.GitLabForge{}
	for _, h := range cfg.GitLab {
		if _, ok := gl[strings.ToLower(h.Host)]; !ok {
			gl[strings.ToLower(h.Host)] = forge.NewGitLab(h.Account(forge.GitLab))
		}
	}
	for _, key := range cfg.RepoKeys() {
		s, err := cfg.Effective(key)
		if err != nil {
			r.add(Fail, "Repo", key+": "+err.Error(), "")
			continue
		}
		if s.OnNew == config.OnNewOff {
			continue
		}
		switch {
		case s.Path == "":
			r.add(Warn, "Repo", key+": no path (review only possible after auto-mapping)", "juhas config scan --write")
			continue
		case !isDir(s.Path):
			r.add(Fail, "Repo", key+": directory does not exist: "+config.Contract(s.Path), "fix the path or remove the repo from the config")
			continue
		}
		out, err := exec.CommandContext(ctx, "git", "-C", s.Path, "remote", "get-url", "origin").Output()
		got, ok := forge.RemoteKey(strings.TrimSpace(string(out)))
		switch {
		case err != nil:
			r.add(Fail, "Repo", key+": "+config.Contract(s.Path)+" is not a git repo with an origin", "")
			continue
		case !ok || !strings.EqualFold(got, key):
			r.add(Warn, "Repo", fmt.Sprintf("%s: origin in %s is %s", key, config.Contract(s.Path), got), "check the mapping")
			continue
		}
		msg := fmt.Sprintf("%s → %s [%s]", key, config.Contract(s.Path), s.Mode)
		lvl := OK
		if opt.SkillInfo != nil {
			info, l := opt.SkillInfo(key, s)
			msg += " · " + info
			if l > lvl {
				lvl = l
			}
		}
		r.add(lvl, "Repo", msg, "")

		// A GitLab project where nobody is ever set as reviewer finds nothing in
		// requested mode; suggest open mode.
		host, project := forge.SplitKey(key)
		if f, ok := gl[strings.ToLower(host)]; ok && s.Mode == config.ModeRequested {
			if items, err := f.OpenRaw(ctx, project); err == nil && len(items) >= 3 && !anyReviewer(items) {
				r.add(Warn, "Repo", fmt.Sprintf("%s: none of the %d open MRs has a reviewer — requested mode will find nothing", key, len(items)),
					`set "mode": "open" in repos["`+key+`"]`)
			}
		}
	}
}

func anyReviewer(items []forge.RawMR) bool {
	for _, it := range items {
		if it.Reviewers > 0 {
			return true
		}
	}
	return false
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// shadowed warns when another copy of bin later in PATH has a different
// version: the tray and the terminal may then run different ones.
func shadowed(ctx context.Context, r *Report, section, bin, first, v string) {
	if strings.ContainsRune(bin, os.PathSeparator) {
		return
	}
	seen := map[string]bool{first: true}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		p := filepath.Join(dir, bin)
		if real, err := filepath.EvalSymlinks(p); err == nil && !seen[p] && !seen[real] {
			if st, err := os.Stat(real); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
				seen[p], seen[real] = true, true
				if real == first {
					continue
				}
				if ov := version(ctx, p); ov != v {
					r.add(Warn, section, fmt.Sprintf("%s: %s (%s) is used; another copy %s (%s) comes later in PATH", bin, v, first, ov, p),
						"remove the one you do not use, or set "+strings.ToLower(section)+".bin to the full path in Settings")
				}
			}
		}
	}
}
