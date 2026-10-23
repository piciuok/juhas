package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"juhas/internal/config"
	"juhas/internal/doctor"
	"juhas/internal/forge"
	"juhas/internal/queue"
	"juhas/internal/review"
	"juhas/internal/state"
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
	if len(args) > 0 && args[0] != "-" {
		// manual check: juhas guard 'glab mr note 1 -m x'
		if r := review.Guard(strings.Join(args, " ")); r != "" {
			fmt.Println("DENY:", r)
			os.Exit(2)
		}
		fmt.Println("ALLOW")
		return nil
	}
	reason, err := review.RunHook(os.Stdin)
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

// lookup fetches a PR/MR by URL through the configured forge.
func lookup(ctx context.Context, cfg *config.Config, url string) (forge.Item, error) {
	platform, repo, num, err := forge.ParseURL(url)
	if err != nil {
		return forge.Item{}, err
	}
	host, project := forge.SplitKey(repo)
	var f forge.Forge
	if platform == forge.GitHub {
		f = forge.NewGitHub(host)
	} else {
		f = forge.NewGitLab(host)
	}
	it, err := f.Get(ctx, project, num)
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

	store := state.Open()
	var final state.Job
	r := &review.Runner{Cfg: cfg, Store: store, OnDone: func(j state.Job) { final = j }, Echo: os.Stdout}
	r.Enqueue(it, dir, *agent)
	r.Wait()
	if final.Status != state.Done {
		return fmt.Errorf("%s", final.Error)
	}
	fmt.Println("Brief:", final.Brief)
	return nil
}
