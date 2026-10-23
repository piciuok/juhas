package forge

import (
	"strings"
	"testing"
)

const ghStatus = `github.com
  ✓ Logged in to github.com account jan-dev (keyring)
  - Active account: true
  - Token scopes: 'gist', 'read:org', 'repo', 'workflow'
`

const ghTwo = `github.com
  ✓ Logged in to github.com account jan-home (keyring)
  - Active account: true
  - Token scopes: 'gist', 'read:org', 'repo'

  ✓ Logged in to github.com account jan-work (keyring)
  - Active account: false
  - Token scopes: 'repo'
`

const glabStatus = `gitlab.com
  x gitlab.com: API call failed: GET https://gitlab.com/api/v4/user: 401 {message: 401 Unauthorized}
  ✓ Git operations for gitlab.com configured to use ssh protocol.
  ! No token found (checked config file, keyring, and environment variables).
gitlab.example.com
  ✓ Logged in to gitlab.example.com as jan.kowalski (/Users/x/Library/Application Support/glab-cli/config.yml)
  ✓ Git operations for gitlab.example.com configured to use https protocol.
`

func TestParseAuthStatus(t *testing.T) {
	gh := parseAuthStatus(ghStatus)
	if len(gh) != 1 || !gh[0].LoggedIn || gh[0].Account != "jan-dev" {
		t.Errorf("gh: %+v", gh)
	}
	two := parseAuthStatus(ghTwo)
	if len(two) != 2 || two[0].Account != "jan-home" || !two[0].Active || two[1].Account != "jan-work" || two[1].Active ||
		!two[1].LoggedIn || two[1].Host != "github.com" || strings.Contains(two[1].Detail, "read:org") {
		t.Errorf("two gh accounts: %+v", two)
	}
	gl := parseAuthStatus(glabStatus)
	if len(gl) != 2 {
		t.Fatalf("glab: %+v", gl)
	}
	if gl[0].Host != "gitlab.com" || gl[0].LoggedIn {
		t.Errorf("gitlab.com should be logged out: %+v", gl[0])
	}
	if gl[1].Host != "gitlab.example.com" || !gl[1].LoggedIn || gl[1].Account != "jan.kowalski" {
		t.Errorf("gitlab.example.com: %+v", gl[1])
	}
}
