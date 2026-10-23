package review

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	return (&guard{}).line(command, 0)
}

// guard carries what one command line has established so far.
type guard struct {
	scratch bool // cd'd into a review worktree or a temp dir: relative writes are fine
}

// cmdPosition finds a command name that is computed at run time — a variable
// or a substitution — which the guard cannot check ($(echo git) push, $G push).
var cmdPosition = regexp.MustCompile(`(?m)(^|[;&|(\n{])\s*(?:(?:then|do|else|elif|if|while|until|!)\s+)*(?:[A-Za-z_][A-Za-z0-9_]*=\S*\s+)*["']?(\$\(|` + "`" + `|\$\{?[A-Za-z_])`)

var singleQuoted = regexp.MustCompile(`'[^']*'`)

func (g *guard) line(command string, depth int) string {
	if depth > 4 {
		return "command nested too deeply"
	}
	if cmdPosition.MatchString(singleQuoted.ReplaceAllString(command, "''")) { // nothing expands inside '…'
		return "a command name built at run time ($VAR, $(...)) cannot be checked — write the command literally"
	}
	for _, seg := range splitCommands(command) {
		if r := g.segment(seg, depth); r != "" {
			return r
		}
	}
	return ""
}

// guardDepth checks a nested command line (sh -c, eval, scripts) on its own.
func guardDepth(command string, depth int) string {
	return (&guard{}).line(command, depth)
}

var (
	envAssign   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	leadDigit   = regexp.MustCompile(`^\d`)
	gqlMutation = regexp.MustCompile(`(?i)(^|[\s{(=])mutation\b`)
)

// wrappers run the rest of the line as a command; the value lists name the
// options that take an argument (so it is not mistaken for the command).
var wrappers = map[string]map[string]bool{
	"sudo": {"-u": true, "-g": true, "-C": true, "-D": true, "-h": true, "-p": true, "-r": true, "-t": true, "-U": true},
	"doas": {"-u": true, "-C": true}, "env": {"-u": true, "-C": true, "--unset": true, "--chdir": true},
	"command": {}, "builtin": {}, "time": {}, "nohup": {}, "exec": {"-a": true}, "unbuffer": {},
	"nice": {"-n": true}, "ionice": {"-c": true, "-n": true}, "stdbuf": {"-i": true, "-o": true, "-e": true},
	"timeout":    {"-s": true, "-k": true, "--signal": true, "--kill-after": true},
	"xargs":      {"-n": true, "-I": true, "-L": true, "-P": true, "-d": true, "-s": true, "-a": true, "-E": true, "--max-args": true, "--max-procs": true, "--delimiter": true, "--arg-file": true},
	"caffeinate": {"-t": true, "-w": true}, "chrt": {}, "taskset": {}, "setsid": {}, "watch": {"-n": true, "--interval": true},
}

// interpreters run inline code given with these flags; it cannot be parsed,
// so inline code may not start processes, touch the network or write files.
var interpreters = map[string][]string{
	"python": {"-c"}, "python3": {"-c"}, "node": {"-e", "--eval", "-p", "--print"}, "deno": {"eval"}, "bun": {"-e", "--eval"},
	"perl": {"-e", "-E"}, "ruby": {"-e"}, "php": {"-r"}, "osascript": {"-e"}, "awk": nil, "gawk": nil,
}

var riskyCode = regexp.MustCompile(`(?i)system|exec|spawn|popen|subprocess|child_process|\bos\.|fetch\(|urlopen|requests\.|https?\.|\bnet\b|socket|writefile|appendfile|unlink|rmsync|rmdir|\bfs\b|open\([^)]*['"][wa+]|getline|print\s*>|\|\s*"`)

var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "fish": true}

