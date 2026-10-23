package scan

import (
	"context"
	"os"
	"strings"

	"juhas/internal/config"
)

// Change is one mapping AutoMap made.
type Change struct {
	Key, Dir, Old string // Old is set when a moved clone's path was fixed
}

// AutoMap adds every clone in cfg.ScanDirs that lives on a configured
// GitHub/GitLab host and is not yet in repos, and repoints repos whose path
// no longer exists to a clone found under scan_dirs. It edits cfg in place;
// the caller saves it when the result is non-empty.
func AutoMap(ctx context.Context, cfg *config.Config) ([]Change, error) {
	var roots, hosts []string
	for _, d := range cfg.ScanDirs {
		roots = append(roots, config.Expand(d))
	}
	for _, h := range append(append([]config.Host{}, cfg.GitHub...), cfg.GitLab...) {
		hosts = append(hosts, h.Host)
	}
	if len(roots) == 0 || len(hosts) == 0 {
		return nil, nil
	}
	clones := OnHosts(Clones(ctx, roots), hosts)
	var changes []Change
	seen := map[string]bool{}
	for _, c := range clones {
		lk := strings.ToLower(c.Key)
		if seen[lk] {
			continue // first clone of a repo wins
		}
		seen[lk] = true
		key, exists := cfg.RepoKey(c.Key)
		if !exists {
			if err := cfg.SetRepoPath(c.Key, c.Dir); err != nil {
				return changes, err
			}
			changes = append(changes, Change{Key: c.Key, Dir: c.Dir})
			continue
		}
		s, err := cfg.Effective(key)
		if err != nil || s.Path == "" || isDir(s.Path) {
			continue // explicit empty path means "auto-guess", keep it
		}
		if err := cfg.SetRepoPath(key, c.Dir); err != nil {
			return changes, err
		}
		changes = append(changes, Change{Key: key, Dir: c.Dir, Old: s.Path})
	}
	return changes, nil
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
