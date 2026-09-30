# robotgo: can it help? No, except as a reference

Status: evaluated, **no code changed** · 2026-09-30

The owner asked whether [go-vgo/robotgo](https://github.com/go-vgo/robotgo)
can replace or improve our input and screen automation. Short answer: nothing
in it does anything we need better than what we already have, and it would add
a heavy dependency graph. The one real gap it points at (the Swift helper
that finds the window ID) is better closed with about 60 lines of purego, which
was prototyped here. The Windows VM was not used.

## What we do today, and where it hurts

| need | how we do it now | file |
|---|---|---|
| type into the VM during Setup/UEFI | AppleScript `input keystroke vm text` sent to UTM | `internal/utmvm/assets/boot.applescript`, `vm_create.go:1092` |
| find the VM window | `swift windowid.swift` → `CGWindowListCopyWindowInfo` | `internal/utmvm/vm_screen.go`, `assets/windowid.swift` |
| photograph the VM window | `screencapture -x -o -l <id>` | `vm_screen.go` |
| act inside the guest | PowerShell in dev's session through the `/it` scheduled task | `assets/desktop-reset.ps1`, `desktop_reset.go` |
| dismiss the file dialog in conformance | macOS: purego `abortModal`; Windows: `GetWindow(GW_ENABLEDPOPUP)` + `WM_CLOSE` | `examples/conformance/platform_{darwin,windows}_test.go` |

UTM's `input keystroke` sends keys to the VM by id. It needs no focus, does
not care which app is in front, and keeps working while the owner switches
apps. `screencapture -l` captures a window on another Space without
activating it. Both are **already the no-focus-stealing routes**. The file
dialog is already dismissed through the OS with no cgo. The remaining pain is
the AppleScript escaping trap (fixed and covered by `vm_boot_test.go`) and the
need for Xcode CLT `swift` just to get a window ID.

## robotgo as it stands (checked 30 Sep 2026)

- Latest release **v1.1.0**, 22 Sep 2026. `v2.0.0-beta4` is the same commit
  (12f16b7). The module path has no `/v2`, so the v2 tags cannot be used as Go
  module versions. Apache-2.0, 10.8k stars, 5 open issues. Many issues were
  closed in bulk in Sep 2026 by commits titled "Review robotgo Issue".
- **Default backend is cgo** (`robotgo.go`: `#cgo darwin LDFLAGS`, C headers
  under `base/`, `key/`, `mouse/`, `screen/`, `window/`). The README still says
  "make sure Golang, GCC is installed", and on Windows asks for llvm-mingw.
- **New: experimental pure-Go backends**, selected by build tag (`mac`, `win`,
  or `purego` for both). They were added Jun–Jul 2026 in commits 8b9e625 /
  2dbe3d4, whose messages say *"This is not tested generated experimental
  code"*, and 26392db ("add pure go Darwin support"). The README calls them
  experimental.
- In the pure-Go macOS backend, window management is not implemented:
  `darwin/window.go` returns `ErrNotSupported` from `ActiveName`, and
  `GetTitle`/`MinWindow`/`MaxWindow`/`CloseWindow` do nothing. Its package doc
  says AX "is not reachable without Objective-C". Screen capture is
  `CGDisplayCreateImageForRect` loaded through purego, which captures the
  display only. That API is marked obsoleted in the macOS 15 SDK (robotgo#680).
- Windows pure-Go backend: `SendInput` for keys and mouse, `BitBlt` from the
  screen DC for capture (a screen rectangle, not `PrintWindow`, so it captures
  whatever covers the window), and `SetForegroundWindow` for activation.
- Neither backend captures a single window. robotgo#625 "Screenshot
  background windows" has been open since 2023.

### Builds measured here (go1.27.1, darwin/arm64 host, robotgo v1.1.0)

Program: `GetScreenSize`, `Location`, `CaptureImg`, in `/tmp/claude-501/robotgo-eval/try`.

| GOOS/GOARCH | CGO_ENABLED | tags | result |
|---|---|---|---|
| darwin/arm64 | 1 | — | builds, 6.7 MB; runs |
| darwin/arm64 | 0 | — | **fails**: `undefined: Move, DragSmooth, MoveSmooth, Location, Click` (robotgo_fn_v1.go) |
| darwin/arm64 | 0 | `purego` (= `mac`) | builds, 4.1 MB; runs |
| windows/arm64 | 0 | — | **fails**, same undefined symbols |
| windows/arm64 | 0 | `purego` (= `win`) | builds, 4.0 MB (not run: VM not used) |
| windows/amd64 | 0 | `win` | builds, 4.3 MB |
| windows/arm64 | 1 | — | **fails** from the Mac: `runtime/cgo: 'windows.h' file not found` (needs an arm64 mingw cross toolchain) |

On this Mac (macOS 27 / Darwin 27.0.0), in a terminal that already has Screen
Recording permission, both darwin builds captured the full 3456×2234 display
(519–560 distinct sampled colours, so the image was not blank).
`FindIds("UTM")` returned two pids with either backend.
`ActiveName("UTM")` returned `ErrNotSupported` with `purego`. With cgo it
returned nil, so it probably raised UTM on the owner's screen. That is the
focus-stealing behaviour we avoid, and the probe should not have called it.
Keyboard and mouse injection were not run: they act on whatever is focused on
the owner's desktop.

The pure-Go builds pull in `gopsutil/v4`, `vcaesar/{gops,imgo,keycode,screenshot}`,
`x/image`, `tklauser/go-sysconf`. On Windows they also pull `tailscale/win`,
`dblohm7/wingoes`, `go-ole`, `yusufpapurcu/wmi` and `x/exp`, and the test
module's go.sum has 57 lines.

## 1. What it could do for us, per need

| need | robotgo | vs. what we have |
|---|---|---|
| keys into the UTM window | `KeyTap`/`Type` post `CGEvent`s to the HID tap (goes to the **focused** app), or to a pid via `CGEventPostToPid` | worse. Needs Accessibility permission, and typing lands in the owner's editor if they switch apps, which is exactly what they do during an install. Whether UTM's display view accepts pid-posted events while it is in the background is unverified. UTM's `input keystroke` needs neither focus nor Accessibility (only Automation, already checked by `CheckAutomation`) |
| find/activate the UTM window | cgo: AX-based `ActivePid`/`GetBounds`; purego: not supported | we never want to activate it. Finding the ID is the only need (see §3) |
| capture one window | none, on any backend (display or rectangle only; #625 open) | `screencapture -l` already captures one window, off-Space, without focus |
| image search | `vcaesar/bitmap` / `gcv` (cgo, OpenCV) | not needed. We capture for people and for a record, and assert nothing on pixels |
| clipboard | yes | the conformance suite already tests native's clipboard. Using robotgo to check it would test one library with another |
| inside the guest: click a dialog | `win` tag: `SendInput` + `SetForegroundWindow`, builds cgo-free for windows/arm64 | must still run in dev's session via the `/it` task, where PowerShell already runs. `SendInput` is blind (coordinates), blocked by UIPI for elevated windows, and needs the foreground lock. The one dialog we dismiss is already closed through the OS by its owner HWND |
| conformance: capture own window | screen `BitBlt` / display capture | wrong primitive. It captures what is on top, so it cannot prove a window's content. Windows `PrintWindow` / DWM and macOS `screencapture -l` (the other agent's work) are the right calls |

Permissions on macOS: robotgo needs Accessibility (input, AX) and Screen
Recording (capture). Without them it "silently fails (or returns empty
results)", in the words of `darwin/doc.go`, which conflicts with our rule that
nothing reports success it did not verify. For the macOS 15+ reliability of
the cgo backend there are closed reports of SIGBUS in `_Cfunc_keyCode` on M3
(#690), "cannot work macOS 15.4.1" (#722) and slow capture (#731). None of the
three could be reproduced or ruled out here without injecting input into the
owner's desktop.

## 2. The cgo cost

- **Shipped tool (root module).** The default backend needs cgo. GoReleaser
  builds with `CGO_ENABLED=0` (`.goreleaser.yaml`), and `go:check`
  cross-compiles linux and windows with `CGO_ENABLED=0`. With cgo, the build
  would need Xcode per arch. `-trimpath -buildvcs=false` reproducibility would
  then depend on the C toolchain version, and cross-builds for linux/windows
  would break. With the `purego` tag it builds, but the tag has to be passed
  everywhere: GoReleaser, `go:check`, `go:lint`, `go:tool`, `.mcp.json` and
  every `go run`/`go test`. The one place it is forgotten fails with
  "undefined: Move". It also adds about 10 modules to `go list -deps
  ./cmd/irgo-winvm` for no capability we lack.
- **examples/conformance.** The Windows binary is cross-compiled **on the Mac**
  with `CGO_ENABLED=0` (`glazecheck/check.go:257`, `mise-tasks/app/test`,
  `go:check`), and that is "the claim this project rests on". A cgo robotgo
  there fails to build for Windows from the Mac (`windows.h` not found above),
  so `glaze:windows` and `app:test` break. Only the native CI jobs could build
  it, and whether GitHub's `windows-11-arm` image ships an arm64 C compiler
  was not checked. The `purego` tag avoids that. It would still be a second
  library under test inside a suite whose job is testing glaze and native, and
  the dialog it would dismiss is already dismissed.
- **Separate helper binary.** It could live on its own, like `site`, but there
  is no job for it: every host-side need is met without focus-stealing, and
  guest-side input is better done by HWND or UI Automation than by coordinates.

## 3. cgo-free alternatives, when a need appears

- **macOS window ID without Swift.** `CGWindowListCopyWindowInfo` plus
  CoreFoundation getters are plain C functions, so purego can call them with no
  Objective-C. It was prototyped in `/tmp/claude-501/robotgo-eval/cgwin`, about
  60 lines, `CGO_ENABLED=0`. It returned the **same three UTM window IDs**
  (1048, 1060, 1309) as `swift windowid.swift`, in 0.36 s cold, against
  1.03 s cold and 0.25 s warm for Swift. Titles need Screen Recording
  permission either way. This would remove the Xcode CLT requirement from
  `vm-screen` and the temp-file dance in `windowID`. The cost is purego in the
  root module, which the tool does not import today (it is already in
  `examples`). Its darwin files need a build tag, with the current stub kept
  for linux/windows in `go:check`.
- **macOS input/AX.** `CGEventCreateKeyboardEvent`/`CGEventPostToPid` are C and
  purego-callable, and `AXUIElement*` are C too (CF types, no objc). Not needed
  while UTM's `input keystroke` works.
- **Windows, inside the guest.** `golang.org/x/sys/windows` (already a
  dependency) or `syscall.NewLazyDLL("user32.dll")`, as the conformance suite
  does, for `EnumWindows`/`GetWindow`/`PostMessage`/`SendInput`/`PrintWindow`.
  For buttons by name, UI Automation over COM with `go-ole` (maintained, pushed
  Mar 2026). There is no maintained pure-Go UIA wrapper: `zzl/go-win32api` is
  generated and has 50 stars (last push May 2024), and `lxn/win` has been idle
  since Sep 2023. PowerShell's `System.Windows.Automation` in the existing `/it`
  task needs no Go at all.
- **Screen capture libraries.** `kbinani/screenshot` needs cgo on darwin
  (`//go:build cgo && darwin`) and is display-only. `vcaesar/screenshot` is
  robotgo's fork. `progrium/darwinkit` is cgo. None captures a single window.

## 4. Recommendation

**Do not adopt robotgo**, not in the tool, not in `examples/conformance`, and
not as a helper. It has no single-window capture on any OS. It has no window
management on the cgo-free macOS path. It is focus-dependent where we are
focus-independent. Its cgo-free backends are self-described as untested
generated code. Our current routes (UTM `input keystroke`, `screencapture -l`,
OS-level dialog dismissal) are the ones robotgo lacks.

Keep it as a reading reference: `darwin/cg.go` shows purego signatures for
CGEvent and CGDisplay, and `win/` shows `SendInput` structs.

**Next step (optional, small):** replace `windowid.swift` with the purego
`CGWindowListCopyWindowInfo` call proven above, in `internal/utmvm`
(darwin-only file, stub elsewhere). Verify it by comparing IDs with the Swift
output on a machine running UTM, and add a negative control (wrong owner name →
"no UTM window titled"). Only do it if dropping the Xcode CLT requirement for
`vm-screen` is worth adding purego to the shipped binary's dependencies. That
is the owner's call.
