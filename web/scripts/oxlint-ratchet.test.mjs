// The ratchet's own test. A gate that has quietly stopped failing looks exactly
// like a clean repo, which is why it is tested before it is trusted — same
// shape as this repo's other ratchets (file-length, i18n literals).
//
// `compare` is the whole decision, so it is the whole test surface: running
// oxlint itself is not what can silently break.

import test from "node:test";
import assert from "node:assert/strict";
import {
  compare,
  refreshCommand,
  BASELINE_FILE,
  TYPE_AWARE_BASELINE_FILE,
} from "./oxlint-ratchet.mjs";

const clean = { total: 0, counts: {} };

test("a clean tree against a clean baseline passes", () => {
  const res = compare(clean, {});
  assert.equal(res.ok, true);
  assert.equal(res.total, 0);
  assert.deepEqual(res.regressions, []);
});

test("a NEW rule appearing fails", () => {
  const res = compare(clean, { "react(jsx-key)": 1 });
  assert.equal(res.ok, false, "a new finding must fail the ratchet");
  assert.deepEqual(res.regressions, [{ rule: "react(jsx-key)", was: 0, now: 1 }]);
});

test("an existing rule going UP fails, and names the rule", () => {
  const base = { total: 2, counts: { "react(exhaustive-deps)": 2 } };
  const res = compare(base, { "react(exhaustive-deps)": 3 });
  assert.equal(res.ok, false);
  assert.equal(res.regressions.length, 1);
  assert.equal(res.regressions[0].rule, "react(exhaustive-deps)");
  assert.equal(res.regressions[0].now, 3);
});

test("an unchanged count passes", () => {
  const base = { total: 2, counts: { "react(exhaustive-deps)": 2 } };
  assert.equal(compare(base, { "react(exhaustive-deps)": 2 }).ok, true);
});

// The property the gate was missing: a rule that beats its baseline has to
// fail too. Clearing five findings of a rule baselined at twelve and leaving
// the baseline at twelve is an allowance for those five to come back, and the
// run before this change reported the same green either way.
test("going DOWN fails, so the baseline has to follow the work down", () => {
  const base = { total: 5, counts: { "react(exhaustive-deps)": 5 } };
  const res = compare(base, { "react(exhaustive-deps)": 1 });
  assert.equal(res.ok, false, "a stale allowance must fail, not pass quietly");
  assert.deepEqual(res.improvements, [{ rule: "react(exhaustive-deps)", was: 5, now: 1 }]);
  assert.deepEqual(res.regressions, [], "an improvement is not a regression");
});

test("a rule cleared entirely is an improvement, and still fails", () => {
  const base = { total: 3, counts: { "react(exhaustive-deps)": 3 } };
  const res = compare(base, {});
  assert.equal(res.ok, false);
  assert.equal(res.improvements[0].now, 0);
  assert.deepEqual(res.regressions, []);
});

test("the refresh command names the mode it refreshes", () => {
  // Printed verbatim on every failure, so the fix is one paste. The two modes
  // read and write different baseline files; a command missing --type-aware
  // would refresh the wrong one.
  assert.equal(refreshCommand(false), "node scripts/oxlint-ratchet.mjs --write");
  assert.equal(refreshCommand(true), "node scripts/oxlint-ratchet.mjs --type-aware --write");
});

test("one rule improving does not mask another regressing", () => {
  // The failure mode a naive total-only comparison would miss entirely: the
  // sum is unchanged, but a new defect class has appeared.
  const base = { total: 4, counts: { "react(exhaustive-deps)": 4 } };
  const res = compare(base, { "react(exhaustive-deps)": 2, "react(jsx-key)": 2 });
  assert.equal(res.total, base.total, "totals match, so only a per-rule check can catch this");
  assert.equal(res.ok, false, "a per-rule regression must fail even when the total is flat");
  assert.deepEqual(res.regressions, [{ rule: "react(jsx-key)", was: 0, now: 2 }]);
  assert.deepEqual(res.improvements, [{ rule: "react(exhaustive-deps)", was: 4, now: 2 }]);
});

// The four transitions, stated together, are the ratchet's whole contract.
test("all four transitions land on the right verdict", () => {
  const base = { total: 2, counts: { "react(exhaustive-deps)": 2 } };
  assert.equal(compare(base, { "react(exhaustive-deps)": 3 }).ok, false, "up fails");
  assert.equal(compare(base, { "react(exhaustive-deps)": 1 }).ok, false, "down fails");
  assert.equal(compare(base, { "react(exhaustive-deps)": 2 }).ok, true, "unchanged passes");
  assert.equal(
    compare(base, { "react(exhaustive-deps)": 2, "react(jsx-key)": 1 }).ok,
    false,
    "a new rule fails",
  );
});

test("a missing counts key in the baseline is treated as zero, not a crash", () => {
  const res = compare({}, { "react(jsx-key)": 1 });
  assert.equal(res.ok, false);
});

// `compare` is shared by both modes (syntax and --type-aware), so the tests
// above already cover its behaviour end to end. What's specific to the
// second mode is that it reads and writes a DIFFERENT file — if that ever
// collapsed to one path, a --type-aware run (soaking a non-zero baseline)
// would compare against the zero-tolerance syntax baseline and fail every
// run, or worse, a --write from the type-aware mode would silently loosen
// the syntax gate.
test("the type-aware baseline is a distinct file from the syntax baseline", () => {
  assert.notEqual(TYPE_AWARE_BASELINE_FILE, BASELINE_FILE);
  assert.equal(BASELINE_FILE, "oxlint-baseline.json");
  assert.equal(TYPE_AWARE_BASELINE_FILE, "oxlint-type-aware-baseline.json");
});

test("a type-aware-only rule regresses independently of the syntax baseline", () => {
  // A non-zero type-aware baseline (soak phase) must still catch a NEW
  // regression on top of it, the same way the zero syntax baseline does.
  const typeAwareBaseline = { total: 23, counts: { "typescript(no-floating-promises)": 23 } };
  const res = compare(typeAwareBaseline, { "typescript(no-floating-promises)": 24 });
  assert.equal(res.ok, false);
  assert.deepEqual(res.regressions, [
    { rule: "typescript(no-floating-promises)", was: 23, now: 24 },
  ]);
});
