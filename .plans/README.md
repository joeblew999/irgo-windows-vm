# Plans

One file per plan, named `YYYY-MM-DD_HHMM_slug.md`. Finished plans move to `done/`.
A plan must stand on its own: symptom, evidence, cause, the exact change, and how to verify it.

## Work order

| # | Plan | State |
|---|---|---|
| 1 | [`2026-09-30_1500_fast-dev-cycle.md`](2026-09-30_1500_fast-dev-cycle.md) — macOS-first loop, batched Windows gate, build tool once, poll not sleep, full logs, `vm:repair:test` | in progress — 1 and 4 done (`go:tool`, `glaze:mac`); 2 half done (`glaze:windows`, one verdict, still one push per binary) |

Earlier work is in Done: [`done/`](done/) — upstream workflow restored (`upstream:*`), glaze#34 reported,
VM hardened (`vm-repair`, never-expiring password, fail-fast `-gui`).

## Working on glaze or native

See [docs/CONTRIBUTING.md](../docs/CONTRIBUTING.md#does-glaze-work) — not repeated here.