func (g *guard) segment(tok []string, depth int) string {
	// redirections the tokenizer marked: > file, >> file, 2> file
	var plain []string
	for _, t := range tok {
		if strings.HasPrefix(t, redirMark) {
			if r := g.writable(strings.TrimPrefix(t, redirMark)); r != "" {
				return "writing " + r + ": files in the clone are read-only during review (write to /tmp or a review worktree)"
			}
			continue
		}
		plain = append(plain, t)
	}
	tok = plain
	for len(tok) > 0 {
		t := tok[0]
		switch {
		case envAssign.MatchString(t):
			tok = tok[1:]
		case wrappers[filepath.Base(t)] != nil:
			w := filepath.Base(t)
			valued := wrappers[w]
			tok = tok[1:]
			for len(tok) > 0 && strings.HasPrefix(tok[0], "-") {
				if w == "env" && (tok[0] == "-S" || tok[0] == "--split-string") && len(tok) > 1 {
					return guardDepth(strings.Join(tok[1:], " "), depth+1)
				}
				if valued[tok[0]] && len(tok) > 1 {
					tok = tok[1:]
				}
				tok = tok[1:]
			}
			if w == "timeout" && len(tok) > 0 && leadDigit.MatchString(tok[0]) {
				tok = tok[1:]
			}
			if (w == "taskset" || w == "chrt") && len(tok) > 0 && leadDigit.MatchString(tok[0]) {
				tok = tok[1:]
			}
			if w == "watch" {
				return guardDepth(strings.Join(tok, " "), depth+1)
			}
		default:
			goto run
		}
	}
	return ""
run:
	name := filepath.Base(tok[0])
	args := tok[1:]
	if strings.HasPrefix(name, "python3.") || strings.HasPrefix(name, "python2") {
		name = "python"
	}
	switch name {
	case "cd", "pushd":
		g.scratch = len(args) > 0 && scratchPath(args[len(args)-1], false)
		return ""
	case "sh", "bash", "zsh", "dash", "ksh", "fish":
		for i, a := range args {
			// -c, or a short-flag cluster containing c (-lc, -ec)
			isC := strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "c")
			if isC && i+1 < len(args) {
				return guardDepth(args[i+1], depth+1)
			}
		}
		return g.script(name, args, depth)
	case "source", ".":
		return g.script(name, args, depth)
	case "eval":
		return guardDepth(strings.Join(args, " "), depth+1)
	case "find":
		for i, a := range args {
			if a == "-exec" || a == "-execdir" || a == "-ok" || a == "-okdir" {
				var sub []string
				for _, b := range args[i+1:] {
					if b == ";" || b == "+" {
						break
					}
					sub = append(sub, b)
				}
				if r := g.segment(sub, depth+1); r != "" {
					return r
				}
			}
			if a == "-delete" || a == "-fprint" || a == "-fprintf" || a == "-fls" {
				if len(args) == 0 || g.writable(args[0]) != "" {
					return "find " + a + ": changing files in the clone is blocked during review"
				}
			}
		}
	case "rm", "rmdir", "unlink", "shred", "touch", "truncate", "chmod", "chown", "tee", "mv", "cp", "ln", "install", "dd", "sed", "perl":
		if r := g.fileWrite(name, args); r != "" {
			return r
		}
		if name == "perl" {
			return g.inline(name, args)
		}
	case "git":
		return guardGitIn(args, g.scratch, 0)
	case "gh":
		return guardGH(args)
	case "glab":
		return guardGlab(args)
	case "curl", "wget", "http", "https", "xh":
		return guardHTTP(name, args)
	case "python", "python3", "node", "deno", "ruby", "php", "osascript", "awk", "gawk":
		if name == "osascript" {
			return "osascript: controlling other apps is blocked during review"
		}
		return g.inline(name, args)
	case "npm", "pnpm", "yarn", "bun":
		if name == "bun" {
			if r := g.inline(name, args); r != "" {
				return r
			}
		}
		for _, a := range args {
			if a == "publish" || a == "unpublish" || a == "deprecate" || a == "dist-tag" {
				return name + " " + a + ": publishing packages is blocked"
			}
		}
	}
	return ""
}

