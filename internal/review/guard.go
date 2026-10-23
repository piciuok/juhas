package review

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
)

// Guard is the PreToolUse hook every review session runs with. Claude Code's
// prefix permission rules cannot tell `glab api projects/x/notes` (read) from
// `glab api projects/x/notes -f body=...` (post), so the hook parses each Bash
// command, including chains, pipes, $(...) and sh -c, and rejects anything
// that writes to a forge, to git history or to a package registry.
//
// It returns "" to allow or the reason to deny.
func Guard(command string) string {
	return guardDepth(command, 0)
}

func guardDepth(command string, depth int) string {
	if depth > 4 {
		return "command nested too deeply"
	}
	for _, seg := range splitCommands(command) {
		if r := guardSegment(seg, depth); r != "" {
			return r
		}
	}
	return ""
}

var (
	envAssign   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	leadDigit   = regexp.MustCompile(`^\d`)
	gqlMutation = regexp.MustCompile(`(?i)(^|[\s{(=])mutation\b`)
)

// wrappers run the rest of the line as a command.
var wrappers = map[string]bool{"sudo": true, "env": true, "command": true, "time": true, "nice": true, "nohup": true, "exec": true, "xargs": true, "timeout": true, "builtin": true}

func guardSegment(tok []string, depth int) string {
	for len(tok) > 0 {
		t := tok[0]
		switch {
		case envAssign.MatchString(t):
			tok = tok[1:]
		case wrappers[filepath.Base(t)]:
			tok = tok[1:]
			// skip wrapper options and timeout's duration
			for len(tok) > 0 && (strings.HasPrefix(tok[0], "-") || (filepath.Base(t) == "timeout" && leadDigit.MatchString(tok[0]))) {
				tok = tok[1:]
			}
		default:
			goto run
		}
	}
	return ""
run:
	name := filepath.Base(tok[0])
	args := tok[1:]
	switch name {
	case "sh", "bash", "zsh", "dash", "ksh", "fish":
		for i, a := range args {
			// -c, or a short-flag cluster containing c (-lc, -ec)
			isC := strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "c")
			if isC && i+1 < len(args) {
				return guardDepth(args[i+1], depth+1)
			}
		}
	case "eval":
		return guardDepth(strings.Join(args, " "), depth+1)
	case "git":
		return guardGit(args)
	case "gh":
		return guardGH(args)
	case "glab":
		return guardGlab(args)
	case "curl", "wget", "http", "https", "xh":
		return guardHTTP(name, args)
	case "npm", "pnpm", "yarn", "bun":
		for _, a := range args {
			if a == "publish" || a == "unpublish" || a == "deprecate" || a == "dist-tag" {
				return name + " " + a + ": publishing packages is blocked"
			}
		}
	}
	return ""
}

// --- git -----------------------------------------------------------------

var gitDeny = map[string]string{
	"push": "", "commit": "", "merge": "", "rebase": "", "cherry-pick": "", "revert": "",
	"am": "", "tag": "", "stash": "", "clean": "", "rm": "", "mv": "", "add": "",
	"filter-branch": "", "filter-repo": "", "replace": "", "notes": "", "gc": "", "prune": "",
	"update-ref": "", "send-email": "", "request-pull": "",
}

// gitWorktreeOnly may run only inside a review worktree (git -C .../worktrees/...).
var gitWorktreeOnly = map[string]bool{"checkout": true, "switch": true, "reset": true, "restore": true, "pull": true}

func guardGit(args []string) string {
	dir := ""
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "-C" && i+1 < len(args):
			dir = args[i+1]
			i += 2
		case a == "-c" && i+1 < len(args):
			i += 2
		case strings.HasPrefix(a, "--git-dir=") || strings.HasPrefix(a, "--work-tree="):
			if strings.HasPrefix(a, "--work-tree=") {
				dir = strings.TrimPrefix(a, "--work-tree=")
			}
			i++
		case strings.HasPrefix(a, "-"):
			i++
		default:
			goto sub
		}
	}
	return ""
sub:
	sub := args[i]
	rest := args[i+1:]
	if _, deny := gitDeny[sub]; deny {
		return "git " + sub + ": changing the repo is blocked (review is read-only)"
	}
	if gitWorktreeOnly[sub] && !strings.Contains(dir, "worktrees") {
		return "git " + sub + ": allowed only in a review worktree (git -C <repo>/.claude/worktrees/...); do not switch branches in the main clone"
	}
	switch sub {
	case "branch":
		for _, a := range rest {
			if a == "-d" || a == "-D" || a == "--delete" || a == "-m" || a == "-M" || a == "--move" || a == "-f" || a == "--force" {
				return "git branch " + a + ": blocked"
			}
		}
	case "remote":
		if len(rest) > 0 && rest[0] != "-v" && rest[0] != "show" && rest[0] != "get-url" && !strings.HasPrefix(rest[0], "-") {
			return "git remote " + rest[0] + ": blocked"
		}
	case "config":
		if !hasAny(rest, "--get", "--get-all", "--get-regexp", "--list", "-l", "get") {
			return "git config: writing config is blocked"
		}
	case "worktree":
		// add/remove/list/prune of review worktrees is how repo skills check out a PR
	}
	return ""
}

