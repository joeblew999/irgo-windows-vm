# Upstream bugs

Defects this project has found in the open-source projects it depends on:

- **[crgimenes/glaze](https://github.com/crgimenes/glaze)** — the webview
- **[crgimenes/native](https://github.com/crgimenes/native)** — the OS integration
- **[utmapp/UTM](https://github.com/utmapp/UTM)** — the hypervisor this tool drives

Bugs in these projects are fixed **there**, not worked around here. This page
is the ledger: what was found, how to reproduce it, and where the fix stands.

An entry is listed only if a correct consumer, reading only the public
documentation, would hit it. If we called an API wrongly, the bug is ours and is
fixed in this repository; those are listed [separately](#ours-not-theirs--fixed-in-this-repo)
so the distinction stays visible.

## Status

| finding | project | severity | status | link |
|---|---|---|---|---|
| [`New` blocks forever if anything ran `NSApp` first](#1-glaze--new-blocks-forever-if-anything-ran-nsapp-first) — while that loop is still running (tray callback) | glaze | high | `FIXED UPSTREAM` in **v0.0.48** (bd5d994) | [glaze#31](https://github.com/crgimenes/glaze/issues/31), reported by someone else |
| [the same, after that loop has stopped](#1-glaze--new-blocks-forever-if-anything-ran-nsapp-first) (tray ran and returned, then `New`) — still hangs in v0.0.61 | glaze | high | `PATCHED LOCALLY`, not reported | branch `fix/nsapp-first` (`8623e26`), not pushed |
| [absolute `app://` URLs silently do not load on Windows](#1b-glaze--absolute-app-urls-silently-do-not-load-on-windows) | glaze | high | `PATCHED LOCALLY`, **not verified on Windows**, not reported | branch `fix/windows-custom-scheme` (`75f3ea1`), not pushed |
| [`ErrUnsupported` sentinels do not wrap the standard one](#2-native--glaze--errunsupported-sentinels-do-not-wrap-errorserrunsupported) | glaze + native | medium | `PATCHED LOCALLY`, not reported | branch `fix/errunsupported-wrap` (glaze `50cc331`, native `854cdb9`), not pushed |
| [no way to have a tray *and* a window](#3-nativetray--no-way-to-have-a-tray-and-a-window) | native | low | `FOUND HERE` — a limitation, not reported | question drafted |
| [WebView2 "not found" when its registration is stale](#4-glaze--webview2-not-found-when-its-registration-is-stale) | glaze | high | `FILED` 30 Sep 2026, no reply yet | [glaze#34](https://github.com/crgimenes/glaze/issues/34); branch `fix/webview2-stale-registration` (`ba8775b`) on the fork, **no PR** |
| [`New` crashes if the main goroutine has moved thread](#5-glaze--new-crashes-if-the-main-goroutine-has-moved-thread) (macOS) | glaze | medium | `FOUND HERE` 30 Sep 2026, not reported, not patched | — |
| [`utmctl` reports failure and exits 0](#utmctl-reports-failure-and-exits-0) | UTM | high | `FOUND HERE`, not reported | drafted |
| [`utmctl exec` never returns the guest's output](#utmctl-exec-never-returns-the-guests-output) | UTM | high | `FOUND HERE`, not reported | drafted |
| [`suspend --save-state` power-cuts the guest](#utmctl-suspend---save-state-reports-success-and-power-cuts-the-guest) | UTM | high | `FOUND HERE`, not reported | drafted |
| [`ip-address` hangs rather than failing](#utmctl-ip-address-hangs-rather-than-failing) | UTM | medium | `FOUND HERE`, not reported | drafted |
| [a rejected config names no field](#a-rejected-config-names-no-field) | UTM | medium | `FOUND HERE`, not reported | drafted |
| [the guest agent stops answering](#the-guest-agent-stops-answering) | UTM (unconfirmed) | — | `OPEN` — cause not isolated | — |

"Drafted" means the issue text (and PR text, where there is a patch) is written
in `.plans/2026-09-30_1800_upstream-reports.md` and waits on the owner's
go-ahead.

### Status values

Chosen so that none of them overstates progress:

- `FOUND HERE` — diagnosed and written up. **Upstream does not know.**
- `PATCHED LOCALLY` — a fix exists **only in a clone on one machine**, as a
  local branch (the row says which). Not pushed, not proposed.
- `FILED` — reported upstream, with the link.
- `FIXED UPSTREAM` — landed in a release, with the version.
- `OPEN` — observed, cause not established, not yet filable.

**One finding has been reported upstream by this project:** glaze#34, on
30 Sep 2026. Every other finding is unreported. glaze#31 is the same defect as
§1, filed on 12 Aug 2026 by **@nako-ruru**, who hit it independently twelve days
after it was diagnosed here; the maintainer's fix for *that* report is what
shipped. Unreported findings get fixed on someone else's schedule, or not at
all.

### Where the patches are

Patches are **commits on local branches** in the clones at
`$UPSTREAM_DIR` = `~/workspace/go/src/github.com/crgimenes/{glaze,native}`.
None is pushed except `fix/webview2-stale-registration`, which is on the fork
for #34.

| clone | branch | commit | based on | what |
|---|---|---|---|---|
| glaze | `fix/nsapp-first` | `8623e26` | trunk `0dce849` (v0.0.61) | §1, the stopped-loop case, with a test |
| glaze | `fix/errunsupported-wrap` | `50cc331` | trunk `0dce849` (v0.0.61) | §2, glaze half, a test per package |
| glaze | `fix/windows-custom-scheme` | `75f3ea1` | `origin/trunk` (v0.0.61) | §1b |
| glaze | `fix/webview2-stale-registration` | `ba8775b` | trunk `0dce849` | §4, glaze#34 |
| native | `fix/errunsupported-wrap` | `854cdb9` | trunk `58f48b7` (v0.1.15) | §2, native half, all ten packages, a test each |

Superseded, kept only until the PRs exist: `patch/nsapp-new-blocks` (`2b145f9`,
glaze) and `patch/errunsupported-wraps-std` (glaze `a5fedb7`, native
`66b4497`). These are the original working-tree edits, committed on 30 Sep so
nothing was lost, but on the stale `master` branches (glaze v0.0.47, native
v0.1.6) rather than `trunk`, which upstream releases from. The `fix/*` branches
carry those patches onto trunk and extend them to what trunk has added since.

A fix is not listed as verified until it has been *run*: upstream's tests, this
repository's tests, and the probe binary built from the patched code executing
on Windows 11 ARM64 in the VM. `irgo-winvm app-create` does the last.

## 1. glaze — `New` blocks forever if anything ran `NSApp` first

**Severity:** high. Silent infinite hang: no window, no error, nothing logged.

**Summary.** `glaze.New` never returns if any code ran the Cocoa application
loop before the first web view was created — `native/tray`, an Ebitengine
window, or any library that raises a Cocoa dialog. Nothing in glaze's
documentation says a window must be created first.

**Cause.** `webview_darwin.go` / `windowInit` enters a temporary `[NSApp run]`
and relies on `applicationDidFinishLaunching:` to stop it. AppKit sends that
**once per process**. Code that ran `NSApp` earlier consumes it, and the
temporary loop has nothing to stop it:

```
goroutine 1 [syscall, locked to thread]:          (glaze v0.0.47)
  ...objc.ID.Send
  glaze.(*webview).windowInit.func2()       webview_darwin.go:504
  glaze.NewWithOptions(...)                 webview_darwin.go:477
  glaze.New(...)                            webview_darwin.go:448
  main.main()
```

There are two routes in. Upstream has fixed the first.

**Route 1 — `New` while another loop is still running** (from a tray
`OnClick`). Reported as [glaze#31](https://github.com/crgimenes/glaze/issues/31),
*"[macOS] glaze.New(true) blocks indefinitely when initialized inside tray
OnClick callback"*, opened on 12 Aug 2026 by @nako-ruru, twelve days after it
was diagnosed here while the fix sat uncommitted in a local clone. Fixed in
bd5d994 (*"darwin: a webview born under someone else's run loop"*): when
`[NSApp isRunning]`, glaze skips the bootstrap and marshals itself to the main
thread. **`FIXED UPSTREAM` in v0.0.48**; this repository is on v0.0.61.

**Route 2 — `New` after that loop has stopped** (`tray.Run` returned after
`tray.Stop`, a dialog closed, another toolkit's window gone). `isRunning` is
false by then, so bd5d994's check does not fire, the bootstrap is taken, and it
waits for a notification AppKit already sent. **Still hangs in v0.0.61.**

**Reproduction** (route 2; Mac, no VM; measured 30 Sep 2026):

```
tray.Run (stops itself after 1.5s), then glaze.New
  released glaze v0.0.61       step 1, step 2, then nothing — killed at 20s
  stack: glaze.(*webview).windowInit.func1()  webview_darwin.go:651  (the temporary [NSApp run])
  glaze fix/nsapp-first        step 1, 2, 3, 4, exit 0
```

**Fix** (route 2, on trunk). Also ask AppKit whether it has already finished
launching, and if so take the path the delegate callback would have taken,
minus the stop that has nothing to stop:

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
`finishLaunching`, so the two paths cannot drift. The branch carries the
original patch (`2b145f9`, made on v0.0.47) onto the code bd5d994 rewrote, plus
`TestNewAfterAppFinishedLaunching`, which runs route 2 in a child process of the
test binary: it hangs on trunk and passes with the fix (negative control run).
glaze `go test ./...` and `-short` pass on macOS 27; `GOOS=windows` and
`GOOS=linux` build and vet; `golangci-lint run` for darwin and windows reports 0
issues. The code is macOS-only, so the Windows VM has nothing to say about it.

**Status.** `PATCHED LOCALLY` — branch `fix/nsapp-first`, commit `8623e26`, on
glaze trunk `0dce849` (v0.0.61). Not pushed, **not reported**. Issue and PR
text: `.plans/2026-09-30_1800_upstream-reports.md` §1.

**In this repository.** Nothing works around route 2; no example creates a web
view after a loop has stopped. The comment on `probeTray` in
`examples/glaze-all` still describes route 1 as unfixed ("this order is what a
released glaze still requires"). Since v0.0.48 that is no longer true; the
window-first order is kept only because the tray is posted with `w.Dispatch`,
which needs the window.

## 1b. glaze — absolute `app://` URLs silently do not load on Windows

**Severity:** high. A glaze app that works on macOS loses every stylesheet and
script on Windows, with no error anywhere. This is the class of bug this project
exists to find: it is invisible from a Mac.

**Summary.** An absolute `app://home/app.js` reference inside a page loads on
macOS and is never requested on Windows. No error, no console message — a page
with no CSS and no JavaScript.

**Cause.** On macOS, WKWebView registers `app` as a real scheme. On Windows,
`webview2_scheme_windows.go` emulates the scheme with a virtual host:

```go
func schemeVHost(scheme string) string { return "https://" + scheme + ".localhost" }
...
out := schemeVHost(u.Scheme) + u.Path     // app://home/index.html -> https://app.localhost/index.html
```

The document therefore loads from `https://app.localhost/`, and an absolute
`app://home/app.js` inside it names a scheme WebView2 has never heard of.

**Reproduction.** Windows 11 ARM64, `examples/verify` loading the same asset
twice, once absolutely and once relatively (the output below is that program's;
it is now `TestAppScheme` in `examples/conformance`, where
`TestAppScheme/absolute_subresources` fails on Windows and is listed in
`glazecheck.KnownUpstream`):

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
document's real origin, so it reaches the handler, while the absolute URLs the
developer wrote do not. The probe distinguishes the two cases, so this cannot
silently regress into a bare "timed out".

Two further consequences of the same rewrite:

- **`location.origin` differs by platform** — `app://home` on macOS,
  `https://app.localhost` on Windows. Origin-dependent code diverges.
- **The URL's host is dropped.** `app://home/x` and `app://other/x` both become
  `https://app.localhost/x`, so two hosts collide silently.

**Fix.** Stop emulating. WebView2 supports real custom schemes through
`ICoreWebView2EnvironmentOptions4::GetCustomSchemeRegistrations`, with
`HasAuthorityComponent` for the host and `TreatAsSecure`. glaze passed `null`
environment options; the fix passes a read-only options object implemented over
Go vtables (cgo-free, as glaze's completion handlers already are), filters
`app:*`, and deletes the vhost rewrite. A window with schemes gets its own
WebView2 user data folder, because WebView2 refuses different registrations on
one browser process. Design, checks and the upstream text:
`.plans/2026-09-30_1745_glaze-1b-upstream.md`.

**Workaround for glaze users today:** reference assets **relatively**. It works
on both platforms. `TestEvents` in `examples/conformance` does exactly that,
and with it the Events bridge passes completely on Windows.

**Status.** `PATCHED LOCALLY` — branch `fix/windows-custom-scheme` (`75f3ea1`,
on `origin/trunk` = v0.0.61) in `$UPSTREAM_DIR/glaze`, not pushed. Checked on
the Mac only: builds for all six targets, `go vet` and `golangci-lint` clean for
darwin/linux/windows, glaze's macOS tests pass. **Not run on Windows, so not
verified**; that needs `mise run upstream:link && mise run glaze:windows` with
the branch checked out (verify must PASS) and `mise run upstream:test:windows`.
**Not reported**: no issue or PR upstream as of 30 Sep 2026, and released
v0.0.61 still emulates. The issue text is written and waits on the owner's
go-ahead.

## 2. native + glaze — `ErrUnsupported` sentinels do not wrap `errors.ErrUnsupported`

**Severity:** medium. Correct programs on correct platforms report failure.

**Summary.** Twelve packages define their own "unsupported" sentinel with
`errors.New`, so none matches `errors.Is(err, errors.ErrUnsupported)`, the check
the standard library defines for this purpose:

| package | sentinel |
|---|---|
| `native/alert`, `bookmark`, `clipboard`, `mmap`, `nocapture`, `openurl`, `pointer`, `power`, `singleinstance`, `tray` | `ErrUnsupported` |
| `glaze/menu` | `ErrUnsupported` |
| `glaze` | `ErrIconUnsupported` |

A caller handling several must import every package just to name its sentinel,
and any package added later silently breaks that list.

**Reproduction.** Measured in this repository:

- `glaze.SetAppIcon` is unsupported **on Windows**, by design — the platform
  this project exists to test.
- `nocapture.Protect` is unsupported **on macOS**, by design — Apple removed
  the API.

A run in which every capability behaved exactly as documented reported two
FAILURES and exited non-zero.

**Fix.** Wrap the standard sentinel. This is source- and behaviour-compatible:
`errors.Is` against the package sentinel still matches.

```go
var ErrUnsupported = fmt.Errorf("clipboard: not supported on this platform: %w", errors.ErrUnsupported)
```

Each package gets a test asserting the wrapping (native: `unsupported_test.go`
in all ten; glaze: `appicon_unsupported_test.go` for both sentinels and
`menu/unsupported_test.go`), so a package added later cannot reintroduce it.
native's README, which documented the old pattern as house style, is updated.
Both repositories: `go test ./...` passes on macOS 27, `GOOS=windows` and
`GOOS=linux` build, `golangci-lint run` for darwin and windows reports 0 issues.

Verified against this repository with a `go.work` pointing at the branches (not
committed) and the stand-in below **deleted**, so `glaze-all` makes the one
standard check: `go -C examples build/vet/test ./...` pass, `GOOS=windows`
builds, and `mise run glaze:mac` answers "YES: all four passed on
darwin/arm64", with

```
nocapture.Protect        UNSUPPORTED  nocapture: not supported on this platform: unsupported operation
```

The same deletion against released v0.0.61 / v0.1.15 reads
`nocapture.Protect  FAILED` — the negative control.

On Windows, the original patch (the seven native packages of v0.1.6, glaze
v0.0.47) was run in the VM: `glaze.SetAppIcon` read `UNSUPPORTED ...:
unsupported operation` and the run passed. The trunk branches have **not** been
run on Windows yet.

**Status.** `PATCHED LOCALLY` — branch `fix/errunsupported-wrap` in each clone,
on trunk: glaze `50cc331` (on `0dce849`, v0.0.61), native `854cdb9` (on
`58f48b7`, v0.1.15). Not pushed, **not reported**; issue and PR text in
`.plans/2026-09-30_1800_upstream-reports.md` §2 and §3. Checked upstream on
30 Sep 2026: neither trunk wraps, and no issue or PR mentions it.

**In this repository.** `unsupportedErrs` and `isUnsupported` in
`examples/glaze-all/main.go`, marked *STANDING IN FOR AN UPSTREAM FIX*, list
each package's sentinel by name. That is the kind of workaround the rule
forbids; it stays only because no released glaze or native contains the fix,
and deleting it today makes every macOS run fail on `nocapture`. **Removed
when:** a glaze and a native release contain the wrapping. Then bump
`examples/go.mod` to them and replace the list with one
`errors.Is(err, errors.ErrUnsupported)`, exactly as verified above.

## 3. native/tray — no way to have a tray *and* a window

**Severity:** low. A limitation rather than a defect; each package's documented
behaviour is correct on its own.

**Summary.** `tray.Run` blocks driving the OS event loop, and its documentation
requires the main goroutine locked to the main OS thread. A `glaze.WebView`
needs exactly the same. Both are documented; nothing says they are mutually
exclusive, and a desktop app with a tray icon and a window is not unusual.

**Workaround.** What works today, and what `examples/glaze-all` does: post
`tray.Run` onto the UI thread **after** the window exists and never wait on it,
letting the nested loop run until `tray.Stop`. Verified on macOS 15 and
Windows 11 ARM64. It is undocumented, so it works by luck rather than contract.

**Fix.** An API that attaches a tray to a run loop somebody else owns.

**Status.** `FOUND HERE` — not reported. A question (not a bug report) is
drafted in `.plans/2026-09-30_1800_upstream-reports.md` §4.

## 4. glaze — WebView2 "not found" when its registration is stale

**Severity:** high. A glaze app refuses to start on a machine whose WebView2
runtime is installed and working.

**Summary.** On `irgo-win11` every glaze window failed with
`webview2: Edge WebView2 Runtime not found (install it)` while a working runtime
was installed. The Evergreen runtime had self-updated to `154.0.4258.37` and
deleted the `151.0.4129.86` folder, but the registry still named the deleted
folder. Same failure on glaze v0.0.47 and v0.0.61, so it is long-standing, not a
regression. The likely trigger — inferred, not proven — is an update interrupted
by a shutdown, which end users can hit after a power cut.

**Reproduction** (in the VM, as SYSTEM): point the registration at a folder that
does not exist, then run `examples/verify` (now `TestAppScheme` in
`examples/conformance`) against released glaze.

```
reg add "HKLM\SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\ClientState\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}" /v EBWebView /t REG_SZ /d "C:\Program Files (x86)\Microsoft\EdgeWebView\Application\1.0.0.0" /f
```

**Cause.** `findEmbeddedBrowserDLL` in `webview2_windows.go` (v0.0.61) reads
`EBWebView` from the registry and treats it as the **only** source. If that
folder is missing, it reports the runtime as not installed.

**Fix.** When the registered folder is missing, scan the system and per-user
install roots for the newest valid runtime beside it; the registry stays
authoritative otherwise. On the VM, with the registration broken, released
v0.0.61 `verify` FAILS and the patched glaze PASSES, finding `154.0.4258.37`.
glaze's Windows test suite passes with the patch (`upstream:test:windows`), and
lint is clean for darwin and windows (`upstream:lint`). Full diagnosis:
`.plans/done/2026-09-30_1250_glaze-webview2-stale-registration.md`.

**In this repository.** `irgo-winvm vm-repair -reboot` repairs the registration
on an affected VM (see [DEVELOPMENT.md](DEVELOPMENT.md#when--gui-stops-working-on-an-old-vm)).

**Status.** `FILED` as [glaze#34](https://github.com/crgimenes/glaze/issues/34)
on 30 Sep 2026, no reply yet. The fix is on branch
`fix/webview2-stale-registration` (`ba8775b`), pushed to the fork
`joeblew999/glaze`; **no PR opened**, because the maintainer fixes his own bugs
and a PR is sent only if asked.

## 5. glaze — `New` crashes if the main goroutine has moved thread

On macOS, `glaze.New` called from `main` after other work crashes with SIGTRAP
inside the temporary `[NSApp run]` that `windowInit` starts. The stack shows
goroutine 1 on `m=4`, not the main thread `m0`. glaze pins the thread with
`uiThreadOnce.Do(runtime.LockOSThread)` inside `NewWithOptions`, which pins
whatever thread the goroutine is on at that moment. Nothing pins the main
goroutine to the main thread before then, so the scheduler is free to move it.

Measured 30 Sep 2026 with `examples/glaze-all -probe` (since replaced by the
conformance suite), when an openurl probe taking several seconds of `osascript`
subprocesses and sleeps ran before `New`: two crashes in fourteen runs, exit
status 2. With `New` moved first: none in ten. Nothing in glaze's
documentation says `New` must come first.

Fix, in glaze: `func init() { runtime.LockOSThread() }` in `webview_darwin.go`,
which pins the main goroutine to the main thread before `main` runs, as Cocoa
bindings generally do. Until then, call `New` before anything slow on the main
goroutine.

## UTM

[utmapp/UTM](https://github.com/utmapp/UTM), Apache-2.0. This repository
verifies its config schema against **4.7.5**, recorded as
`utmvm.VerifiedVersion`. 4.7.5 is the current release, so everything below is a
live defect, not an artefact of running something old.

These were long treated as local traps to work around — the opposite of the rule
applied to glaze and native. They are listed here so that is visible. None has
been reported. A search of utmapp/UTM issues on 30 Sep 2026 found no existing
report of any of them (related, not the same: UTM#3819, UTM#7669). Reports for
all five are drafted in `.plans/2026-09-30_1800_upstream-reports.md` §5–§9.

### `utmctl` reports failure and exits 0

**Severity:** high. The exit status cannot tell a script whether a command
worked.

**Reproduction.**

- **`utmctl delete`** on a VM whose bundle is gone prints *"couldn't be
  removed"* and exits **0**.
- **`utmctl ip-address`** with no guest agent prints its complaint as ordinary
  stdout and exits **0**. Unless every line is validated as an address, the
  error text is mistaken for one — which made a status check here report a
  working agent on a VM that had none.

**Cause.** In `utmctl`'s source, the event error handler prints and returns;
only `snapshot create` checks the failure it records.

**In this repository.** After `delete`, the tool checks whether the bundle still
exists rather than trusting the status. This defect is also why this tool's own
[exit codes](DEVELOPMENT.md#what-it-exits-with) exist and are documented: they
are the only reliable signal a caller gets.

**Status.** `FOUND HERE` — not reported; drafted.

### `utmctl exec` never returns the guest's output

**Severity:** high. A suite that ran nothing is indistinguishable from a suite
that passed.

**Summary.** `utmctl exec` does not stream the process's output back and exits
0 whatever the guest command did.

Two related quirks, same call:

- A complex command line does not survive it. `cmd.exe /c "prog" > "out" 2>&1`
  produces neither file: cmd applies its own quote-stripping to a string that
  already contains quotes, and the line silently does nothing.
- A whole command line passed as **one argument** makes the agent look for a
  file by that entire name and answer *"No such file or directory"* — which is
  indistinguishable from a dead agent, and caused a wrong diagnosis here.

**In this repository.** Everything that needs output writes a batch file that
redirects to a file in the guest, runs it by path, and pulls the file back.
That machinery exists solely because of this.

**Status.** `FOUND HERE` — not reported; drafted.

### `utmctl suspend --save-state` reports success and power-cuts the guest

**Severity:** high. Exit 0, no state file written, VM left `stopped`, and the
guest's next boot goes through *"Diagnosing your PC"* — the signature of an
unclean shutdown.

**Summary.** It either refuses (naming GPU acceleration, then NVMe) or does the
above.

**In this repository.** Plain `suspend` works and is what the tool uses;
`--save-state` must never be called.

**Status.** `FOUND HERE` — not reported; drafted.

### `utmctl ip-address` hangs rather than failing

**Severity:** medium. Against a guest with no agent it waits indefinitely
instead of failing.

**Reproduction.** A VM with no Windows installed hung this CLI for ten minutes
with no output, because everything that asks "is this VM usable" is built on
`ip-address`.

**In this repository.** Every `utmctl` call is wrapped in a deadline.

**Status.** `FOUND HERE` — not reported; drafted.

### A rejected config names no field

**Severity:** medium. Any schema mismatch surfaces as a single generic *"cannot
import this VM"*, with no indication of which field is wrong.

**Cause.** UTM decodes `config.plist` with Swift `Codable` and non-optional
fields.

**Reproduction.** Six distinct config mistakes were found by bisection because
of it, each costing an import cycle to identify. They are listed in
[DEVELOPMENT.md](DEVELOPMENT.md#known-traps).

**Status.** `FOUND HERE` — not reported; drafted.

### The guest agent stops answering

**Status:** `OPEN` — cause not isolated. Not filable as a UTM bug yet.

**Summary.** `utmctl ip-address` answers one call and times out the next with
`Error from event: The operation couldn't be completed. (OSStatus error -2700.)`
/ `Timed out waiting for RPC`. The tool's log recorded
`VM not answering; recovering` seven times in one session. The guest's desktop
was up and healthy throughout, confirmed by screenshot.

**Not established.** Whether the guest agent service has actually stopped
responding, or is alive and unreachable from the host. Those are two different
bugs in two different projects. This repository currently attributes the
symptom to Windows Update keeping the agent busy — a guest-side explanation
that has never been checked.

**To isolate it.** Query the `qemu-ga` service inside the guest, over RDP or the
console rather than through `utmctl`, while a host call is timing out. If the
service is running and responsive while the host call fails, the bug is UTM's.
If it is not, it belongs to the guest and this entry should be deleted. Until
then, a report would cost a maintainer time without being actionable.

## Not bugs

- **A package returning its own `ErrUnsupported` on a platform it documents as
  unsupported.** `nocapture` on macOS is right to refuse: `NSWindowSharingNone`
  stopped working in 15.4, there is no public replacement, and on macOS 26 the
  legacy value can stop the window rendering at all.

## Ours, not theirs — fixed in this repo

Mistakes in how this repository called the libraries, listed so they are never
mistaken for upstream problems.

| what | whose | why |
|---|---|---|
| `menu.Set` called with no `Options.Window` | ours | required on Windows, documented, and the error says so |
| `menu.Set` called from a goroutine without `Options.Dispatch` | ours | documented, including the hang it causes if passed too early |
| waiting on `tray.Run` | ours | documented as blocking |
| `openurl.Open != nil` as a capability check | ours | a function value is never nil; `go vet` says so |
| `file://` URL built by concatenation | ours | Windows needs `file:///C:/dir`; covered by `TestFileURL` |
