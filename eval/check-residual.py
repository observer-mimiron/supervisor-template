#!/usr/bin/env python3
"""Report injected faults still present in the working tree.

The mutation gate writes each applied mutation to a log as
`name<US>path<US>original<US>injected<US>l1-expectation`, where `<US>` is the
ASCII unit separator (0x1f). The separator must not be `|`: injections contain
`||` (see `terminal-uniqueness-off`), and splitting on `|` truncates the injected
text so the comparison would check the wrong string.

This checker compares the FULL injected text — not just its first line — because
several injections contain the original text as a prefix (`final-guard-off`
appends to the original signature), and a first-line comparison would report a
false residual.

Exit code 0 when nothing is left behind, 1 when a residual is found.
"""

import sys

SEPARATOR = "\x1f"


def unescape(value: str) -> str:
    # Must stay in sync with the unescape in eval/mutation-gate.sh.
    return value.replace("\\n", "\n").replace("\\t", "\t").replace("\\&", "&")


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
        fields = line.split(SEPARATOR)
        if len(fields) < 4:
            print(f"malformed mutation log line ({len(fields)} fields): {line[:60]!r}")
            residual += 1
            continue
        path, injected = fields[1], unescape(fields[3])
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
