#!/bin/sh
# Installs the latest Juhas release into ~/.local/bin and enables start at
# login:
#   curl -fsSL https://raw.githubusercontent.com/piciuok/juhas/main/scripts/install.sh | sh
# Options (environment): JUHAS_VERSION=v0.2.0 to pin a version,
# JUHAS_NO_START=1 to only put the binary in place.
set -eu

repo="piciuok/juhas"
bindir="${HOME}/.local/bin"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in darwin|linux) ;; *) echo "juhas: unsupported OS: $os" >&2; exit 1;; esac
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "juhas: unsupported architecture: $arch" >&2; exit 1 ;;
esac

tag="${JUHAS_VERSION:-}"
if [ -z "$tag" ]; then
  tag=$(curl -fsSL "https://api.github.com/repos/$repo/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)
fi
[ -n "$tag" ] || { echo "juhas: could not find the latest release" >&2; exit 1; }

ver="${tag#v}"
name="juhas_${ver}_${os}_${arch}.tar.gz"
base="https://github.com/$repo/releases/download/$tag"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading Juhas $tag ($os/$arch)…"
curl -fsSL -o "$tmp/$name" "$base/$name"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt"

want=$(grep " $name\$" "$tmp/checksums.txt" | awk '{print $1}')
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$tmp/$name" | awk '{print $1}')
else
  got=$(shasum -a 256 "$tmp/$name" | awk '{print $1}')
fi
[ -n "$want" ] && [ "$want" = "$got" ] || { echo "juhas: checksum mismatch — aborting" >&2; exit 1; }

tar -C "$tmp" -xzf "$tmp/$name"
mkdir -p "$bindir"
install -m 0755 "$tmp/${name%.tar.gz}/juhas" "$bindir/juhas.new"
mv "$bindir/juhas.new" "$bindir/juhas"
echo "Installed $bindir/juhas ($("$bindir/juhas" version))"

if [ -z "${JUHAS_NO_START:-}" ]; then
  [ -f "${XDG_CONFIG_HOME:-$HOME/.config}/juhas/config.json" ] || "$bindir/juhas" config init || true
  "$bindir/juhas" install
  "$bindir/juhas" doctor || true
fi
case ":$PATH:" in *":$bindir:"*) ;; *) echo "Add $bindir to your PATH to run juhas from a shell." ;; esac
