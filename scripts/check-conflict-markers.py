#!/usr/bin/env python3
import os
import sys
from pathlib import Path

MARKERS = ("<" * 7, "=" * 7, ">" * 7)
SKIP_DIRS = {'.git', 'vendor', 'node_modules', 'dist', 'build', 'bin'}
TEXT_SUFFIXES = {'.go', '.py', '.sh', '.toml', '.md', '.yaml', '.yml', '.json', '.txt'}
TEXT_NAMES = {'Makefile', 'Dockerfile'}


def candidates(root: Path):
    if root.is_file():
        yield root
        return
    for base, dirs, files in os.walk(root):
        dirs[:] = [d for d in dirs if d not in SKIP_DIRS]
        for name in files:
            p = Path(base) / name
            if p.suffix in TEXT_SUFFIXES or name in TEXT_NAMES:
                yield p


def main():
    roots = [Path(x) for x in sys.argv[1:]] or [Path('.')]
    findings = []
    for root in roots:
        for path in candidates(root):
            try:
                with path.open('r', encoding='utf-8') as f:
                    for number, line in enumerate(f, 1):
                        stripped = line.lstrip()
                        if any(stripped.startswith(marker) for marker in MARKERS):
                            findings.append((str(path), number, stripped.rstrip()))
            except (UnicodeDecodeError, OSError):
                continue
    for path, number, text in findings:
        print(f'{path}:{number}: unresolved merge conflict marker: {text}', file=sys.stderr)
    if findings:
        raise SystemExit(1)
    print('conflict_marker_check_ok')

if __name__ == '__main__':
    main()
