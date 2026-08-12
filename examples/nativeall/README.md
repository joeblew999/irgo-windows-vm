# nativeall — a glaze example exercising every native capability

One program that calls **all** of it, with probes to verify each one.

No such program existed anywhere: not in this repo, not in glaze's examples, not
in `crgimenes/native`. All of those test one capability per binary, so nobody
had ever seen the whole native surface run together on Windows — which is
exactly what a desktop app depends on.

It covers both halves:

- **`crgimenes/native`** — clipboard, power, single-instance, mmap, open-url,
  tray, no-capture
- **glaze's own** — native file dialogs, the menu bar, the app icon

It exits on its own with a status line per capability and a non-zero code if any
FAILED, so it runs unattended.

## Two modes

**The report** is the default, because that is how the VM runs it: no flags,
no supervision, a status line per capability and a non-zero exit if any FAILED.

**`-i` is for using them.** The same capabilities, driven by hand in a window —
a tray you click, a file dialog you pick a file in, a Dock icon that changes
colour, a second copy of the program handing its arguments to the first. The
report answers *does it work*; this answers *what is it like*, which a table
cannot.

On the machine you are sitting at — both need a desktop session, so neither
works over ssh:

```sh
mise run example       # the report, exits on its own
mise run example:try   # by hand, stays open until you close it
```

In the Windows VM, which is the point of the exercise:

```sh
mise run vm:probe-gui  # the report, plus the two glaze probes
mise run vm:try        # by hand, inside Windows
```

`-i` mirrors its log to stdout as well as to the page, so tray clicks and menu
choices come back through `irgo-winvm run` even though they happen on a screen
you are not looking at. To see the screen itself:

```sh
go run ./cmd/irgo-winvm screenshot -vm irgo-win11 -o shot.png
```

That needs no guest agent, so it works while the app is holding the session.
Verified this way on Windows 11 ARM64: the page renders in WebView2, in the
interactive session, in the guest's own light theme.

### What `-i` gives you that the report does not

| | report | `-i` |
|---|---|---|
| tray | raised, torn down 2s later | stays up; click its items, they report back |
| file dialogs | `OpenFile` presented, left open | all four, and you pick a real path |
| menu bar | installed | installed with working items, plus Edit wired to the responder chain so Cmd+C/V reach the page |
| app icon | a 1×1 transparent pixel | a visible 128px icon, a new colour per click |
| single instance | acquire, re-acquire refused | hold it, then launch a second copy and watch its arguments arrive |
| `mmap` | mapped, wrote a byte | write through the mapping, then read the file off disk to prove it |
| `openurl` | a `file://` URL | any URL you type, and the scheme guard refusing what it should |
| events bridge | not used | both directions — every button and every log line |

`-gui` is not optional for this one. Under the QEMU guest agent alone the binary
runs as SYSTEM in session 0, where there is no window station and every windowed
call fails — WebView2 included, which reports it as an environment-creation
failure and reads like a missing runtime.

## Measured on macOS 15 / darwin/arm64

```
CAPABILITY               STATUS       DETAIL
------------------------------------------------------------------------------
clipboard.write          OK
clipboard.read           OK           round trip verified
power.preventSleep       OK           acquired + released
singleinstance.acquire   OK           lock held; re-acquire refused
mmap.map                 OK           mapped 10 bytes, wrote through
openurl.Open             OK           opened a file:// URL in the default handler
openurl.Open/refused     OK           custom protocol scheme correctly refused
openurl.Reveal           OK           revealed a directory in the file manager
glaze.SetAppIcon         OK           
nocapture.Protect        UNSUPPORTED  nocapture: not supported on this platform
menu.Set                 OK           native menu bar installed
tray.Run                 OK           icon raised and removed
glaze.OpenFile           OK           dialog presented (left open, process exits)
```

`nocapture` on macOS is UNSUPPORTED **by design**, not broken: the old
`NSWindowSharingNone` stopped working in macOS 15.4, Apple documents no
replacement, and on macOS 26 the legacy value can stop the window rendering at
all — so the package refuses to set it.

## Things that cost hours, encoded so they cannot regress

The same table the top-level README keeps, for this program.

| trap | what happens |
|---|---|
| every package defines its **own** `ErrUnsupported` | none of them wrap `errors.ErrUnsupported`, so a check against that alone matches nothing and a platform behaving as documented reports **FAILED** with a non-zero exit. `glaze.SetAppIcon` is unsupported on Windows by design — the platform this exists to test |
| `tray.Run` **blocks**, driving the event loop until `Stop` | waiting on it deadlocks. Post it and leave it; `Stop` is safe from any goroutine |
| the tray started **before** the window | glaze's `New` runs a temporary `[NSApp run]` that ends only when `applicationDidFinishLaunching` fires — once per process. A tray started first consumes it and `glaze.New` blocks forever, with no window and nothing printed |
| `menu.Set` with no `Options.Window` | required on Windows (the HWND); it returns an error naming it, on the one platform that matters here |
| `menu.Set` with `Options.Dispatch` set **before** `Run` | Set blocks until its UI work has run, and nothing drains the queue until the run loop starts. Pass it only when calling from a goroutine while the UI is already going — which is this program |
| `file://` URL built by concatenation | Windows paths are `C:\dir`; the URL wants `file:///C:/dir`. Without the leading slash `net/url` writes `file:C:/dir` and ShellExecuteW rejects it. Covered by `TestFileURL`, which asserts the Windows shape while running on macOS |
| `openurl.Open != nil` as a capability check | a function value is never nil. `go vet` says so outright — the check checked nothing |

## Why the dialog is left open

`glaze.OpenFile` is modal and blocks until dismissed, and there is no API to
dismiss it. What is worth testing is whether presenting one crashes or errors —
the COM and WinRT plumbing behind it is the fragile part, not the user's
eventual click. So it is presented, given four seconds to fail, and the process
exits with it still on screen.
