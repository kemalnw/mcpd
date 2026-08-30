#!/usr/bin/env python3
from __future__ import annotations
import pathlib, re, sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
SKILLS = ROOT / "skills"
NAME_RE = re.compile(r"^[a-z0-9]+(?:-[a-z0-9]+)*$")

def parse_frontmatter(path: pathlib.Path) -> dict[str, str]:
    text = path.read_text(encoding="utf-8")
    lines = text.splitlines()
    if not lines or lines[0].strip() != "---":
        raise ValueError("missing opening YAML frontmatter delimiter")
    try:
        end = next(i for i, line in enumerate(lines[1:], 1) if line.strip() == "---")
    except StopIteration as exc:
        raise ValueError("missing closing YAML frontmatter delimiter") from exc
    out: dict[str, str] = {}
    for line in lines[1:end]:
        if not line or line[0].isspace() or ":" not in line:
            continue
        key, value = line.split(":", 1)
        out[key.strip()] = value.strip().strip('"\'')
    return out

def main() -> int:
    if not SKILLS.is_dir():
        print("skills directory missing", file=sys.stderr)
        return 1
    found = 0
    for skill_dir in sorted(p for p in SKILLS.iterdir() if p.is_dir()):
        skill = skill_dir / "SKILL.md"
        if not skill.is_file():
            print(f"{skill_dir}: missing SKILL.md", file=sys.stderr)
            return 1
        try:
            meta = parse_frontmatter(skill)
        except ValueError as exc:
            print(f"{skill}: {exc}", file=sys.stderr)
            return 1
        name = meta.get("name", "")
        description = meta.get("description", "")
        if name != skill_dir.name or not NAME_RE.fullmatch(name) or len(name) > 64:
            print(f"{skill}: invalid name {name!r}", file=sys.stderr)
            return 1
        if not description or len(description) > 1024:
            print(f"{skill}: description must be 1..1024 chars", file=sys.stderr)
            return 1
        if len(skill.read_text(encoding="utf-8").splitlines()) > 500:
            print(f"{skill}: SKILL.md exceeds 500 lines", file=sys.stderr)
            return 1
        found += 1
        print(f"skill_ok name={name}")
    if not found:
        print("no skills found", file=sys.stderr)
        return 1
    return 0

if __name__ == "__main__":
    raise SystemExit(main())
