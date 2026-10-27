package tray

import "testing"

func TestProjectKey(t *testing.T) {
	for in, want := range map[string]string{
		"gitlab.example.com/team/app":                             "gitlab.example.com/team/app",
		"https://gitlab.example.com/team/sub/app":                 "gitlab.example.com/team/sub/app",
		"https://gitlab.example.com/team/app/-/merge_requests":    "gitlab.example.com/team/app",
		"https://gitlab.example.com/team/app/-/merge_requests/12": "gitlab.example.com/team/app",
		"https://github.com/acme/widgets/pulls":                   "github.com/acme/widgets",
		"https://github.com/acme/widgets/pull/3":                  "github.com/acme/widgets",
		"git@gitlab.example.com:team/app.git":                     "gitlab.example.com/team/app",
		"  GitLab.example.com/team/app  ":                         "gitlab.example.com/team/app",
	} {
		got, err := projectKey(in)
		if err != nil || got != want {
			t.Errorf("projectKey(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "app", "gitlab.example.com", "gitlab.example.com/team"} {
		if got, err := projectKey(in); err == nil {
			t.Errorf("projectKey(%q) = %q, want an error", in, got)
		}
	}
}
