#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TMP=$(mktemp -d)
SERVER_PID=""
cleanup() {
  if [ -n "$SERVER_PID" ]; then kill "$SERVER_PID" 2>/dev/null || true; fi
  rm -rf "$TMP"
}
trap cleanup EXIT HUP INT TERM

VERSION=v0.1.0-test
EPOCH=1700000000
DATE=2023-11-14T22:13:20Z
COMMIT=$(git -C "$ROOT" rev-parse HEAD)

sh -n "$ROOT/scripts/package-release.sh"
sh -n "$ROOT/scripts/install.sh"
DIST_DIR="$TMP/dist-a" VERSION="$VERSION" COMMIT="$COMMIT" \
  SOURCE_DATE_EPOCH="$EPOCH" DATE="$DATE" "$ROOT/scripts/package-release.sh" >/dev/null
DIST_DIR="$TMP/dist-b" VERSION="$VERSION" COMMIT="$COMMIT" \
  SOURCE_DATE_EPOCH="$EPOCH" DATE="$DATE" "$ROOT/scripts/package-release.sh" >/dev/null
cmp "$TMP/dist-a/checksums.txt" "$TMP/dist-b/checksums.txt"

for arch in amd64 arm64; do
  file="$TMP/dist-a/mcpd_0.1.0-test_linux_${arch}.tar.gz"
  test -s "$file"
  tar -tzf "$file" | grep -q "mcpd_0.1.0-test_linux_${arch}/mcpd$"
done

case "$(uname -m)" in
  x86_64|amd64) HOST_ARCH=amd64 ;;
  aarch64|arm64) HOST_ARCH=arm64 ;;
  *) echo "unsupported test architecture" >&2; exit 1 ;;
esac
mkdir -p "$TMP/extract"
tar -xzf "$TMP/dist-a/mcpd_0.1.0-test_linux_${HOST_ARCH}.tar.gz" -C "$TMP/extract"
"$TMP/extract/mcpd_0.1.0-test_linux_${HOST_ARCH}/mcpd" version | grep -q '"version": "v0.1.0-test"'
PORT=$((19000 + $$ % 1000))
python3 -m http.server "$PORT" --bind 127.0.0.1 --directory "$TMP/dist-a" >/dev/null 2>&1 &
SERVER_PID=$!
for _ in 1 2 3 4 5; do
  if curl -fsS "http://127.0.0.1:$PORT/checksums.txt" >/dev/null 2>&1; then break; fi
  sleep 0.2
done

mkdir -p "$TMP/bin"
MCPD_VERSION="$VERSION" MCPD_RELEASE_BASE_URL="http://127.0.0.1:$PORT" \
  MCPD_INSTALL_DIR="$TMP/bin" "$ROOT/scripts/install.sh" >/dev/null
"$TMP/bin/mcpd" version | grep -q '"version": "v0.1.0-test"'

if MCPD_REQUIRE_SIGNATURE=1 MCPD_VERSION="$VERSION" \
  MCPD_RELEASE_BASE_URL="http://127.0.0.1:$PORT" MCPD_INSTALL_DIR="$TMP/required" \
  "$ROOT/scripts/install.sh" >/dev/null 2>&1; then
  echo "signature-required install unexpectedly succeeded without a test bundle" >&2
  exit 1
fi

archive="$TMP/dist-a/mcpd_0.1.0-test_linux_${HOST_ARCH}.tar.gz"
printf x >> "$archive"
if MCPD_VERSION="$VERSION" MCPD_RELEASE_BASE_URL="http://127.0.0.1:$PORT" \
  MCPD_INSTALL_DIR="$TMP/tampered" "$ROOT/scripts/install.sh" >/dev/null 2>&1; then
  echo "tampered archive unexpectedly installed" >&2
  exit 1
fi
python3 - "$archive" <<'PY'
import io, sys, tarfile
path = sys.argv[1]
with tarfile.open(path, "w:gz") as tf:
    data = b"escape"
    info = tarfile.TarInfo("../escape")
    info.size = len(data)
    tf.addfile(info, io.BytesIO(data))
PY
(
  cd "$TMP/dist-a"
  sha256sum "$(basename "$archive")" > checksums.txt
)
if MCPD_VERSION="$VERSION" MCPD_RELEASE_BASE_URL="http://127.0.0.1:$PORT" \
  MCPD_INSTALL_DIR="$TMP/traversal" "$ROOT/scripts/install.sh" >/dev/null 2>&1; then
  echo "path traversal archive unexpectedly installed" >&2
  exit 1
fi

echo "release packaging and installer tests passed"

