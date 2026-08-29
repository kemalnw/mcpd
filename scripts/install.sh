#!/bin/sh
set -eu

REPO=${MCPD_REPO:-kemalnw/mcpd}
VERSION=${MCPD_VERSION:-}
INSTALL_DIR=${MCPD_INSTALL_DIR:-/usr/local/bin}
REQUIRE_SIGNATURE=${MCPD_REQUIRE_SIGNATURE:-0}
INSTALL_SERVICE=${MCPD_INSTALL_SERVICE:-0}
SETUP_MODE=${MCPD_SETUP:-auto}

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "mcpd installer: required command not found: $1" >&2
    exit 1
  }
}

need curl
need tar
need sha256sum
need uname
need grep
need sort
need awk
need install
need id
need chmod
need mktemp
if [ -z "$VERSION" ]; then
  latest=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest")
  VERSION=${latest##*/}
fi
if ! printf '%s\n' "$VERSION" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$'; then
  echo "mcpd installer: invalid version: $VERSION" >&2
  exit 2
fi

case "$(uname -s)" in
  Linux) ;;
  *) echo "mcpd installer: only Linux is supported" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "mcpd installer: unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
VERSION_NO_V=${VERSION#v}
ARCHIVE="mcpd_${VERSION_NO_V}_linux_${ARCH}.tar.gz"
BASE=${MCPD_RELEASE_BASE_URL:-"https://github.com/$REPO/releases/download/$VERSION"}
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT HUP INT TERM

# Allow an explicit HTTP localhost mirror for integration tests only.
case "$BASE" in
  http://127.0.0.1:*|http://localhost:*) CURL_TEST_HTTP=1 ;;
  https://*) CURL_TEST_HTTP=0 ;;
  *) echo "mcpd installer: release URL must use HTTPS" >&2; exit 1 ;;
esac

fetch() {
  if [ "$CURL_TEST_HTTP" = 1 ]; then
    curl -fsSL --retry 3 --retry-delay 1 "$1" -o "$2"
  else
    curl -fsSL --retry 3 --retry-delay 1 --proto '=https' --tlsv1.2 "$1" -o "$2"
  fi
}
fetch "$BASE/$ARCHIVE" "$TMP/$ARCHIVE"
fetch "$BASE/checksums.txt" "$TMP/checksums.txt"

(
  cd "$TMP"
  expected=$(grep "  $ARCHIVE$" checksums.txt || true)
  [ -n "$expected" ] || {
    echo "mcpd installer: checksum entry missing for $ARCHIVE" >&2
    exit 1
  }
  printf '%s\n' "$expected" | sha256sum -c -
)

if command -v cosign >/dev/null 2>&1; then
  fetch "$BASE/checksums.txt.sigstore.json" "$TMP/checksums.txt.sigstore.json"
  identity="https://github.com/$REPO/.github/workflows/release.yml@refs/tags/$VERSION"
  cosign verify-blob "$TMP/checksums.txt" \
    --bundle "$TMP/checksums.txt.sigstore.json" \
    --certificate-identity "$identity" \
    --certificate-oidc-issuer "https://token.actions.githubusercontent.com" >/dev/null
  echo "mcpd installer: Sigstore signature verified"
elif [ "$REQUIRE_SIGNATURE" = 1 ]; then
  echo "mcpd installer: cosign is required when MCPD_REQUIRE_SIGNATURE=1" >&2
  exit 1
else
  echo "mcpd installer: cosign not found; SHA-256 verified, signature verification skipped" >&2
fi
PKG="mcpd_${VERSION_NO_V}_linux_${ARCH}"
expected=$(printf '%s\n' "$PKG/" "$PKG/LICENSE" "$PKG/README.md" "$PKG/mcpd" | sort)
actual=$(tar -tzf "$TMP/$ARCHIVE" | sort)
if [ "$actual" != "$expected" ]; then
  echo "mcpd installer: archive manifest is not the expected release layout" >&2
  exit 1
fi
if ! tar -tvzf "$TMP/$ARCHIVE" | awk -v p="$PKG/mcpd" '
  $NF == p { seen++; if (substr($1,1,1) != "-") bad=1 }
  END { exit bad || seen != 1 }
'; then
  echo "mcpd installer: mcpd archive member is not a regular file" >&2
  exit 1
fi
BIN="$TMP/mcpd"
tar -xOzf "$TMP/$ARCHIVE" "$PKG/mcpd" > "$BIN"
chmod 0755 "$BIN"
[ -s "$BIN" ] || { echo "mcpd installer: archive contains an empty mcpd binary" >&2; exit 1; }

install_binary() {
  target="$INSTALL_DIR/mcpd"
  if [ -d "$INSTALL_DIR" ] && [ -w "$INSTALL_DIR" ]; then
    install -m 0755 "$BIN" "$target"
  elif [ "$(id -u)" -eq 0 ]; then
    mkdir -p "$INSTALL_DIR"
    install -m 0755 "$BIN" "$target"
  elif command -v sudo >/dev/null 2>&1; then
    sudo mkdir -p "$INSTALL_DIR"
    sudo install -m 0755 "$BIN" "$target"
  else
    echo "mcpd installer: $INSTALL_DIR is not writable and sudo is unavailable" >&2
    exit 1
  fi
}
install_binary
printf 'mcpd %s installed to %s/mcpd\n' "$VERSION" "$INSTALL_DIR"

if [ "$INSTALL_SERVICE" = 1 ]; then
  if [ "$(id -u)" -eq 0 ]; then
    "$INSTALL_DIR/mcpd" install
  elif command -v sudo >/dev/null 2>&1; then
    sudo "$INSTALL_DIR/mcpd" install
  else
    echo "mcpd installer: service installation requested but sudo is unavailable" >&2
    exit 1
  fi
  exit 0
fi

run_setup() {
  if [ "$(id -u)" -eq 0 ]; then
    "$INSTALL_DIR/mcpd" setup </dev/tty >/dev/tty 2>/dev/tty
  elif command -v sudo >/dev/null 2>&1; then
    sudo "$INSTALL_DIR/mcpd" setup </dev/tty >/dev/tty 2>/dev/tty
  else
    echo "mcpd installer: interactive setup requires root or sudo" >&2
    exit 1
  fi
}

case "$SETUP_MODE" in
  0|false|no)
    echo "Next: run 'sudo mcpd setup' to configure the service."
    ;;
  1|true|yes)
    if [ ! -t 2 ] || [ ! -r /dev/tty ] || [ ! -w /dev/tty ]; then
      echo "mcpd installer: MCPD_SETUP=1 requires an interactive terminal" >&2
      exit 1
    fi
    run_setup
    ;;
  auto)
    if [ -t 2 ] && [ -r /dev/tty ] && [ -w /dev/tty ]; then
      run_setup
    else
      echo "Next: run 'sudo mcpd setup' to configure the service."
    fi
    ;;
  *)
    echo "mcpd installer: MCPD_SETUP must be auto, 0, or 1" >&2
    exit 2
    ;;
esac
