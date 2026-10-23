package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sample = `{
  "gitlab": [{"host": "gitlab.example.com"}],
  "defaults": {
    "on_new": "silent",
    "urgent": {"labels": ["p1"]},
    "review": {"max_turns": 30}
  },
  "repos": {
    "gitlab.example.com/team/app": {
      "path": "~/Projekty/app",
      "mode": "open",
      "urgent": {"older_than": "48h"},
      "review": {"skill_args": "--agents 2"}
    },
    "github.com/acme/widgets": "~/Projekty/widgets"
  }
}`

func TestEffectiveLayers(t *testing.T) {
	c, err := Parse([]byte(sample), "test")
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.Effective("gitlab.example.com/team/app")
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode != ModeOpen {
		t.Errorf("mode = %q, want open (repo override)", s.Mode)
	}
	if s.OnNew != OnNewSilent {
		t.Errorf("on_new = %q, want silent (from defaults)", s.OnNew)
	}
	if s.Urgent.OlderThan.D() != 48*time.Hour {
		t.Errorf("urgent.older_than = %v, want 48h (repo)", s.Urgent.OlderThan.D())
	}
	if len(s.Urgent.Labels) != 1 || s.Urgent.Labels[0] != "p1" {
		t.Errorf("urgent.labels = %v, want [p1] (defaults, deep-merged past repo urgent)", s.Urgent.Labels)
	}
	if s.Review.MaxTurns != 30 || s.Review.SkillArgs != "--agents 2" || s.Review.Fallback != "builtin" {
		t.Errorf("review = %+v, want max_turns from defaults, skill_args from repo, fallback builtin", s.Review)
	}
	if !strings.HasSuffix(s.Path, filepath.FromSlash("Projekty/app")) || strings.HasPrefix(s.Path, "~") {
		t.Errorf("path = %q, want expanded", s.Path)
	}
	if !s.Flash || !s.CountOpen {
		t.Errorf("flash/count_open should default to true: %+v", s)
	}
}

func TestStringShorthandAndCaseInsensitiveKey(t *testing.T) {
	c, err := Parse([]byte(sample), "test")
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.Effective("github.com/Acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(s.Path, "widgets") || s.Mode != ModeRequested {
		t.Errorf("got %+v", s)
	}
}

func TestUnknownRepoGetsDefaults(t *testing.T) {
	c, _ := Parse([]byte(sample), "test")
	s, err := c.Effective("github.com/other/repo")
	if err != nil {
		t.Fatal(err)
	}
	if s.Path != "" || s.OnNew != OnNewSilent || s.Review.MaxTurns != 30 {
		t.Errorf("got %+v", s)
	}
}

func TestValidation(t *testing.T) {
	bad := []string{
		`{"repos": {"x/y": {"mode": "all"}}}`,
		`{"defaults": {"on_new": "maybe"}}`,
		`{"repos": {"x/y": {"pth": "~/x"}}}`,
		`{"poll_intervall": "3m"}`,
		`{"defaults": {"urgent": {"older_than": "soon"}}}`,
	}
	for _, b := range bad {
		if _, err := Parse([]byte(b), "t"); err == nil {
			t.Errorf("Parse(%s) succeeded, want error", b)
		}
	}
}

func TestDurationDays(t *testing.T) {
	c, err := Parse([]byte(`{"defaults": {"urgent": {"older_than": "2d"}}}`), "t")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := c.Effective("a/b")
	if s.Urgent.OlderThan.D() != 48*time.Hour {
		t.Errorf("got %v", s.Urgent.OlderThan.D())
	}
}

func TestSetRepoPathAndSaveRoundTrip(t *testing.T) {
	c, _ := Parse([]byte(sample), "test")
	if err := c.SetRepoPath("gitlab.example.com/team/app", filepath.Join(Home(), "elsewhere")); err != nil {
		t.Fatal(err)
	}
	if err := c.SetRepoPath("gitlab.example.com/team/infra", filepath.Join(Home(), "infra")); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "c.json")
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `"~/elsewhere"`) {
		t.Errorf("saved file lacks contracted path:\n%s", b)
	}
	c2, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := c2.Effective("gitlab.example.com/team/app")
	if s.Mode != ModeOpen || !strings.HasSuffix(s.Path, "elsewhere") {
		t.Errorf("override lost on round trip: %+v", s)
	}
	if len(c2.Repos) != 3 {
		t.Errorf("repos = %d, want 3", len(c2.Repos))
	}
}

func TestFormatDuration(t *testing.T) {
	for in, want := range map[time.Duration]string{3 * time.Minute: "3m", 48 * time.Hour: "48h", 90 * time.Second: "1m30s", 600 * time.Millisecond: "600ms"} {
		if got := FormatDuration(in); got != want {
			t.Errorf("FormatDuration(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestDefaultRoundTrips(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	if err := Default().Save(p); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err != nil {
		t.Fatal(err)
	}
}

func TestDurationsAndAccountsValidated(t *testing.T) {
	for _, s := range []string{"-3m", "2xd", "-1d", "soon"} {
		if _, err := parseDuration(s); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
	if d, err := parseDuration("1.5d"); err != nil || d != 36*time.Hour {
		t.Errorf("1.5d = %v %v", d, err)
	}
	bad := []string{
		`{"poll_interval": "5s"}`,
		`{"gitlab": [{"host": "gl.x"}, {"host": "gl.x", "user": "work"}]}`,
	}
	for _, b := range bad {
		if _, err := Parse([]byte(b), "c.json"); err == nil {
			t.Errorf("accepted: %s", b)
		}
	}
	if _, err := Parse([]byte(`{"gitlab": [{"host": "gl.x"}, {"host": "gl.x", "user": "work", "config_dir": "/tmp/g"}]}`), "c.json"); err != nil {
		t.Errorf("second account with its own glab dir: %v", err)
	}
}

func TestWriteRawConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	v1, err := WriteRaw(path, []byte(`{"concurrency":1}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := Modify(path, func(c *Config) error { c.ScanDirs = []string{"~/x"}; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteRaw(path, []byte(`{"concurrency":2}`), v1); err != ErrChanged {
		t.Errorf("stale save: %v, want ErrChanged", err)
	}
}
