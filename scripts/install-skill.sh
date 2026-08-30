#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SKILL=${1:-mcpd-parallel-engineering}
DEST_ROOT=${2:-${CODEX_HOME:-"$HOME/.codex"}/skills}
FORCE=${MCPD_SKILL_FORCE:-0}
SRC="$ROOT/skills/$SKILL"
DEST="$DEST_ROOT/$SKILL"

[ -f "$SRC/SKILL.md" ] || { echo "unknown bundled skill: $SKILL" >&2; exit 2; }
if [ -e "$DEST" ] && [ "$FORCE" != 1 ]; then
  echo "destination already exists: $DEST (set MCPD_SKILL_FORCE=1 to replace)" >&2
  exit 1
fi
mkdir -p "$DEST_ROOT"
if [ -e "$DEST" ]; then rm -rf "$DEST"; fi
cp -R "$SRC" "$DEST"
printf 'installed MCPD skill %s to %s\n' "$SKILL" "$DEST"
