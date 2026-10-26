package scan

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/piciuok/juhas/internal/config"
)

func gitInit(t *testing.T, dir, origin string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", origin}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestClones(t *testing.T) {
	root := t.TempDir()
	gitInit(t, filepath.Join(root, "app"), "git@gitlab.example.com:team/app.git")
	gitInit(t, filepath.Join(root, "org", "widgets"), "https://github.com/acme/widgets")
	gitInit(t, filepath.Join(root, "node_modules", "dep"), "git@github.com:x/dep.git")
	gitInit(t, filepath.Join(root, "local"), "/srv/git/local.git")
	// nested inside a clone: must not be listed separately
	gitInit(t, filepath.Join(root, "app", "sub"), "git@github.com:x/sub.git")

	got := Clones(context.Background(), []string{root})
	if len(got) != 2 {
		t.Fatalf("got %d clones: %+v", len(got), got)
	}
	if got[0].Key != "github.com/acme/widgets" || got[1].Key != "gitlab.example.com/team/app" {
		t.Errorf("keys = %s, %s", got[0].Key, got[1].Key)
	}
	if f := Find(got, "GitLab.example.com/team/APP"); len(f) != 1 {
		t.Errorf("Find case-insensitive: %+v", f)
	}
	if h := OnHosts(got, []string{"github.com"}); len(h) != 1 || h[0].Key != "github.com/acme/widgets" {
		t.Errorf("OnHosts: %+v", h)
	}
}

func TestAutoMap(t *testing.T) {
	root := t.TempDir()
	gitInit(t, filepath.Join(root, "app"), "git@gitlab.example.com:team/app.git")
	gitInit(t, filepath.Join(root, "moved", "widgets"), "https://github.com/acme/widgets")
	gitInit(t, filepath.Join(root, "bb"), "git@bitbucket.org:x/y.git") // host not configured
	raw := `{"scan_dirs": ["` + root + `"], "github": [{"host": "github.com"}], "gitlab": [{"host": "gitlab.example.com"}],
	  "repos": {"github.com/acme/widgets": {"path": "` + filepath.Join(root, "gone") + `", "mode": "open"}}}`
	cfg, err := config.Parse([]byte(raw), "t")
	if err != nil {
		t.Fatal(err)
	}
	ch, err := AutoMap(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(ch) != 2 {
		t.Fatalf("changes = %+v", ch)
	}
	s, _ := cfg.Effective("github.com/acme/widgets")
	if s.Path != filepath.Join(root, "moved", "widgets") || s.Mode != "open" {
		t.Errorf("moved clone not repointed or override lost: %+v", s)
	}
	if _, ok := cfg.RepoKey("gitlab.example.com/team/app"); !ok {
		t.Error("new clone not added")
	}
	if _, ok := cfg.RepoKey("bitbucket.org/x/y"); ok {
		t.Error("clone on an unconfigured host added")
	}
	if ch, _ := AutoMap(context.Background(), cfg); len(ch) != 0 {
		t.Errorf("second run changed: %+v", ch)
	}
}
