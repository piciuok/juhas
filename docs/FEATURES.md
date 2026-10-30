# Juhas features

Everything Juhas does, grouped by area. See the [README](../README.md) for installation and the
configuration reference.

## Watching PR/MRs

- **GitHub and GitLab**, including GitHub Enterprise and self-hosted GitLab, through the logins of `gh`
  and `glab`. Juhas never stores tokens.
- **Two modes per repo:**
  - `requested`: PR/MRs where your review was requested (on GitHub, team requests too) or you are the assignee.
  - `open`: every open PR/MR of the repo, minus yours, those you already approved, and bots (`ignore_authors`, regexes supported).
- **📌 Pinned repos** (`pinned: true`, or *📌 Pin a project…* in the menu and the Panel): the repo always shows,
  with all its open PR/MRs, even when nobody asks you to review there (e.g. a team that never assigns reviewers).
  PR/MRs you are asked about still show as usual. The others are folded under *Show all*, or listed right in the
  menu with `show_all: inline` (toggle ↕ per repo). The Panel finds projects to pin among the config, the scanned
  clones and the projects you are a member of, or takes a pasted project/MR URL, and can review a PR/MR from the
  full list although nobody asked you.
- **Ready first:** the main list, the icon and the "to review" count hold what waits for you and is not a draft.
  Drafts, yours too, are folded under *Show all* (under *📝 Show drafts* in repos listed inline).
- **Steady refresh:** a check that changes nothing leaves an open menu open, and the Panel redraws a part of the
  page only when its own content changed.
- **Filters:** drafts hidden by default (`include_drafts`); `on_new: off` hides a repo entirely.
- **Polling** every `poll_interval` (3 min by default, at least 30 s) plus *Check now*. Lists are paginated up
  to 1000 PR/MRs, and one failing call does not drop a whole host.
- **A quiet first poll:** PR/MRs that are already open when you install Juhas are recorded silently, so it
  does not ask about all of them at once. If that poll fails (e.g. no network at login), the next one starts over.
- **Per-repo counters:** open PR/MRs, what waits for you, and changed files as `+new ~modified −deleted`.
- **Working hours** (`working_hours`, e.g. 08:00–16:00 Mon–Fri): outside them Juhas does not check on its own and
  nothing calls for you — no questions, notifications, kicks or sparks; the icon is a magnifier asleep in bed.
  *Check now* still refreshes the list, *☀️ Work now* (menu, Panel) wakes it for an hour, and what came in
  overnight is asked about when the hours start.
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
  to the button. The accepted comments go with the decision (*✓ Approve with 2 accepted comments*): first
  the comments, then the decision, which is not sent if a comment fails to post. GitHub sends it as the review event; GitLab approves the reviewed commit (refused if the head
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

## Chat about the review

- **A chat panel on the right of the proposal page** (also Panel → *💬 Chat*, and for reviews with no
  proposed comments). Ask why something is a blocker, whether a test covers it, what a fix would look like.
- **Continues the review's own session**, so the agent remembers the diff and the code it read: Claude forks
  it (`--fork-session`, the review session stays as it was), Codex resumes its thread in the kept worktree.
  Without a saved session (older reviews) it starts a new one seeded with the brief and your decisions on the
  proposal. After a new review, the chat continues from that one.
- **Read-only like the review:** the same guard hook (Claude) or OS sandbox (Codex), at most
  `chat.max_turns` steps per question, *⏹ Stop* any time, *↺ New* to start over.
- **Suggested comments** come as cards with *➕ Add to the proposal*: they land there as pending, for you to
  accept and publish. Nothing goes out from the chat itself.
- **Local:** the conversation is stored in `~/.local/state/juhas/chats/`.

## Worktrees

- **One worktree per PR/MR** (in the user cache folder), used by Codex reviews and the chat. A new review moves
  it to the new head.
- **Cleaned up on their own:** when the PR/MR is merged or closed (checked when it leaves the lists, at most
  hourly), or after `worktrees.keep_for` without use (a week by default).
- **Panel → 🌳 Worktrees** lists them with size and last use; *🗑 Remove* deletes one (the chat puts it back
  when needed).

## Briefs and logs

- **Markdown briefs** in `briefs_dir`, with a header (PR/MR, commit, agent, method) and the proposal.
- **A live run log** per review (menu → *Live view*, or the Panel), following new output as it arrives.
- **Stale briefs** are marked after new commits (⟳), with a recheck question.

## Tray menu and Panel

- **Tray menu** (macOS menu bar, Linux StatusNotifier tray):
  - PR/MRs grouped by repo, with status icons (⏳ running, 📝 reviewed, ⟳ stale, ❌ failed, 🔕 skipped) and the
    author with a colour badge derived from the username (also in the Panel);
  - *Show all PR/MRs (n)* at the end of each repo: every open PR/MR of the repo, the ones waiting for you
    marked with •, and *📌 Pin* / *↕ List the others right in the menu* for the repo;
  - *📊 Limits used*: one line with the Claude and Codex 5-hour and weekly usage (reset times on hover).
    Claude's comes from its reviews, Codex's from its own session files;
  - per-item actions: review with Claude or Codex, cancel, skip, open the brief, open the PR/MR;
  - *Check now*, Diagnostics, Settings, the briefs folder, Start at login, the on-screen kick toggle, and
    *Check for updates*.
- **Panel:** a browser page with the same queue that stays open while you click. A tab per project counts
  what waits for you (📝 decisions, ⚡ urgent); History, Worktrees and Pin a project have their own tabs.
  Every row has the same columns in any state: the state and whose move it is (yours, the agent's, the
  author's), the title and details, then one main action (Decide, Review, Retry, Live view…) and a ⋯ menu with
  the rest. Rows needing you come first. Start at login and the kick preview are in Settings → General.
  Its extra actions:
  - acknowledge urgent items;
  - map a clone for a repo without one;
  - un-skip;
  - pin a project (search your projects or paste a URL), fold or list a repo's other PR/MRs, and review one
    of them although nobody asked you;
  - *☀️ Work now* outside working hours;
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
  *🔍 Check for updates* answers in a dialog, so the result shows even when notifications are muted.
- **One-click install:**
  1. downloads the archive for your OS and architecture and verifies its SHA-256;
  2. swaps the binary atomically (fsynced);
  3. starts the new version as a fresh process (through launchd under the LaunchAgent), which then confirms
     the update in a dialog.

  First the install is pending: queued reviews do not start, the running ones finish (and open questions get
  an answer), then it installs and restarts — and the queue carries over and resumes in the new version.
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
- **A one-line installer** that verifies the release checksum, offers to install git, gh, glab, Claude Code
  and/or Codex and the desktop helpers (each with what it is for), runs the logins, creates a config from your
  clones and enables start at login (a LaunchAgent on macOS, an XDG autostart entry on Linux).

## Command line

| Command | What it does |
|---|---|
| `juhas` / `juhas tray [--foreground]` | the tray icon (default); from a terminal it starts in the background and gives the shell back |
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
