#!/bin/sh
# Install irgo-winvm from a GitHub release, checked against its SHA256SUMS.
#
#   curl -fsSL https://raw.githubusercontent.com/joeblew999/irgo-windows-vm/main/install.sh | sh
#
# Settings, all optional, from the environment:
#   IRGO_WINVM_VERSION  a tag such as v0.5.0 (default: the latest release)
#   IRGO_WINVM_DIR      where to put it (default: ~/.local/bin)
#   IRGO_WINVM_BASE     where the release files are (default: GitHub; the
#                       snapshot test points it at a local dist/)
#
# It refuses anything but macOS, and a binary whose SHA-256 is not the one
# SHA256SUMS names. Nothing is run as root.
set -eu

repo=joeblew999/irgo-windows-vm
version=${IRGO_WINVM_VERSION:-latest}
dir=${IRGO_WINVM_DIR:-$HOME/.local/bin}

say() { printf 'irgo-winvm install: %s\n' "$*"; }
die() { printf 'irgo-winvm install: %s\n' "$*" >&2; exit 1; }

[ "$(uname -s)" = Darwin ] || die "macOS only: it drives UTM, which runs only on a Mac (this is $(uname -s))"
case "$(uname -m)" in
  arm64) arch=arm64 ;;
  x86_64) arch=amd64 ;;
  *) die "no build for $(uname -m)" ;;
esac
asset=irgo-winvm-darwin-$arch

if [ -n "${IRGO_WINVM_BASE:-}" ]; then
  base=$IRGO_WINVM_BASE
elif [ "$version" = latest ]; then
  base=https://github.com/$repo/releases/latest/download
else
  base=https://github.com/$repo/releases/download/$version
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

say "downloading $asset and SHA256SUMS from $base"
curl -fsSL -o "$tmp/$asset" "$base/$asset" || die "could not download $base/$asset"
curl -fsSL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS" || die "could not download $base/SHA256SUMS"

want=$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1 }' "$tmp/SHA256SUMS")
[ -n "$want" ] || die "SHA256SUMS names no $asset"
got=$(shasum -a 256 "$tmp/$asset" | awk '{ print $1 }')
[ "$got" = "$want" ] || die "$asset has SHA-256 $got, and SHA256SUMS says $want: not installing it"
say "SHA-256 verified: $got"

chmod +x "$tmp/$asset"
# curl sets no quarantine flag, but a copy fetched by a browser into the same
# place would have one, and Gatekeeper then refuses the unsigned binary.
xattr -d com.apple.quarantine "$tmp/$asset" 2>/dev/null || true

mkdir -p "$dir"
mv -f "$tmp/$asset" "$dir/irgo-winvm"
say "installed $("$dir/irgo-winvm" version) at $dir/irgo-winvm"

case ":$PATH:" in
  *":$dir:"*) ;;
  *) say "$dir is not on your PATH; add it, e.g.: echo 'export PATH=\"$dir:\$PATH\"' >> ~/.zshrc" ;;
esac
say "next: irgo-winvm doctor"
