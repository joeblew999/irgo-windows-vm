# Glaze status

Does glaze work on the Mac and on Windows? The last recorded answer for each,
written by `irgo-winvm glaze-check` (`mise run glaze:mac` and
`mise run glaze:windows`) and read back by `irgo-winvm glaze-status`,
which also says whether it still describes the tree. Generated: do not edit it by
hand. Each run replaces only its own section. Every row is one test of
`examples/conformance`, from its test2json events; what each checks is in its
comment, and how the suite runs is in [CONTRIBUTING.md](CONTRIBUTING.md#does-glaze-work).

<!-- glaze-status:mac commit=55a25b05f1a6d79775f5a5a2adf90378d7b28a68 examples-dirty=true glaze=v0.0.61 native=github.com/joeblew999/native@v0.1.16-0.20261001015633-2fbbf2d09e65 -->
## On the Mac — YES: 40 passed, 1 skipped

- when: 2026-10-01 09:08 +0700, took 20s
- platform: darwin/arm64 (this machine, natively)
- this repository: commit `55a25b05f1a6`, **with uncommitted changes**
- glaze v0.0.61 (released)
- native from the fork github.com/joeblew999/native@v0.1.16-0.20261001015633-2fbbf2d09e65 (go.mod requires v0.1.15 and replaces it)
- full log: `~/Library/Application Support/irgo-winvm/logs/glaze-mac-20261001-090850.log`
- test2json events: `~/Library/Application Support/irgo-winvm/logs/glaze-mac-20261001-090850.json`
- screenshots: 14 of the 14 tests that open a window took one — see [Screenshots](#screenshots)

| test | result | first message |
|---|---|---|
| TestDriveType | PASS |  |
| TestDriveType/before | PASS |  |
| TestDriveType/after | PASS |  |
| TestDriveClick | PASS |  |
| TestDriveClick/before | PASS |  |
| TestDriveClick/after | PASS |  |
| TestDriveClick/script_click_is_untrusted | PASS |  |
| TestDriveClickAt | PASS |  |
| TestDriveClickAt/clicked | PASS |  |
| TestDriveScroll | PASS |  |
| TestDriveScroll/before | PASS |  |
| TestDriveScroll/after | PASS |  |
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

<!-- glaze-status:windows commit=f16189091b5ee7d1eadc6e5f1668686233bbf95f examples-dirty=false glaze=v0.0.61 native=v0.1.15 -->
## On Windows — KNOWN BUGS ONLY: TestAppScheme/absolute_subresources (docs/UPSTREAM.md §1b) fail, known upstream bugs listed in docs/UPSTREAM.md; nothing else did

- when: 2026-09-30 15:43 +0700, took 30s
- platform: windows/arm64, VM irgo-win11 (through app-create -gui)
- this repository: commit `f16189091b5e`
- glaze v0.0.61 (released)
- native v0.1.15 (released)
- full log: `~/Library/Application Support/irgo-winvm/logs/glaze-windows-20260930-154316.log`
- test2json events: `~/Library/Application Support/irgo-winvm/logs/glaze-windows-20260930-154316.json`
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
| TestAppScheme | fail (a subtest failed) |  |
| TestAppScheme/js_calls_go | PASS |  |
| TestAppScheme/relative_subresources | PASS |  |
| TestAppScheme/absolute_subresources | **FAIL** — known upstream: docs/UPSTREAM.md §1b | `scheme_test.go:206: app://home/abs.js did not run; the scheme handler was asked for it: false. The document's origin is https://app.localhost — on Windows this is glaze bug docs/UPSTREAM.md §1b: the scheme is emulated with a virtual host, so an absolute app:// URL inside the page names a scheme WebView2 does not know` |
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
| TestAppIcon | skip | `windowed_test.go:201: appicon is unsupported on windows by design: glaze: setting the application icon at runtime is not supported on this platform` |
| TestFileDialog | PASS |  |
<!-- /glaze-status:windows -->

## Screenshots

Every test that opens a window photographs it at the moment that shows what it checked — the page loaded, the tray up, the menu installed, the dialog open — and the picture is recorded with the run it came from. A capture that failed says why instead of showing a picture; a black or one-colour frame counts as failed. How each is taken is in `examples/conformance/shots_test.go`.

- Mac: 2026-10-01 09:08 +0700, commit `55a25b05f1a6`, darwin/arm64 (this machine, natively)
- Windows: 2026-09-30 15:43 +0700, commit `f16189091b5e`, windows/arm64, VM irgo-win11 (through app-create -gui)

| test | Mac | Windows |
|---|---|---|
| TestDriveType/before | PASS<br><a href="screens/conformance/mac/TestDriveType_before.png"><img src="screens/conformance/mac/TestDriveType_before.png" width="280" alt="TestDriveType/before on Mac"></a> | no picture in this run: the test skipped or failed first, or was not in it |
| TestDriveType/after | PASS<br><a href="screens/conformance/mac/TestDriveType_after.png"><img src="screens/conformance/mac/TestDriveType_after.png" width="280" alt="TestDriveType/after on Mac"></a> | no picture in this run: the test skipped or failed first, or was not in it |
| TestDriveClick/before | PASS<br><a href="screens/conformance/mac/TestDriveClick_before.png"><img src="screens/conformance/mac/TestDriveClick_before.png" width="280" alt="TestDriveClick/before on Mac"></a> | no picture in this run: the test skipped or failed first, or was not in it |
| TestDriveClick/after | PASS<br><a href="screens/conformance/mac/TestDriveClick_after.png"><img src="screens/conformance/mac/TestDriveClick_after.png" width="280" alt="TestDriveClick/after on Mac"></a> | no picture in this run: the test skipped or failed first, or was not in it |
| TestDriveClickAt/clicked | PASS<br><a href="screens/conformance/mac/TestDriveClickAt_clicked.png"><img src="screens/conformance/mac/TestDriveClickAt_clicked.png" width="280" alt="TestDriveClickAt/clicked on Mac"></a> | no picture in this run: the test skipped or failed first, or was not in it |
| TestDriveScroll/before | PASS<br><a href="screens/conformance/mac/TestDriveScroll_before.png"><img src="screens/conformance/mac/TestDriveScroll_before.png" width="280" alt="TestDriveScroll/before on Mac"></a> | no picture in this run: the test skipped or failed first, or was not in it |
| TestDriveScroll/after | PASS<br><a href="screens/conformance/mac/TestDriveScroll_after.png"><img src="screens/conformance/mac/TestDriveScroll_after.png" width="280" alt="TestDriveScroll/after on Mac"></a> | no picture in this run: the test skipped or failed first, or was not in it |
| TestEvents | PASS<br><a href="screens/conformance/mac/TestEvents.png"><img src="screens/conformance/mac/TestEvents.png" width="280" alt="TestEvents on Mac"></a> | PASS<br><a href="screens/conformance/windows/TestEvents.png"><img src="screens/conformance/windows/TestEvents.png" width="280" alt="TestEvents on Windows"></a> |
| TestAppScheme | PASS<br><a href="screens/conformance/mac/TestAppScheme.png"><img src="screens/conformance/mac/TestAppScheme.png" width="280" alt="TestAppScheme on Mac"></a> | fail (a subtest failed)<br><a href="screens/conformance/windows/TestAppScheme.png"><img src="screens/conformance/windows/TestAppScheme.png" width="280" alt="TestAppScheme on Windows"></a> |
| TestTray/running | PASS<br><a href="screens/conformance/mac/TestTray_running.png"><img src="screens/conformance/mac/TestTray_running.png" width="280" alt="TestTray/running on Mac"></a><br><sub>the window the tray was started beside; the status item is drawn outside this process and its window could not be captured: screencapture -o -l 4294967296: exit status 1: could not create image from window</sub> | PASS<br><a href="screens/conformance/windows/TestTray_running.png"><img src="screens/conformance/windows/TestTray_running.png" width="280" alt="TestTray/running on Windows"></a> |
| TestMenu | PASS<br><a href="screens/conformance/mac/TestMenu.png"><img src="screens/conformance/mac/TestMenu.png" width="280" alt="TestMenu on Mac"></a><br><sub>the window the menu was installed for; macOS draws the menu bar outside this process, and a capture of that part of the screen shows the desktop behind it, not the menus</sub> | PASS<br><a href="screens/conformance/windows/TestMenu.png"><img src="screens/conformance/windows/TestMenu.png" width="280" alt="TestMenu on Windows"></a> |
| TestNoCapture | skip<br><a href="screens/conformance/mac/TestNoCapture.png"><img src="screens/conformance/mac/TestNoCapture.png" width="280" alt="TestNoCapture on Mac"></a> | PASS<br><a href="screens/conformance/windows/TestNoCapture.png"><img src="screens/conformance/windows/TestNoCapture.png" width="280" alt="TestNoCapture on Windows"></a><br><sub>black: the window is excluded from capture, which is what Protect is for</sub> |
| TestAppIcon | PASS<br><a href="screens/conformance/mac/TestAppIcon.png"><img src="screens/conformance/mac/TestAppIcon.png" width="280" alt="TestAppIcon on Mac"></a> | skip<br><a href="screens/conformance/windows/TestAppIcon.png"><img src="screens/conformance/windows/TestAppIcon.png" width="280" alt="TestAppIcon on Windows"></a> |
| TestFileDialog | PASS<br><a href="screens/conformance/mac/TestFileDialog.png"><img src="screens/conformance/mac/TestFileDialog.png" width="280" alt="TestFileDialog on Mac"></a> | PASS<br><a href="screens/conformance/windows/TestFileDialog.png"><img src="screens/conformance/windows/TestFileDialog.png" width="280" alt="TestFileDialog on Windows"></a> |
