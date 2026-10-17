#!/usr/bin/env node
// knip (dead code in the web app), as a ratchet. Unused files, exports, types
// and dependencies are counted per item and compared with knip-baseline.json.
//
// Same shape as oxlint-ratchet.mjs: the gate is symmetric. An item that is not
// in the baseline fails (new dead code), and so does a baseline item that is
// gone (stale allowance: it leaves room for that item to come back with the
// gate still green). The failure prints the refresh command.
//
// `--write` refreshes the baseline after a deliberate change.
// `--report-only` prints the same report and always exits 0. CI runs this mode
// while the gate soaks on real pull requests; drop the flag to enforce.
//
// Items are keyed `type:file:name`, without line numbers, so moving code inside
// a file does not read as one finding gone and another new.
//
// A floor sits under every mode, --write and --report-only included: knip
// exits 0 with no findings when it parses nothing (a TypeScript major its
// parser does not support, a project glob that matches no file), and no
// findings on a zero baseline reads as clean (vibecortex#29). Each run first
// asks `knip --performance` how many files it analysed, and exits 2 below
// MIN_ANALYSED_FILES.

import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

export const BASELINE_FILE = "knip-baseline.json";

// knip analyses 139 files in web/ (2026-10-17: the 133 under src plus configs
// and scripts). The floor sits well under that, so normal deletions pass and a
// run that reads a fraction of the tree does not.
export const MIN_ANALYSED_FILES = 100;

// The issue types knip reports that mean "this is unused". `duplicates` and
// `enumMembers` are left out on purpose: they are style findings, not dead code.
export const ITEM_TYPES = [
  "dependencies",
  "devDependencies",
  "optionalPeerDependencies",
  "unlisted",
  "binaries",
  "unresolved",
  "exports",
  "types",
];

/** The command that refreshes the baseline, printed verbatim on every failure. */
export function refreshCommand() {
  return "node scripts/knip-ratchet.mjs --write";
}

/**
 * Flatten knip's JSON report to a sorted list of unique item keys.
 * Pure, so the gate can be tested without running knip.
 */
export function flatten(report) {
  const keys = new Set();
  for (const file of report.files ?? []) keys.add(`files:${file}:`);
  for (const issue of report.issues ?? []) {
    // knip's json reporter lists an unused file as an issue of its own.
    if ((issue.files ?? []).length > 0) keys.add(`files:${issue.file}:`);
    for (const type of ITEM_TYPES) {
      for (const item of issue[type] ?? []) {
        keys.add(`${type}:${issue.file}:${item.name}`);
      }
    }
  }
  return [...keys].sort();
}

/**
 * Compare the current items with the baseline.
 *
 *   - an item not in the baseline  -> added (new dead code)
 *   - a baseline item no longer found -> removed (stale allowance)
 *
 * Both fail, so `ok` requires neither.
 */
export function compare(baselineItems, currentItems) {
  const was = new Set(baselineItems);
  const now = new Set(currentItems);
  const added = [...now].filter((k) => !was.has(k)).sort();
  const removed = [...was].filter((k) => !now.has(k)).sort();
  return {
    ok: added.length === 0 && removed.length === 0,
    added,
    removed,
    total: now.size,
    baselineTotal: was.size,
  };
}

/**
 * Files knip analysed, read from the `getImportsAndExports` row of the
 * `--performance` table (its second column is the call count, one per file).
 * Output without that row counts as zero files.
 */
export function analysedFiles(output) {
  const row = output.split("\n").find((line) => line.trimStart().startsWith("getImportsAndExports"));
  if (!row) return 0;
  const count = Number(row.trim().split(/\s+/)[1]);
  return Number.isInteger(count) ? count : 0;
}

/** The floor check: an error message when knip analysed too few files, else null. */
export function floorProblem(analysed, min = MIN_ANALYSED_FILES) {
  if (analysed >= min) return null;
  return `knip analysed ${analysed} file(s), expected at least ${min}: the run did not read the tree`;
}

// The web package's own knip, pinned in package.json. `npx knip` could fetch a
// different version at run time.
const WEB_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
export const KNIP = [path.join(WEB_DIR, "node_modules", ".bin", "knip")];

/** Run `knip --performance` in dir and return its output. */
export function runKnipPerformance(dir, knip = KNIP) {
  try {
    return execFileSync(knip[0], [...knip.slice(1), "--performance", "--no-exit-code"], {
      cwd: dir,
      encoding: "utf8",
      maxBuffer: 64 * 1024 * 1024,
      stdio: ["ignore", "pipe", "pipe"],
    });
  } catch (err) {
    return err.stdout ?? "";
  }
}

export function runKnip(dir, knip = KNIP) {
  let raw = "";
  try {
    raw = execFileSync(knip[0], [...knip.slice(1), "--reporter", "json", "--no-exit-code"], {
      cwd: dir,
      encoding: "utf8",
      maxBuffer: 64 * 1024 * 1024,
    });
  } catch (err) {
    if (!err.stdout) throw new Error(`knip did not run: ${err.message}`);
    raw = err.stdout;
  }
  return JSON.parse(raw);
}

/**
 * The gate over the package in `dir`; returns the exit code. The floor exits 2
 * in every mode. `--report-only` turns a disagreement with the baseline into
 * exit 0; it never touches the floor.
 */
export function run({ dir = WEB_DIR, args = [], minFiles = MIN_ANALYSED_FILES, out = console.log, err = console.error } = {}) {
  const baselinePath = path.join(dir, BASELINE_FILE);
  const write = args.includes("--write");
  const reportOnly = args.includes("--report-only");

  const problem = floorProblem(analysedFiles(runKnipPerformance(dir)), minFiles);
  if (problem) {
    err(`knip floor FAILED: ${problem}`);
    return 2;
  }

  const items = flatten(runKnip(dir));

  if (write) {
    fs.writeFileSync(baselinePath, `${JSON.stringify({ total: items.length, items }, null, 2)}\n`);
    out(`knip baseline written: ${items.length} items`);
    return 0;
  }

  const baseline = fs.existsSync(baselinePath)
    ? JSON.parse(fs.readFileSync(baselinePath, "utf8"))
    : { total: 0, items: [] };
  const res = compare(baseline.items, items);

  if (res.ok && res.total === 0) {
    out("knip: clean");
    return 0;
  }
  out(`knip: ${res.total} items (baseline ${res.baselineTotal})`);
  if (res.ok) {
    out("knip: on the baseline, exactly.");
    return 0;
  }

  const say = reportOnly ? out : err;
  say("");
  say(
    reportOnly
      ? "knip ratchet: the tree and the baseline disagree, but this run is report-only."
      : "knip ratchet FAILED: the tree and the baseline disagree.",
  );
  if (res.added.length) {
    say("");
    say("Dead code that is not in the baseline:");
    for (const k of res.added) say(`  ${k}`);
    say("");
    say("Delete it, or stop exporting it if only its own file uses it.");
  }
  if (res.removed.length) {
    say("");
    say("Baseline items that are gone, so the baseline is now stale:");
    for (const k of res.removed) say(`  ${k}`);
    say("");
    say("The baseline has to follow the work down. Lock the improvement in:");
    say(`  ${refreshCommand()}`);
  }

  if (reportOnly) {
    out("");
    out("knip ratchet: report-only run, not gating.");
    return 0;
  }
  return 1;
}

// Only run when invoked directly, so the test can import the pure helpers.
if (process.argv[1] && fileURLToPath(import.meta.url) === path.resolve(process.argv[1])) {
  const code = run({ args: process.argv.slice(2) });
  if (code !== 0) process.exit(code);
}
