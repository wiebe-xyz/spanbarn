#!/usr/bin/env python3
"""staticcheck over one Go module, with a floor on the dead-code check.

staticcheck enforces every check, U1000 (unused code) included, at zero
findings: any finding fails the build. That is the Go side's dead-code gate.

staticcheck exits 0 when it analyses nothing, the same as a clean tree: a
package pattern that matches no package, or a toolchain the pinned staticcheck
cannot load. So this script also reads how many packages U1000 ran on, from
staticcheck's own -debug.measure-analyzers output, and fails below the module's
floor in FLOORS. The count includes test variants (pkg, pkg [pkg.test],
pkg_test). Raise a floor as the tree grows; lowering it shows in the diff. A
module without a floor fails, so a new module has to declare one.

staticcheck skips the analyzers for a package whose result it has cached, and
then measures nothing. Each run therefore gets an empty STATICCHECK_CACHE of its
own. The Go build cache stays warm.

Run with:  python3 scripts/staticcheck-gate.py <module dir>
"""

from __future__ import annotations

import os
import subprocess
import sys
import tempfile
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent

# Pinned: a new staticcheck release would otherwise change what CI enforces with
# no commit on our side.
STATICCHECK = "honnef.co/go/tools/cmd/staticcheck@v0.8.1"

# The analyzer that reports unused code.
DEAD_CODE_ANALYZER = "U1000"

# Packages U1000 must have analysed, per module. Counts on 2026-10-17: 93 and 3.
FLOORS = {
    ".": 80,
    "sdks/go": 2,
}


def analysed_packages(measure_output: str, analyzer: str = DEAD_CODE_ANALYZER) -> int:
    """Distinct packages `analyzer` ran on, from -debug.measure-analyzers output.

    Each line is `<analyzer>\\t<package>\\t<nanoseconds>`.
    """
    packages = set()
    for line in measure_output.splitlines():
        fields = line.split("\t")
        if len(fields) == 3 and fields[0] == analyzer:
            packages.add(fields[1])
    return len(packages)


def check_floor(measure_output: str, min_packages: int) -> str | None:
    """The floor's verdict: an error message, or None when U1000 saw enough packages."""
    count = analysed_packages(measure_output)
    if count < min_packages:
        return (
            f"staticcheck ran {DEAD_CODE_ANALYZER} on {count} package(s), "
            f"expected at least {min_packages}: nothing was measured"
        )
    return None


def floor_for(module: str) -> int | None:
    return FLOORS.get(os.path.normpath(module))


def run(module_dir: Path, min_packages: int, out=sys.stdout, err=sys.stderr) -> int:
    """Runs staticcheck on module_dir and returns the exit code the gate should report."""
    with tempfile.TemporaryDirectory() as tmp:
        measure = Path(tmp, "measure.tsv")
        proc = subprocess.run(
            ["go", "run", STATICCHECK, f"-debug.measure-analyzers={measure}", "./..."],
            cwd=module_dir,
            env={**os.environ, "STATICCHECK_CACHE": str(Path(tmp, "cache"))},
            capture_output=True,
            text=True,
        )
        measured = measure.read_text() if measure.exists() else ""

    out.write(proc.stdout)
    err.write(proc.stderr)
    code = 0
    if proc.returncode != 0:
        err.write(f"staticcheck gate FAILED: staticcheck exited {proc.returncode}\n")
        code = 1
    problem = check_floor(measured, min_packages)
    if problem:
        err.write(f"staticcheck gate FAILED: {problem}\n")
        code = 1
    if code == 0:
        out.write(f"staticcheck: clean, {DEAD_CODE_ANALYZER} analysed {analysed_packages(measured)} packages\n")
    return code


def main(argv: list[str]) -> int:
    if len(argv) != 1:
        sys.stderr.write("usage: staticcheck-gate.py <module dir>\n")
        return 2
    module = argv[0]
    floor = floor_for(module)
    if floor is None:
        sys.stderr.write(f"staticcheck gate FAILED: no floor for module {module!r}; add one to FLOORS\n")
        return 1
    return run(REPO_ROOT / module, floor)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
