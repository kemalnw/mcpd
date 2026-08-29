#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
DIST=${DIST_DIR:-"$ROOT/dist"}
VERSION=${VERSION:-${1:-}}
COMMIT=${COMMIT:-$(git -C "$ROOT" rev-parse HEAD)}
SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-$(git -C "$ROOT" show -s --format=%ct "$COMMIT")}
DATE=${DATE:-$(date -u -d "@$SOURCE_DATE_EPOCH" +%Y-%m-%dT%H:%M:%SZ)}

if ! printf '%s\n' "$VERSION" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$'; then
  echo "VERSION must be a v-prefixed semantic version" >&2
  exit 2
fi

rm -rf "$DIST"
mkdir -p "$DIST"
version_no_v=${VERSION#v}
build_arch() {
  arch=$1
  name="mcpd_${version_no_v}_linux_${arch}"
  stage=$(mktemp -d)
  trap 'rm -rf "$stage"' EXIT HUP INT TERM
  mkdir -p "$stage/$name"

  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
    go build -mod=readonly -trimpath -buildvcs=false \
    -ldflags "-s -w -X github.com/kemalnw/mcpd/internal/version.Version=$VERSION -X github.com/kemalnw/mcpd/internal/version.Commit=$COMMIT -X github.com/kemalnw/mcpd/internal/version.Date=$DATE" \
    -o "$stage/$name/mcpd" "$ROOT/cmd/mcpd"

  cp "$ROOT/LICENSE" "$stage/$name/LICENSE"
  cp "$ROOT/README.md" "$stage/$name/README.md"
  chmod 0755 "$stage/$name/mcpd"
  archive="$DIST/$name.tar.gz"
  (
    cd "$stage"
    tar --sort=name --mtime="@$SOURCE_DATE_EPOCH" \
      --owner=0 --group=0 --numeric-owner -cf - "$name" | gzip -n > "$archive"
  )
  rm -rf "$stage"
  trap - EXIT HUP INT TERM
}

for arch in amd64 arm64; do
  build_arch "$arch"
done

cp "$ROOT/scripts/install.sh" "$DIST/install.sh"
chmod 0755 "$DIST/install.sh"
(
  cd "$DIST"
  sha256sum mcpd_*.tar.gz install.sh > checksums.txt
)
printf 'release artifacts written to %s\n' "$DIST"
