# Juhas features

Everything Juhas does, grouped by area. See the [README](../README.md) for installation and the
configuration reference.

## Watching PR/MRs

- **GitHub and GitLab**, including GitHub Enterprise and self-hosted GitLab, through the logins of `gh`
  and `glab`. Juhas never stores tokens.
- **Two modes per repo:**
  - `requested`: PR/MRs where your review was requested (on GitHub, team requests too) or you are the assignee.
  - `open`: every open PR/MR of the repo, minus yours, those you already approved, and bots (`ignore_authors`, regexes supported).
- **Filters:** drafts hidden by default (`include_drafts`); `on_new: off` hides a repo entirely.
- **Polling** every `poll_interval` (3 min by default, at least 30 s) plus *Check now*. Lists are paginated up
  to 1000 PR/MRs, and one failing call does not drop a whole host.
- **A quiet first poll:** PR/MRs that are already open when you install Juhas are recorded silently, so it
  does not ask about all of them at once. If that poll fails (e.g. no network at login), the next one starts over.
- **Per-repo counters:** open PR/MRs, what waits for you, and changed files as `+new ~modified −deleted`.
- **New commits** on a PR/MR that was reviewed, skipped or postponed bring it back with a "changed" question.

## Several accounts

- **Several GitHub accounts per host** (work and personal side by side). Each one acts as itself, whichever
  account is active in `gh`.
- **Several GitLab accounts per host**, each with its own glab config directory (`config_dir`).
- **Settings → Accounts** lists every login `gh`/`glab` has, with a **Watch** checkbox for whose review
  requests to check.
