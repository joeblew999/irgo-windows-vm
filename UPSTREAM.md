# What this repo has found in glaze and native

Every bug here belongs to [crgimenes/glaze](https://github.com/crgimenes/glaze)
or [crgimenes/native](https://github.com/crgimenes/native) and is fixed **there**,
not worked around here. The README states the rule; this file is the ledger.

Each entry has to answer one question before it is listed: *does a correct
consumer, reading only the public documentation, hit this?* If the answer is no
— we called the API wrongly — it is our bug and it is fixed in this repo. Those
are at the bottom, so the distinction stays visible rather than being quietly
rewritten later.

Fixes are prepared in local clones at
`~/workspace/go/src/github.com/crgimenes/{glaze,native}`, which
`the upstream clones` creates and `the upstream clones` makes every
module in this repo build against. **Nothing has been pushed or proposed
upstream yet** — that is a separate, deliberate act.

A fix is not listed as verified here until it has been *run*: their tests, our
tests, and the probe binary built from the edit executing on Windows 11 ARM64
in the VM. `the upstream clones` does the first three, `irgo-winvm app-create`
the last.

---

## 1. glaze — `New` blocks forever if anything ran `NSApp` first

**Severity: high.** Silent infinite hang, no window, no error, nothing logged.

`webview_darwin.go` / `windowInit` enters a temporary `[NSApp run]` and relies
on `applicationDidFinishLaunching:` to stop it. AppKit sends that **once per
process**. Any code that ran `NSApp` before the first `WebView` consumes it —
`native/tray`, an Ebitengine window, any library that raises a Cocoa dialog —
and the temporary loop then has nothing to stop it.

Nothing in glaze's documentation says a window must be created first, and the
failure names nothing:

```
goroutine 1 [syscall, locked to thread]:
  ...objc.ID.Send
  glaze.(*webview).windowInit.func2()       webview_darwin.go:504
  glaze.NewWithOptions(...)                 webview_darwin.go:477
  glaze.New(...)                            webview_darwin.go:448
  main.main()
```

**Reproducer** — `tray.Run` (which stops itself after 1.5s), then `glaze.New`.
Before the fix it never reaches step 3.

**Fix**: ask AppKit whether it has already launched, and if so take the same
path the delegate callback would have taken, minus the stop that has nothing to
stop.

```go
func appFinishedLaunching() bool {
	app := class("NSRunningApplication").Send(sel("currentApplication"))
	if app == 0 {
		return false
	}
	return app.Send(sel("isFinishedLaunching")) != 0
}
```

The body shared with `onApplicationDidFinishLaunching` is split into
`finishLaunching`, so the two paths cannot drift.

**Status**: fixed, with a negative control. The same reproducer binary, built
against the same clone with only this change stashed and restored:

| glaze | result |
|---|---|
| upstream `v0.0.47` | reaches step 2, then hangs indefinitely — killed at 20s |
| patched | reaches step 3 and 4, exits 0 |

macOS-only code, so the Windows VM has nothing to say about it; `go test ./...`
passes in glaze.

---

## 1b. glaze — absolute `app://` URLs silently do not load on Windows

**Severity: high.** A glaze app that works on macOS loses every stylesheet and
script on Windows, with no error anywhere.

This is the bug this whole project exists to find, and it is invisible from a
Mac.

**What happens.** On macOS, WKWebView registers `app` as a real scheme, so
`app://home/app.js` loads. On Windows, `webview2_scheme_windows.go` emulates the
scheme with a virtual host:

```go
func schemeVHost(scheme string) string { return "https://" + scheme + ".localhost" }
...
out := schemeVHost(u.Scheme) + u.Path     // app://home/index.html -> https://app.localhost/index.html
```

So the document loads from `https://app.localhost/`, and an absolute
`app://home/app.js` inside it names a scheme WebView2 has never heard of. The
request is never made. No error, no console message — just a page with no CSS
and no JavaScript.

**Measured**, Windows 11 ARM64, by `glaze-probes/verify` loading the same asset
twice, once absolutely and once relatively:

```
scheme handler served: app://home/index.html -> text/html
scheme handler served: app://home/rel.css    -> text/css        <- relative: arrives
scheme handler served: app://home/rel.js     -> text/javascript <- relative: arrives
scheme handler served: app://home/favicon.ico -> text/html
                                                                <- app://home/app.css: NEVER REQUESTED
                                                                <- app://home/app.js:  NEVER REQUESTED
origin:        https://app.localhost
href:          https://app.localhost/index.html
secureContext: true
```

`favicon.ico` arriving is the tell: the browser resolves it against the
document's real origin, so it matches the handler, while the absolute URLs
written by the developer do not.

**Two further consequences of the same rewrite:**

- **`location.origin` differs by platform** — `app://home` on macOS,
  `https://app.localhost` on Windows. Any origin-dependent code diverges.
- **The URL's host is dropped.** `app://home/x` and `app://other/x` both become
  `https://app.localhost/x`, so two hosts collide silently.

**The fix** is to stop emulating: WebView2 supports real custom schemes through
`ICoreWebView2EnvironmentOptions4::SetCustomSchemeRegistrations`, with
`HasAuthorityComponent` for the host and `TreatAsSecure`. That is COM plumbing
glaze does not have yet, and is a bigger change than the ones below — which is
why it is written up rather than patched here.

**Interim, for anyone using glaze today:** reference assets **relatively**.
It works on both platforms. `glaze-probes/verifyevents` was changed to do
exactly that, and with it the Events bridge passes completely on Windows.

**Status**: diagnosed and reproducible, **not fixed**. The probe now
distinguishes the two cases, so this cannot silently regress into a bare
"timed out" again.

## 2. native + glaze — `ErrUnsupported` sentinels do not wrap `errors.ErrUnsupported`

**Severity: medium.** Correct programs on correct platforms report failure.

Nine packages define their own sentinel with `errors.New`, so none of them
matches the one check the standard library defines for exactly this purpose:

| package | sentinel |
|---|---|
| `native/clipboard`, `power`, `singleinstance`, `mmap`, `openurl`, `nocapture`, `tray` | `ErrUnsupported` |
| `glaze/menu` | `ErrUnsupported` |
| `glaze` | `ErrIconUnsupported` |

A caller handling several of them has to import every package purely to name
its sentinel, and any package added later silently breaks that list again. The
consequence is not cosmetic — it is measured, in this repo:

- `glaze.SetAppIcon` is unsupported **on Windows**, by design. Windows is the
  platform this project exists to test.
- `nocapture.Protect` is unsupported **on macOS**, by design — Apple removed
  the API.

So a run in which every capability behaves exactly as documented reported two
FAILURES and exited non-zero.

**Fix**: wrap, which is source- and behaviour-compatible — `errors.Is` against
the package sentinel still matches.

```go
var ErrUnsupported = fmt.Errorf("clipboard: not supported on this platform: %w", errors.ErrUnsupported)
```

**Status**: fixed in all nine packages, and **verified on Windows 11 ARM64**.
Each native package gained an `unsupported_test.go` asserting the wrapping, and
glaze an `appicon_unsupported_test.go` covering both of its sentinels, so a
package added later cannot reintroduce it. `go test ./...` passes in both
repos; native's README, which documented the old pattern as the house style, is
updated.

The run that proves it — `examples/glaze-all` built against the patched clones,
executed in the VM. The trailing `: unsupported operation` is the wrapped
sentinel, and it is the only visible difference:

```
glaze.SetAppIcon   UNSUPPORTED  glaze: setting the application icon at runtime
                                is not supported on this platform: unsupported operation
...
all capabilities OK or cleanly unsupported
```

Before the fix that same line read `FAILED`, and the process exited non-zero on
a run in which nothing was wrong.

---

## 3. native/tray — no way to have a tray *and* a window

**Severity: low. Reported as a limitation, not a bug — not yet filed.**

`tray.Run` blocks driving the OS event loop and its doc requires the main
goroutine locked to the main OS thread. A `glaze.WebView` wants exactly the
same thing. Both are documented; nothing says they are mutually exclusive, and
a desktop app that wants a tray icon and a window is not unusual.

What works today, and what `examples/glaze-all` does: post `tray.Run` onto the
UI thread **after** the window exists and never wait on it, letting the nested
loop run until `tray.Stop`. Verified on macOS 15 and Windows 11 ARM64. It is
undocumented, so it is luck rather than contract.

An honest fix is an API that attaches a tray to a run loop somebody else owns.

---

## Not bugs

- **A package returning its own `ErrUnsupported` on a platform it documents as
  unsupported.** `nocapture` on macOS is right to refuse: `NSWindowSharingNone`
  stopped working in 15.4, there is no public replacement, and on macOS 26 the
  legacy value can stop the window rendering at all.

## Ours, not theirs — fixed in this repo

Listed so they are never mistaken for upstream problems.

| what | whose | why |
|---|---|---|
| `menu.Set` called with no `Options.Window` | ours | required on Windows, documented, and the error says so |
| `menu.Set` called from a goroutine without `Options.Dispatch` | ours | documented, including the hang it causes if passed too early |
| waiting on `tray.Run` | ours | documented as blocking |
| `openurl.Open != nil` as a capability check | ours | a function value is never nil; `go vet` says so |
| `file://` URL built by concatenation | ours | Windows needs `file:///C:/dir`; covered by `TestFileURL` |
