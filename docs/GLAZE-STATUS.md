# Glaze status

Does glaze work on the Mac and on Windows? The last recorded answer for each,
written by `irgo-winvm glaze-check` (`mise run glaze:mac` and
`mise run glaze:windows`) and read back by `irgo-winvm glaze-status`,
which also says whether it still describes the tree. Generated: do not edit it by
hand. Each run replaces only its own section. What each program checks is in
[CONTRIBUTING.md](CONTRIBUTING.md#does-glaze-work).

<!-- glaze-status:mac commit=8643df82d0c01699ff9301d14cfe71666a0cd53d examples-dirty=false glaze=v0.0.61 native=v0.1.15 -->
## On the Mac — YES: all 4 passed

- when: 2026-09-30 14:01 +0700, took 12s
- platform: darwin/arm64 (this machine, natively)
- this repository: commit `8643df82d0c0`
- glaze v0.0.61 (released)
- native v0.1.15 (released)
- full log: `~/Library/Application Support/irgo-winvm/logs/glaze-mac-20260930-140114.log`

| program | result | first failure |
|---|---|---|
| probe | PASS |  |
| verify | PASS |  |
| verify-events | PASS |  |
| glaze-all | PASS |  |
<!-- /glaze-status:mac -->

<!-- glaze-status:windows commit=8643df82d0c01699ff9301d14cfe71666a0cd53d examples-dirty=false glaze=v0.0.61 native=v0.1.15 -->
## On Windows — NO: failed: verify

- when: 2026-09-30 14:01 +0700, took 1m0s
- platform: windows/arm64, VM irgo-win11 (through app-create)
- this repository: commit `8643df82d0c0`
- glaze v0.0.61 (released)
- native v0.1.15 (released)
- full log: `~/Library/Application Support/irgo-winvm/logs/glaze-windows-20260930-140147.log`

| program | result | first failure |
|---|---|---|
| probe | PASS |  |
| verify | **FAIL** | `FAIL: absolute app:// sub-resources never loaded (glaze bug, docs/UPSTREAM.md §1b); relative ones did: msg="ABSOLUTE-SUBRESOURCES-FAILED" n=-1` |
| verify-events | PASS |  |
| glaze-all | PASS |  |
<!-- /glaze-status:windows -->