// script checks a shell script (sh file.sh, source file) by reading it.
// A shell reading its commands from stdin (… | sh) cannot be checked.
func (g *guard) script(name string, args []string, depth int) string {
	file := ""
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			file = a
			break
		}
	}
	if file == "" {
		return name + " reading commands from stdin cannot be checked — run the commands directly"
	}
	// Only scripts the agent could have written itself (temp dirs, review
	// worktrees with the PR's files) are read and checked; tooling such as
	// ~/.nvm/nvm.sh or the clone's own scripts runs as is.
	if !scratchPath(file, g.scratch) {
		return ""
	}
	b, err := os.ReadFile(file)
	if err != nil || len(b) > 1<<18 {
		return name + " " + file + ": the script cannot be inspected, so it may not run during review"
	}
	return guardDepth(string(b), depth+1)
}

// inline checks interpreter code given on the command line.
func (g *guard) inline(name string, args []string) string {
	flags, known := interpreters[name]
	if !known {
		return ""
	}
	code := ""
	if flags == nil { // awk: the program is the first non-flag argument
		for i := 0; i < len(args); i++ {
			if args[i] == "-f" || args[i] == "-v" || args[i] == "-F" {
				i++
				continue
			}
			if !strings.HasPrefix(args[i], "-") {
				code = args[i]
				break
			}
		}
	}
	for i, a := range args {
		for _, f := range flags {
			if a == f && i+1 < len(args) {
				code += "\n" + args[i+1]
			} else if strings.HasPrefix(a, f) && len(f) == 2 && len(a) > 2 && f[0] == '-' && a[1] == f[1] {
				code += "\n" + a[2:] // -e'code'
			}
		}
	}
	if code != "" && riskyCode.MatchString(code) {
		return name + " inline code that runs commands, uses the network or writes files is blocked during review"
	}
	return ""
}

// fileWrite checks commands that change files: their targets must be in a
// temp dir or a review worktree.
func (g *guard) fileWrite(name string, args []string) string {
	var paths []string
	nonFlag := func() []string {
		var out []string
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				out = append(out, a)
			}
		}
		return out
	}
	switch name {
	case "sed", "perl":
		inPlace := false
		for _, a := range args {
			if a == "-i" || strings.HasPrefix(a, "-i") && name == "sed" || strings.HasPrefix(a, "--in-place") || name == "perl" && strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "i") {
				inPlace = true
			}
		}
		if !inPlace {
			return ""
		}
		files := nonFlag()
		hasE := hasAny(args, "-e", "--expression", "-f")
		if !hasE && len(files) > 0 {
			files = files[1:] // the script
		}
		paths = files
	case "cp", "ln", "install":
		if f := nonFlag(); len(f) > 0 {
			paths = f[len(f)-1:]
		}
	case "dd":
		for _, a := range args {
			if strings.HasPrefix(a, "of=") {
				paths = append(paths, strings.TrimPrefix(a, "of="))
			}
		}
	default:
		paths = nonFlag()
		if name == "chmod" || name == "chown" {
			if len(paths) > 0 {
				paths = paths[1:] // the mode / owner
			}
		}
	}
	for _, p := range paths {
		if r := g.writable(p); r != "" {
			return name + " " + r + ": files in the clone are read-only during review (use /tmp or a review worktree)"
		}
	}
	return ""
}

// writable returns "" when a command may write path: a temp dir, a review
// worktree, a device, or a relative path after cd into one of those.
func (g *guard) writable(path string) string {
	switch {
	case path == "", strings.HasPrefix(path, "&"), strings.HasPrefix(path, "/dev/"):
		return ""
	case scratchPath(path, g.scratch):
		return ""
	}
	return path
}

