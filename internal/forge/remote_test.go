package forge

import "testing"

func TestRemoteKey(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"git@gitlab.example.com:team/app.git", "gitlab.example.com/team/app", true},
		{"git@gitlab.example.com:group/sub/mobile.git", "gitlab.example.com/group/sub/mobile", true},
		{"git@github.com:acme/widgets.git", "github.com/acme/widgets", true},
		{"https://github.com/acme/widgets-api", "github.com/acme/widgets-api", true},
		{"https://user@bitbucket.org/team/site.git", "bitbucket.org/team/site", true},
		{"ssh://deploy@git.example.org/var/git/site.git", "git.example.org/var/git/site", true},
		{"ssh://git@GitLab.Example.com:2222/group/proj.git/", "gitlab.example.com/group/proj", true},
		{"https://oauth2:tok@gitlab.example.com/a/b.git", "gitlab.example.com/a/b", true},
		{"/srv/git/local.git", "", false},
		{"./relative/repo", "", false},
		{"file:///srv/git/x.git", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := RemoteKey(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("RemoteKey(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParseURL(t *testing.T) {
	p, r, n, err := ParseURL("https://gitlab.example.com/team/app/-/merge_requests/883/diffs")
	if err != nil || p != GitLab || r != "gitlab.example.com/team/app" || n != 883 {
		t.Errorf("gitlab: %s %s %d %v", p, r, n, err)
	}
	p, r, n, err = ParseURL("https://github.com/acme/widgets/pull/340/files")
	if err != nil || p != GitHub || r != "github.com/acme/widgets" || n != 340 {
		t.Errorf("github: %s %s %d %v", p, r, n, err)
	}
	if _, _, _, err := ParseURL("https://github.com/acme/widgets/issues/1"); err == nil {
		t.Error("issue URL accepted")
	}
}

func TestFilesString(t *testing.T) {
	var nilF *Files
	if nilF.String() != "" {
		t.Error("nil")
	}
	if s := (&Files{Added: 2, Modified: 5, Deleted: 1}).String(); s != "+2 ~5 −1" {
		t.Errorf("got %q", s)
	}
	if s := (&Files{Modified: 3, More: 40}).String(); s != "~3 …40" {
		t.Errorf("got %q", s)
	}
}
