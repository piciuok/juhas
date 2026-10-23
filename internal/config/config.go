// Package config loads ~/.config/juhas/config.json and resolves the
// effective settings of a repository: builtin defaults -> "defaults" -> repos[key].
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"juhas/internal/forge"
)

// Duration is a time.Duration that reads/writes as "3m", "48h", "".
type Duration time.Duration

func (d Duration) D() time.Duration { return time.Duration(d) }

func (d Duration) MarshalJSON() ([]byte, error) {
	if d == 0 {
		return []byte(`""`), nil
	}
	return json.Marshal(FormatDuration(time.Duration(d)))
}

// FormatDuration prints 3m instead of 3m0s and 48h instead of 48h0m0s.
func FormatDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration: %w", err)
	}
	if s == "" {
		*d = 0
		return nil
	}
	v, err := parseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

// parseDuration accepts Go durations plus a "d" suffix for days ("2d").
func parseDuration(s string) (time.Duration, error) {
	var v time.Duration
	if strings.HasSuffix(s, "d") {
		n, err := strconv.ParseFloat(strings.TrimSuffix(s, "d"), 64)
		if err != nil {
			return 0, fmt.Errorf("duration %q: use e.g. 2d, 36h or 90m", s)
		}
		v = time.Duration(n * float64(24*time.Hour))
	} else {
		var err error
		if v, err = time.ParseDuration(s); err != nil {
			return 0, fmt.Errorf("duration %q: use e.g. 2d, 36h or 90m", s)
		}
	}
	if v < 0 {
		return 0, fmt.Errorf("duration %q: must not be negative", s)
	}
	return v, nil
}

// MinPollInterval keeps the forge APIs (and rate limits) safe from a typo.
const MinPollInterval = 30 * time.Second

// Host is one account on a GitHub/GitLab host. Without User it is the CLI's
// login (gh: the active account). User picks one of several gh accounts on
// the same host; a second GitLab account needs its own glab config
// directory (ConfigDir), since glab keeps one login per host.
type Host struct {
	Host      string `json:"host"`
	User      string `json:"user,omitempty"`
	ConfigDir string `json:"config_dir,omitempty"`
	Watch     *bool  `json:"watch,omitempty"` // false: kept, but its review requests are not checked
}

// Watched: the account's review requests are checked (default on).
func (h Host) Watched() bool { return h.Watch == nil || *h.Watch }

// Account is the forge account of a host entry.
func (h Host) Account(platform string) forge.Account {
	return forge.Account{Platform: platform, Host: strings.ToLower(h.Host), User: h.User, ConfigDir: Expand(h.ConfigDir)}
}

// Accounts lists every configured account, watched or not.
func (c *Config) Accounts() []forge.Account {
	var out []forge.Account
	for _, h := range c.GitHub {
		out = append(out, h.Account(forge.GitHub))
	}
	for _, h := range c.GitLab {
		out = append(out, h.Account(forge.GitLab))
	}
	return out
}