func scratchPath(p string, relOK bool) bool {
	if strings.Contains(p, "..") {
		return false
	}
	if strings.Contains(p, "/worktrees/") || strings.HasPrefix(p, "worktrees/") || strings.HasPrefix(p, ".claude/worktrees") {
		return true
	}
	for _, t := range []string{"$TMPDIR", "${TMPDIR}", "/tmp", "/private/tmp", "/var/folders/", "/private/var/folders/", strings.TrimSuffix(os.TempDir(), "/")} {
		if t != "" && (p == t || strings.HasPrefix(p, t+"/") || strings.HasSuffix(t, "/") && strings.HasPrefix(p, t)) {
			return true
		}
	}
	if relOK && !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "~") && !strings.HasPrefix(p, "$") {
		return true
	}
	return false
}

// --- git -----------------------------------------------------------------

var gitDeny = map[string]string{
	"push": "", "commit": "", "merge": "", "rebase": "", "cherry-pick": "", "revert": "",
	"am": "", "tag": "", "stash": "", "clean": "", "rm": "", "mv": "", "add": "",
	"filter-branch": "", "filter-repo": "", "replace": "", "notes": "", "gc": "", "prune": "",
	"update-ref": "", "send-email": "", "request-pull": "",
	"send-pack": "", "http-push": "", "apply": "", "read-tree": "", "checkout-index": "", "update-index": "",
	"bisect": "", "fast-import": "", "commit-tree": "", "mktag": "", "write-tree": "", "maintenance": "",
	"repack": "", "pack-refs": "", "init": "", "lfs": "", "submodule": "", "subtree": "",
}

// gitRead are subcommands that cannot be aliases' targets worth resolving.
var gitRead = map[string]bool{
	"status": true, "log": true, "diff": true, "show": true, "grep": true, "rev-parse": true, "ls-files": true,
	"ls-tree": true, "cat-file": true, "blame": true, "branch": true, "remote": true, "config": true, "fetch": true,
	"worktree": true, "describe": true, "merge-base": true, "rev-list": true, "shortlog": true, "for-each-ref": true,
	"show-ref": true, "name-rev": true, "version": true, "help": true, "reflog": true, "symbolic-ref": true,
	"checkout": true, "switch": true, "reset": true, "restore": true, "pull": true, "clone": true, "range-diff": true,
	"whatchanged": true, "count-objects": true, "var": true, "check-ignore": true, "check-attr": true, "archive": true,
}

// gitValued are global options that take a separate value.
var gitValued = map[string]bool{"-C": true, "-c": true, "--git-dir": true, "--work-tree": true, "--namespace": true, "--super-prefix": true, "--config-env": true, "--exec-path": false}

