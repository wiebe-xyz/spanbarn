#!/usr/bin/env node
// oxlint, as a ratchet. `npm run lint` is this script: the baseline started as
// a report-only soak while its findings got triaged (fix the real ones,
// suppress false positives at the call site with a reason, disable a rule
// only after auditing every one of its hits), and it gates for real now that
// the baseline is clean.
//
// Why oxlint at all: typescript-eslint consumes the TypeScript compiler API
// directly and cannot load against TS 7. oxlint parses TypeScript itself in
// Rust, so it is unaffected, which makes it the migration target.
//
// The gate is symmetric: a rule above its baseline fails, and so does a rule
// BELOW it. An improvement fails because the baseline has to follow the work
// down — clear five findings of a rule baselined at twelve and a stale
// allowance of twelve is room for those five to come back with the gate still
// green. The fix is one paste, and the script prints it.
//
// `--write` refreshes the baseline after a deliberate change.
// `--report-only` prints the same report but always exits 0 — useful for
// checking findings locally without failing the run.
//
// `--type-aware` runs a second, separate mode: `oxlint --type-aware`, which
// evaluates the type-aware rule class (no-floating-promises,
// no-misused-promises, await-thenable, no-unnecessary-condition,
// no-unnecessary-type-assertion, plus whatever else the "correctness"
// category pulls in once type information is available) via oxlint-tsgolint.
// It compares against its OWN baseline file, oxlint-type-aware-baseline.json
// — kept separate from the syntax baseline so the existing zero-tolerance
// syntax gate can't be quietly loosened by a type-aware finding sharing the
// same file, and so the type-aware soak (currently report-only in CI, same
// shape as the original oxlint soak before it started gating) can carry a
// non-zero baseline without touching the syntax one.

import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

export const BASELINE_FILE = "oxlint-baseline.json";
export const TYPE_AWARE_BASELINE_FILE = "oxlint-type-aware-baseline.json";
export const LINT_TARGETS = ["src"];

/**
 * The command that refreshes the baseline. Printed verbatim on every failure
 * so the fix is a paste, not a guess.
 */
export function refreshCommand(typeAware = false) {
  return `node scripts/oxlint-ratchet.mjs${typeAware ? " --type-aware" : ""} --write`;
}

/**
 * Compare current counts against the baseline.
 *
 *   - a rule above its baseline count (or absent from the baseline) -> regression
 *   - a rule below it (or gone entirely)                            -> improvement
 *
 * Both fail, so `ok` requires neither. Improvements fail because a baseline
 * that does not follow the work down leaves an allowance a future regression
 * can occupy unnoticed.
 *
 * Pure, so the gate can be tested without running oxlint.
 * Returns { ok, regressions, improvements, total, baselineTotal }.
 */
export function compare(baseline, counts) {
  const regressions = [];
  const improvements = [];
  const rules = new Set([...Object.keys(baseline.counts ?? {}), ...Object.keys(counts)]);
  for (const rule of rules) {
    const was = baseline.counts?.[rule] ?? 0;
    const now = counts[rule] ?? 0;
    if (now > was) regressions.push({ rule, was, now });
    else if (now < was) improvements.push({ rule, was, now });
  }
  const total = Object.values(counts).reduce((a, b) => a + b, 0);
  const baselineTotal = Object.values(baseline.counts ?? {}).reduce((a, b) => a + b, 0);
  return {
    ok: regressions.length === 0 && improvements.length === 0,
    regressions,
    improvements,
    total,
    baselineTotal,
  };
}

export function runOxlint(webDir, { typeAware = false } = {}) {
  let raw = "";
  try {
    raw = execFileSync(
      // No explicit -c: oxlint auto-discovers .oxlintrc.json from cwd, and a
      // bare-string config path here reads as an unresolved import to knip's
      // static analysis (it flags the literal as an import specifier).
      "npx",
      ["oxlint", ...(typeAware ? ["--type-aware"] : []), "-f", "json", ...LINT_TARGETS],
      { cwd: webDir, encoding: "utf8", maxBuffer: 64 * 1024 * 1024 },
    );
  } catch (err) {
    if (!err.stdout) throw new Error(`oxlint did not run: ${err.message}`);
    raw = err.stdout;
  }
  // oxlint returns a bare array in some versions and { diagnostics } in others.
  const parsed = JSON.parse(raw);
  const findings = Array.isArray(parsed) ? parsed : (parsed.diagnostics ?? []);
  const counts = {};
  for (const f of findings) {
    const code = f.code ?? "unknown";
    counts[code] = (counts[code] ?? 0) + 1;
  }
  return counts;
}

function main() {
  const webDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
  const typeAware = process.argv.includes("--type-aware");
  const baselinePath = path.join(webDir, typeAware ? TYPE_AWARE_BASELINE_FILE : BASELINE_FILE);
  const write = process.argv.includes("--write");
  const reportOnly = process.argv.includes("--report-only");

  const counts = runOxlint(webDir, { typeAware });
  const total = Object.values(counts).reduce((a, b) => a + b, 0);

  if (write) {
    fs.writeFileSync(baselinePath, `${JSON.stringify({ total, counts }, null, 2)}\n`);
    console.log(`oxlint${typeAware ? " type-aware" : ""} baseline written: ${total} findings`);
    return;
  }

  const baseline = fs.existsSync(baselinePath)
    ? JSON.parse(fs.readFileSync(baselinePath, "utf8"))
    : { total: 0, counts: {} };
  const res = compare(baseline, counts);
  const label = typeAware ? "oxlint --type-aware" : "oxlint";

  if (res.ok && res.total === 0) {
    console.log(`${label}: clean`);
    return;
  }

  console.log(`${label}: ${res.total} findings (baseline ${res.baselineTotal})`);
  for (const [rule, n] of Object.entries(counts).sort((a, b) => b[1] - a[1])) {
    console.log(`  ${String(n).padStart(4)}  ${rule}`);
  }

  if (res.ok) {
    console.log("");
    console.log(`${label}: on the baseline, exactly.`);
    return;
  }

  // Report-only says the same things, on stdout, and exits 0.
  const say = reportOnly ? console.log : console.error;
  say("");
  say(
    reportOnly
      ? "oxlint ratchet: the tree and the baseline disagree, but this run is report-only."
      : "oxlint ratchet FAILED: the tree and the baseline disagree.",
  );

  if (res.regressions.length) {
    say("");
    say("New findings that are not in the baseline:");
    for (const r of res.regressions) {
      say(`  ${r.rule}: ${r.was} -> ${r.now}`);
    }
    say("");
    say("Fix them, or if the finding is wrong, suppress it at the call site with");
    say("`// oxlint-disable-next-line <rule>` and a comment saying why. Note the");
    say("directive applies to the line DIRECTLY below it: an explanation placed");
    say("underneath it silently consumes the directive and suppresses nothing.");
  }

  if (res.improvements.length) {
    say("");
    say("Findings below the baseline, so the baseline is now stale:");
    for (const i of res.improvements) {
      say(`  ${i.rule}: ${i.was} -> ${i.now}`);
    }
    say("");
    say("The baseline has to follow the work down, or it leaves room for those");
    say("findings to come back with this gate still green. Lock the improvement in:");
    say(`  ${refreshCommand(typeAware)}`);
  }

  if (reportOnly) {
    console.log("");
    console.log("oxlint ratchet: report-only run, not gating.");
    return;
  }
  process.exit(1);
}


// Only run when invoked directly, so the test can import the pure helpers.
if (process.argv[1] && fileURLToPath(import.meta.url) === path.resolve(process.argv[1])) {
  main();
}
