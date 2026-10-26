package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/piciuok/juhas/internal/config"
	"github.com/piciuok/juhas/internal/doctor"
	"github.com/piciuok/juhas/internal/forge"
	"github.com/piciuok/juhas/internal/queue"
	"github.com/piciuok/juhas/internal/review"
	"github.com/piciuok/juhas/internal/state"
)

func init() {
	showSettings = func(key string, s config.Settings) any {
		return struct {
			config.Settings
			ReviewMethod review.Method `json:"review_method"`
		}{s, review.Resolve(s.Path, s.Review)}
	}
	doctorOptions = func(withClaude bool) doctor.Options {
		return doctor.Options{WithClaude: withClaude, SkillInfo: func(key string, s config.Settings) (string, doctor.Level) {
			m := review.ResolveFor(s.Agent, s.Path, s.Review)
			switch m.Kind {
			case review.KindNone:
				return "review: no skill and fallback=none", doctor.Warn
			case review.KindSkill:
				if m.Note != "" {
					return "review: " + m.String() + " (" + m.Note + ")", doctor.Warn
				}
			}
			return "review: " + config.AgentLabel(s.Agent) + " · " + m.String(), doctor.OK
		}}
	}
}

// cmdGuard is the PreToolUse hook: exit 2 with the reason on stderr denies.
func cmdGuard(args []string) error {
	var allow []string
	if len(args) > 1 && args[0] == "--allow" {
		allow = strings.Split(args[1], "\n") // review.guard_allow
		args = args[2:]
	}
	if len(args) > 0 && args[0] != "-" {
		// manual check: juhas guard 'glab mr note 1 -m x'
		if r := review.Guard(strings.Join(args, " ")); r != "" {
			fmt.Println("DENY:", r)
			os.Exit(2)
		}
		fmt.Println("ALLOW")
		return nil
	}
	reason, err := review.RunHook(os.Stdin, allow)
	if err != nil {
		// unreadable payload: fail closed
		fmt.Fprintln(os.Stderr, "juhas guard: unreadable hook input:", err)
		os.Exit(2)
	}
	if reason != "" {
		fmt.Fprintln(os.Stderr, "Blocked by Juhas (review is read-only): "+reason)
		os.Exit(2)
	}
	return nil
}

// lookup fetches a PR/MR by URL through the configured accounts: the
// repo's "account" first, then the others of the host until one sees it.
func lookup(ctx context.Context, cfg *config.Config, url string) (forge.Item, error) {
	platform, repo, num, err := forge.ParseURL(url)
	if err != nil {
		return forge.Item{}, err
	}
	host, project := forge.SplitKey(repo)
	key := repo
	if k, ok := cfg.RepoKey(repo); ok {
		key = k
	}
	s, _ := cfg.Effective(key)
	var tries []forge.Account
	for _, a := range cfg.Accounts() {
		if a.Platform != platform || !strings.EqualFold(a.Host, host) {
			continue
		}
		if s.Account != "" && strings.EqualFold(a.User, s.Account) {
			tries = append([]forge.Account{a}, tries...)
		} else {
			tries = append(tries, a)
		}
	}
	if len(tries) == 0 {
		tries = []forge.Account{{Platform: platform, Host: host}}
	}
	var it forge.Item
	for _, a := range tries {
		if it, err = forge.New(a).Get(ctx, project, num); err == nil {
			break
		}
	}
	if err != nil {
		return it, err
	}
	if k, ok := cfg.RepoKey(it.Repo); ok {
		it.Repo = k
	}
	return it, nil
}

// repoDir is the configured clone, else one found in scan_dirs.
func repoDir(ctx context.Context, cfg *config.Config, key string) string {
	s, _ := cfg.Effective(key)
	if s.Path != "" {
		if st, err := os.Stat(s.Path); err == nil && st.IsDir() {
			return s.Path
		}
	}
	return queue.NewCollector(cfg).Guess(ctx, key)
}

func cmdReview(args []string) error {
	fs := flag.NewFlagSet("review", flag.ExitOnError)
	agent := fs.String("agent", "", "claude | codex (default: the repo setting)")
	fs.Parse(args)
	args = fs.Args()
	if len(args) != 1 {
		return fmt.Errorf("usage: juhas review [--agent claude|codex] <PR/MR url>")
	}
	if *agent != "" && *agent != config.AgentClaude && *agent != config.AgentCodex {
		return fmt.Errorf("--agent %q: claude or codex", *agent)
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	ctx := context.Background()
	it, err := lookup(ctx, cfg, args[0])
	if err != nil {
		return err
	}
	store := state.Open()
	if st, err := store.Read(); err == nil && st.Job(it.URL).LiveOwner() {
		return fmt.Errorf("%s %s is already being reviewed by juhas (pid %d) — see the Panel", it.Repo, it.Ref(), st.Job(it.URL).Owner)
	}
	dir := repoDir(ctx, cfg, it.Repo)
	s, err := cfg.Effective(it.Repo)
	if err != nil {
		return err
	}
	if *agent == "" {
		*agent = s.Agent
	}
	m := review.Result{Agent: *agent, Method: review.ResolveFor(*agent, dir, s.Review)}.Label()
	fmt.Printf("%s %s — %s\n  dir:     %s\n  method:  %s\n  log:     %s\n\nReviewing (this can take several minutes), live progress:\n\n",
		it.Repo, it.Ref(), it.Title, config.Contract(dir), m, review.LogPath(state.Open(), it.URL))

	var final state.Job
	r := &review.Runner{Cfg: cfg, Store: store, OnDone: func(j state.Job) { final = j }, Echo: os.Stdout}
	// Ctrl-C stops the agent too (it runs in its own process group)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() { <-sig; fmt.Println("\nstopping the review…"); r.Stop() }()
	r.Enqueue(it, dir, *agent)
	r.Wait()
	if final.Status != state.Done {
		return fmt.Errorf("%s", final.Error)
	}
	fmt.Println("Brief:", final.Brief)
	return nil
}
