#!/bin/sh
# Installs Juhas and everything it needs, then enables start at login:
#   curl -fsSL https://raw.githubusercontent.com/piciuok/juhas/main/scripts/install.sh | sh
#
# Steps: the juhas binary → git, gh, glab → Claude Code and/or Codex →
# desktop helpers → logins → config from your clones → start at login → doctor.
# Missing CLIs come from Homebrew when it is there, otherwise their official
# release archives go to ~/.local/bin (no sudo). Questions are read from the
# terminal even when the script is piped into sh.
#
# Options (environment):
#   JUHAS_VERSION=v0.3.1   pin a Juhas version
#   JUHAS_AI=claude|codex|both|none   which AI CLI to install (default: ask; claude)
#   JUHAS_YES=1            answer every question with its default (no logins)
#   JUHAS_NO_DEPS=1        do not install gh, glab, the AI CLI or desktop helpers
#   JUHAS_NO_START=1       only put the binary in place
set -eu

repo="piciuok/juhas"
bindir="${HOME}/.local/bin"
user_path="$PATH"
export PATH="$bindir:$PATH"
[ -d /opt/homebrew/bin ] && PATH="/opt/homebrew/bin:$PATH"

say() { printf '\n==> %s\n' "$*"; }
warn() { printf 'juhas: %s\n' "$*" >&2; }
die() { warn "$*"; exit 1; }
has() { command -v "$1" >/dev/null 2>&1; }

# a terminal to ask on: stdin is the script itself when piped into sh
tty=""
if [ -z "${JUHAS_YES:-}" ] && (exec </dev/tty) 2>/dev/null; then tty=/dev/tty; fi
# ask "question" default(y|n): returns 0 for yes
ask() {
  if [ -z "$tty" ]; then [ "$2" = y ]; return; fi
  hint="[y/N]"; [ "$2" = y ] && hint="[Y/n]"
  printf '%s %s ' "$1" "$hint" >"$tty"
  read -r ans <"$tty" || ans=""
  case "${ans:-$2}" in [yY]*) return 0 ;; *) return 1 ;; esac
}

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in darwin|linux) ;; *) die "unsupported OS: $os" ;; esac
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) die "unsupported architecture: $arch" ;;
esac
has curl || die "curl is needed"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

sha256() {
  if has sha256sum; then sha256sum "$1" | awk '{print $1}'; else shasum -a 256 "$1" | awk '{print $1}'; fi
}
# verify file checksums.txt: the line for the file's name must match
verify() {
  want=$(grep " \*\{0,1\}$(basename "$1")\$" "$2" | awk '{print $1}' | head -n1)
  [ -n "$want" ] && [ "$want" = "$(sha256 "$1")" ] || die "checksum mismatch for $(basename "$1") — aborting"
}
# put dir/name (a file inside an unpacked archive) into bindir as bin
place() {
  mkdir -p "$bindir"
  install -m 0755 "$1" "$bindir/$2.new" && mv "$bindir/$2.new" "$bindir/$2"
}
gh_latest() { # owner/repo → tag of the latest release
  curl -fsSL "https://api.github.com/repos/$1/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1
}

# ---------------------------------------------------------------- juhas
tag="${JUHAS_VERSION:-}"
[ -n "$tag" ] || tag=$(gh_latest "$repo")
[ -n "$tag" ] || die "could not find the latest release"
ver="${tag#v}"
name="juhas_${ver}_${os}_${arch}.tar.gz"
base="https://github.com/$repo/releases/download/$tag"

say "Juhas $tag ($os/$arch)"
curl -fsSL -o "$tmp/$name" "$base/$name"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt"
verify "$tmp/$name" "$tmp/checksums.txt"
tar -C "$tmp" -xzf "$tmp/$name"
place "$tmp/${name%.tar.gz}/juhas" juhas
echo "Installed $bindir/juhas ($("$bindir/juhas" version))"

# ---------------------------------------------------------------- dependencies
brew=""
[ "$os" = darwin ] && has brew && brew=brew

