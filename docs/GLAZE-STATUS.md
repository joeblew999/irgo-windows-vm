# Glaze status

Does glaze work on the Mac and on Windows? The last recorded answer for each,
written by `irgo-winvm glaze-check` (`mise run glaze:mac` and
`mise run glaze:windows`) and read back by `irgo-winvm glaze-status`,
which also says whether it still describes the tree. Generated: do not edit it by
hand. Each run replaces only its own section. Every row is one test of
`examples/conformance`, from its test2json events; what each checks is in its
comment, and how the suite runs is in [CONTRIBUTING.md](CONTRIBUTING.md#does-glaze-work).

<!-- glaze-status:mac commit=0870aec6de077bd3cdb359814bd187e4f06f4256 examples-dirty=false glaze=v0.0.61 native=v0.1.15 -->
## On the Mac — YES: 28 passed, 1 skipped

- when: 2026-09-30 15:13 +0700, took 3s
- platform: darwin/arm64 (this machine, natively)
- this repository: commit `0870aec6de07`
- glaze v0.0.61 (released)
- native v0.1.15 (released)
- full log: `~/Library/Application Support/irgo-winvm/logs/glaze-mac-20260930-151330.log`
- test2json events: `~/Library/Application Support/irgo-winvm/logs/glaze-mac-20260930-151330.json`

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

<!-- glaze-status:windows commit=8cda2ecf105f14262ad4b9d7e194784e1bfecd1d examples-dirty=false glaze=v0.0.61 native=v0.1.15 -->
## On Windows — KNOWN BUGS ONLY: TestAppScheme/absolute_subresources (docs/UPSTREAM.md §1b) fail, known upstream bugs listed in docs/UPSTREAM.md; nothing else did

- when: 2026-09-30 15:21 +0700, took 30s
- platform: windows/arm64, VM irgo-win11 (through app-create -gui)
- this repository: commit `8cda2ecf105f`
- glaze v0.0.61 (released)
- native v0.1.15 (released)
- full log: `~/Library/Application Support/irgo-winvm/logs/glaze-windows-20260930-152124.log`
- test2json events: `~/Library/Application Support/irgo-winvm/logs/glaze-windows-20260930-152124.json`

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
| TestAppScheme | fail (a subtest failed) |  |
| TestAppScheme/js_calls_go | PASS |  |
| TestAppScheme/relative_subresources | PASS |  |
| TestAppScheme/absolute_subresources | **FAIL** — known upstream: docs/UPSTREAM.md §1b | `scheme_test.go:197: app://home/abs.js did not run; the scheme handler was asked for it: false. The document's origin is https://app.localhost — on Windows this is glaze bug docs/UPSTREAM.md §1b: the scheme is emulated with a virtual host, so an absolute app:// URL inside the page names a scheme WebView2 does not know` |
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
| TestNoCapture | PASS |  |
| TestAppIcon | skip | `windowed_test.go:191: appicon is unsupported on windows by design: glaze: setting the application icon at runtime is not supported on this platform` |
| TestFileDialog | PASS |  |
<!-- /glaze-status:windows -->
