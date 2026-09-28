#!/bin/sh
# moshpit installer: downloads the release binary for this machine, verifies its
# checksum, and installs it as `mp`.
#
#   curl -fsSL https://raw.githubusercontent.com/BlackFueg/moshpit/main/install.sh | sh
#
# Environment:
#   MOSHPIT_VERSION   release tag to install (default: latest), e.g. v0.1.0
#   PREFIX            install prefix (default: ~/.local, binary goes to $PREFIX/bin)
#   MOSHPIT_BASE_URL  download from a mirror instead of GitHub releases
set -eu

REPO="BlackFueg/moshpit"
VERSION="${MOSHPIT_VERSION:-latest}"
PREFIX="${PREFIX:-$HOME/.local}"
BIN="$PREFIX/bin"

if [ -t 1 ]; then B=$(printf '\033[1m'); C=$(printf '\033[36m'); Y=$(printf '\033[33m'); R=$(printf '\033[0m'); else B='' C='' Y='' R=''; fi
say()  { printf '%s==>%s %s\n' "$C" "$R" "$*"; }
warn() { printf '%s  !%s %s\n' "$Y" "$R" "$*"; }
die()  { printf 'moshpit: %s\n' "$*" >&2; exit 1; }

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  darwin|linux) ;;
  *) die "unsupported OS '$os' (macOS and Linux only; on Windows use WSL)" ;;
esac
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) die "unsupported architecture '$arch'" ;;
esac

if [ "$VERSION" = latest ]; then
  base="https://github.com/$REPO/releases/latest/download"
else
  base="https://github.com/$REPO/releases/download/$VERSION"
fi
base="${MOSHPIT_BASE_URL:-$base}"
asset="mp_${os}_${arch}.tar.gz"

command -v curl >/dev/null 2>&1 || die "curl is required"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading $asset ($VERSION)"
curl -fsSL "$base/$asset" -o "$tmp/$asset" || die "download failed: $base/$asset"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt" || die "download failed: $base/checksums.txt"

want=$(grep " $asset\$" "$tmp/checksums.txt" | cut -d' ' -f1)
[ -n "$want" ] || die "no checksum listed for $asset"
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$tmp/$asset" | cut -d' ' -f1)
else
  got=$(shasum -a 256 "$tmp/$asset" | cut -d' ' -f1)
fi
[ "$want" = "$got" ] || die "checksum mismatch for $asset"

tar -xzf "$tmp/$asset" -C "$tmp"
mkdir -p "$BIN"
install -m 755 "$tmp/mp_${os}_${arch}/mp" "$BIN/mp"
say "Installed ${B}$("$BIN/mp" -version)${R} to $BIN/mp"

# Local prerequisites: only warnings, mp itself is installed either way.
case ":$PATH:" in
  *":$BIN:"*) ;;
  *) warn "$BIN is not on your PATH. Add this to your shell profile:"
     # shellcheck disable=SC2016 # $PATH is meant literally in the printed hint
     printf '      export PATH="%s:$PATH"\n' "$BIN" ;;
esac
if ! command -v ssh >/dev/null 2>&1; then
  warn "OpenSSH client not found; mp needs ssh and ssh-keygen"
else
  v=$(ssh -V 2>&1 | sed -n 's/^OpenSSH_\([0-9]*\)\.\([0-9]*\).*/\1 \2/p')
  if [ -n "$v" ]; then
    # shellcheck disable=SC2086 # split "MAJOR MINOR" into $1 $2
    set -- $v
    if [ "$1" -lt 8 ] || { [ "$1" -eq 8 ] && [ "$2" -lt 4 ]; }; then
      warn "OpenSSH $1.$2 is older than 8.4; adding hosts by password needs 8.4+"
    fi
  fi
fi
if ! command -v mosh >/dev/null 2>&1; then
  if [ "$os" = darwin ]; then hint="brew install mosh"; else hint="install the 'mosh' package"; fi
  warn "mosh not found: sessions will connect over ssh. For roaming-proof connections: $hint"
fi
say "Run ${B}mp${R} to get started"
