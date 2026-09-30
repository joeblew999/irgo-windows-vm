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
| **glaze** | [`New` blocks forever if anything ran `NSApp` first](#1-glaze--new-blocks-forever-if-anything-ran-nsapp-first) — while that loop is still running (tray callback) | high | `FIXED UPSTREAM` in **v0.0.48** (bd5d994), reported by someone else as [glaze#31](https://github.com/crgimenes/glaze/issues/31) |
| **glaze** | [the same, after that loop has stopped](#1-glaze--new-blocks-forever-if-anything-ran-nsapp-first) (tray ran and returned, then `New`) — still hangs in v0.0.61 | high | `PATCHED LOCALLY` — branch `fix/nsapp-first` (`8623e26`) in the glaze clone, on trunk, not pushed; not reported — text ready in `.plans/2026-09-30_1800_upstream-reports.md` |
| **glaze** | WebView2 "not found" when the registered runtime folder is stale (self-update left `EBWebView` pointing at a deleted version) — see `.plans/2026-09-30_1250_glaze-webview2-stale-registration.md` | high | `FILED` as [glaze#34](https://github.com/crgimenes/glaze/issues/34) on 30 Sep 2026, no reply yet; fix on branch `fix/webview2-stale-registration` (`ba8775b`), pushed to the fork, **no PR opened** |
| **glaze** | [absolute `app://` URLs silently do not load on Windows](#1b-glaze--absolute-app-urls-silently-do-not-load-on-windows) | high | `PATCHED LOCALLY` — committed on branch `fix/windows-custom-scheme` (`75f3ea1`) in the glaze clone, not pushed; **not verified on Windows**; not reported — see `.plans/2026-09-30_1745_glaze-1b-upstream.md` |
| **glaze + native** | [`ErrUnsupported` sentinels do not wrap the standard one](#2-native--glaze--errunsupported-sentinels-do-not-wrap-errorserrunsupported) | medium | `PATCHED LOCALLY` — branch `fix/errunsupported-wrap` in each clone, on trunk (glaze `50cc331`, native `854cdb9`), not pushed; not reported — text ready in `.plans/2026-09-30_1800_upstream-reports.md` |
| **native** | [no way to have a tray *and* a window](#3-nativetray--no-way-to-have-a-tray-and-a-window) | low | `FOUND HERE` — a limitation, not reported; question drafted in `.plans/2026-09-30_1800_upstream-reports.md` |
| **UTM** | [`utmctl` reports failure and exits 0](#utm) | high | `FOUND HERE` — not reported; drafted in `.plans/2026-09-30_1800_upstream-reports.md` |
| **UTM** | [`utmctl exec` never returns the guest's output](#utm) | high | `FOUND HERE` — not reported; drafted in `.plans/2026-09-30_1800_upstream-reports.md` |
| **UTM** | [`suspend --save-state` power-cuts the guest](#utm) | high | `FOUND HERE` — not reported; drafted in `.plans/2026-09-30_1800_upstream-reports.md` |
| **UTM** | [`ip-address` hangs rather than failing](#utm) | medium | `FOUND HERE` — not reported; drafted in `.plans/2026-09-30_1800_upstream-reports.md` |
| **UTM** | [a rejected config names no field](#utm) | medium | `FOUND HERE` — not reported; drafted in `.plans/2026-09-30_1800_upstream-reports.md` |
| **UTM** | [the guest agent stops answering](#the-guest-agent-stops-answering) | — | `OPEN` — cause not isolated |

What the words mean, and they are chosen so none of them can flatter:

- `FOUND HERE` — diagnosed and written up. **Upstream does not know.**
- `PATCHED LOCALLY` — a fix exists **only in a clone on one machine**, as
  uncommitted edits or a local branch (the row says which). Not pushed, not
  proposed.
- `FILED` — reported upstream, with the link.
- `FIXED UPSTREAM` — landed in a release, with the version.
- `OPEN` — observed, cause not established, not yet filable.

**One thing in this file has been reported upstream by this project:**
glaze#34, on 30 Sep 2026. Everything else is unreported, and every unreported
finding has its issue (and, where there is a patch, PR) text drafted in
`.plans/2026-09-30_1800_upstream-reports.md`, waiting for the owner's go-ahead.
That is worth stating plainly: glaze#31 is the same defect as §1, filed on
12 Aug 2026 by **@nako-ruru** — a stranger who hit it independently, twelve days
after it was diagnosed here, and it was the maintainer's fix for *their* report
that shipped. Finding bugs and not reporting them is how that happens.

Patches live as **commits on local branches** in the clones at
`$UPSTREAM_DIR` = `~/workspace/go/src/github.com/crgimenes/{glaze,native}`.
None is pushed except `fix/webview2-stale-registration` (to the fork, for #34).

| clone | branch | commit | on | what |
|---|---|---|---|---|
| glaze | `fix/nsapp-first` | `8623e26` | trunk `0dce849` (v0.0.61) | §1, the stopped-loop case, with a test |
| glaze | `fix/errunsupported-wrap` | `50cc331` | trunk `0dce849` (v0.0.61) | §2, glaze half, a test per package |
| glaze | `fix/windows-custom-scheme` | `75f3ea1` | — | §1b (see its plan) |
| glaze | `fix/webview2-stale-registration` | `ba8775b` | trunk `0dce849` | glaze#34 |
| native | `fix/errunsupported-wrap` | `854cdb9` | trunk `58f48b7` (v0.1.15) | §2, native half, all ten packages, a test each |

Superseded, kept only until the PRs exist: `patch/nsapp-new-blocks` (`2b145f9`,
glaze) and `patch/errunsupported-wraps-std` (glaze `a5fedb7`, native
`66b4497`). These are the original working-tree edits, committed on 30 Sep —
nothing was lost — but on the stale `master` branches (glaze v0.0.47, native
v0.1.6), not on `trunk`, which is what upstream releases from. The `fix/*`
branches are those patches carried onto trunk and extended to what trunk has
added since.

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
goroutine 1 [syscall, locked to thread]:          (glaze v0.0.47)
  ...objc.ID.Send
  glaze.(*webview).windowInit.func2()       webview_darwin.go:504
  glaze.NewWithOptions(...)                 webview_darwin.go:477
  glaze.New(...)                            webview_darwin.go:448
  main.main()
```

There are two routes in, and upstream has fixed one of them.

**Route 1 — `New` while someone else's loop is still running** (from a tray
`OnClick`). Reported by somebody else as
[glaze#31](https://github.com/crgimenes/glaze/issues/31), *"[macOS]
glaze.New(true) blocks indefinitely when initialized inside tray OnClick
callback"*, opened on 12 Aug 2026 by **@nako-ruru** — twelve days after this
was diagnosed here, while the fix sat uncommitted in a local clone. The
maintainer fixed it in bd5d994 (*"darwin: a webview born under someone else's
run loop"*): when `[NSApp isRunning]`, glaze skips the bootstrap and marshals
itself to the main thread. **`FIXED UPSTREAM` in v0.0.48**; this repo is on
v0.0.61.

**Route 2 — `New` after that loop has stopped** (`tray.Run` returned after
`tray.Stop`, a dialog closed, another toolkit's window gone). `isRunning` is
false by then, so bd5d994's check does not fire, the bootstrap is taken, and it
waits for a notification AppKit already sent. **Still hangs in v0.0.61**,
measured on 30 Sep 2026 with the reproducer below (Mac, no VM):

```
tray.Run (stops itself after 1.5s), then glaze.New
  released glaze v0.0.61       step 1, step 2, then nothing — killed at 20s
  stack: glaze.(*webview).windowInit.func1()  webview_darwin.go:651  (the temporary [NSApp run])
  glaze fix/nsapp-first        step 1, 2, 3, 4, exit 0
```

**Fix** (route 2, on trunk): also ask AppKit whether it has already launched,
and if so take the path the delegate callback would have taken, minus the stop
that has nothing to stop.

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

**Status**: `PATCHED LOCALLY` — branch `fix/nsapp-first`, commit `8623e26`, on
glaze trunk `0dce849` (v0.0.61), not pushed, **not reported**. It carries the
original patch (`2b145f9`, made on v0.0.47) onto the code bd5d994 rewrote, plus
`TestNewAfterAppFinishedLaunching`, which runs route 2 in a child process of
the test binary: it hangs on trunk and passes with the fix (negative control
run). glaze `go test ./...` and `-short` pass on macOS 27; `GOOS=windows` and
`GOOS=linux` build and vet; `golangci-lint run` for darwin and windows reports 0
issues. macOS-only code, so the Windows VM has nothing to say about it. Issue
and PR text: `.plans/2026-09-30_1800_upstream-reports.md` §1.

Nothing in this repo works around route 2 — no example creates a web view after
a loop has stopped. The comment on `probeTray` in `examples/glaze-all` still
describes route 1 as unfixed ("this order is what a released glaze still
requires"); since v0.0.48 that is no longer true, and the window-first order is
kept only because the tray is posted with `w.Dispatch`, which needs the window.

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

**Measured**, Windows 11 ARM64, by `examples/verify` loading the same asset
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
`ICoreWebView2EnvironmentOptions4::GetCustomSchemeRegistrations`, with
`HasAuthorityComponent` for the host and `TreatAsSecure`. glaze passed `null`
environment options; the fix passes a read-only options object it implements
over Go vtables (cgo-free, as its completion handlers already are), filters
`app:*`, and deletes the vhost rewrite. A window with schemes gets its own
WebView2 user data folder, because WebView2 refuses different registrations on
one browser process. Design, checks and the upstream text:
`.plans/2026-09-30_1745_glaze-1b-upstream.md`.

**Interim, for anyone using glaze today:** reference assets **relatively**.
It works on both platforms. `examples/verify-events` was changed to do
exactly that, and with it the Events bridge passes completely on Windows.

**Status**: `PATCHED LOCALLY` — committed on branch `fix/windows-custom-scheme`
(`75f3ea1`, on `origin/trunk` = v0.0.61) in `$UPSTREAM_DIR/glaze`, not pushed.
Checked on the Mac only: builds for all six targets, `go vet` and
`golangci-lint` clean for darwin/linux/windows, glaze's macOS tests pass.
**Not run on Windows**, so not verified: that needs `mise run upstream:link &&
mise run glaze:windows` with the branch checked out (verify must PASS) and
`mise run upstream:test:windows`. **Not reported**: nobody upstream knows (no
issue or PR as of 30 Sep 2026; released v0.0.61 still emulates). The issue text
is written and waits on the owner's go-ahead. The probe distinguishes the two
cases, so this cannot silently regress into a bare "timed out" again.

## 2. native + glaze — `ErrUnsupported` sentinels do not wrap `errors.ErrUnsupported`

**Severity: medium.** Correct programs on correct platforms report failure.

Twelve packages define their own sentinel with `errors.New`, so none of them
matches the one check the standard library defines for exactly this purpose:

| package | sentinel |
|---|---|
| `native/alert`, `bookmark`, `clipboard`, `mmap`, `nocapture`, `openurl`, `pointer`, `power`, `singleinstance`, `tray` | `ErrUnsupported` |
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

**Status**: `PATCHED LOCALLY` — branch `fix/errunsupported-wrap` in each
clone, on trunk: glaze `50cc331` (on `0dce849`, v0.0.61), native `854cdb9` (on
`58f48b7`, v0.1.15). Not pushed, **not reported**; issue and PR text in
`.plans/2026-09-30_1800_upstream-reports.md` §2 and §3. Checked upstream on
30 Sep 2026: neither trunk wraps, and no issue or PR mentions it.

Each package has a test asserting the wrapping (native: `unsupported_test.go`
in all ten; glaze: `appicon_unsupported_test.go` for both sentinels and
`menu/unsupported_test.go`), so a package added later cannot reintroduce it.
native's README, which documented the old pattern as the house style, is
updated. Both repos: `go test ./...` passes on macOS 27, `GOOS=windows` and
`GOOS=linux` build, `golangci-lint run` for darwin and windows reports 0 issues.

This repo against those branches (a `go.work` pointing at them, not
committed), with the stand-in below **deleted** so `glaze-all` makes the one
standard check: `go -C examples build/vet/test ./...` pass, `GOOS=windows`
builds, and `mise run glaze:mac` answers "YES: all four passed on
darwin/arm64", with

```
nocapture.Protect        UNSUPPORTED  nocapture: not supported on this platform: unsupported operation
```

The same deletion against the released v0.0.61 / v0.1.15 reads
`nocapture.Protect  FAILED` — the negative control.

Windows: the original patch (the seven native packages of v0.1.6, glaze
v0.0.47) was run in the VM, where `glaze.SetAppIcon` read `UNSUPPORTED ...:
unsupported operation` and the run passed. The trunk branches have **not** been
run on Windows yet.

**Why `examples/glaze-all` still has a workaround.** `unsupportedErrs` and
`isUnsupported` in `examples/glaze-all/main.go`, marked *STANDING IN FOR AN
UPSTREAM FIX*, list each package's sentinel by name. That is the workaround the
rule forbids, and it stays only because the fix is in no released glaze or
native: deleting it today makes every macOS run fail on `nocapture`. **What
removes it:** a glaze and a native release containing the wrapping; then bump
`examples/go.mod` to them and replace the list with one
`errors.Is(err, errors.ErrUnsupported)`, exactly as verified above.

---

## 3. native/tray — no way to have a tray *and* a window

**Severity: low.** A limitation rather than a defect — the documented behaviour
of each package is correct on its own.

**Status**: `FOUND HERE` — not reported. Nothing upstream knows this has been
hit, and the workaround below is undocumented, so it is luck rather than
contract. A question (not a bug report) is drafted in
`.plans/2026-09-30_1800_upstream-reports.md` §4.

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
invisible. None has been reported; a search of utmapp/UTM issues on 30 Sep 2026
found no existing report of any of them (related, not the same: UTM#3819,
UTM#7669). Reports for all five are drafted in
`.plans/2026-09-30_1800_upstream-reports.md` §5–§9. Reading `utmctl`'s source
located the exit-status one: its event error handler prints and returns, and
only `snapshot create` checks the failure it records.

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
