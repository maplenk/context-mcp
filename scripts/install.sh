#!/bin/sh
set -eu

VERSION="__VERSION__"
ARCHIVE_URL="__ARCHIVE_URL__"
ARCHIVE_SHA256="__ARCHIVE_SHA256__"
ARCHIVE_NAME="context-mcp-darwin-arm64.tar.gz"
DEFAULT_PREFIX="${HOME}/.local/bin"
SYSTEM_PREFIX="/usr/local/bin"
BASE_URL="${CONTEXT_MCP_BASE_URL:-}"
ARCHIVE_PATH="${CONTEXT_MCP_ARCHIVE:-}"
BINARY_NAME="context-mcp"

usage() {
  cat <<'EOF'
Usage: install.sh [options]

Install context-mcp from the packaged macOS release into a PATH directory.

Options:
  --system          Install into /usr/local/bin (uses sudo if needed)
  --user            Install into ~/.local/bin or a writable PATH directory
  --prefix DIR      Install into a specific directory
  --dir DIR         Alias for --prefix
  --archive PATH    Install from an already-downloaded archive
  --help            Show this help

Examples:
  curl -fsSL https://github.com/maplenk/context-mcp/releases/latest/download/install.sh | sh
  curl -fsSL https://github.com/maplenk/context-mcp/releases/latest/download/install.sh | sh -s -- --system
  sh install.sh --archive ~/Downloads/context-mcp-darwin-arm64.tar.gz
EOF
}

log() {
  printf '%s\n' "$*" >&2
}

die() {
  log "install.sh: $*"
  exit 1
}

have() {
  command -v "$1" >/dev/null 2>&1
}

download() {
  url=$1
  out=$2
  if have curl; then
    curl -fsSL "$url" -o "$out"
    return
  fi
  if have wget; then
    wget -qO "$out" "$url"
    return
  fi
  die "need curl or wget to download the release archive"
}

sha256_file() {
  if have shasum; then
    shasum -a 256 "$1" | awk '{print $1}'
    return
  fi
  if have sha256sum; then
    sha256sum "$1" | awk '{print $1}'
    return
  fi
  die "need shasum or sha256sum to verify the download"
}

path_contains() {
  case ":${PATH:-}:" in
    *":$1:"*) return 0 ;;
    *) return 1 ;;
  esac
}

choose_user_prefix() {
  for dir in /opt/homebrew/bin /usr/local/bin; do
    if [ -d "$dir" ] && [ -w "$dir" ] && path_contains "$dir"; then
      printf '%s\n' "$dir"
      return
    fi
  done
  printf '%s\n' "$DEFAULT_PREFIX"
}

install_prefix=""
install_mode="user"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --system)
      install_mode="system"
      shift
      ;;
    --user)
      install_mode="user"
      shift
      ;;
    --prefix)
      [ "$#" -ge 2 ] || die "--prefix requires a directory"
      install_prefix="$2"
      shift 2
      ;;
    --dir)
      [ "$#" -ge 2 ] || die "--dir requires a directory"
      install_prefix="$2"
      shift 2
      ;;
    --archive)
      [ "$#" -ge 2 ] || die "--archive requires a path"
      ARCHIVE_PATH="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
done

case "$(uname -s)" in
  Darwin)
    ;;
  *)
    die "this release asset currently ships the macOS binary only"
    ;;
esac

case "$(uname -m)" in
  arm64|aarch64)
    ;;
  *)
    die "this release ships the darwin/arm64 binary only"
    ;;
esac

if [ -z "$install_prefix" ]; then
  if [ "$install_mode" = "system" ]; then
    install_prefix="$SYSTEM_PREFIX"
  else
    install_prefix=$(choose_user_prefix)
  fi
fi

if [ -n "$BASE_URL" ]; then
  ARCHIVE_URL="${BASE_URL}/${ARCHIVE_NAME}"
fi

tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/context-mcp-install.XXXXXX")"
trap 'rm -rf "$tmpdir"' EXIT

archive="${tmpdir}/${ARCHIVE_NAME}"
binary="${tmpdir}/${BINARY_NAME}"
target="${install_prefix}/${BINARY_NAME}"

if [ -n "$ARCHIVE_PATH" ]; then
  [ -f "$ARCHIVE_PATH" ] || die "archive not found: $ARCHIVE_PATH"
  cp "$ARCHIVE_PATH" "$archive"
else
  log "Downloading context-mcp ${VERSION}"
  download "$ARCHIVE_URL" "$archive"
fi

actual_sha="$(sha256_file "$archive")"
if [ "$actual_sha" != "$ARCHIVE_SHA256" ]; then
  die "checksum mismatch for ${ARCHIVE_NAME} (expected ${ARCHIVE_SHA256}, got ${actual_sha})"
fi

tar -xzf "$archive" -C "$tmpdir" "$BINARY_NAME"
[ -f "$binary" ] || die "expected ${BINARY_NAME} inside the archive"

if [ "$install_mode" = "system" ]; then
  if [ ! -d "$install_prefix" ]; then
    sudo mkdir -p "$install_prefix"
  fi
  if [ -w "$install_prefix" ]; then
    install -m 0755 "$binary" "$target"
  else
    sudo install -m 0755 "$binary" "$target"
  fi
else
  mkdir -p "$install_prefix"
  install -m 0755 "$binary" "$target"
fi

log "Installed context-mcp ${VERSION} to ${target}"

if path_contains "$install_prefix"; then
  log "Run '${BINARY_NAME} --help' to verify the install."
elif [ "$install_prefix" = "$DEFAULT_PREFIX" ]; then
  cat <<EOF >&2
Add this to your shell profile to use context-mcp from anywhere:
  export PATH="\$HOME/.local/bin:\$PATH"
EOF
else
  cat <<EOF >&2
Add this to your shell profile to use context-mcp from anywhere:
  export PATH="${install_prefix}:\$PATH"
EOF
fi

log "Checksum verified: ${actual_sha}"
