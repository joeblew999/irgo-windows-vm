# Plans

One file per plan, named `YYYY-MM-DD_HHMM_slug.md`. Finished plans move to `done/`.
A plan must stand on its own: symptom, evidence, cause, the exact change, and how to verify it.

## Work order

| # | Plan | State |
|---|---|---|
| 0 | [`2026-09-30_1730_fix-it-all.md`](2026-09-30_1730_fix-it-all.md) — the whole work order and which agent owns each part; one agent on the VM at a time | in progress |
| 1 | [`2026-09-30_1500_fast-dev-cycle.md`](2026-09-30_1500_fast-dev-cycle.md) — macOS-first loop, batched Windows gate, build tool once, poll not sleep, full logs, `vm:repair:test` | mostly done — `go:tool`, `glaze:mac`, logs, compressed pushes, polling; left: guest-pull transfer (firewall) |
| 2 | [`2026-09-30_1700_vm-golden-image.md`](2026-09-30_1700_vm-golden-image.md) — ready VM in minutes: seal a golden image once per machine, APFS-clone it per agent (0 s), optional private R2 cache; no public distribution (Windows licence); BitLocker must be off first | phase 1 code done (agent H), merged only after the disposable-VM run |
| 3 | [`2026-09-30_1900_robotgo-evaluation.md`](2026-09-30_1900_robotgo-evaluation.md) — can go-vgo/robotgo help? No: no single-window capture, focus-dependent input, cgo by default and untested purego backends; optional follow-up: find the UTM window ID with purego instead of `swift` | evaluated, not adopted |
| 4 | [`2026-09-30_2000_utm-5.md`](2026-09-30_2000_utm-5.md) — UTM 5.0.x are betas; 4.7.5 is the latest stable. Config format, `utmctl` commands, clone and AppleScript are unchanged in 5.0.6; none of our five `utmctl` findings is fixed; the risks are GICv3, the new 3D QEMU arguments, the new guest-tools ISO and open Win11 boot bugs. Stay on 4.7.5, trial on a disposable VM, adopt at stable. `doctor` now reports UTM updates; `vm-create` installs stable only, tested | assessed; code done; trial not run (needs the VM free) |

Earlier work is in Done: [`done/`](done/) — upstream workflow restored (`upstream:*`), glaze#34 reported,
VM hardened (`vm-repair`, never-expiring password, fail-fast `-gui`).

## Working on glaze or native

See [docs/CONTRIBUTING.md](../docs/CONTRIBUTING.md#does-glaze-work) — not repeated here.