// --- gh ------------------------------------------------------------------

var ghReadSubs = map[string]map[string]bool{
	"pr":       {"view": true, "diff": true, "checks": true, "list": true, "status": true},
	"issue":    {"view": true, "list": true, "status": true},
	"run":      {"view": true, "list": true, "watch": true, "download": true},
	"workflow": {"view": true, "list": true},
	"release":  {"view": true, "list": true, "download": true},
	"repo":     {"view": true, "list": true},
	"label":    {"list": true},
	"cache":    {"list": true},
	"auth":     {"status": true, "token": false},
	"search":   nil, // all read
	"status":   nil,
	"version":  nil,
	"api":      nil, // checked below
	"help":     nil,
}

func guardGH(args []string) string {
	sub, rest := firstWord(args, map[string]bool{"-R": true, "--repo": true, "--hostname": true})
	if sub == "" {
		return ""
	}
	subs, known := ghReadSubs[sub]
	if !known {
		return "gh " + sub + ": not allowed during review (read-only: pr view/diff/checks, api GET, search)"
	}
	if sub == "api" {
		return guardAPI("gh", rest)
	}
	if subs == nil {
		return ""
	}
	verb, _ := firstWord(rest, map[string]bool{"-R": true, "--repo": true})
	if verb == "" || subs[verb] {
		return ""
	}
	return fmt.Sprintf("gh %s %s: not allowed during review (do not publish anything on the PR)", sub, verb)
}

// --- glab ----------------------------------------------------------------

var glabReadSubs = map[string]map[string]bool{
	"mr":       {"view": true, "diff": true, "list": true, "issues": true},
	"issue":    {"view": true, "list": true},
	"ci":       {"view": true, "list": true, "status": true, "get": true, "trace": true},
	"pipeline": {"view": true, "list": true, "status": true, "get": true, "trace": true},
	"job":      {"view": true, "list": true, "trace": true, "artifact": true},
	"repo":     {"view": true, "list": true},
	"release":  {"view": true, "list": true, "download": true},
	"label":    {"list": true},
	"auth":     {"status": true},
	"version":  nil,
	"api":      nil,
	"help":     nil,
}

func guardGlab(args []string) string {
	sub, rest := firstWord(args, map[string]bool{"-R": true, "--repo": true})
	if sub == "" {
		return ""
	}
	subs, known := glabReadSubs[sub]
	if !known {
		return "glab " + sub + ": not allowed during review (read-only: mr view/diff, api GET)"
	}
	if sub == "api" {
		return guardAPI("glab", rest)
	}
	if subs == nil {
		return ""
	}
	verb, _ := firstWord(rest, map[string]bool{"-R": true, "--repo": true, "-F": true, "--output": true})
	if verb == "" || subs[verb] {
		return ""
	}
	return fmt.Sprintf("glab %s %s: not allowed during review (do not publish anything on the MR)", sub, verb)
}

// guardAPI allows only GET. Both CLIs switch to POST as soon as a field or
// an input body is given, unless the method is set explicitly.
func guardAPI(bin string, args []string) string {
	method := ""
	body := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-X" || a == "--method":
			if i+1 < len(args) {
				method = strings.ToUpper(args[i+1])
				i++
			}
		case strings.HasPrefix(a, "-X") && len(a) > 2:
			method = strings.ToUpper(a[2:])
		case strings.HasPrefix(a, "--method="):
			method = strings.ToUpper(strings.TrimPrefix(a, "--method="))
		case a == "-f" || a == "-F" || a == "--field" || a == "--raw-field" || a == "--input" || a == "--form",
			strings.HasPrefix(a, "--field=") || strings.HasPrefix(a, "--raw-field=") || strings.HasPrefix(a, "--input=") || strings.HasPrefix(a, "--form="),
			strings.HasPrefix(a, "-f") && len(a) > 2, strings.HasPrefix(a, "-F") && len(a) > 2:
			body = true
		}
	}
	if (method == "" && !body) || method == "GET" || method == "HEAD" {
		// graphql is POST by nature; only queries are allowed
		if hasGraphQLMutation(args) {
			return bin + " api graphql: mutations are blocked"
		}
		return ""
	}
	if method == "" {
		method = "POST"
	}
	if len(args) > 0 && args[0] == "graphql" && !hasGraphQLMutation(args) && method == "POST" {
		return ""
	}
	return fmt.Sprintf("%s api %s: only GET requests are allowed (review publishes nothing)", bin, method)
}

func hasGraphQLMutation(args []string) bool {
	if len(args) == 0 || args[0] != "graphql" {
		return false
	}
	for _, a := range args {
		if gqlMutation.MatchString(a) {
			return true
		}
	}
	return false
}

