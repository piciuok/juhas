package forge

import (
	"net/url"
	"strings"
)

// RemoteKey turns a git remote URL into the "host/path" key used in config:
//
//	git@gitlab.example.com:group/sub/proj.git -> gitlab.example.com/group/sub/proj
//	ssh://git@host:2222/group/proj.git        -> host/group/proj
//	https://user@github.com/org/repo          -> github.com/org/repo
//
// The host is lower-cased; ok is false for local paths and unknown forms.
func RemoteKey(remote string) (key string, ok bool) {
	r := strings.TrimSpace(remote)
	if r == "" {
		return "", false
	}
	var host, path string
	if strings.Contains(r, "://") {
		u, err := url.Parse(r)
		if err != nil || u.Host == "" || u.Scheme == "file" {
			return "", false
		}
		host, path = u.Hostname(), u.Path
	} else {
		// scp-like: [user@]host:path, but not a Windows/local path.
		colon := strings.Index(r, ":")
		if colon <= 0 || strings.Contains(r[:colon], "/") {
			return "", false
		}
		host, path = r[:colon], r[colon+1:]
		if at := strings.LastIndex(host, "@"); at >= 0 {
			host = host[at+1:]
		}
	}
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.TrimSuffix(path, "/")
	if host == "" || path == "" {
		return "", false
	}
	return strings.ToLower(host) + "/" + path, true
}

// SplitKey splits "host/group/proj" into host and project path.
func SplitKey(key string) (host, project string) {
	host, project, _ = strings.Cut(key, "/")
	return host, project
}
