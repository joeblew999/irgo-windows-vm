# Glaze status

Does glaze work on the Mac and on Windows? The last recorded answer for each,
written by `irgo-winvm glaze-check` (`mise run glaze:mac` and
`mise run glaze:windows`) and read back by `irgo-winvm glaze-status`,
which also says whether it still describes the tree. Generated: do not edit it by
hand. Each run replaces only its own section. Every row is one test of
`examples/conformance`, from its test2json events; what each checks is in its
comment, and how the suite runs is in [CONTRIBUTING.md](CONTRIBUTING.md#does-glaze-work).

<!-- glaze-status:mac commit=22166ad28ddd6e17676efd93eb0e0d77e5a27013 examples-dirty=false glaze=v0.0.61 native=v0.1.15 -->
## On the Mac — YES: 28 passed, 1 skipped

- when: 2026-09-30 15:33 +0700, took 11s
- platform: darwin/arm64 (this machine, natively)
- this repository: commit `22166ad28ddd`
- glaze v0.0.61 (released)
- native v0.1.15 (released)
- full log: `~/Library/Application Support/irgo-winvm/logs/glaze-mac-20260930-153349.log`
- test2json events: `~/Library/Application Support/irgo-winvm/logs/glaze-mac-20260930-153349.json`
- screenshots: 7 of the 7 tests that open a window took one — see [Screenshots](#screenshots)

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
| TestNoCapture | skip | `windowed_test.go:190: nocapture is unsupported on darwin by design: nocapture: not supported on this platform` |
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

## Screenshots

Every test that opens a window photographs it at the moment that shows what it checked — the page loaded, the tray up, the menu installed, the dialog open — and the picture is recorded with the run it came from. A capture that failed says why instead of showing a picture; a black or one-colour frame counts as failed. How each is taken is in `examples/conformance/shots_test.go`.

- Mac: 2026-09-30 15:33 +0700, commit `22166ad28ddd`, darwin/arm64 (this machine, natively)
- Windows: no pictures recorded yet

| test | Mac | Windows |
|---|---|---|
| TestEvents | PASS<br><a href="screens/conformance/mac/TestEvents.png"><img src="screens/conformance/mac/TestEvents.png" width="280" alt="TestEvents on Mac"></a> | — |
| TestAppScheme | PASS<br><a href="screens/conformance/mac/TestAppScheme.png"><img src="screens/conformance/mac/TestAppScheme.png" width="280" alt="TestAppScheme on Mac"></a> | — |
| TestTray/running | PASS<br><a href="screens/conformance/mac/TestTray_running.png"><img src="screens/conformance/mac/TestTray_running.png" width="280" alt="TestTray/running on Mac"></a><br><sub>the window the tray was started beside; the status item is drawn outside this process and its window could not be captured: screencapture -o -l 4294967296: exit status 1: could not create image from window</sub> | — |
| TestMenu | PASS<br><a href="screens/conformance/mac/TestMenu.png"><img src="screens/conformance/mac/TestMenu.png" width="280" alt="TestMenu on Mac"></a><br><sub>the window the menu was installed for; macOS draws the menu bar outside this process, and a capture of that part of the screen shows the desktop behind it, not the menus</sub> | — |
| TestNoCapture | skip<br><a href="screens/conformance/mac/TestNoCapture.png"><img src="screens/conformance/mac/TestNoCapture.png" width="280" alt="TestNoCapture on Mac"></a> | — |
| TestAppIcon | PASS<br><a href="screens/conformance/mac/TestAppIcon.png"><img src="screens/conformance/mac/TestAppIcon.png" width="280" alt="TestAppIcon on Mac"></a> | — |
| TestFileDialog | PASS<br><a href="screens/conformance/mac/TestFileDialog.png"><img src="screens/conformance/mac/TestFileDialog.png" width="280" alt="TestFileDialog on Mac"></a> | — |