install_gh() {
  if [ -n "$brew" ]; then brew install gh; return; fi
  t=$(gh_latest cli/cli); v="${t#v}"
  case "$os" in darwin) f="gh_${v}_macOS_${arch}.zip" ;; *) f="gh_${v}_linux_${arch}.tar.gz" ;; esac
  u="https://github.com/cli/cli/releases/download/$t"
  curl -fsSL -o "$tmp/$f" "$u/$f" || return 1
  curl -fsSL -o "$tmp/gh.sums" "$u/gh_${v}_checksums.txt" || return 1
  verify "$tmp/$f" "$tmp/gh.sums" || return 1
  mkdir -p "$tmp/gh" || return 1
  case "$f" in *.zip) (cd "$tmp/gh" && unzip -q "../$f") ;; *) tar -C "$tmp/gh" -xzf "$tmp/$f" ;; esac || return 1
  d="${f%.zip}"; d="${d%.tar.gz}"
  place "$tmp/gh/$d/bin/gh" gh || return 1
}

install_glab() {
  if [ -n "$brew" ]; then brew install glab; return; fi
  api="https://gitlab.com/api/v4/projects/gitlab-org%2Fcli"
  t=$(curl -fsSL "$api/releases/permalink/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)
  [ -n "$t" ] || { warn "could not find the latest glab release"; return 1; }
  v="${t#v}"; f="glab_${v}_${os}_${arch}.tar.gz"
  u="$api/packages/generic/glab/$v"
  curl -fsSL -o "$tmp/$f" "$u/$f" || return 1
  curl -fsSL -o "$tmp/glab.sums" "$u/checksums.txt" || return 1
  verify "$tmp/$f" "$tmp/glab.sums" || return 1
  mkdir -p "$tmp/glab" && tar -C "$tmp/glab" -xzf "$tmp/$f" || return 1
  place "$tmp/glab/bin/glab" glab || return 1
}

install_claude() {
  curl -fsSL https://claude.ai/install.sh | bash || return 1
}

install_codex() {
  if [ -n "$brew" ]; then brew install codex; return; fi
  t=$(gh_latest openai/codex)
  case "$arch" in amd64) a=x86_64 ;; arm64) a=aarch64 ;; esac
  case "$os" in darwin) target="$a-apple-darwin" ;; *) target="$a-unknown-linux-musl" ;; esac
  f="codex-$target.tar.gz"
  curl -fsSL -o "$tmp/$f" "https://github.com/openai/codex/releases/download/$t/$f" || return 1
  mkdir -p "$tmp/codex" && tar -C "$tmp/codex" -xzf "$tmp/$f" || return 1
  place "$tmp/codex/codex-$target" codex || return 1
}

# offer bin "why it is needed" install_fn [default y|n]: says what it is
# for and asks before installing (nothing is installed without a yes)
offer() {
  if has "$1"; then echo "✓ $1 ($(command -v "$1"))"; return; fi
  printf '\n• %s — %s\n' "$1" "$2"
  if ask "  Install $1?" "${4:-y}"; then
    "$3" || warn "installing $1 failed — \`juhas doctor\` shows other ways"
  else
    echo "  skipped $1"
  fi
}

# install packages with the system package manager (asks for sudo)
syspkg() {
  if has apt-get; then sudo apt-get install -y "$@"
  elif has dnf; then sudo dnf install -y "$@"
  elif has pacman; then sudo pacman -S --needed --noconfirm "$@"
  elif has zypper; then sudo zypper install -y "$@"
  else return 1
  fi
}
install_git() {
  if [ "$os" = darwin ]; then xcode-select --install; warn "finish the Xcode tools install, then run this script again"; return 1; fi
  syspkg git
}
install_notify() {
  if [ "$os" = darwin ]; then brew install terminal-notifier
  elif has apt-get; then syspkg libnotify-bin zenity
  else syspkg libnotify zenity
  fi
}

