package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"juhas/internal/config"
	"juhas/internal/forge"
	"juhas/internal/scan"
)

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func cmdConfig(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: juhas config init|scan|show|ui|path")
	}
	switch args[0] {
	case "init":
		return configInit(args[1:])
	case "scan":
		return configScan(args[1:])
	case "show":
		return configShow(args[1:])
	case "ui":
		return configUI(args[1:])
	case "path":
		fmt.Println(config.DefaultPath())
		return nil
	}
	return fmt.Errorf("unknown config subcommand %q", args[0])
}

// candidateScanDirs are common places for clones; only existing ones are used.
var candidateScanDirs = []string{"~/code", "~/src", "~/projects", "~/Projects", "~/dev", "~/work", "~/repos", "~/git"}

func configInit(args []string) error {
	fs := flag.NewFlagSet("config init", flag.ExitOnError)
	force := fs.Bool("force", false, "overwrite an existing config")
	var dirs multiFlag
	fs.Var(&dirs, "dir", "directory with clones (can be given multiple times)")
	fs.Parse(args)

	path := config.DefaultPath()
	if _, err := os.Stat(path); err == nil && !*force {
		return fmt.Errorf("%s already exists (use --force or config scan --write)", path)
	}
	ctx := context.Background()
	c := config.Default()
	if len(dirs) == 0 {
		for _, d := range candidateScanDirs {
			if st, err := os.Stat(config.Expand(d)); err == nil && st.IsDir() {
				dirs = append(dirs, d)
			}
		}
	}
	c.ScanDirs = dirs

	// Hosts come from where gh/glab are actually logged in.
	if hs := forge.LoggedInHosts(ctx, "gh"); len(hs) > 0 {
		c.GitHub = nil
		for _, h := range hs {
			c.GitHub = append(c.GitHub, config.Host{Host: h})
		}
	}
	for _, h := range forge.LoggedInHosts(ctx, "glab") {
		c.GitLab = append(c.GitLab, config.Host{Host: h})
	}

	added, err := mapClones(ctx, c, true)
	if err != nil {
		return err
	}
	if err := c.Save(path); err != nil {
		return err
	}
	fmt.Printf("Saved %s\n  github: %s\n  gitlab: %s\n  scan_dirs: %s\n  repos: %d\n",
		path, hostList(c.GitHub), hostList(c.GitLab), strings.Join(c.ScanDirs, ", "), added)
	fmt.Println("\nSet \"mode\": \"open\" for repos where nobody assigns reviewers (juhas doctor will suggest it).")
	return nil
}

func configScan(args []string) error {
	fs := flag.NewFlagSet("config scan", flag.ExitOnError)
	write := fs.Bool("write", false, "add missing mappings to the config")
	var dirs multiFlag
	fs.Var(&dirs, "dir", "directory with clones (default: scan_dirs from the config)")
	fs.Parse(args)

	c, err := config.Load(config.DefaultPath())
	if err != nil {
		return err
	}
	if len(dirs) > 0 {
		c.ScanDirs = dirs
	}
	added, err := mapClones(context.Background(), c, *write)
	if err != nil {
		return err
	}
	if *write && added > 0 {
		if err := c.Save(c.Path()); err != nil {
			return err
		}
		fmt.Printf("\nAdded %d mappings to %s\n", added, c.Path())
	} else if added > 0 {
		fmt.Printf("\n%d new mappings — run with --write to save them.\n", added)
	}
	return nil
}

// mapClones prints every clone on a configured gh/gl host and, when apply is
// set, adds the unmapped ones to c. It returns how many were (or would be) added.
func mapClones(ctx context.Context, c *config.Config, apply bool) (int, error) {
	var roots []string
	for _, d := range c.ScanDirs {
		roots = append(roots, config.Expand(d))
	}
	if len(roots) == 0 {
		return 0, errors.New("no scan_dirs — pass --dir ~/Projects")
	}
	var hosts []string
	hosts = c.HostNames()
	all := scan.Clones(ctx, roots)
	clones := scan.OnHosts(all, hosts)
	fmt.Printf("Clones in %s: %d (on hosts %s: %d)\n\n", strings.Join(c.ScanDirs, ", "), len(all), strings.Join(hosts, ", "), len(clones))

	added := 0
	seen := map[string]bool{}
	for _, cl := range clones {
		k, exists := c.RepoKey(cl.Key)
		mark := "+"
		switch {
		case exists:
			s, _ := c.Effective(k)
			if s.Path != "" && filepath.Clean(s.Path) != filepath.Clean(cl.Dir) {
				mark = "≠" // configured elsewhere
			} else {
				mark = "="
			}
		case seen[strings.ToLower(cl.Key)]:
			mark = "2" // second clone of the same repo; first one wins
		}
		fmt.Printf("  %s %-50s %s\n", mark, cl.Key, config.Contract(cl.Dir))
		if mark == "+" {
			added++
			if apply {
				if err := c.SetRepoPath(cl.Key, cl.Dir); err != nil {
					return added, err
				}
			}
		}
		seen[strings.ToLower(cl.Key)] = true
	}
	fmt.Println("\n  + new   = already in config   ≠ different path in config   2 another clone of the same repo (skipped)")
	return added, nil
}

func configShow(args []string) error {
	c, err := config.Load(config.DefaultPath())
	if err != nil {
		return err
	}
	keys := args
	if len(keys) == 0 {
		keys = c.RepoKeys()
	}
	out := map[string]any{}
	for _, k := range keys {
		s, err := c.Effective(k)
		if err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
		out[k] = showSettings(k, s)
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
	return nil
}

// showSettings is a hook for extra resolved info (e.g. the detected skill).
var showSettings = func(key string, s config.Settings) any { return s }

func hostList(hs []config.Host) string {
	var out []string
	for _, h := range hs {
		out = append(out, h.Host)
	}
	if len(out) == 0 {
		return "-"
	}
	return strings.Join(out, ", ")
}
