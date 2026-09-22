#!/bin/sh
# barrage installer — fetches a prebuilt binary from GitHub releases,
# falls back to `go install` when no binary matches.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/codetesla51/barrage/main/install.sh | bash
#   curl -fsSL .../install.sh | bash -s -- --version v0.6.1 --dir ~/.local/bin
#   ./install.sh --from-source --version latest
#
# Flags:
#   --version TAG   release tag to install (default: latest)
#   --dir DIR       install directory (default: /usr/local/bin, else ~/.local/bin)
#   --from-source   skip prebuilt binaries, build via `go install`
#   --repo SLUG     GitHub repo (default: codetesla51/barrage)
#   -h, --help      print this help
#
# Env overrides: BARRAGE_VERSION, BARRAGE_DIR, BARRAGE_REPO.
set -eu

REPO="${BARRAGE_REPO:-codetesla51/barrage}"
VERSION="${BARRAGE_VERSION:-latest}"
DIR="${BARRAGE_DIR:-}"
FROM_SOURCE=0

usage() { sed -n '2,/^set -eu$/p' "$0" | sed -n '1,/^set -eu$/p' | sed '$d' | sed 's/^# \{0,1\}//'; }

while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="${2:?--version needs a tag}"; shift 2 ;;
    --version=*) VERSION="${1#--version=}"; shift ;;
    --dir) DIR="${2:?--dir needs a directory}"; shift 2 ;;
    --dir=*) DIR="${1#--dir=}"; shift ;;
    --from-source) FROM_SOURCE=1; shift ;;
    --repo) REPO="${2:?--repo needs owner/name}"; shift 2 ;;
    --repo=*) REPO="${1#--repo=}"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "install.sh: unknown flag $1 (see --help)" >&2; exit 1 ;;
  esac
done

log() { printf '%s\n' "$*"; }
die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

# Pick a writable default: system dir when root, else the user's local bin.
if [ -z "$DIR" ]; then
  if [ "$(id -u)" = "0" ]; then DIR="/usr/local/bin"; else DIR="$HOME/.local/bin"; fi
fi

resolve_latest() {
  # `latest` is resolved via the GitHub API; on failure the caller falls back
  # to source. No jq dependency — the tag is greppable out of the JSON.
  if have curl; then
    curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null \
      | grep -m1 '"tag_name"' | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/' || true
  elif have wget; then
    wget -qO- "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null \
      | grep -m1 '"tag_name"' | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/' || true
  fi
}

if [ "$VERSION" = "latest" ]; then
  log "resolving latest release of $REPO..."
  VERSION="$(resolve_latest)" || VERSION=""
  [ -n "$VERSION" ] || die "could not resolve latest release (network/API down?). Retry with --version vX.Y.Z or --from-source."
  log "latest is $VERSION"
fi

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$OS" in
  linux) OS="linux" ;;
  darwin) OS="darwin" ;;
  mingw*|msys*|cygwin*|windowsnt) OS="windows" ;;
  *) die "unsupported OS: $(uname -s)" ;;
esac
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) die "unsupported arch: $(uname -m) (barrage ships linux/darwin × amd64/arm64, windows × amd64)" ;;
esac
if [ "$OS" = "windows" ] && [ "$ARCH" = "arm64" ]; then
  die "no windows/arm64 binary — rerun with --from-source"
fi

BIN_NAME="barrage"
[ "$OS" = "windows" ] && BIN_NAME="barrage.exe"
ASSET="barrage-$OS-$ARCH"
[ "$OS" = "windows" ] && ASSET="$ASSET.exe"
URL="https://github.com/$REPO/releases/download/$VERSION/$ASSET"

TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT INT TERM

download() {
  if have curl; then curl -fsSL --retry 2 -o "$TMPDIR/$ASSET" "$URL"
  elif have wget; then wget -qO "$TMPDIR/$ASSET" "$URL"
  else return 1; fi
}

install_binary() {
  mkdir -p "$DIR"
  if [ -w "$DIR" ]; then
    install -m 755 "$TMPDIR/$ASSET" "$DIR/$BIN_NAME"
  elif have sudo; then
    sudo install -m 755 "$TMPDIR/$ASSET" "$DIR/$BIN_NAME"
  else
    die "$DIR is not writable and sudo is unavailable — rerun with --dir ~/.local/bin"
  fi
}

install_from_source() {
  have go || die "no prebuilt binary for $OS/$ARCH at $VERSION and no Go toolchain found. Install Go 1.25+ from https://go.dev/dl/ and retry."
  log "building from source: $REPO/cmd/barrage@$VERSION ..."
  # `go install` honours GOBIN; GOBIN-unaware setups get the module cache path.
  GOBIN="$DIR" go install "github.com/${REPO#https://}/cmd/barrage@$VERSION" 2>/dev/null \
    || GOBIN="$DIR" go install "github.com/codetesla51/barrage/cmd/barrage@$VERSION"
}

if [ "$FROM_SOURCE" = "1" ]; then
  install_from_source
else
  log "downloading $URL ..."
  if download; then
    install_binary
  else
    log "no prebuilt binary ($ASSET missing for $VERSION?) — falling back to source build."
    install_from_source
  fi
fi

# Verify on PATH or at the install location, and nudge PATH when needed.
if have "$DIR/$BIN_NAME"; then INSTALLED_VERSION="$("$DIR/$BIN_NAME" version 2>/dev/null || echo ok)"; else INSTALLED_VERSION=""; fi
case ":$PATH:" in
  *":$DIR:"*) ON_PATH=1 ;;
  *) ON_PATH=0 ;;
esac

log ""
log "barrage installed to $DIR/$BIN_NAME ${INSTALLED_VERSION:-($VERSION)}"
if [ "$ON_PATH" = "0" ]; then
  log "NOTE: $DIR is not on your PATH. Add it:"
  log "  export PATH=\"\$PATH:$DIR\"   # then restart your shell"
else
  log "Try it:  barrage version && barrage run -c config.yaml"
fi
