# Plans

One file per plan, named `YYYY-MM-DD_HHMM_slug.md`. Finished plans move to `done/`.
A plan must stand on its own: symptom, evidence, cause, the exact change, and how to verify it.

## Work order

Every workstream has a plan here — running, queued, or waiting on the owner. Agents named are the
main session's background agents; the main session reviews and merges their branches.

| # | Plan | Owner | State |
|---|---|---|---|
| 0 | [`2026-09-30_1730_fix-it-all.md`](2026-09-30_1730_fix-it-all.md) — index of all work, "Resume here" | main session | living |
| 1 | [`2026-10-01_1000_release-prime-time.md`](2026-10-01_1000_release-prime-time.md) — install, first run, golden auto-pull, agent guide, v0.5.0 | main session | merged; tag v0.5.0 after Z/AA/BB/CC; live-test golden auto-pull |
| 2 | [`2026-10-01_1020_shared-mac.md`](2026-10-01_1020_shared-mac.md) — VM ownership, leases, reaping, resource guard, per-VM staging | agent Z | in progress |
| 3 | [`2026-10-01_1030_worker-ledger.md`](2026-10-01_1030_worker-ledger.md) — D1 ledger of agents/machines/VMs, dashboard | agent AA | in progress |
| 4 | [`2026-10-01_1040_vm-conformance.md`](2026-10-01_1040_vm-conformance.md) — the glaze suite's machinery pointed at the VM; VM-STATUS.md | agent BB | in progress |
| 5 | [`2026-10-01_1100_remote-cross-platform.md`](2026-10-01_1100_remote-cross-platform.md) — Windows/Linux/GitHub clients drive a Mac through the Worker | agent CC | starting |
| 5a | [`2026-10-01_1130_vm-capacity.md`](2026-10-01_1130_vm-capacity.md) — disk/RAM budget, quotas, retention, capacity in CLI/MCP/Worker | agent DD | starting |
| 5b | [`2026-10-01_1140_docs-restructure.md`](2026-10-01_1140_docs-restructure.md) — docs by audience, single source per topic | agent EE | starting (merges last) |
| 6 | [`2026-10-01_1010_issue-intake-and-triage.md`](2026-10-01_1010_issue-intake-and-triage.md) — report command, forms, labels done; triage routine next | main session | intake done; routine next |
| 7 | [`2026-10-01_1050_native-input-and-drive.md`](2026-10-01_1050_native-input-and-drive.md) — fork PRs #1/#2, examples/drive, Windows CI reliable | — | done; owner merges fork PRs |
| 8 | [`2026-09-30_1700_vm-golden-image.md`](2026-09-30_1700_vm-golden-image.md) — golden image, 23 s per VM, private R2 cache via the Worker | — | done (auto-pull in plan 1) |
| 9 | [`2026-10-01_1110_utmvm-professional-pass.md`](2026-10-01_1110_utmvm-professional-pass.md) — utmvm cleanup | queued | after plans 2 and 4 |
| 10 | [`2026-09-30_2000_utm-5.md`](2026-09-30_2000_utm-5.md) — stay on UTM 4.7.5; trial 5.x on a disposable VM | queued | needs a free VM slot |
| 11 | [`2026-09-30_1745_glaze-1b-upstream.md`](2026-09-30_1745_glaze-1b-upstream.md) — glaze §1b fix on a branch; Windows run, then file | owner + VM | waiting: Windows run, owner's "send" |
| 12 | [`2026-09-30_1800_upstream-reports.md`](2026-09-30_1800_upstream-reports.md) — nine drafted upstream reports | owner | waiting: "send them" |
| 13 | [`2026-09-30_1500_fast-dev-cycle.md`](2026-09-30_1500_fast-dev-cycle.md) — fast loop | — | done except guest-pull (superseded by SMB push) |

Earlier work is in Done: [`done/`](done/) — upstream workflow restored (`upstream:*`), glaze#34 reported,
VM hardened (`vm-repair`, never-expiring password, fail-fast `-gui`).

## Working on glaze or native

See [docs/CONTRIBUTING.md](../docs/CONTRIBUTING.md#does-glaze-work) — not repeated here.
