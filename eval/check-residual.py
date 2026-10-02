#!/usr/bin/env python3
"""Report injected faults still present in the working tree.

The mutation gate writes each applied mutation to a log as
`name|path|original|injected`. This checker compares the FULL injected text —
not just its first line — because several injections contain the original text
as a prefix (`final-guard-off` appends to the original signature), and a
first-line comparison would report a false residual.

Exit code 0 when nothing is left behind, 1 when a residual is found.
"""

import sys


def unescape(value: str) -> str:
    return value.replace("\\n", "\n").replace("\\&", "&")


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: check-residual.py <applied-mutations-log>")
        return 2
    try:
        lines = open(sys.argv[1], encoding="utf-8").read().splitlines()
    except OSError:
        return 0  # no log means the gate never applied anything
    residual = 0
    for line in lines:
        if not line.strip():
            continue
        _, path, _, injected = line.split("|", 3)
        injected = unescape(injected)
        try:
            content = open(path, encoding="utf-8").read()
        except OSError as error:
            print(f"cannot read back {path}: {error}")
            residual += 1
            continue
        if injected and injected in content:
            print(f"RESIDUAL MUTATION in {path}: {injected.splitlines()[0]}")
            residual += 1
    return 1 if residual else 0


if __name__ == "__main__":
    sys.exit(main())