- **The asking account does the work:** a PR/MR is fetched, reviewed (the agent's own gh/glab calls) and
  commented on by the account it was found with. Publishing refuses when that account is no longer
  configured, instead of posting under another name.
- **PR/MRs authored by any of your accounts** count as yours and are never queued.
- **Per-repo `account`** picks the login that reads a repo in `open` mode.

## Urgent items

- **Rules** (globally or per repo): `labels`, `older_than` (e.g. `2d`), `target_branches` (globs such as `release/*`),
  for PR/MRs assigned to you or requesting your review.
- **A sparking lightning-bolt icon** until you acknowledge, with optional flashing (`flash`, `flash_interval`).
- **A "kick" for each new urgent item:** an on-screen electric discharge on macOS (`screen_effect`, toggle in
  the menu; `juhas zap` to preview it) and a loud notification.

## Asking and running reviews

- **"Run AI review?"** for each new PR/MR, with buttons for **Claude** or **Codex**, **Later** or **Skip**.
  Several new ones are batched into one question.
  - *Later* asks again after new commits.
  - *Skip* stays quiet until the head changes, or until a manual *Check now*.
- **`on_new` per repo:**
  - `ask`;
  - `auto` (start right away, capped by `auto_max_per_poll`);
  - `silent` (menu only);
  - `off`.
- **A queue** with `concurrency` parallel reviews. You can cancel queued or running reviews, and the live
  log shows your place in line.
- **Repo review skills:** `.claude/skills/review`, `code-review`, `commands/review.md` or any `*review*`
  skill is used when present (`review.skill: auto`, a name, or `none`).
- **Fallbacks** when a repo has no skill: `builtin` (a built-in prompt), `claude-code-review` (`/code-review`)
  or `none`.
- **Per-repo agent options:** model, effort, max turns, timeout, extra allowed or disallowed tools.
- **Languages:** the brief and the comments can be in different languages (`language.brief`,
  `language.comments`).
- **Codex runs:** Juhas pre-fetches the description, discussion, diff and CI status, and checks out a
  detached worktree at the reviewed commit. The worktree is cleaned up afterwards, also after a timeout.

## Read-only guard

- **`review.guard: read-only` (default).** A `PreToolUse` hook checks every Bash command and MCP tool call of a
  Claude review:
  - **Bash commands** are parsed (chains, pipes, `$(…)`, `sh -c`, `eval`, wrappers such as `xargs`, `env`,
    `nice` or `find -exec`, git aliases, and scripts written to temp dirs). Blocked are:
    - comments, approvals and merges via gh/glab, non-GET API calls and GraphQL mutations;
    - `git push`, `commit`, `send-pack` and other writes to history, plus branch switches outside the review worktree;
    - uploads with curl or wget, package publishing, printing tokens;
    - writes to files in your clone (writing to `/tmp` and to review worktrees is fine);
    - command names built at run time.
  - **MCP tools:** reading ones (`get…`, `list…`, `search…`, `fetch…`) work, so a review can read the Jira
    ticket or the docs. Ones that create, edit, comment, transition or delete are blocked.
- **Fails closed:** if the guard binary is missing or crashes, the tool call is denied, and a read-only
  review does not start without the guard.
- **`review.guard_allow`** for exceptions: an MCP tool, a `prefix*`, or `Bash(git push*)`.
- **`review.guard: off`** (globally or per repo) lifts all restrictions for those who want the agent to act
  fully. Codex then gets `danger-full-access` instead of its read-only sandbox.
- **Codex** in read-only mode runs in its OS sandbox, without network or write access.

## Human in the loop: proposals

- **The agent proposes, you decide.** Each review ends with a verdict, a general comment and `file:line`
  comments.
- **A proposal page** where you accept, edit or reject each comment. Edits save automatically, and
  *Accept all* and *Reject all* work in bulk.
- **File preview next to each comment:** the file's diff in the PR/MR, or the whole file as the review saw it,
  with the commented line highlighted. You never leave the page.
- **Publishing posts only the accepted comments**, from your account:
  - GitHub: one review anchored to the reviewed commit. If GitHub rejects a line, that comment is folded into
    the general comment.
  - GitLab: diff discussions on the reviewed MR version, including unchanged context lines. A line outside
    the diff becomes a general note with its `file:line`.
- **Approve or request changes** with the publish: pick *Comment only*, *Approve* or *Request changes* next
  to the button. GitHub sends it as the review event; GitLab approves the reviewed commit (refused if the head
  moved) and marks "changes requested" (17.2+). A change request needs a general comment that says why.
- **Verdict rules** (`review.verdict_rules`, editable in Settings): how the agent picks approve / request
  changes / needs discussion. By default: any BLOCK → request changes, only WARN/INFO → approve.
  `review.publish_event: from_verdict` preselects that verdict on the page; the default `comment` does not.
- **Safe publishing:**
  - Juhas warns you when new commits arrived after the review; a second click within 5 s publishes anyway.
  - A second click or a second tab cannot publish twice.
  - A timeout or server error is reported rather than retried, so nothing is posted twice.
  - Empty comments are refused.
- **A blinking ❓ next to the icon** and a Panel badge remind you of proposals waiting for a decision.

## Briefs and logs

- **Markdown briefs** in `briefs_dir`, with a header (PR/MR, commit, agent, method) and the proposal.
- **A live run log** per review (menu → *Live view*, or the Panel), following new output as it arrives.
- **Stale briefs** are marked after new commits (⟳), with a recheck question.

## Tray menu and Panel

- **Tray menu** (macOS menu bar, Linux StatusNotifier tray):
  - PR/MRs grouped by repo, with status icons (⏳ running, 📝 reviewed, ⟳ stale, ❌ failed, 🔕 skipped);
  - per-item actions: review with Claude or Codex, cancel, skip, open the brief, open the PR/MR;
  - *Check now*, Diagnostics, Settings, the briefs folder, Start at login, the on-screen kick toggle, and
    *Check for updates*.
- **Panel:** a browser page with the same queue that stays open while you click. Its extra actions:
  - acknowledge urgent items;
  - map a clone for a repo without one;
  - un-skip;
  - preview the kick;
  - update.

## Settings

- **A local web form** (127.0.0.1 with a one-time token) over a plain JSON file at `~/.config/juhas/config.json`.
- **Layered settings:** built-in → `defaults` → `repos[key]`. `juhas config show <repo>` prints the result.
- **A folder picker**, *Add from scan*, and **auto-mapping** (`auto_map`) of clones found in `scan_dirs`
  (their `origin` decides the repo), including clones that moved.
- **Validation** before saving: unknown fields, bad durations, duplicate or conflicting accounts, and a
  minimum poll interval.
- **No lost edits:** saving refuses when Juhas changed the file meanwhile (e.g. by auto-mapping), and the
  tray's own changes are made under a write lock. The previous file is kept as `.bak`.

## Diagnostics

- **`juhas doctor`** (also in the menu, the Panel and Settings) checks:
  - git, gh, glab, Claude Code and Codex: present, versions, logins;
  - each configured account, and logins that are not used;
  - token scopes;
  - repo paths and their `origin`;
  - desktop integration on Linux (tray host, notifications).

  Each problem comes with a command to copy. The checks run in the same environment as the tray (PATH, Keychain).
- **Mode hints:** repos where nobody assigns reviewers are pointed out for `mode: open`.

## Updates

- **Daily checks** of GitHub releases (`updates.interval`), with a notification and an *⬆ Update* menu item.
- **One-click install:**
  1. downloads the archive for your OS and architecture and verifies its SHA-256;
  2. swaps the binary atomically (fsynced);
  3. restarts Juhas in place.

  First it pauses new reviews and waits for running ones and open questions.
- **Options:** `prerelease` (semver ordering: rc.9 < rc.10) and `auto_install`. `juhas update [--check] [--pre] [--force]` does the same from a shell.
- **Builds that are not release tags** are never replaced automatically. A symlinked binary (e.g. from a
  package manager) is not overwritten either.

## Reliability

- **Restarts and crashes:** a review whose Juhas process is gone is marked failed, and the agent left behind
  is stopped. A review owned by another live Juhas process is left alone.
- **Clean shutdown** on logout or SIGTERM. Ctrl-C on `juhas review` stops the agent too.
- **One tray per user.** `juhas poll` refuses while the tray runs, and two processes never review the same
  PR/MR at once.
- **State and config** are written atomically under file locks.

## Platforms and install

- **macOS** (arm64, amd64) and **Linux** (amd64, arm64: KDE, XFCE, Cinnamon; GNOME with AppIndicator).
- **Native dialogs and notifications:**
  - macOS: `osascript`, optionally `alerter` and `terminal-notifier`;
  - Linux: `notify-send` actions, `zenity` or `kdialog`.
- **A one-line installer** that verifies the release checksum, creates a config from your clones and enables
  start at login (a LaunchAgent on macOS, an XDG autostart entry on Linux).

## Command line

| Command | What it does |
|---|---|
| `juhas` / `juhas tray` | the tray icon (default) |
| `juhas panel` | open the Panel of the running tray |
| `juhas poll [--dry-run]` | check the queue once |
| `juhas review [--agent claude\|codex] <url>` | review one PR/MR in the terminal, with live output |
| `juhas doctor [--with-ai]` | diagnostics (optionally a short real agent call) |
| `juhas config ui \| init \| scan [--write] \| show [<repo>] \| path` | settings |
| `juhas update [--check] [--pre] [--force]` | self-update |
| `juhas install \| uninstall` | start at login |
| `juhas guard '<command>'` | test what the read-only guard would do with a command |
| `juhas zap` | preview the on-screen kick |
| `juhas version` | the version |