if [ -z "${JUHAS_NO_DEPS:-}" ] && [ -z "${JUHAS_NO_START:-}" ]; then
  say "Dependencies"
  where="Homebrew"; [ -n "$brew" ] || where="its release, into $bindir"
  offer git "required: Juhas reads diffs and remotes of your clones (sudo / Xcode tools)" install_git
  echo "Juhas needs the CLI of each forge you review on (gh, glab or both) — from $where:"
  offer gh "GitHub CLI: finds the pull requests waiting for you and publishes the comments you accept. Skip it if you do not use GitHub." install_gh
  offer glab "GitLab CLI: the same for merge requests on gitlab.com or your own GitLab. Skip it if you do not use GitLab." install_glab

  ai="${JUHAS_AI:-}"
  if [ -z "$ai" ]; then
    if has claude || has codex; then ai=keep
    elif [ -n "$tty" ]; then
      printf '\n• AI CLI — runs the read-only review in your clone and proposes comments; you need at least one.\n' >"$tty"
      printf '  claude = Claude Code (Anthropic account), codex = Codex (OpenAI account)\n' >"$tty"
      printf '  Install which? [claude/codex/both/none] (claude) ' >"$tty"
      read -r ai <"$tty" || ai=""
      ai="${ai:-claude}"
    else ai=claude
    fi
  fi
  case "$ai" in
    claude|both) has claude && echo "✓ claude" || { say "Installing Claude Code (claude.ai/install.sh)"; install_claude || warn "installing Claude Code failed"; } ;;
  esac
  case "$ai" in
    codex|both) has codex && echo "✓ codex" || { say "Installing Codex"; install_codex || warn "installing Codex failed"; } ;;
  esac
  case "$ai" in
    keep) for b in claude codex; do has "$b" && echo "✓ $b ($(command -v "$b"))"; done ;;
    claude|codex|both|none) ;;
    *) warn "JUHAS_AI must be claude, codex, both or none (got: $ai)" ;;
  esac

  # optional desktop helpers
  if [ "$os" = darwin ]; then
    [ -n "$brew" ] && offer terminal-notifier "optional: notifications you can click to open the brief or the PR (without it they only show text)" install_notify
  elif ! has notify-send && ! has zenity && ! has kdialog; then
    printf '\n• notify-send + zenity — optional: notifications and the "Review it?" question with buttons; without them only the tray menu shows new PR/MRs (sudo)\n'
    if ask "  Install them?" y; then install_notify || warn "install libnotify and zenity with your package manager"; else echo "  skipped"; fi
  fi
fi

# ---------------------------------------------------------------- logins, config, start
if [ -z "${JUHAS_NO_START:-}" ]; then
  if [ -n "$tty" ]; then
    say "Logins — each CLI keeps its own login; Juhas never sees your tokens. Skip the ones you do not use."
    if has gh && ! gh auth status >/dev/null 2>&1 && ask "Log in to GitHub (gh auth login)?" y; then
      gh auth login </dev/tty >/dev/tty 2>&1 || warn "gh login failed — run: gh auth login"
    fi
    if has glab && ! glab auth status >/dev/null 2>&1 && ask "Log in to GitLab (glab auth login)?" y; then
      glab auth login </dev/tty >/dev/tty 2>&1 || warn "glab login failed — run: glab auth login --hostname <host>"
    fi
    if has claude && ! claude auth status >/dev/null 2>&1 && ask "Log in to Claude (claude auth login)?" y; then
      claude auth login </dev/tty >/dev/tty 2>&1 || warn "Claude login failed — run \`claude\` and log in"
    fi
    if has codex && ! codex login status >/dev/null 2>&1 && ask "Log in to Codex (codex login)?" y; then
      codex login </dev/tty >/dev/tty 2>&1 || warn "Codex login failed — run: codex login"
    fi
  fi

  say "Config and start at login"
  [ -f "${XDG_CONFIG_HOME:-$HOME/.config}/juhas/config.json" ] || "$bindir/juhas" config init || true
  "$bindir/juhas" install
  say "Diagnostics"
  "$bindir/juhas" doctor || true
fi
case ":$user_path:" in *":$bindir:"*) ;; *) echo; echo "Add $bindir to your PATH to run juhas, gh, glab, claude or codex from a shell." ;; esac
