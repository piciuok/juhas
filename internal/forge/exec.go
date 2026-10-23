package forge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Timeout bounds every gh/glab call so a hung network never stalls a poll.
var Timeout = 60 * time.Second

// run executes name args and returns stdout; stderr is folded into the error.
func run(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(append(os.Environ(), noPrompt...), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 400 {
			msg = msg[:400] + "…"
		}
		if msg == "" {
			msg = err.Error()
		}
		return stdout.Bytes(), fmt.Errorf("%s %s: %s", name, strings.Join(redact(args), " "), msg)
	}
	return stdout.Bytes(), nil
}

// runCombined returns stdout+stderr regardless of exit status (for `auth status`).
func runCombined(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), noPrompt...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// noPrompt keeps gh/glab from waiting on a TTY (there is none under the tray
// app) or piping into a pager.
var noPrompt = []string{"GH_PROMPT_DISABLED=1", "GLAB_NO_PROMPT=1", "NO_COLOR=1", "PAGER=cat", "GH_PAGER=cat", "GLAB_PAGER=cat"}

func redact(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if strings.Contains(strings.ToLower(a), "token") {
			a = "<redacted>"
		}
		out[i] = a
	}
	return out
}

// Available reports whether bin is on PATH.
func Available(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}
