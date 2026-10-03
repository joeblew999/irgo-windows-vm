// Package conformance is the test suite that answers "does glaze work?" — on
// this Mac, on the Windows VM, and on GitHub's hosted macOS and Windows ARM64
// runners — with the same tests in every place.
//
// There is no code here, only tests. They exercise glaze and native the way a
// desktop app does: a real window, a real run loop, real WebView2 or WKWebView,
// the real clipboard. What each test proves, and what it deliberately does not,
// is in its own comment.
//
// Run it:
//
//	go -C examples test ./conformance            # everything, on this Mac: opens windows
//	go -C examples test -short ./conformance     # the headless tests only
//	mise run glaze:mac                           # the same, recorded in docs/GLAZE-STATUS.md
//	mise run glaze:windows                       # the same binary, cross-compiled, on the VM
//
// On Windows the suite is compiled with `go test -c` and the .test.exe is run
// in the VM's desktop session by `irgo-winvm app-create -gui`, with
// -test.v=test2json; internal/glazecheck turns what it printed into test2json
// events on the host and records every test's result. Nothing greps for FAIL.
//
// A test skips only for two reasons, and says which: -short (the test needs a
// desktop session), or the package reported its own ErrUnsupported on an OS
// where it is documented as unsupported. The same error on an OS where the
// capability is supposed to work is a failure, not a skip — see unsupported.
//
// A known upstream bug is not skipped either. It fails, under a name that says
// what broke, and internal/glazecheck knows which failures are already reported
// upstream (glazecheck.KnownUpstream) so a run can tell "still broken the known
// way" from "broken in a new way".
//
// Linux is out of scope for this repository (docs/concepts/architecture.md), so every
// test file is built for darwin and windows only; on Linux this package has no
// tests rather than tests that fail for want of a display.
package conformance
