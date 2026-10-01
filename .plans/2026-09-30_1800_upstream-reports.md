# Upstream reports: drafted, then sent

Status: **SENT** 1 Oct 2026 (owner's go-ahead: "Send what you want. I trust you.") · drafted 2026-09-30

Every finding in [docs/UPSTREAM.md](../docs/UPSTREAM.md) that upstream did not
know about, written out ready to send. Sent on 1 Oct 2026; see "What was sent"
below. The bodies further down are the 30 Sep drafts. What went out was
tightened and updated to current trunk, and in two cases differs in substance
(§4 not sent, §6 rewritten around the measured cause). §1b has its own plan
(`2026-09-30_1745_glaze-1b-upstream.md`) and went out as glaze#39. glaze#34 was
filed on 30 Sep, and its PR is glaze#40.

## What was checked first (30 Sep 2026)

- `gh issue list` / `gh pr list --state all` on crgimenes/glaze and
  crgimenes/native: no report of §1-sequential, §2 or §3. glaze#31 (the
  tray-callback route of §1) was closed by the owner, fixed in bd5d994, released
  in **v0.0.48**. glaze#34 is ours, open, unanswered, no PR yet.
- `gh search issues` on utmapp/UTM for utmctl exit status, exec output,
  save-state, ip-address, import errors: no existing report of any of the five.
  Related, not duplicates: UTM#3819 (snapshot refused with VirGL or NVMe — the
  *refusal* half of the save-state finding), UTM#7669 (suspend/quit ordering
  loses state).
- utmctl source (`utmctl/UTMCtl.swift`, v4.7.5 and main at b1e1b12a0) read to
  locate causes where it can be done without a debugger. Nothing below claims a
  cause that was not read in the source or measured.

## What was sent (1 Oct 2026)

**Checked again first.** On crgimenes/glaze, crgimenes/native and utmapp/UTM
(issues and PRs, all states, several searches per finding), nothing duplicated
any report. glaze trunk had moved to `be1017e` (after v0.0.65). None of its
seven commits touches the `ErrUnsupported` sentinels, the `isRunning` bootstrap
check or `webview2_scheme_windows.go`. native trunk was still v0.1.15. UTM's
latest is 5.0.6 beta; utmctl on main still has both bugs.

**glaze/native PRs.** Both CONTRIBUTING files welcome small fixes as direct PRs
and ask for an issue first only for features, APIs and backends. So §1–§3 went
as issue + PR, and §1b (a large COM change with a behaviour choice) went as an
issue only. Before pushing, the two glaze branches were rebased onto `be1017e`
and re-checked: `go fix`, `gofmt`, `go vet`, `go test -short`, golangci-lint for
darwin/linux/windows, six cross-builds, and the examples module. The nsapp test
was re-run with trunk's `webview_darwin.go` (hangs) and with the fix (passes).
native: the same checks plus `make cross`. The fork `joeblew999/native` already
existed (made for the input/screen work), so a `fork` remote was added to the
clone; no new fork was created.