// HostNames lists the distinct GitHub and GitLab hosts.
func (c *Config) HostNames() []string {
	var out []string
	seen := map[string]bool{}
	for _, h := range append(append([]Host{}, c.GitHub...), c.GitLab...) {
		if k := strings.ToLower(h.Host); !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// Agent configures one AI CLI (claude, codex): its command and the
// model/effort it runs reviews with ("" = the CLI's own default).
type Agent struct {
	Bin       string   `json:"bin"`
	Model     string   `json:"model"`
	Effort    string   `json:"effort"`
	ExtraArgs []string `json:"extra_args,omitempty"`
}

const (
	AgentClaude = "claude"
	AgentCodex  = "codex"
)

// Agents lists the supported AI CLIs.
var Agents = []string{AgentClaude, AgentCodex}

// AgentLabel is the display name.
func AgentLabel(name string) string {
	switch name {
	case AgentClaude:
		return "Claude"
	case AgentCodex:
		return "Codex"
	}
	return name
}

// Updates: how the tray looks for new releases.
type Updates struct {
	Check       *bool    `json:"check,omitempty"` // default on
	Interval    Duration `json:"interval"`
	Prerelease  bool     `json:"prerelease"`
	AutoInstall bool     `json:"auto_install"` // install on its own when no review runs
}

// CheckOn: look for new releases (default on).
func (u Updates) CheckOn() bool { return u.Check == nil || *u.Check }

// Config is the whole file. Defaults and Repos stay raw so they can be merged
// as JSON objects (objects merge deeply, lists and scalars replace).
type Config struct {
	PollInterval   Duration                   `json:"poll_interval"`
	FlashInterval  Duration                   `json:"flash_interval"`
	BriefsDir      string                     `json:"briefs_dir"`
	ScanDirs       []string                   `json:"scan_dirs"`
	AutoMap        *bool                      `json:"auto_map,omitempty"`
	ScreenEffect   *bool                      `json:"screen_effect,omitempty"`
	Concurrency    int                        `json:"concurrency"`
	AskStyle       string                     `json:"ask_style"`
	AutoMaxPerPoll int                        `json:"auto_max_per_poll"`
	Claude         Agent                      `json:"claude"`
	Codex          Agent                      `json:"codex"`
	GitHub         []Host                     `json:"github"`
	GitLab         []Host                     `json:"gitlab"`
	Updates        Updates                    `json:"updates"`
	Defaults       json.RawMessage            `json:"defaults,omitempty"`
	Repos          map[string]json.RawMessage `json:"repos"`

	path string
}

// Settings is what applies to a single repository after merging.
type Settings struct {
	Path          string   `json:"path"`
	Mode          string   `json:"mode"` // requested | open
	IncludeDrafts bool     `json:"include_drafts"`
	IgnoreAuthors []string `json:"ignore_authors"`
	CountOpen     bool     `json:"count_open"`
	OnNew         string   `json:"on_new"` // ask | auto | silent | off
	Review        Review   `json:"review"`
	Urgent        Urgent   `json:"urgent"`
	Flash         bool     `json:"flash"`
	FlashDecide   bool     `json:"flash_decisions"` // blinking ? while a proposal waits for me
	Language      Language `json:"language"`
	Agent         string   `json:"agent"`   // claude | codex
	Account       string   `json:"account"` // which login reads the repo when a host has several ("" = the first)
}

// Language of what the review produces: the brief I read, and the comments
// it drafts for the PR/MR. A code (en, pl, de, …) or a language name.
type Language struct {
	Brief    string `json:"brief"`
	Comments string `json:"comments"`
}

type Review struct {
	Skill           string   `json:"skill"` // auto | none | <name>
	SkillArgs       string   `json:"skill_args"`
	Fallback        string   `json:"fallback"` // builtin | claude-code-review | none
	MaxTurns        int      `json:"max_turns"`
	Timeout         Duration `json:"timeout"`
	Model           string   `json:"model"`  // overrides the agent's model for this repo
	Effort          string   `json:"effort"` // overrides the agent's effort for this repo
	AllowedTools    []string `json:"allowed_tools"`
	DisallowedTools []string `json:"disallowed_tools"`
}

type Urgent struct {
	Labels         []string `json:"labels"`
	OlderThan      Duration `json:"older_than"`
	TargetBranches []string `json:"target_branches"`
}

const (
	ModeRequested = "requested"
	ModeOpen      = "open"

	OnNewAsk    = "ask"
	OnNewAuto   = "auto"
	OnNewSilent = "silent"
	OnNewOff    = "off"
)

// builtinSettings is the bottom layer; "defaults" and repos[key] override it.
const builtinSettings = `{
  "mode": "requested",
  "include_drafts": false,
  "ignore_authors": ["re:^project_\\d+_bot"],
  "count_open": true,
  "on_new": "ask",
  "review": {
    "skill": "auto",
    "skill_args": "",
    "fallback": "builtin",
    "max_turns": 60,
    "timeout": "30m",
    "model": "",
    "effort": "",
    "allowed_tools": [],
    "disallowed_tools": []
  },
  "urgent": { "labels": ["urgent", "priority::high", "hotfix"], "older_than": "", "target_branches": [] },
  "flash": true,
  "flash_decisions": true,
  "language": { "brief": "en", "comments": "en" },
  "agent": "claude",
  "account": ""
}`

// BuiltinSettings is the bottom settings layer as JSON (for the config UI).
func BuiltinSettings() string { return builtinSettings }

// DefaultPath is ~/.config/juhas/config.json on every OS (also macOS),
// overridable with JUHAS_CONFIG.
func DefaultPath() string {
	if p := os.Getenv("JUHAS_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(Home(), ".config", "juhas", "config.json")
}

func Home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

// Expand replaces a leading ~ with the home directory.
func Expand(p string) string {
	if p == "~" {
		return Home()
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(Home(), p[2:])
	}
	return p
}

// Contract is the inverse of Expand, for writing paths back to the file.
func Contract(p string) string {
	h := Home()
	if p == h {
		return "~"
	}
	if strings.HasPrefix(p, h+string(filepath.Separator)) {
		return "~/" + filepath.ToSlash(p[len(h)+1:])
	}
	return p
}

// Default returns a config with sane values and no repositories. Its
// "defaults" block spells out every builtin setting so the file documents itself.
func Default() *Config {
	on := true
	c := &Config{Repos: map[string]json.RawMessage{}, Defaults: json.RawMessage(builtinSettings), AutoMap: &on}
	c.applyDefaults()
	return c
}

func (c *Config) applyDefaults() {
	if c.PollInterval == 0 {
		c.PollInterval = Duration(3 * time.Minute)
	}
	if c.FlashInterval == 0 {
		c.FlashInterval = Duration(600 * time.Millisecond)
	}
	if c.Updates.Interval <= 0 {
		c.Updates.Interval = Duration(24 * time.Hour)
	}
	if c.BriefsDir == "" {
		c.BriefsDir = "~/ReviewBriefs"
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 1
	}
	if c.AskStyle == "" {
		c.AskStyle = "dialog"
	}
	if c.AutoMaxPerPoll <= 0 {
		c.AutoMaxPerPoll = 2
	}
	if c.Claude.Bin == "" {
		c.Claude.Bin = "claude"
	}
	if c.Codex.Bin == "" {
		c.Codex.Bin = "codex"
	}
	if len(c.GitHub) == 0 {
		c.GitHub = []Host{{Host: "github.com"}}
	}
	if c.Repos == nil {
		c.Repos = map[string]json.RawMessage{}
	}
}

// ErrNotFound is returned by Load when the file does not exist.
var ErrNotFound = errors.New("config not found")

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	if err != nil {
		return nil, err
	}
	return Parse(b, path)
}

func Parse(b []byte, path string) (*Config, error) {
	c := &Config{}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.path = path
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func (c *Config) Path() string { return c.path }

// AgentConfig returns the CLI settings of an agent, with the repo's
// review.model/review.effort applied when the repo runs that same agent.
func (c *Config) AgentConfig(name string, s Settings) Agent {
	a := c.Claude
	if name == AgentCodex {
		a = c.Codex
	}
	if name == s.Agent {
		if s.Review.Model != "" {
			a.Model = s.Review.Model
		}
		if s.Review.Effort != "" {
			a.Effort = s.Review.Effort
		}
	}
	return a
}

// AutoMapOn: clones found in scan_dirs are added to repos automatically
// (default on).
func (c *Config) AutoMapOn() bool { return c.AutoMap == nil || *c.AutoMap }

// ScreenEffectOn: a new urgent item zaps the screen (default on; macOS).
func (c *Config) ScreenEffectOn() bool { return c.ScreenEffect == nil || *c.ScreenEffect }

// Validate resolves every repository once, so a typo fails at load time.
func (c *Config) Validate() error {
	seen := map[string]bool{}
	for _, a := range c.Accounts() {
		if a.Host == "" {
			return fmt.Errorf("%s: an account without host", a.Platform)
		}
		k := a.Platform + " " + a.Key()
		if seen[k] {
			return fmt.Errorf("%s account %s is listed twice", a.Platform, a.Label())
		}
		seen[k] = true
	}
	defaultLogin := map[string]int{} // glab keeps one login per host and config dir
	for _, h := range c.GitLab {
		if h.ConfigDir == "" {
			defaultLogin[strings.ToLower(h.Host)]++
		}
	}
	for host, n := range defaultLogin {
		if n > 1 {
			return fmt.Errorf("gitlab %s: %d accounts use glab's default login — glab has one login per host, give the others their own config_dir", host, n)
		}
	}
	if c.PollInterval.D() > 0 && c.PollInterval.D() < MinPollInterval {
		return fmt.Errorf("poll_interval %s: at least %s", c.PollInterval.D(), MinPollInterval)
	}
	if _, err := c.settings(nil); err != nil {
		return fmt.Errorf("defaults: %w", err)
	}
	for _, k := range c.RepoKeys() {
		if _, err := c.Effective(k); err != nil {
			return fmt.Errorf("repos[%q]: %w", k, err)
		}
	}
	return nil
}

// RepoKeys returns the configured repository keys, sorted.
func (c *Config) RepoKeys() []string {
	keys := make([]string, 0, len(c.Repos))
	for k := range c.Repos {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// RepoKey finds the configured key matching k case-insensitively.
func (c *Config) RepoKey(k string) (string, bool) {
	if _, ok := c.Repos[k]; ok {
		return k, true
	}
	for key := range c.Repos {
		if strings.EqualFold(key, k) {
			return key, true
		}
	}
	return "", false
}

// Effective returns the merged settings for key (which need not be configured:
// an unknown repo gets builtin + defaults). Path is ~-expanded.
func (c *Config) Effective(key string) (Settings, error) {
	var repo json.RawMessage
	if k, ok := c.RepoKey(key); ok {
		repo = c.Repos[k]
	}
	return c.settings(repo)
}

func (c *Config) settings(repo json.RawMessage) (Settings, error) {
	merged := map[string]any{}
	if err := json.Unmarshal([]byte(builtinSettings), &merged); err != nil {
		return Settings{}, err
	}
	for _, layer := range []json.RawMessage{c.Defaults, repo} {
		m, err := layerObject(layer)
		if err != nil {
			return Settings{}, err
		}
		merged = Merge(merged, m)
	}
	b, err := json.Marshal(merged)
	if err != nil {
		return Settings{}, err
	}
	var s Settings
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Settings{}, err
	}
	s.Path = Expand(s.Path)
	return s, s.validate()
}

// layerObject accepts an object, or a plain string as shorthand for {"path": s}.
func layerObject(raw json.RawMessage) (map[string]any, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if raw[0] == '"' {
		var p string
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return map[string]any{"path": p}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// Merge overlays src onto dst: nested objects merge, everything else replaces.
func Merge(dst, src map[string]any) map[string]any {
	if dst == nil {
		dst = map[string]any{}
	}
	for k, v := range src {
		sv, sok := v.(map[string]any)
		dv, dok := dst[k].(map[string]any)
		if sok && dok {
			dst[k] = Merge(dv, sv)
			continue
		}
		dst[k] = v
	}
	return dst
}

func (s Settings) validate() error {
	switch s.Mode {
	case ModeRequested, ModeOpen:
	default:
		return fmt.Errorf("mode %q: want requested|open", s.Mode)
	}
	switch s.OnNew {
	case OnNewAsk, OnNewAuto, OnNewSilent, OnNewOff:
	default:
		return fmt.Errorf("on_new %q: want ask|auto|silent|off", s.OnNew)
	}
	switch s.Review.Fallback {
	case "builtin", "claude-code-review", "none":
	default:
		return fmt.Errorf("review.fallback %q: want builtin|claude-code-review|none", s.Review.Fallback)
	}
	if s.Agent != AgentClaude && s.Agent != AgentCodex {
		return fmt.Errorf("agent %q: want claude|codex", s.Agent)
	}
	if s.Language.Brief == "" || s.Language.Comments == "" {
		return errors.New("language.brief and language.comments must not be empty")
	}
	if s.Review.Skill == "" {
		return errors.New("review.skill is empty: use auto, none or a skill name")
	}
	return nil
}

// SetRepoPath sets repos[key].path, keeping the repo's other overrides.
func (c *Config) SetRepoPath(key, path string) error {
	k, ok := c.RepoKey(key)
	if !ok {
		k = key
	}
	m, err := layerObject(c.Repos[k])
	if err != nil {
		return err
	}
	if m == nil {
		m = map[string]any{}
	}
	m["path"] = Contract(path)
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.Repos[k] = b
	return nil
}

// Save writes the config atomically with 2-space indentation.
func (c *Config) Save(path string) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	// Re-indent raw messages, which MarshalIndent leaves compact.
	if err := json.Indent(&buf, b, "", "  "); err != nil {
		return err
	}
	buf.WriteByte('\n')
	if err := writeAtomic(path, buf.Bytes()); err != nil {
		return err
	}
	c.path = path
	return nil
}

// writeAtomic replaces path through a unique temp file in the same dir, so
// two writers never mix their bytes.
func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // no-op after the rename
	if _, err := f.Write(b); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(f.Name(), 0o600)
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// lock serialises writers of the config file (the tray, Settings, the CLI).
func lock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// Modify loads the config fresh under the write lock, applies fn and saves
// it, so a change never overwrites one made meanwhile by someone else.
func Modify(path string, fn func(*Config) error) error {
	unlock, err := lock(path)
	if err != nil {
		return err
	}
	defer unlock()
	c, err := Load(path)
	if err != nil {
		return err
	}
	if err := fn(c); err != nil {
		return err
	}
	return c.Save(path)
}

// Version identifies a config file's content (for edit conflicts).
func Version(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}

// ErrChanged: the file changed since the editor loaded it.
var ErrChanged = errors.New("the config changed on disk since Settings loaded it")

// WriteRaw saves an edited config file. When base is set and the file on
// disk is no longer that version, it refuses with ErrChanged. The previous
// file is kept as .bak.
func WriteRaw(path string, raw []byte, base string) (string, error) {
	unlock, err := lock(path)
	if err != nil {
		return "", err
	}
	defer unlock()
	if old, err := os.ReadFile(path); err == nil {
		if base != "" && Version(old) != base {
			return "", ErrChanged
		}
		_ = writeAtomic(path+".bak", old)
	}
	if err := writeAtomic(path, raw); err != nil {
		return "", err
	}
	return Version(raw), nil
}
