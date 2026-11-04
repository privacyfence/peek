#!/bin/bash
# SessionStart hook for Claude Code cloud sessions: installs mise so the
# `mise run …` tasks in mise.toml (check, check-full, tailwind, …) work.
#
# The cloud proxy blocks https://mise.run and dl.google.com, so mise comes from a
# pinned, checksum-verified GitHub release, and Go is the toolchain already in the
# image (/usr/local/go), registered with mise when its version matches mise.toml.
set -euo pipefail
export MISE_HIDE_UPDATE_WARNING=1

if [ "${CLAUDE_CODE_REMOTE:-}" != "true" ]; then
  exit 0
fi

MISE_VERSION="v2026.9.0"
# Per-architecture checksums from the release's SHASUMS256.txt.
case "$(uname -m)" in
  x86_64 | amd64)
    MISE_ARCH="x64"
    MISE_SHA256="c9b089f2be1db4d4262eacaf367e480e7a328848adcc37d9d1612996902485d0"
    ;;
  aarch64 | arm64)
    MISE_ARCH="arm64"
    MISE_SHA256="0b5c4586353010378d9caca62c95166588c3dc4e310eb2d276aac1068397faec"
    ;;
  *)
    echo "session-start: unsupported architecture $(uname -m); skipping mise setup" >&2
    exit 0
    ;;
esac
BIN_DIR="$HOME/.local/bin"
MISE="$BIN_DIR/mise"
cd "${CLAUDE_PROJECT_DIR:-$(pwd)}"

mkdir -p "$BIN_DIR"
# A binary for another architecture fails to run, so it is replaced here too.
if [ ! -x "$MISE" ] || ! "$MISE" --version 2>/dev/null | grep -q "${MISE_VERSION#v}"; then
  tmp="$(mktemp)"
  curl -fsSL -o "$tmp" "https://github.com/jdx/mise/releases/download/${MISE_VERSION}/mise-${MISE_VERSION}-linux-${MISE_ARCH}"
  echo "${MISE_SHA256}  ${tmp}" | sha256sum -c --quiet -
  install -m 0755 "$tmp" "$MISE"
  rm -f "$tmp"
fi

"$MISE" trust --quiet "$PWD/mise.toml"

# Register the image's Go with mise instead of downloading it (dl.google.com is blocked).
want_go="$(sed -n 's/^go *= *"\(.*\)"/\1/p' mise.toml)"
if [ -x /usr/local/go/bin/go ]; then
  have_go="$(/usr/local/go/bin/go env GOVERSION)"
  have_go="${have_go#go}"
  if [ "$have_go" = "$want_go" ]; then
    go_dir="$("$MISE" where go@"$want_go" 2>/dev/null || true)"
    if [ -z "$go_dir" ]; then
      install_root="${MISE_DATA_DIR:-$HOME/.local/share/mise}/installs/go"
      mkdir -p "$install_root"
      ln -sfn /usr/local/go "$install_root/$want_go"
    fi
  else
    echo "session-start: image Go is $have_go but mise.toml wants $want_go; trying mise install" >&2
  fi
fi
"$MISE" install --yes

# mise.toml's tailwind task writes to bin/ without creating it.
mkdir -p bin

# Warm the Go module cache so builds and tests do not stall on first use.
"$MISE" exec -- go mod download

if [ -n "${CLAUDE_ENV_FILE:-}" ]; then
  data_dir="${MISE_DATA_DIR:-$HOME/.local/share/mise}"
  echo "export PATH=\"$BIN_DIR:$data_dir/shims:\$PATH\"" >> "$CLAUDE_ENV_FILE"
  # mise.jdx.dev (the update check) is blocked by the proxy too.
  echo "export MISE_HIDE_UPDATE_WARNING=1" >> "$CLAUDE_ENV_FILE"
fi