// --- http ----------------------------------------------------------------

func guardHTTP(name string, args []string) string {
	for i, a := range args {
		low := strings.ToLower(a)
		switch {
		case a == "-X" || a == "--request":
			if i+1 < len(args) {
				if m := strings.ToUpper(args[i+1]); m != "GET" && m != "HEAD" {
					return name + " " + m + ": only GET is allowed"
				}
			}
		case strings.HasPrefix(a, "-X") && len(a) > 2:
			if m := strings.ToUpper(a[2:]); m != "GET" && m != "HEAD" {
				return name + " " + m + ": only GET is allowed"
			}
		case a == "-d" || strings.HasPrefix(low, "--data") || a == "-F" || a == "--form" || strings.HasPrefix(low, "--form") ||
			a == "-T" || a == "--upload-file" || strings.HasPrefix(low, "--post-") || a == "--json" ||
			strings.HasPrefix(a, "-d") && len(a) > 2:
			return name + ": sending data is blocked"
		}
	}
	if name == "http" || name == "https" || name == "xh" {
		for _, a := range args {
			switch strings.ToUpper(a) {
			case "POST", "PUT", "PATCH", "DELETE":
				return name + " " + strings.ToUpper(a) + ": only GET is allowed"
			}
		}
	}
	return ""
}

// --- helpers -------------------------------------------------------------

// firstWord returns the first non-flag argument, skipping the values of the
// given flags, and the arguments after it.
func firstWord(args []string, valued map[string]bool) (string, []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if valued[a] {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		return a, args[i+1:]
	}
	return "", nil
}

func hasAny(args []string, want ...string) bool {
	for _, a := range args {
		for _, w := range want {
			if a == w {
				return true
			}
		}
	}
	return false
}

// splitCommands tokenizes a shell line into simple commands. It honours
// quotes and treats ; & | newlines, parentheses, backticks and $( as
// separators. Splitting too eagerly only produces extra segments to check.
func splitCommands(s string) [][]string {
	var cmds [][]string
	var cur []string
	var tok strings.Builder
	inTok := false
	flushTok := func() {
		if inTok {
			cur = append(cur, tok.String())
			tok.Reset()
			inTok = false
		}
	}
	flushCmd := func() {
		flushTok()
		if len(cur) > 0 {
			cmds = append(cmds, cur)
			cur = nil
		}
	}
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '\\' && i+1 < len(rs):
			if rs[i+1] == '\n' {
				i++
				continue
			}
			tok.WriteRune(rs[i+1])
			inTok = true
			i++
		case c == '\'':
			inTok = true
			j := i + 1
			for j < len(rs) && rs[j] != '\'' {
				tok.WriteRune(rs[j])
				j++
			}
			i = j
		case c == '"':
			inTok = true
			j := i + 1
			for j < len(rs) && rs[j] != '"' {
				if rs[j] == '\\' && j+1 < len(rs) {
					tok.WriteRune(rs[j+1])
					j += 2
					continue
				}
				if rs[j] == '`' || (rs[j] == '$' && j+1 < len(rs) && rs[j+1] == '(') {
					// command substitution inside quotes: check it as its own command
					end := matchSubst(rs, j)
					inner := string(rs[j+1 : end])
					if rs[j] == '$' {
						inner = string(rs[j+2 : end])
					}
					cmds = append(cmds, splitCommands(inner)...)
					j = end + 1
					continue
				}
				tok.WriteRune(rs[j])
				j++
			}
			i = j
		case c == ' ' || c == '\t':
			flushTok()
		case c == ';' || c == '&' || c == '|' || c == '\n' || c == '(' || c == ')' || c == '`' || c == '{' && !inTok || c == '}' && !inTok:
			flushCmd()
		case c == '$' && i+1 < len(rs) && rs[i+1] == '(':
			flushCmd()
			i++
		case c == '#' && !inTok:
			// comment to end of line
			for i < len(rs) && rs[i] != '\n' {
				i++
			}
			flushCmd()
		default:
			tok.WriteRune(c)
			inTok = true
		}
	}
	flushCmd()
	return cmds
}

// matchSubst returns the index of the closing ) or ` of a substitution at j.
func matchSubst(rs []rune, j int) int {
	if rs[j] == '`' {
		for k := j + 1; k < len(rs); k++ {
			if rs[k] == '`' {
				return k
			}
		}
		return len(rs) - 1
	}
	depth := 0
	for k := j + 1; k < len(rs); k++ {
		switch rs[k] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return k
			}
		}
	}
	return len(rs) - 1
}

// HookInput is the PreToolUse payload Claude Code sends on stdin.
type HookInput struct {
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

// RunHook reads a PreToolUse payload and returns the deny reason ("" = allow).
func RunHook(r io.Reader) (string, error) {
	var in HookInput
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return "", err
	}
	if in.ToolName != "Bash" {
		return "", nil
	}
	return Guard(in.ToolInput.Command), nil
}
