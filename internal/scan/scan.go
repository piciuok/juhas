// Package scan finds local git clones and maps their origin to config keys.
package scan

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"juhas/internal/forge"
)

// Clone is a local repository whose origin parsed into a key.
type Clone struct {
	Key    string // host/path
	Dir    string
	Remote string
}

// maxDepth limits how deep below a scan dir clones are looked for
// (~/Projekty/x is depth 1, ~/code/org/x is depth 2).
const maxDepth = 3

var skipDirs = map[string]bool{
	"node_modules": true, "vendor": true, ".cache": true, ".worktrees": true,
	"Library": true, ".Trash": true,
}

// Clones walks dirs and returns every clone with a parseable origin, sorted by key.
// Nested repositories (worktrees, submodules) are not descended into.
func Clones(ctx context.Context, dirs []string) []Clone {
	var out []Clone
	seen := map[string]bool{}
	for _, root := range dirs {
		root = filepath.Clean(root)
		walk(root, 0, func(dir string) {
			if seen[dir] {
				return
			}
			seen[dir] = true
			remote := originURL(ctx, dir)
			if key, ok := forge.RemoteKey(remote); ok {
				out = append(out, Clone{Key: key, Dir: dir, Remote: remote})
			}
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Dir < out[j].Dir
	})
	return out
}

func walk(dir string, depth int, found func(string)) {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		found(dir)
		return
	}
	if depth >= maxDepth {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || skipDirs[name] || strings.HasPrefix(name, ".") {
			continue
		}
		walk(filepath.Join(dir, name), depth+1, found)
	}
}

func originURL(ctx context.Context, dir string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Find returns the clones whose key equals key (case-insensitive).
func Find(clones []Clone, key string) []Clone {
	var out []Clone
	for _, c := range clones {
		if strings.EqualFold(c.Key, key) {
			out = append(out, c)
		}
	}
	return out
}

// OnHosts keeps clones whose host is in hosts (case-insensitive).
func OnHosts(clones []Clone, hosts []string) []Clone {
	var out []Clone
	for _, c := range clones {
		h, _ := forge.SplitKey(c.Key)
		for _, want := range hosts {
			if strings.EqualFold(h, want) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}
