package forge

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Account is one login on a forge host. Without User it is whatever the CLI
// is logged in as (gh: the active account). With User, gh calls get that
// account's token (gh keeps several accounts per host); glab has one login
// per host per config, so a second GitLab account lives in its own glab
// config directory (ConfigDir).
type Account struct {
	Platform  string `json:"platform"`
	Host      string `json:"host"`
	User      string `json:"user,omitempty"`
	ConfigDir string `json:"config_dir,omitempty"` // glab only
}

// Key identifies the account: "github.com" or "github.com@piciuok".
func (a Account) Key() string {
	h := strings.ToLower(a.Host)
	if a.User == "" {
		return h
	}
	return h + "@" + a.User
}

// Label is how the UI names it: "piciuok@github.com" or the bare host.
func (a Account) Label() string {
	if a.User == "" {
		return strings.ToLower(a.Host)
	}
	return a.User + "@" + strings.ToLower(a.Host)
}

var (
	accMu    sync.Mutex
	accounts []Account
	tokens   = map[string]cachedToken{}
)

type cachedToken struct {
	token string
	at    time.Time
}

// SetAccounts registers the configured accounts, so a PR/MR found by one of
// them is fetched and commented on by the same one.
func SetAccounts(list []Account) {
	accMu.Lock()
	defer accMu.Unlock()
	accounts = append([]Account(nil), list...)
}

// Accounts returns the registered accounts.
func Accounts() []Account {
	accMu.Lock()
	defer accMu.Unlock()
	return append([]Account(nil), accounts...)
}

// AccountFor picks the account for a platform/host: the one whose Key is
// key, else the first registered for the host, else the CLI's default.
func AccountFor(platform, host, key string) Account {
	host = strings.ToLower(host)
	accMu.Lock()
	defer accMu.Unlock()
	var first *Account
	for i := range accounts {
		a := &accounts[i]
		if a.Platform != platform || strings.ToLower(a.Host) != host {
			continue
		}
		if key != "" && strings.EqualFold(a.Key(), key) {
			return *a
		}
		if first == nil {
			first = a
		}
	}
	if first != nil {
		return *first
	}
	return Account{Platform: platform, Host: host}
}

// ItemAccount is the account a PR/MR was found with.
func ItemAccount(it Item) Account {
	host, _ := SplitKey(it.Repo)
	return AccountFor(it.Platform, host, it.Account)
}

// PublishAccount is the account to post as: exactly the one that found the
// PR/MR. Falling back to another login would post under someone else's name.
func PublishAccount(it Item) (Account, error) {
	a := ItemAccount(it)
	if it.Account != "" && !strings.EqualFold(a.Key(), it.Account) {
		return a, fmt.Errorf("account %s, which found this PR/MR, is no longer in the config — add it back (Settings → Accounts) to publish", it.Account)
	}
	return a, nil
}

// Env is what gh/glab need in their environment to act as this account
// (nil for the CLI's default login).
func (a Account) Env(ctx context.Context) ([]string, error) {
	switch {
	case a.Platform == GitLab && a.ConfigDir != "":
		return []string{"GLAB_CONFIG_DIR=" + a.ConfigDir}, nil
	case a.Platform == GitHub && a.User != "":
		tok, err := a.token(ctx)
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(a.Host, "github.com") {
			return []string{"GH_TOKEN=" + tok}, nil
		}
		return []string{"GH_ENTERPRISE_TOKEN=" + tok, "GH_HOST=" + a.Host}, nil
	}
	return nil, nil
}

// token asks gh for the account's token (kept for a few minutes: it comes
// from the keyring, which is slow-ish).
func (a Account) token(ctx context.Context) (string, error) {
	accMu.Lock()
	c, ok := tokens[a.Key()]
	accMu.Unlock()
	if ok && time.Since(c.at) < 5*time.Minute {
		return c.token, nil
	}
	out, err := run(ctx, nil, "gh", "auth", "token", "--hostname", a.Host, "--user", a.User)
	if err != nil {
		return "", fmt.Errorf("%s: no gh login (gh auth login --hostname %s, as %s)", a.Label(), a.Host, a.User)
	}
	tok := strings.TrimSpace(string(out))
	accMu.Lock()
	tokens[a.Key()] = cachedToken{tok, time.Now()}
	accMu.Unlock()
	return tok, nil
}

// Env returns the environment for acting on a PR/MR as the account that
// found it (for review agents that call gh/glab themselves).
func Env(ctx context.Context, it Item) []string {
	env, _ := ItemAccount(it).Env(ctx)
	return env
}

// runAs is run() as an account.
func runAs(ctx context.Context, a Account, name string, args ...string) ([]byte, error) {
	env, err := a.Env(ctx)
	if err != nil {
		return nil, err
	}
	return run(ctx, env, name, args...)
}
