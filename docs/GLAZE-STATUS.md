# Glaze status

Does glaze work on the Mac and on Windows? The last recorded answer for each,
written by `irgo-winvm glaze-check` (`mise run glaze:mac` and
`mise run glaze:windows`) and read back by `irgo-winvm glaze-status`,
which also says whether it still describes the tree. Generated: do not edit it by
hand. Each run replaces only its own section. Every row is one test of
`examples/conformance`, from its test2json events; what each checks is in its
comment, and how the suite runs is in [CONTRIBUTING.md](CONTRIBUTING.md#does-glaze-work).

<!-- glaze-status:mac commit=e3899f1f7a49d01848c8d1b10cefa6b9d7f6f957 examples-dirty=false glaze=v0.0.61 native=v0.1.15 -->
## On the Mac — YES: 28 passed, 1 skipped

- when: 2026-09-30 14:57 +0700, took 4s
- platform: darwin/arm64 (this machine, natively)
- this repository: commit `e3899f1f7a49`
- glaze v0.0.61 (released)
- native v0.1.15 (released)
- full log: `~/Library/Application Support/irgo-winvm/logs/glaze-mac-20260930-145720.log`
- test2json events: `~/Library/Application Support/irgo-winvm/logs/glaze-mac-20260930-145720.json`

| test | result | first message |
|---|---|---|
| TestEvents | PASS |  |
| TestEvents/js_to_go | PASS |  |
| TestEvents/go_to_js_unsolicited | PASS |  |
| TestClipboard | PASS |  |
| TestPowerPreventSleep | PASS |  |
| TestSingleInstance | PASS |  |
| TestSingleInstance/second_acquire_refused | PASS |  |
| TestSingleInstance/send_reaches_holder | PASS |  |
| TestSingleInstance/release_frees_it | PASS |  |
| TestMmap | PASS |  |
| TestAppScheme | PASS |  |
| TestAppScheme/js_calls_go | PASS |  |
| TestAppScheme/relative_subresources | PASS |  |
| TestAppScheme/absolute_subresources | PASS |  |
| TestAppScheme/origin_capabilities | PASS |  |
| TestOpenURL | PASS |  |
| TestOpenURL/refuses_custom_protocol | PASS |  |
| TestOpenURL/refuses_javascript | PASS |  |
| TestOpenURL/refuses_app_scheme | PASS |  |
| TestOpenURL/refuses_no_scheme | PASS |  |
| TestOpenURL/refuses_empty | PASS |  |
| TestOpenURL/reveal_refuses_missing_path | PASS |  |
| TestTray | PASS |  |
| TestTray/running | PASS |  |
| TestTray/stop_removes_it | PASS |  |
| TestMenu | PASS |  |
| TestNoCapture | skip | `windowed_test.go:172: nocapture is unsupported on darwin by design: nocapture: not supported on this platform` |
| TestAppIcon | PASS |  |
| TestFileDialog | PASS |  |
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
