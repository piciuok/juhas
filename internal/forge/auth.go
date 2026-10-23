package forge

import (
	"context"
	"strings"
)

// HostAuth is one account of `gh auth status` / `glab auth status` (gh
// lists several accounts under one host).
type HostAuth struct {
	Host     string
	LoggedIn bool
	Account  string
	Active   bool   // gh: the account gh uses by default
	Detail   string // the account's lines, for doctor output
}

// AuthStatusIn is AuthStatus with extra environment (e.g. another glab
// config directory).
func AuthStatusIn(ctx context.Context, env []string, bin string) ([]HostAuth, error) {
	out, err := runCombinedEnv(ctx, env, bin, "auth", "status")
	if out == "" && err != nil {
		return nil, err
	}
	return parseAuthStatus(out), nil
}

// AuthStatus parses `<bin> auth status`. Both CLIs print the host flush left
// followed by indented lines, one of which says "Logged in to <host> ...".
// Exit status is ignored: both exit non-zero when any one host is broken.
func AuthStatus(ctx context.Context, bin string) ([]HostAuth, error) {
	out, err := runCombined(ctx, bin, "auth", "status")
	if out == "" && err != nil {
		return nil, err
	}
	return parseAuthStatus(out), nil
}

func parseAuthStatus(out string) []HostAuth {
	var hosts []HostAuth
	var cur *HostAuth
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if line[0] != ' ' && line[0] != '\t' {
			h := strings.TrimSpace(line)
			if strings.ContainsAny(h, " :") || !strings.Contains(h, ".") {
				continue // banner/warning text, not a hostname
			}
			hosts = append(hosts, HostAuth{Host: strings.ToLower(h)})
			cur = &hosts[len(hosts)-1]
			continue
		}
		if cur == nil {
			continue
		}
		t := strings.TrimSpace(line)
		login := strings.Contains(t, "Logged in to ") || strings.Contains(t, "Failed to log in to ")
		if login && cur.Account != "" { // gh: the next account of the same host
			hosts = append(hosts, HostAuth{Host: cur.Host})
			cur = &hosts[len(hosts)-1]
		}
		cur.Detail += t + "\n"
		if strings.Contains(t, "Active account: true") {
			cur.Active = true
		}
		if i := strings.Index(t, "Failed to log in to "); i >= 0 {
			if j := strings.Index(t, " account "); j >= 0 {
				acc := t[j+len(" account "):]
				if k := strings.IndexAny(acc, " ("); k >= 0 {
					acc = acc[:k]
				}
				cur.Account = acc
			}
		}
		if i := strings.Index(t, "Logged in to "); i >= 0 {
			cur.LoggedIn = true
			rest := t[i+len("Logged in to "):]
			// gh: "github.com account NAME (keyring)"; glab: "host as NAME (path)"
			for _, sep := range []string{" account ", " as "} {
				if j := strings.Index(rest, sep); j >= 0 {
					acc := rest[j+len(sep):]
					if k := strings.IndexAny(acc, " ("); k >= 0 {
						acc = acc[:k]
					}
					cur.Account = acc
					break
				}
			}
		}
	}
	return hosts
}

// LoggedInHosts returns the hosts bin is authenticated against.
func LoggedInHosts(ctx context.Context, bin string) []string {
	if !Available(bin) {
		return nil
	}
	st, _ := AuthStatus(ctx, bin)
	var out []string
	for _, h := range st {
		if h.LoggedIn {
			out = append(out, h.Host)
		}
	}
	return out
}
