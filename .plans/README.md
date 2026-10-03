# Plans

One file per plan, named `YYYY-MM-DD_HHMM_slug.md`. Finished plans move to `done/`.
A plan must stand on its own: symptom, evidence, cause, the exact change, and how to verify it.

## Work order

Every workstream has a plan here — running, queued, or waiting on the owner. Finished plans are in
[`done/`](done/), each ending with a "Closed" line saying what was delivered.

| # | Plan | Owner | State |
|---|---|---|---|
| 1 | [`2026-10-01_1200_follow-ups.md`](2026-10-01_1200_follow-ups.md) — small items left by the 1 Oct round | main session / agents | open |
| 3 | [`2026-10-01_1110_utmvm-professional-pass.md`](2026-10-01_1110_utmvm-professional-pass.md) — utmvm cleanup | agent | queued |
| 4 | [`2026-09-30_2000_utm-5.md`](2026-09-30_2000_utm-5.md) — stay on UTM 4.7.5; trial 5.x on a disposable VM | agent | queued |
| 5 | [`2026-09-30_1745_glaze-1b-upstream.md`](2026-09-30_1745_glaze-1b-upstream.md) — glaze §1b fix on a branch; Windows run, then file | owner + VM | waiting: Windows run, owner's "send" |
| 6 | [`2026-09-30_1800_upstream-reports.md`](2026-09-30_1800_upstream-reports.md) — nine drafted upstream reports | owner | waiting: "send them" |
| 7 | [`2026-10-02_1950_linux-vms.md`](2026-10-02_1950_linux-vms.md) — Linux VMs in the same tool, for claude-rig: cloud image, one guest seam, phases | owner, then agent | waiting: owner's decisions (the distribution, before phase 1) |
| 8 | [`2026-10-03_1200_docs-site-charter.md`](2026-10-03_1200_docs-site-charter.md) — charter's GitHub Pages site in place of docsite: what depends on docsite, and the steps | owner | waiting: owner's decision |

Earlier work is in Done: [`done/`](done/) — 1 Oct: golden image, release v0.5.0, shared Mac, ledger, VM conformance, native + drive, remote driving, capacity, docs, Worker API; before that: upstream workflow restored (`upstream:*`), glaze#34 reported,
VM hardened (`vm-repair`, never-expiring password, fail-fast `-gui`).

## Working on glaze or native

See [docs/TESTING.md](../docs/TESTING.md#does-glaze-work) — not repeated here.