**glaze#34.** No comment or objection by 1 Oct, so the branch was opened as PR
[glaze#40](https://github.com/crgimenes/glaze/pull/40). Merged onto `be1017e` it is clean: lint for three OSes,
Windows builds, vet and test compile.

**§4 not sent.** native#8 (closed, shipped in v0.1.11) gave `tray.Config.OnReady`
for exactly "open a window once the tray is up". glaze#31 (v0.0.48) made
`glaze.New` work from a tray callback. That is the maintainer's supported tray +
window pattern, documented in the tray README and run in CI. The question
as drafted asked about the opposite order, which is a choice made in
`examples/glaze-all`. Asking would cost the maintainer time for an answer he
has already given.

**§6 re-run, then rewritten.** I ran the repro against irgo-win11 (read-only;
the VM was not stopped). `utmctl exec irgo-win11 --cmd cmd.exe /c echo hello`
printed nothing and exited 0, and `... /c exit 3` exited 0. Then I isolated it:

- AppleScript `execute … with output capturing`, `get result`, `delay 3`,
  `get result`: the first poll gave `exited:false`, the second `exited:true,
  exit code:3, output text:"hello \n", output data:"aGVsbG8gDQo="`. The guest
  agent and UTM's scripting layer are fine.
- A Swift `SBApplication` probe made the same `executeAt:…outputCapturing:` and
  `getResult` calls utmctl makes. The dictionary keys were `exited, exitCode,
  signalCode, outputText, outputData, errorText, errorData`, and
  `result["hasExited"]` was `nil`.
- utmctl (`UTMCtl.swift:469` in v4.7.5, `:584` on main) loops
  `while result["hasExited"] as? Bool == false`. `nil == false` is false, so
  the loop stops after the first poll. A utmctl bug, so it was filed as a bug
  with the one-word fix. The "one-argument command line" quirk was dropped: it
  is the agent correctly reporting that no executable has that name.

**Wording changes from the drafts.** Versions now say "v0.0.61 through trunk
be1017e". The UTM reports cross-link #7933 for the exit status. §7 names the
VM's `-gl` display and NVMe disk instead of attaching config.plist. §9 cites
the `try? VMData` lines.

## How to send one

The bodies are between markers in this file. From the repo root:

```sh
body() { awk -v k="$1" '$0=="<!-- body:"k" -->"{f=1;next} $0=="<!-- /body -->"{f=0} f' \
  .plans/2026-09-30_1800_upstream-reports.md; }
U="$HOME/workspace/go/src/github.com/crgimenes"   # $UPSTREAM_DIR
```

Each item gives the exact commands. An issue is filed first and its URL is
substituted into the PR body (`ISSUE_URL`).

| # | where | what | branch | filed |
|---|---|---|---|---|
| 1 | glaze | `New` hangs after a run loop has *stopped* (the case #31's fix does not cover) | `fix/nsapp-first` a13b3c8 (rebased on be1017e) | [glaze#35](https://github.com/crgimenes/glaze/issues/35) + PR [#36](https://github.com/crgimenes/glaze/pull/36) |
| 2 | glaze | `ErrIconUnsupported`, `menu.ErrUnsupported` do not wrap `errors.ErrUnsupported` | `fix/errunsupported-wrap` 02dfa99 (rebased on be1017e) | [glaze#37](https://github.com/crgimenes/glaze/issues/37) + PR [#38](https://github.com/crgimenes/glaze/pull/38) |
| 3 | native | ten `ErrUnsupported` sentinels do not wrap `errors.ErrUnsupported` | `fix/errunsupported-wrap` 854cdb9 | [native#9](https://github.com/crgimenes/native/issues/9) + PR [#10](https://github.com/crgimenes/native/pull/10) |
| 4 | native | no supported way to have a tray *and* a window | — | **not sent**: answered by native#8 / glaze#31 |
| 5 | UTM | `utmctl` reports failure and exits 0 | — | [UTM#7933](https://github.com/utmapp/UTM/issues/7933) |
| 6 | UTM | `utmctl exec` returns no output | — | [UTM#7932](https://github.com/utmapp/UTM/issues/7932), cause measured |
| 7 | UTM | `suspend --save-state` reports success and power-cuts the guest | — | [UTM#7934](https://github.com/utmapp/UTM/issues/7934) |
| 8 | UTM | `ip-address` blocks indefinitely with no guest agent | — | [UTM#7935](https://github.com/utmapp/UTM/issues/7935) |
| 9 | UTM | a rejected `config.plist` names no field | — | [UTM#7936](https://github.com/utmapp/UTM/issues/7936) |

The glaze and native branches live in the clones at `$U/{glaze,native}` (made
in worktrees under `/tmp/claude-501/upstream-wt/`, so the refs are in the
clones). Each is one commit on `origin/trunk` as of today (glaze 0dce849 =
v0.0.61, native 58f48b7 = v0.1.15); rebase before pushing if trunk has moved.
Verified Mac-side only — see docs/UPSTREAM.md for what was run.

---

## 1. glaze — `New` hangs when the app finished launching under a loop that has since stopped

```sh
url=$(gh issue create -R crgimenes/glaze \
  --title "[macOS] glaze.New blocks forever when an earlier run loop has stopped (case not covered by #31)" \
  --body "$(body glaze-nsapp-issue)")
git -C "$U/glaze" push fork fix/nsapp-first
gh pr create -R crgimenes/glaze --base trunk --head joeblew999:fix/nsapp-first \
  --title "darwin: New must not block when the app already finished launching" \
  --body "$(body glaze-nsapp-pr | sed "s|ISSUE_URL|$url|")"
```

<!-- body:glaze-nsapp-issue -->
Thanks for the quick fix to #31 — the tray-callback route works in v0.0.61. A
neighbouring route still hangs: the run loop has **run and stopped** before the
first web view is created.

**Reproduce** (macOS, glaze v0.0.61, native v0.1.15, `CGO_ENABLED=0`):

```go
package main

import (
	"fmt"
	"runtime"
	"time"

	"github.com/crgimenes/glaze"
	"github.com/crgimenes/native/tray"
)

func init() { runtime.LockOSThread() }

func main() {
	go func() { time.Sleep(1500 * time.Millisecond); tray.Stop() }()
	_ = tray.Run(tray.Config{Title: "repro", Items: []tray.Item{{Title: "x"}}})
	fmt.Println("tray stopped; calling glaze.New")
	w, _ := glaze.New(false) // never returns
	fmt.Println("window created")
	w.Destroy()
}
```

It prints the first line and nothing else. Stack:

```
glaze.(*webview).windowInit.func1()   webview_darwin.go:651   <- the temporary [NSApp run]
glaze.newWebView(...)                 webview_darwin.go:612
glaze.NewWithOptions(...)             webview_darwin.go:591
glaze.New(...)                        webview_darwin.go:545
```

**Why.** bd5d994 skips the bootstrap when `[NSApp isRunning]`. Here it is not
running any more, so the bootstrap is taken and waits for
`applicationDidFinishLaunching:` — which AppKit delivers once per process and
already delivered to the tray's loop. The same happens after anything else that
ran and stopped `NSApp` first (a dialog, another toolkit's window).

A fix and a test are in the linked PR.
<!-- /body -->

<!-- body:glaze-nsapp-pr -->
Fixes ISSUE_URL.

`windowInit` now also asks `NSRunningApplication.currentApplication` whether
the app `isFinishedLaunching`; if it has, it takes the path the delegate
callback takes, minus stopping a temporary loop that was never started. The
shared body moves into `finishLaunching` so the two paths cannot drift.

`TestNewAfterAppFinishedLaunching` runs the case in a child process of the
test binary (it needs a process in which no web view exists yet, which
`TestMain`'s scenarios have already spent): run `NSApp`, stop it after a
second, then `New`, with a 10 s watchdog. It fails with a hang on trunk and
passes with the change. Skipped under `-short`.

Checked: `go test ./...` and `go test -short ./...` on macOS 27 (M2 Pro);
`GOOS=windows` and `GOOS=linux` build and vet; `golangci-lint run` for darwin
and windows, 0 issues.
<!-- /body -->

---

## 2. glaze — unsupported sentinels do not wrap `errors.ErrUnsupported`

```sh
url=$(gh issue create -R crgimenes/glaze \
  --title "ErrIconUnsupported and menu.ErrUnsupported do not match errors.Is(err, errors.ErrUnsupported)" \
  --body "$(body glaze-errunsupported-issue)")
git -C "$U/glaze" push fork fix/errunsupported-wrap
gh pr create -R crgimenes/glaze --base trunk --head joeblew999:fix/errunsupported-wrap \
  --title "ErrUnsupported sentinels wrap errors.ErrUnsupported" \
  --body "$(body glaze-errunsupported-pr | sed "s|ISSUE_URL|$url|")"
```

<!-- body:glaze-errunsupported-issue -->
`glaze.ErrIconUnsupported` and `menu.ErrUnsupported` are plain `errors.New`
values, so the standard check for "not available here" matches neither:

```go
err := glaze.SetAppIcon(png)           // on Windows or Linux
errors.Is(err, errors.ErrUnsupported)  // false
```

`SetAppIcon` returns this on every Windows and Linux run, by design, so a
program that treats unsupported capabilities uniformly reports a failure on a
platform behaving exactly as documented — unless it imports each package just
to name its sentinel. native has the same pattern across its packages (filed
separately there).

Wrapping keeps `errors.Is(err, glaze.ErrIconUnsupported)` working, so it is
compatible:

```go
var ErrIconUnsupported = fmt.Errorf("glaze: setting the application icon at runtime is not supported on this platform: %w", errors.ErrUnsupported)
```

Happy to send it as a PR (linked).
<!-- /body -->

<!-- body:glaze-errunsupported-pr -->
Fixes ISSUE_URL.

Wraps `errors.ErrUnsupported` with `%w` in `glaze.ErrIconUnsupported` and
`menu.ErrUnsupported`. `errors.Is` against the package variables still
matches; the only visible change is the message gaining
`: unsupported operation`.

Tests: `appicon_unsupported_test.go` (both sentinels) and
`menu/unsupported_test.go`.

Checked: `go test ./...` on macOS 27; `GOOS=windows` / `GOOS=linux` build and
vet; `golangci-lint run` darwin and windows, 0 issues. The same change on
v0.0.47 was run on Windows 11 ARM64, where `SetAppIcon` then reads as
unsupported rather than failed.
<!-- /body -->

---

## 3. native — `ErrUnsupported` sentinels do not wrap `errors.ErrUnsupported`

native has no `fork` remote yet; the first command makes one.

```sh
(cd "$U/native" && gh repo fork crgimenes/native --remote --remote-name fork)
url=$(gh issue create -R crgimenes/native \
  --title "ErrUnsupported sentinels do not match errors.Is(err, errors.ErrUnsupported)" \
  --body "$(body native-errunsupported-issue)")
git -C "$U/native" push fork fix/errunsupported-wrap
gh pr create -R crgimenes/native --base trunk --head joeblew999:fix/errunsupported-wrap \
  --title "ErrUnsupported sentinels wrap errors.ErrUnsupported" \
  --body "$(body native-errunsupported-pr | sed "s|ISSUE_URL|$url|")"
```

<!-- body:native-errunsupported-issue -->
Every package's `ErrUnsupported` is a plain `errors.New`, so
`errors.Is(err, errors.ErrUnsupported)` matches none of them. An app using
several packages has to import each one just to name its sentinel, and every
package added later (alert, bookmark and pointer since August) silently extends
that list.

```go
err := nocapture.Protect(win)          // on macOS, unsupported by design
errors.Is(err, errors.ErrUnsupported)  // false
```

The README documents the unwrapped form as the convention. Wrapping keeps
`errors.Is(err, pkg.ErrUnsupported)` working:

```go
var ErrUnsupported = fmt.Errorf("clipboard: not supported on this platform: %w", errors.ErrUnsupported)
```

PR linked, with a test per package so a new package cannot reintroduce it.
<!-- /body -->

<!-- body:native-errunsupported-pr -->
Fixes ISSUE_URL.

Wraps `errors.ErrUnsupported` with `%w` in alert, bookmark, clipboard, mmap,
nocapture, openurl, pointer, power, singleinstance and tray. `errors.Is`
against each package variable still matches; messages gain
`: unsupported operation`. The README's per-package conventions now describe
the wrapped form.

Each package gains `unsupported_test.go` asserting the wrapping.

Checked: `go test ./...` on macOS 27; `GOOS=windows` / `GOOS=linux` build;
`golangci-lint run` darwin and windows, 0 issues. The seven packages that
existed at v0.1.6 were also run wrapped on Windows 11 ARM64.
<!-- /body -->

---

## 4. native/tray — a tray and a window in one process

A question, not a defect: each package's documented behaviour is right on its
own. Worth asking before anyone writes an API.

```sh
gh issue create -R crgimenes/native \
  --title "tray: supported way to run a tray icon alongside a glaze window?" \
  --body "$(body native-tray-question)"
```

<!-- body:native-tray-question -->
`tray.Run` blocks driving the OS event loop and wants the main goroutine locked
to the main thread; so does a `glaze.WebView`'s `Run`. Nothing says they are
mutually exclusive, and an app with a tray icon and a window is common.

What works today (macOS 15/27 and Windows 11 ARM64): create the window first,
then post `tray.Run` onto the UI thread with `w.Dispatch` and never wait on it,
letting the nested loop run until `tray.Stop`:

```go
w, _ := glaze.New(false)
w.Dispatch(func() { _ = tray.Run(cfg) }) // not waited on
w.Run()
```

It is undocumented, so it may break without notice. Is this the intended
pattern (and worth a line in the tray docs), or would you rather have an API
that attaches a tray to a run loop someone else owns?
<!-- /body -->

---

## 5. UTM — `utmctl` reports failure and exits 0

```sh
gh issue create -R utmapp/UTM \
  --title "utmctl: a failed command prints the error but exits 0" \
  --body "$(body utm-exit-status)"
```

<!-- body:utm-exit-status -->
**Describe the issue**
When the Apple event behind a `utmctl` command fails, utmctl prints the error
and exits **0**, so scripts cannot detect the failure.

Two measured cases:

```sh
# 1. a VM whose guest agent is not running
utmctl ip-address <vm>; echo "exit=$?"
# prints the agent error, exit=0

# 2. a VM entry whose bundle was deleted outside UTM
utmctl delete <uuid>; echo "exit=$?"
# prints "... couldn't be removed ...", exit=0
```

**Cause, from the source** (`utmctl/UTMCtl.swift`):
`EventErrorHandler.eventDidFail` writes the error to stderr and returns `nil`;
`UTMAPICommand.run()` then returns normally. On main (b1e1b12a0) the handler
records `hasFailed`, but only `snapshot create` consults it. Checking it once
in the shared entry point would cover every subcommand:

```swift
try run(with: utmApp)
if UTMCtl.EventErrorHandler.shared.hasFailed {
    throw ExitCode.failure
}
```

(Not built here — offered as a pointer, happy to send a PR if wanted.)

**Configuration**
* UTM Version: 4.7.5 (current release); source read at v4.7.5 and main
* macOS Version: 27.0.1 (also seen on 26.x)
* Mac Chip: M2 Pro
<!-- /body -->

---

## 6. UTM — `utmctl exec` returns no output from a Windows ARM64 guest

**Before filing:** re-run the repro below on the current VM and paste the real
output. The source says output *should* come back (`outputCapturing: true`,
written to stdout, exit code propagated), so this may be the guest's qemu-ga
rather than utmctl; the report says so rather than guessing. Needs the VM,
which this pass did not have.

```sh
gh issue create -R utmapp/UTM \
  --title "utmctl exec returns no stdout/stderr and exit 0 from a Windows 11 ARM64 guest" \
  --body "$(body utm-exec-output)"
```

<!-- body:utm-exec-output -->
**Describe the issue**
Against a Windows 11 ARM64 guest with the UTM guest tools installed,
`utmctl exec` returns no output and exits 0 whatever the guest command did:

```sh
utmctl exec <vm> --cmd cmd.exe /c echo hello; echo "exit=$?"
# (nothing printed) exit=0
utmctl exec <vm> --cmd cmd.exe /c exit 3; echo "exit=$?"
# exit=0
```

The command does run (a redirect to a file in the guest, pulled back with
`utmctl file pull`, contains the output). `exec`'s help says "The return value
of the command will be returned from this tool", and `UTMCtl.swift` asks for
`outputCapturing: true` and propagates `exitCode`, so something between the
guest agent and utmctl is dropping both. We have not established which side.

Related quirk: passing the whole command line as one argument makes the agent
look for a file of that entire name and answer "No such file or directory",
which reads like a dead agent.

**Configuration**
* UTM Version: 4.7.5
* macOS Version: 27.0.1
* Mac Chip: M2 Pro
* Guest: Windows 11 ARM64, UTM guest tools (qemu-ga) from UTM's download
<!-- /body -->

---

## 7. UTM — `suspend --save-state` reports success and power-cuts the guest

Comment on UTM#3819 instead if the maintainers prefer; the refusal half is
that issue, the silent power-cut is not.

```sh
gh issue create -R utmapp/UTM \
  --title "utmctl suspend --save-state exits 0 but writes no state and hard-stops the guest" \
  --body "$(body utm-save-state)"
```

<!-- body:utm-save-state -->
**Describe the issue**
`utmctl suspend --save-state <vm>` on a running QEMU VM (Windows 11 ARM64
guest) either refuses — naming GPU acceleration, then NVMe (see #3819) — or
**exits 0, writes no state file, and leaves the VM `stopped`**. The guest's next boot goes through
Windows' "Diagnosing your PC", the signature of an unclean power-off.

```sh
utmctl suspend --save-state <vm>; echo "exit=$?"   # exit=0
utmctl status <vm>                                  # stopped
# no saved state in the bundle; next start is a cold boot with repair
```

Plain `utmctl suspend` (to memory) works correctly on the same VM.

Expected: either a saved state that resumes, or a non-zero exit with the
reason, and the guest left running.

**Configuration**
* UTM Version: 4.7.5
* macOS Version: 26.x / 27.0.1
* Mac Chip: M2 Pro
* Guest: Windows 11 ARM64 (attach its config.plist when filing)
<!-- /body -->

---

## 8. UTM — `ip-address` blocks indefinitely with no guest agent

```sh
gh issue create -R utmapp/UTM \
  --title "utmctl ip-address hangs indefinitely when the guest has no agent" \
  --body "$(body utm-ip-hang)"
```

<!-- body:utm-ip-hang -->
**Describe the issue**
`utmctl ip-address <vm>` against a running VM whose guest has no agent (here:
no OS installed yet) prints nothing and does not return — it was killed after
ten minutes. Anything that asks "is this VM usable" through it hangs with it.
On a guest whose agent is merely slow the same call returns
`Timed out waiting for RPC`, so a timeout exists on some path but not this one.

```sh
utmctl ip-address <vm-with-no-os>   # never returns
```

Expected: a bounded wait and a non-zero exit (see also the exit-status report).

**Configuration**
* UTM Version: 4.7.5
* macOS Version: 26.x
* Mac Chip: M2 Pro
<!-- /body -->

---

## 9. UTM — a rejected `config.plist` names no field

A feature request in practice; file it as one.

```sh
gh issue create -R utmapp/UTM \
  --title "Importing a VM with an invalid config.plist should name the offending key" \
  --body "$(body utm-config-field)"
```

<!-- body:utm-config-field -->
**Describe the issue**
When a `config.plist` does not decode, UTM reports only "cannot import this
VM". The config is decoded with Swift `Codable`, and the `DecodingError` it
throws carries the key path and the reason, but that is not shown.

Two examples from generating configs for 4.7.5, each found by bisection:

- omitting `PS2Controller` (decoded with a non-optional `decode()`, no default)
- `UsbBusSupport` = `"USB3_0"` instead of the raw value `"3.0"`

Both produce the same generic message. Surfacing
`DecodingError.Context.codingPath` and `debugDescription` (even in the debug
log) would turn each of these from an import-bisect cycle into a one-line fix.

**Configuration**
* UTM Version: 4.7.5
* macOS Version: 26.x
* Mac Chip: M2 Pro
<!-- /body -->

## Verify

Done 1 Oct 2026: docs/UPSTREAM.md rows set to `FILED` with links, and the
`patch/*` branches deleted from both clones. Not done: moving this plan to
`.plans/done/`, because `.plans/README.md` had another session's uncommitted
edits at the time. Move it, with its index row, when that file is free.
Next: watch the issues and PRs, and when a release contains a fix, bump
`examples/go.mod` and act on the "Removed when" notes in docs/UPSTREAM.md.
