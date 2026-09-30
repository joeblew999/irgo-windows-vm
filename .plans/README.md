# Plans

One file per plan, named `YYYY-MM-DD_HHMM_slug.md`. Finished plans move to `done/`.
A plan must stand on its own: symptom, evidence, cause, the exact change, and how to verify it.

## Work order

| # | Plan | State |
|---|---|---|
| 1 | [`2026-09-30_1500_fast-dev-cycle.md`](2026-09-30_1500_fast-dev-cycle.md) — macOS-first loop, batched Windows gate, build tool once, poll not sleep, full logs, `vm:repair:test` | in progress — 1 dropped (measured), 4 done (`app:mac`) |

Earlier work is in Done: [`done/`](done/) — upstream workflow restored (`upstream:*`), glaze#34 reported,
VM hardened (`vm-repair`, never-expiring password, fail-fast `-gui`).

## Working on glaze or native

```sh
mise run upstream:clone && mise run upstream:verify
mise run app:mac               # all four probes natively on the Mac, ~15 s, while linked
mise run upstream:lint && mise run upstream:test:windows
mise run app:create:verify     # probes on the VM; if -gui fails: irgo-winvm vm-repair -reboot
mise run upstream:unlink
```
