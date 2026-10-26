package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/piciuok/juhas/internal/config"
	"github.com/piciuok/juhas/internal/forge"
	"github.com/piciuok/juhas/internal/queue"
)

func loadConfig() (*config.Config, error) {
	c, err := config.Load(config.DefaultPath())
	if err != nil {
		return nil, fmt.Errorf("%w\nCreate it with: juhas config init", err)
	}
	forge.SetAccounts(c.Accounts())
	return c, nil
}

func cmdPoll(args []string) error {
	fs := flag.NewFlagSet("poll", flag.ExitOnError)
	dry := fs.Bool("dry-run", false, "only print the queue; do not ask or run reviews")
	asJSON := fs.Bool("json", false, "print the snapshot as JSON")
	fs.Parse(args)

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	snap := queue.NewCollector(cfg).Collect(context.Background())
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(snap)
	}
	printSnapshot(snap)
	if *dry {
		return nil
	}
	return pollActions(cfg, snap)
}

func printSnapshot(snap *queue.Snapshot) {
	fmt.Printf("Queue: %d to review, %d urgent\n\n", len(snap.Entries), snap.UrgentCount())
	for _, r := range snap.Repos {
		open := "?"
		if r.Open >= 0 {
			open = fmt.Sprint(r.Open)
		}
		u := ""
		if r.Urgent > 0 {
			u = fmt.Sprintf(" · 🔴%d", r.Urgent)
		}
		fmt.Printf("%s [%s] — %s open · %d for me%s\n", r.Key, r.Mode, open, r.Queued, u)
		for _, e := range snap.Entries {
			if e.Repo != r.Key {
				continue
			}
			mark := "•"
			if e.Urgent {
				mark = "🔴"
			}
			who := []string{}
			if e.Requested {
				who = append(who, "reviewer")
			}
			if e.Assigned {
				who = append(who, "assignee")
			}
			extra := ""
			if len(who) > 0 {
				extra = " (" + strings.Join(who, ",") + ")"
			}
			if e.Urgent {
				extra += " — " + e.UrgentWhy
			}
			if f := e.Files.String(); f != "" {
				extra += "  [" + f + "]"
			}
			fmt.Printf("  %s %s %s — %s%s\n", mark, e.Ref(), e.Author, e.Title, extra)
			switch {
			case e.Path != "":
			case e.PathGuess != "":
				fmt.Printf("      ❓ not in config, found clone: %s\n", config.Contract(e.PathGuess))
			default:
				fmt.Printf("      ❓ no local clone\n")
			}
		}
	}
	for _, e := range snap.Errors {
		fmt.Printf("\n⚠ %s", e)
	}
	if len(snap.Errors) > 0 {
		fmt.Println()
	}
}

// pollActions is replaced once the review runner exists.
var pollActions = func(cfg *config.Config, snap *queue.Snapshot) error { return nil }
