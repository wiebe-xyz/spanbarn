# Implementation Plan: [FEATURE]

**Spec**: [link to spec.md] | **Issue**: #[n] | **Date**: [DATE]

<!--
Filled during refinement. The issue's "Implementation Details" become Technical
Context and Project Structure; its "Execution Plan" becomes Approach.
-->

## Summary

[One paragraph: the technical approach and why.]

## Technical Context

**Packages touched**: [e.g. internal/repository, internal/api, web/src]
**Languages**: [Go / TypeScript (web, sdks/js) / Python (sdks/python)]
**Storage**: [main, spans.db or a shard family; new tables; Redis keys]
**New migrations**: [yes/no; which track: main, spans or shard]
**Testing**: [go test, SPANBARN_TEST_LAYOUT=split, vitest, Playwright]

## Constitution Check

Answer each line for this change, or mark it not applicable with a reason.

- [ ] I. Ingest stays fire-and-forget: nothing on the ingest path blocks the instrumented application
- [ ] II. A repository method that writes touches one storage family
- [ ] III. Layering holds: no new `.golangci.yml` exclusion, no new boundaries baseline entry
- [ ] IV. Ratchet metrics (coverage, complexity, file length, duplication) do not worsen
- [ ] V. Secrets and keys stay hashed, sessions stay server-side, new endpoints carry auth and a test
- [ ] VI. Error-level logs go through `slog` so the self-reporting handler sees them
- [ ] VII. Ships through test and staging, promoted by tag, with the version smoke

## Approach

1. [Step, from the issue's Execution Plan]

## Project Structure

```text
[files to add or change]
```

## Risk Log

| Risk | Impact | Mitigation |
|------|--------|------------|
| [risk] | [impact] | [mitigation] |
