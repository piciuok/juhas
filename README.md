# Juhas

[![CI](https://github.com/piciuok/juhas/actions/workflows/ci.yml/badge.svg)](https://github.com/piciuok/juhas/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/piciuok/juhas?sort=semver)](https://github.com/piciuok/juhas/releases)

> Watches your PR/MRs like a shepherd watches the flock — and when something urgent shows up, it kicks like an electric fence.

**Juhas** (in the Polish highlands, the shepherd's helper who actually keeps an eye on the sheep) is a small
menu-bar (macOS) / system-tray (Linux) app that tracks the GitHub pull requests and GitLab merge requests
waiting for your review. On demand it prepares a **code-review brief** with Claude Code or Codex —
using the repo's own review skill when it has one — in **read-only mode**. The agent only *proposes*
comments; you accept, edit or reject each one, and Juhas publishes the accepted ones from your account.

- **Counters** — what waits for you, how many PR/MRs are open per repo, files changed (`+new ~modified −deleted`).
- **Urgent items** assigned to you turn the icon into a **sparking lightning bolt** until you acknowledge them.
  A new urgent one also gets you a **kick**: an on-screen discharge (macOS) and a loud notification.
- **New PR/MR** → "Run AI review?" with a choice of **Claude / Codex**, *Later* or *Skip*.
- **Human in the loop** — a blinking, sparking **❓** next to the icon reminds you of proposals awaiting your decision.
- **Live run log**, briefs as Markdown files, and a browser **Panel** that stays open while you click.
- **Several accounts** — work and personal GitHub logins (and GitLab ones) side by side, each acting as itself.
- **Settings UI** over a plain JSON file: global defaults, per-repo overrides, folder picker, repo auto-mapping.
- **Diagnostics** for every CLI it needs (versions, logins), with copy-paste fixes.
- **Self-updating** from GitHub releases, with checksum verification.

## Contents

- [Install](#install)
- [Requirements](#requirements)
- [Where it gets things from](#where-it-gets-things-from)
- [Accounts](#accounts)
- [Configuration](#configuration)
- [How a review works](#how-a-review-works)
- [Updates](#updates)
- [Commands](#commands)
- [Q&A](#qa)
- [Contributing](#contributing)

## Install

**One-liner** (macOS arm64/amd64, Linux amd64/arm64). It downloads the latest release, verifies its
SHA-256, puts `juhas` in `~/.local/bin`, creates a config from your clones and enables start at login:

```sh
curl -fsSL https://raw.githubusercontent.com/piciuok/juhas/main/scripts/install.sh | sh
```

Pin a version with `JUHAS_VERSION=v0.2.0`, or only put the binary in place with `JUHAS_NO_START=1`.

**Manually** — download `juhas_<version>_<os>_<arch>.tar.gz` from [Releases](https://github.com/piciuok/juhas/releases),
check it against `checksums.txt`, then:

```sh
tar xzf juhas_*.tar.gz && cd juhas_*/
./juhas config init      # finds your gh/glab hosts and clones in ~/code, ~/projects, …
./juhas doctor           # checks CLIs, logins, repos, desktop integration
./juhas install          # copies itself to ~/.local/bin and enables start at login
```

> macOS quarantines binaries downloaded with a browser: `xattr -d com.apple.quarantine ./juhas`.
> The install script and the built-in updater are not affected.

**From source** (Go 1.27+; on macOS also the Xcode Command Line Tools, for cgo):

```sh
make build && ./bin/juhas install
```

`make dist` cross-builds the Linux binaries on any OS. Start at login is a LaunchAgent
(`~/Library/LaunchAgents/pl.piciuok.juhas.plist`) on macOS and `~/.config/autostart/juhas.desktop` on Linux;
toggle it in the menu or with `juhas install` / `juhas uninstall`.

## Requirements

| | macOS | Linux |
|---|---|---|
| git, [gh](https://cli.github.com), [glab](https://gitlab.com/gitlab-org/cli) | `brew install gh glab` | distro packages or releases (`juhas doctor` tells you how) |
| [Claude Code](https://code.claude.com) and/or [Codex](https://github.com/openai/codex) | `curl -fsSL https://claude.ai/install.sh \| bash`, `brew install codex` | the same, or `npm i -g @openai/codex` |
| tray icon | built in (menu bar) | KDE, XFCE, Cinnamon out of the box; **GNOME needs the AppIndicator extension** (default on Ubuntu; Fedora: `sudo dnf install gnome-shell-extension-appindicator`) |
| dialogs with buttons | built in (`osascript`), optionally `alerter` | `notify-send` (libnotify ≥ 0.7.10), `zenity` or `kdialog` |
| clickable notifications | `brew install terminal-notifier` (optional) | `notify-send` |

Minimum versions: git 2.25, gh 2.40, glab 1.40, Claude Code 2.0, Codex 0.150.

Log in once in each CLI: `gh auth login`, `glab auth login --hostname gitlab.example.com`, `claude`, `codex login`.

**Diagnostics** (menu → *🩺 Diagnostics*, the Panel, Settings, or `juhas doctor`) checks the binaries,
their versions and logins *in the same environment the tray runs in* (PATH, Keychain) and gives you a
command to copy for every problem.

## Where it gets things from

**Project folders** (`scan_dirs`) are scanned up to 3 levels deep, and each clone is matched to its repo by
`origin` (`git@host:group/project.git` → `host/group/project`). With `auto_map` on (the default) new clones
are added to `repos` and moved ones get their new path. Settings has a folder picker and an *Add from scan* tab.

**Mode** per repo (`mode`):

- `requested` — PR/MRs where your review was requested or you are the assignee (on GitHub, team requests too).
- `open` — every open PR/MR of the repo except yours, those by `ignore_authors` (bots by default) and those
  you already approved. Handy when a team never assigns reviewers; `juhas doctor` points such repos out.

**Urgent** (`urgent`): assigned to you or requesting your review, and carrying one of `labels`, waiting
longer than `older_than`, or targeting one of `target_branches` (e.g. `release/*`).

## Accounts

Juhas never stores tokens: it uses the logins of `gh` and `glab`. *Settings → Accounts* lists every login
they have, with a **Watch** checkbox for whose review requests to check.

```jsonc
"github": [
  { "host": "github.com", "user": "me-at-work" },
  { "host": "github.com", "user": "me-at-home" },
  { "host": "github.example.com", "watch": false }           // kept, but not checked
],
"gitlab": [
  { "host": "gitlab.example.com" },                           // glab's login
  { "host": "gitlab.example.com", "user": "bot-reviewer",     // a second account on the same host
    "config_dir": "~/.config/juhas/glab/gitlab.example.com-bot-reviewer" }
]
```

- **GitHub:** `gh` keeps several accounts per host (`gh auth login` once for each). With `user` Juhas uses
  exactly that account, whichever one is active in `gh`; without it, `gh`'s active account.
- **GitLab:** `glab` keeps one login per host, so a second account gets its own glab config:
  `GLAB_CONFIG_DIR=<config_dir> glab auth login --hostname <host>` (Settings shows the exact command).
- A PR/MR is **handled by the account that was asked**: fetching the diff, the review agent's own gh/glab calls
  and publishing your accepted comments. A PR/MR written by another of your accounts counts as yours.
- For repos in `open` mode, `"account": "<user>"` in the repo settings picks the login that reads it.

## Configuration

`juhas config ui` (or *Settings…* in the menu) opens a form in your browser (127.0.0.1, one-time token)
that edits `~/.config/juhas/config.json` — the same path on macOS and Linux. Repo settings are layered:
**built-in → `defaults` → `repos[key]`**; objects merge deeply, lists and scalars replace.
`juhas config show <key>` prints the effective result.

```jsonc
{
  "scan_dirs": ["~/Projects"],
  "gitlab": [{ "host": "gitlab.example.com" }],
  "claude": { "bin": "claude", "model": "opus", "effort": "high" },
  "codex":  { "bin": "codex",  "model": "",     "effort": "high" },
  "updates": { "check": true, "interval": "24h" },
  "defaults": {
    "agent": "claude",                          // default AI: claude | codex
    "on_new": "ask",                            // ask | auto | silent | off
    "language": { "brief": "en", "comments": "en" },
    "urgent": { "labels": ["urgent", "hotfix"], "older_than": "2d" }
  },
  "repos": {
    "gitlab.example.com/team/app": { "path": "~/Projects/app", "mode": "open", "language": { "comments": "de" } },
    "github.com/acme/widgets":     "~/Projects/widgets"
  }
}
```

Full example: [`config.example.json`](config.example.json).

## How a review works

1. **Method.** If the repo has `.claude/skills/review` (or `code-review`, `commands/review.md`, `*review*`),
   that skill is used. Otherwise `review.fallback`: `builtin` (the built-in prompt), `claude-code-review`
   (`/code-review`) or `none`.
2. **Claude Code** runs as `claude -p --permission-mode dontAsk` in the repo with a `PreToolUse` hook
   (`juhas guard`). The hook parses every shell command (chains, pipes, `$(…)`, `sh -c`) and blocks writes:
   comments, approvals and merges via gh/glab, non-GET `api` calls, GraphQL mutations,
   `git push/commit/reset`, branch switches outside the review worktree, uploads with curl, package
   publishing. Edit and Write are disabled.
3. **Codex**: Juhas itself fetches the description with the discussion, the diff and the CI status, and checks out
   a detached worktree at the head SHA. `codex exec --sandbox read-only` then works **without network and
   without write access**, enforced by the OS sandbox rather than a deny-list. A repo skill is passed in as the
   procedure to follow.
4. Progress goes to a run log (Panel / menu → *Live view*); the brief lands in `briefs_dir`.
5. **Human in the loop.** The agent ends with a proposal: a verdict, a general comment and `file:line` comments.
   On the proposal page you accept, edit or reject each one; *Publish accepted* posts them from your gh/glab
   account, as diff discussions on GitLab and as a review on GitHub. A line outside the diff goes into the
   general comment with its `file:line`. If new commits arrived since the review, Juhas warns you first.
   **The agent itself never publishes anything.**

Worth knowing:

- Claude's protection is a deny-list in a hook, not a sandbox. It catches the usual write paths, but a repo
  skill does get Bash (that is how its worktree, `pnpm install` or prechecks work).
- PR titles, descriptions and comments are written by other people. The prompt treats them as data, but they
  are still third-party input (prompt injection) — which is why Codex has no network and Claude has no WebFetch.
- You are sending code to Anthropic or OpenAI. Make sure that is allowed where you work.

## Updates

The tray checks [GitHub releases](https://github.com/piciuok/juhas/releases) once a day (`updates.interval`).
When a newer version is out you get a notification and an *⬆ Update to vX.Y.Z* item at the top of the menu
and in the Panel. Installing downloads the archive for your OS/arch, verifies it against `checksums.txt`,
swaps the binary atomically and restarts Juhas in place. It waits until no review is running.

```jsonc
"updates": {
  "check": true,         // look for new releases
  "interval": "24h",
  "prerelease": false,   // also offer -rc / -beta tags
  "auto_install": false  // install on its own when idle
}
```

From a shell: `juhas update --check` (report only) or `juhas update`. Builds that are not a release tag
(e.g. `make build` from a checkout) are never replaced automatically; `juhas update --force` swaps one for
the latest release. `JUHAS_UPDATE_API` points the check at a mirror of the GitHub API.

## Commands

```
juhas [tray]                          tray icon (default)
juhas poll [--dry-run] [--json]       check the queue once
juhas review [--agent codex] <url>    review one PR/MR with live progress in the terminal
juhas doctor [--with-ai]              diagnostics (--with-ai: a tiny real call to claude and codex)
juhas update [--check] [--force]      update to the latest release
juhas config init|scan|show|ui|path
juhas install | uninstall | version
```

State and logs live in `~/.local/state/juhas/` (`poll.log`, `state.json`, `jobs/*.log`).

## Q&A

**Does the AI post anything to my PR/MR?**
No. Agents run read-only (a guard hook and disabled edit tools for Claude, an OS sandbox without network for Codex).
Only you publish, from the proposal page, and only the comments you accepted.

**Which agent should I pick?**
Both work. Claude Code can run a repo's review skill with its own tooling; Codex has the stricter sandbox.
Pick a default in Settings and switch per PR/MR in the "Run AI review?" dialog.

**Our team never assigns reviewers on GitLab, so nothing shows up.**
Set `"mode": "open"` for that repo: you will see every open MR except yours, the bots' and those you already approved.

**Can the brief be in my language and the comments in English?**
Yes: `language.brief` and `language.comments`, globally or per repo.

**The tray icon is missing on GNOME.**
Install and enable the AppIndicator extension; `juhas doctor` checks for a StatusNotifier host.

**What does it cost?**
Juhas is free. Reviews use your Claude or Codex subscription or API usage, and nothing runs without your click
unless you set `on_new: "auto"`.

**Bitbucket? Windows?**
Not yet — see below.

## Contributing

Pull requests are very welcome, especially ones that **extend what Juhas can do**:

- more forges (Bitbucket, Gitea/Forgejo, Azure DevOps),
- more agents (anything with a headless, read-only mode),
- Windows tray support,
- smarter urgency rules, daily digests, better Linux desktop integration.

For bigger changes, open an issue first so we can agree on the shape. Keep agents read-only: publishing
always goes through the user's explicit acceptance. Before sending a PR, run `make vet test`.
