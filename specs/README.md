# Specs

One spec per product area. Each spec states the behaviour SpanBarn ships, links
every functional requirement to the issue that asked for it and the test that
proves it, and records in its `## Drift` section where a document or issue
disagrees with the code. The code is the fact.

The principles every spec answers to are in the
[constitution](../.specify/memory/constitution.md). The templates are in
[`.specify/templates/`](../.specify/templates/).

All specs were last verified against the tree on 2026-10-17 at `41a73f7`.

## Index

| Spec | Status | What it covers |
|---|---|---|
| [001 Telemetry aggregator](./001-telemetry-aggregator/spec.md) | Partial | Ingest (OTLP and native JSON), the spool, retention and aggregation, the dashboard and query APIs, dependencies, alerts and SLOs, the SDKs, auth, and the integrations. Also holds the [plan](./001-telemetry-aggregator/plan.md), [tasks](./001-telemetry-aggregator/tasks.md), [data model](./001-telemetry-aggregator/data-model.md) and the [ingest contract](./001-telemetry-aggregator/contracts/ingest-api.yaml). |
| [002 Temporal sharding](./002-temporal-sharding/spec.md) | Shipped | The span file split, the cut-over of existing databases, time shards for logs, metrics and prompts, shard-aware reads and retention, disk reclaim. The rationale and spike are in [design.md](./002-temporal-sharding/design.md). |

## Workflow

1. **A feature starts as a spec.** Before code, write a new
   `specs/NNN-<feature>/spec.md` from the spec template, or change the spec of
   the area it belongs to. The issue links to that spec. The issue's "What" and
   "Why" become the Purpose, and its acceptance criteria become acceptance
   scenarios and FR-NNN lines.
2. **Refinement fills the plan and the tasks.** The issue's "Implementation
   Details" and "Execution Plan" go into `plan.md`, including the Constitution
   Check. The issue's checklist goes into `tasks.md`, one task per line, each
   naming the FR it serves. Small changes can skip `plan.md` and `tasks.md` when
   the issue already holds them.
3. **A PR that changes behaviour updates its spec.** The same PR adds or edits
   the FR lines, links the new tests, moves resolved items out of Open
   Questions, and adds or clears Drift rows. A reviewer can then check each
   requirement against a test.
4. **Unbacked statements are marked.** A requirement with no issue and no test
   carries `[inferred]` until one of them exists.
5. **Drift is recorded, then fixed.** When a document and the code disagree,
   add a Drift row and treat the code as the fact. Fix the document, or open an
   issue when the code is the thing that is wrong.

Numbers are never reused or renumbered. A retired area keeps its folder with
`Status: Deferred` or `Superseded` and a pointer to what replaced it. Results
and load-test output belong in `docs/`, not under a spec number.

## Related documents

- [CLAUDE.md](../CLAUDE.md) and [AGENTS.md](../AGENTS.md): working rules for this repository
- [deploy/docs/](../deploy/docs/): disaster recovery, attribute storage design, compacting main
