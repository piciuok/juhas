package configui

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"juhas/internal/config"
	"juhas/internal/forge"
)

// detectedAccount is a login gh/glab knows about.
type detectedAccount struct {
	Platform  string `json:"platform"`
	Host      string `json:"host"`
	User      string `json:"user"`
	Active    bool   `json:"active"`    // gh: the default account of the host
	LoggedIn  bool   `json:"logged_in"` // false: the CLI has it but the token does not work
	ConfigDir string `json:"config_dir,omitempty"`
}

// accounts lists what gh and glab are logged in as, plus the glab config
// directories of the GitLab entries in the (unsaved) config, so the
// Accounts tab can offer every login with a "watch" checkbox.
func (s *Server) accounts(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		GitLab []config.Host `json:"gitlab"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req)
	ctx := r.Context()
	out := []detectedAccount{}
	add := func(platform string, st []forge.HostAuth, dir string) {
		for _, a := range st {
			if a.Account == "" && !a.LoggedIn {
				continue // a host block without any login
			}
			out = append(out, detectedAccount{Platform: platform, Host: a.Host, User: a.Account, Active: a.Active || platform == forge.GitLab,
				LoggedIn: a.LoggedIn, ConfigDir: dir})
		}
	}
	if forge.Available("gh") {
		st, _ := forge.AuthStatus(ctx, "gh")
		add(forge.GitHub, st, "")
	}
	if forge.Available("glab") {
		st, _ := forge.AuthStatus(ctx, "glab")
		add(forge.GitLab, st, "")
		seen := map[string]bool{}
		for _, h := range req.GitLab {
			dir := strings.TrimSpace(h.ConfigDir)
			if dir == "" || seen[dir] {
				continue
			}
			seen[dir] = true
			st, _ := forge.AuthStatusIn(ctx, []string{"GLAB_CONFIG_DIR=" + config.Expand(dir)}, "glab")
			add(forge.GitLab, st, dir)
		}
	}
	return map[string]any{"accounts": out, "glab_dir": "~/.config/juhas/glab"}, nil
}
