# What this repo has found upstream

Three projects, all open source, all fixed **there** rather than worked around
here:

- **[crgimenes/glaze](https://github.com/crgimenes/glaze)** — the webview
- **[crgimenes/native](https://github.com/crgimenes/native)** — the OS integration
- **[utmapp/UTM](https://github.com/utmapp/UTM)** — the hypervisor this drives

The README states the rule; this file is the ledger. It is published so a
developer — or an agent — can see what is known without reading the code.

Each entry answers one question before it is listed: *does a correct consumer,
reading only the public documentation, hit this?* If the answer is no — we
called the API wrongly — it is our bug and it is fixed in this repo. Those are
at the bottom, so the distinction stays visible rather than being quietly
rewritten later.

## Status

| | finding | severity | status |
|---|---|---|---|
| **glaze** | [`New` blocks forever if anything ran `NSApp` first](#1-glaze--new-blocks-forever-if-anything-ran-nsapp-first) | high | `PATCHED LOCALLY` · reported by someone else as [glaze#31](https://github.com/crgimenes/glaze/issues/31) |
| **glaze** | [absolute `app://` URLs silently do not load on Windows](#1b-glaze--absolute-app-urls-silently-do-not-load-on-windows) | high | `FOUND HERE` — not reported |
| **glaze + native** | [`ErrUnsupported` sentinels do not wrap the standard one](#2-native--glaze--errunsupported-sentinels-do-not-wrap-errorserrunsupported) | medium | `PATCHED LOCALLY` — not reported |
| **native** | [no way to have a tray *and* a window](#3-nativetray--no-way-to-have-a-tray-and-a-window) | low | `FOUND HERE` — a limitation, not reported |
| **UTM** | [`utmctl` reports failure and exits 0](#utm) | high | `FOUND HERE` — not reported |
| **UTM** | [`utmctl exec` never returns the guest's output](#utm) | high | `FOUND HERE` — not reported |
| **UTM** | [`suspend --save-state` power-cuts the guest](#utm) | high | `FOUND HERE` — not reported |
| **UTM** | [`ip-address` hangs rather than failing](#utm) | medium | `FOUND HERE` — not reported |
| **UTM** | [a rejected config names no field](#utm) | medium | `FOUND HERE` — not reported |
| **UTM** | [the guest agent stops answering](#the-guest-agent-stops-answering) | — | `OPEN` — cause not isolated |

What the words mean, and they are chosen so none of them can flatter:

- `FOUND HERE` — diagnosed and written up. **Upstream does not know.**
- `PATCHED LOCALLY` — a fix exists, as **uncommitted edits in a clone on one
  machine**. Not committed, not pushed, not proposed.
- `FILED` — reported upstream, with the link.
- `FIXED UPSTREAM` — landed in a release, with the version.
- `OPEN` — observed, cause not established, not yet filable.

**Nothing in this file has been reported upstream by this project.** That is
worth stating plainly rather than leaving to be inferred: glaze#31 is the same
defect as §1, and it was filed on 12 Aug 2026 by **@nako-ruru** — a stranger who
hit it independently, twelve days after it was diagnosed here. Finding bugs and
not reporting them is how that happens.

Patches live in clones at `~/workspace/go/src/github.com/crgimenes/{glaze,native}`
as **working-tree changes** — 4 modified files in glaze, 15 in native, none
committed. A `git checkout` in either would destroy them.

A fix is not listed as verified until it has been *run*: their tests, our tests,
and the probe binary built from the edit executing on Windows 11 ARM64 in the
VM. `irgo-winvm app-create` does the last.

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

**Reported by somebody else.** [glaze#31](https://github.com/crgimenes/glaze/issues/31),
*"[macOS] glaze.New(true) blocks indefinitely when initialized inside tray
OnClick callback"*, was opened on 12 Aug 2026 by **@nako-ruru** — the same
defect, reached by the same route (a tray callback), reported independently
twelve days after it was diagnosed here and while the fix above sat uncommitted
in a local clone.

Their report is the one upstream will act on. This entry is not a claim on it.

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

**Status**: `FOUND HERE` — diagnosed and reproducible, **not fixed**, not
reported. The probe now
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

**Status**: `PATCHED LOCALLY` — fixed in all nine packages and **verified on
Windows 11 ARM64**, but the change is an uncommitted edit in a clone and has not
been proposed upstream.
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

**Severity: low.** A limitation rather than a defect — the documented behaviour
of each package is correct on its own.

**Status**: `FOUND HERE` — not reported. Nothing upstream knows this has been
hit, and the workaround below is undocumented, so it is luck rather than
contract.

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

## UTM

[utmapp/UTM](https://github.com/utmapp/UTM), Apache-2.0. The version this repo
verifies its config schema against is **4.7.5**, recorded as
`utmvm.VerifiedVersion` — and 4.7.5 is the current release, so everything below
is a live defect rather than an artefact of running something old.

These have been treated as local traps to work around, which is the opposite of
the rule applied to glaze and native. They are listed here so that stops being
invisible. None has been reported.

### `utmctl` reports failure and exits 0

**Severity: high.** The exit status cannot be used to tell whether a command
worked, which makes every script built on `utmctl` unable to detect its own
failures.

- **`utmctl delete`** on a VM whose bundle is gone prints *"couldn't be
  removed"* and exits **0**. This repo checks whether the bundle still exists
  afterwards rather than trusting the status.
- **`utmctl ip-address`** with no guest agent prints its complaint as ordinary
  stdout and exits **0**. Every line has to be validated as an address, or a
  human-readable error is mistaken for one — which made a status check
  cheerfully report a working agent on a VM that had none.

It is also why this tool's own exit codes exist and are documented: it is the
only honest signal a caller gets.

### `utmctl exec` never returns the guest's output

**Severity: high.** It does not stream the process's output back and exits 0
whatever the guest command did, so a suite that ran nothing is indistinguishable
from a suite that passed.

Everything here that needs output writes a batch file which redirects to a file
in the guest, runs that by path, and pulls the file back. That machinery exists
solely because of this.

Two related quirks, same call:

- A complex command line does not survive it. `cmd.exe /c "prog" > "out" 2>&1`
  produces neither file: cmd applies its own quote-stripping to a string that
  already contains quotes, and the line silently does nothing.
- A whole command line passed as **one argument** makes the agent look for a
  file by that entire name and answer *"No such file or directory"* — which is
  indistinguishable from a dead agent, and cost a wrong diagnosis here.

### `utmctl suspend --save-state` reports success and power-cuts the guest

**Severity: high.** Exit 0, no state file written, VM left `stopped`, and the
guest's next boot goes through *"Diagnosing your PC"* — the signature of an
unclean shutdown.

It either refuses (naming GPU acceleration, then NVMe) or does the above. Plain
`suspend` works and is what this repo uses; `--save-state` must never be called.

### `utmctl ip-address` hangs rather than failing

**Severity: medium.** Against a guest with no agent it does not fail — it waits,
indefinitely. A VM with no Windows installed hung this CLI for ten minutes with
no output, because everything that asks "is this VM usable" is built on it.

Every `utmctl` call here is wrapped in a deadline for this reason.

### A rejected config names no field

**Severity: medium.** UTM decodes `config.plist` with Swift `Codable` and
non-optional fields, so any schema mismatch surfaces as a single generic
*"cannot import this VM"* with no indication of which field is wrong. Six
distinct config mistakes were found by bisection because of it, each costing an
import cycle to identify.

### The guest agent stops answering

**Status: `OPEN` — cause not isolated. Not filable as a UTM bug yet.**

**Observed.** `utmctl ip-address` answers one call and times out the next with
`Error from event: The operation couldn't be completed. (OSStatus error -2700.)`
/ `Timed out waiting for RPC`. The tool's own log records
`VM not answering; recovering` seven times in one session. The guest's desktop
was up and healthy throughout — confirmed by screenshot.

**Not established.** Whether the guest agent service has actually stopped
responding, or whether it is alive and the host cannot reach it. Those are two
different bugs in two different projects, and this repo currently attributes the
symptom to Windows Update keeping the agent busy — a guest-side explanation that
has never been checked.

**What would isolate it.** Query the `qemu-ga` service inside the guest, over
RDP or the console rather than through `utmctl`, at the moment a host call is
timing out. If the service is running and responsive while the host call fails,
it belongs to UTM. If it is not, it belongs to the guest and this entry should
be deleted.

Until that is done, filing it would waste a maintainer's time on a report that
cannot be acted on.

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