// gitAlias resolves a git alias (git refuses to let one shadow a builtin).
var gitAlias = func(dir, name string) string {
	args := []string{"config", "--get", "alias." + name}
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gitWorktreeOnly may run only inside a review worktree (git -C .../worktrees/...).
var gitWorktreeOnly = map[string]bool{"checkout": true, "switch": true, "reset": true, "restore": true, "pull": true}

func guardGit(args []string) string {
	return guardGitIn(args, false, 0)
}

func guardGitIn(args []string, scratch bool, depth int) string {
	if depth > 3 {
		return "git alias nested too deeply"
	}
	dir := ""
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "-C" && i+1 < len(args):
			dir = args[i+1]
			i += 2
		case (a == "-c" || a == "--config-env") && i+1 < len(args):
			if k := strings.ToLower(args[i+1]); strings.HasPrefix(k, "alias.") || strings.HasPrefix(k, "core.") || strings.HasPrefix(k, "credential.") {
				return "git -c " + args[i+1] + ": overriding aliases or core settings is blocked during review"
			}
			i += 2
		case gitValued[a] && i+1 < len(args):
			if a == "--work-tree" || a == "--git-dir" {
				dir = args[i+1]
			}
			i += 2
		case strings.HasPrefix(a, "--git-dir=") || strings.HasPrefix(a, "--work-tree="):
			_, dir, _ = strings.Cut(a, "=")
			i++
		case strings.HasPrefix(a, "-c") && len(a) > 2:
			return "git " + a + ": overriding settings is blocked during review"
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
	if !gitRead[sub] {
		if exp := gitAlias(dir, sub); exp != "" {
			if strings.HasPrefix(exp, "!") {
				return guardDepth(exp[1:]+" "+strings.Join(rest, " "), depth+1)
			}
			return guardGitIn(append(append(append([]string{}, args[:i]...), strings.Fields(exp)...), rest...), scratch, depth+1)
		}
	}
	inWorktree := (dir == "" && scratch) || (dir != "" && scratchPath(dir, false))
	if gitWorktreeOnly[sub] && !inWorktree {
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
	case "reflog":
		if len(rest) > 0 && (rest[0] == "expire" || rest[0] == "delete") {
			return "git reflog " + rest[0] + ": blocked"
		}
	case "symbolic-ref":
		if f, _ := firstWord(rest, nil); f != "" && len(rest) > 1 && !strings.HasPrefix(rest[len(rest)-1], "-") && rest[len(rest)-1] != f {
			return "git symbolic-ref: changing refs is blocked"
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
	if sub == "auth" && hasAny(rest, "--show-token", "-t") {
		return "gh auth --show-token: printing tokens is blocked during review"
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

// hasGraphQLMutation reports a mutation — or a query the guard cannot see:
// read from a file it cannot open, from stdin, or built from a variable.
func hasGraphQLMutation(args []string) bool {
	if len(args) == 0 || args[0] != "graphql" {
		return false
	}
	for i, a := range args {
		v := a
		switch {
		case (a == "-f" || a == "-F" || a == "--field" || a == "--raw-field") && i+1 < len(args):
			v = args[i+1]
		case a == "--input" && i+1 < len(args):
			v = "query=@" + args[i+1]
		}
		if q, ok := strings.CutPrefix(v, "query="); ok {
			if strings.Contains(q, "$") && !strings.Contains(q, "query(") && !strings.Contains(q, "query (") {
				return true // "$Q": the text is not known here
			}
			if f, ok := strings.CutPrefix(q, "@"); ok {
				b, err := os.ReadFile(f)
				if err != nil || f == "-" {
					return true
				}
				v = string(b)
			}
		}
		if gqlMutation.MatchString(v) {
			return true
		}
	}
	return false
}

// --- http ----------------------------------------------------------------

func guardHTTP(name string, args []string) string {
	for i, a := range args {
		low := strings.ToLower(a)
		if name == "curl" && len(a) > 2 && a[0] == '-' && a[1] != '-' {
			// a cluster of short options: -sXPOST, -sd, -sK file
			for j := 1; j < len(a); j++ {
				switch a[j] {
				case 'X':
					m := a[j+1:]
					if m == "" && i+1 < len(args) {
						m = args[i+1]
					}
					if m = strings.ToUpper(m); m != "" && m != "GET" && m != "HEAD" {
						return name + " " + m + ": only GET is allowed"
					}
					j = len(a)
				case 'd', 'F', 'T':
					return name + ": sending data is blocked"
				case 'K':
					return name + " -K: a config file cannot be checked"
				}
			}
			continue
		}
		switch {
		case name == "curl" && (a == "-K" || a == "--config" || strings.HasPrefix(a, "--config=")):
			return name + " --config: a config file cannot be checked"
		case name == "wget" && (strings.HasPrefix(low, "--method") || strings.HasPrefix(low, "--body-")):
			if strings.HasPrefix(low, "--body-") || !strings.HasSuffix(strings.ToUpper(a), "GET") && !(a == "--method" && i+1 < len(args) && strings.EqualFold(args[i+1], "GET")) {
				return name + " " + a + ": only GET is allowed"
			}
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
	pendingRedir, pendingIn := false, false
	flushTok := func() {
		if inTok {
			t := tok.String()
			switch {
			case pendingRedir:
				t = redirMark + t
			case pendingIn:
				t = "" // an input file is not an argument
			}
			if !pendingIn {
				cur = append(cur, t)
			}
			pendingRedir, pendingIn = false, false
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
		case c == '>' || c == '<':
			// a redirection: > file, >> file, 2> file (an fd number before it
			// is not an argument); the target is marked for the write check
			if inTok && isDigits(tok.String()) {
				tok.Reset()
				inTok = false
			}
			flushTok()
			out := c == '>'
			for i+1 < len(rs) && (rs[i+1] == '>' || rs[i+1] == '|') {
				i++
			}
			if i+1 < len(rs) && rs[i+1] == '&' {
				i++ // >&2: to a file descriptor
				for i+1 < len(rs) && rs[i+1] >= '0' && rs[i+1] <= '9' {
					i++
				}
				continue
			}
			for i+1 < len(rs) && (rs[i+1] == ' ' || rs[i+1] == '\t') {
				i++
			}
			if out {
				pendingRedir = true
			} else {
				pendingIn = true
			}
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

// redirMark prefixes a token that is a redirection target.
const redirMark = "\x00>"

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
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
// allow holds review.guard_allow exceptions.
func RunHook(r io.Reader, allow []string) (string, error) {
	var in HookInput
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return "", err
	}
	switch {
	case in.ToolName == "Bash":
		if allowed(allow, "Bash("+strings.TrimSpace(in.ToolInput.Command)+")") {
			return "", nil
		}
		return Guard(in.ToolInput.Command), nil
	case strings.HasPrefix(in.ToolName, "mcp__"):
		if allowed(allow, in.ToolName) {
			return "", nil
		}
		return GuardTool(in.ToolName), nil
	}
	return "", nil
}

// mcpRead are the verbs MCP tools that only read start with
// (getJiraIssue, search_documents, list-files…).
var mcpRead = map[string]bool{
	"get": true, "list": true, "search": true, "fetch": true, "read": true, "query": true, "find": true,
	"lookup": true, "describe": true, "view": true, "show": true, "preview": true, "analyze": true,
	"introspect": true, "inspect": true, "count": true, "browse": true, "retrieve": true, "summarize": true,
}

var wordSplit = regexp.MustCompile(`[A-Z]?[a-z]+|[A-Z]+(?:$|[A-Z][a-z])|\d+`)

// GuardTool lets MCP tools that read through (Jira issues, Confluence pages,
// docs) and blocks the rest: creating, editing, commenting, transitioning,
// deleting, sending, and anything whose name does not say it only reads.
func GuardTool(name string) string {
	parts := strings.SplitN(name, "__", 3)
	if len(parts) < 3 {
		return ""
	}
	tool := parts[2]
	words := wordSplit.FindAllString(strings.NewReplacer("_", " ", "-", " ").Replace(tool), -1)
	if len(words) > 0 && mcpRead[strings.ToLower(words[0])] {
		return ""
	}
	return fmt.Sprintf("MCP tool %s may change data outside the review (only get/list/search/fetch… tools are allowed) — add it to review.guard_allow to permit it", tool)
}

// allowed matches name against guard_allow entries: exact, or a prefix
// ending in "*" (also inside Bash(…): "Bash(git push*)").
func allowed(allow []string, name string) bool {
	for _, a := range allow {
		a = strings.TrimSpace(a)
		switch {
		case a == name:
			return true
		case strings.HasSuffix(a, "*") && strings.HasPrefix(name, strings.TrimSuffix(a, "*")):
			return true
		case strings.HasSuffix(a, "*)") && strings.HasPrefix(name, strings.TrimSuffix(a, "*)")):
			return true
		}
	}
	return false
}
