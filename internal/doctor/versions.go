package doctor

import (
	"fmt"
	"regexp"
	"runtime"
	"strconv"
)

// MinVersions are the oldest CLIs Juhas is known to work with: the flags it
// relies on (gh api graphql, glab api --hostname, claude stream-json with
// --settings hooks and dontAsk, codex exec --json with -c overrides,
// git worktree --detach) and, for codex, the current model names.
var MinVersions = map[string]string{
	"git":    "2.25",
	"gh":     "2.40",
	"glab":   "1.40",
	"claude": "2.0",
	"codex":  "0.150",
}

var versionRe = regexp.MustCompile(`(\d+)\.(\d+)(?:\.(\d+))?`)

// parseVersion finds the first x.y[.z] in a --version line.
func parseVersion(s string) ([3]int, bool) {
	m := versionRe.FindStringSubmatch(s)
	if m == nil {
		return [3]int{}, false
	}
	var v [3]int
	for i := 0; i < 3; i++ {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return v, true
}

func older(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// checkVersion compares a --version line of tool name against MinVersions.
// It returns Warn with an upgrade hint for a too old one, OK otherwise (also
// when the version cannot be read: that is not worth a warning).
func checkVersion(name, line string) (Level, string, string) {
	min, ok := MinVersions[name]
	if !ok {
		return OK, "", ""
	}
	have, ok1 := parseVersion(line)
	want, _ := parseVersion(min)
	if !ok1 || !older(have, want) {
		return OK, "", ""
	}
	return Warn, fmt.Sprintf(" — too old (minimum %s)", min), upgradeHint(name)
}

func upgradeHint(name string) string {
	switch name {
	case "claude":
		return "claude update"
	case "codex":
		return "codex update or: npm install -g @openai/codex@latest"
	}
	if runtime.GOOS == "darwin" {
		return "brew upgrade " + name
	}
	return map[string]string{
		"git":  "sudo apt install --only-upgrade git  |  sudo dnf upgrade git",
		"gh":   "sudo apt install --only-upgrade gh  |  sudo dnf upgrade gh",
		"glab": "download a newer package: https://gitlab.com/gitlab-org/cli/-/releases",
	}[name]
}
