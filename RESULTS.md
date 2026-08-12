# Probe results

The point of this repo is parity: the same probes, the same glaze version, on
both platforms. A pass on one OS proves nothing on its own.

Probes are built from `probe/` (native capabilities) and `glaze-probes/`
(glaze's `app://` scheme and its Events bridge).

## macOS — verified

Host: Apple M2 Pro, macOS 26.5, `glaze v0.0.47`, `CGO_ENABLED=0`.

### Native capabilities

| capability | result |
|---|---|
| `clipboard.write` / `clipboard.read` | OK — round trip verified, original clipboard restored |
| `power.preventSleep` | OK — acquired and released |
| `singleinstance.acquire` | OK — lock held, re-acquire correctly refused |
| `mmap.map` | OK — mapped and wrote through |
| `openurl` / `tray` / `filedialog` / `menu` | skipped — side effects or need a run loop |
| `notifications`, `keychain`, `fswatch` | **missing from the ecosystem** (`native/notify` is planned, not built) |

### glaze `app://` scheme

```
origin:        app://home
secureContext: true
cryptoSubtle:  true
localStorage:  true
pushState:     /deep/link/route
css:           32px
```

`pushState` succeeding is the load-bearing result: it means client-side routing
works. The same probe over `file://` fails with `SecurityError`, which is why
the `app://` scheme handler matters and `file://` is not a substitute.

`css: 32px` is `getComputedStyle` returning the stylesheet's `2rem`, proving the
asset was fetched through the Go handler *and* applied by the engine — not
merely served.

### glaze Events bridge

```
PASS: JS -> Go   : "js-listener-installed"
PASS: Go -> JS   : 3 unsolicited pushes delivered
PASS: round trip : received=["tick:1","tick:2","tick:3"] domChildren=3
sockets during run: NONE
```

`domChildren=3` shows server-initiated pushes actually mutated the DOM. With
zero sockets, this is a genuine SSE substitute for a desktop app — relevant
because glaze's `SchemeResponse` is a buffered `[]byte` on the UI thread and so
cannot stream SSE itself.

## Windows ARM64 — MEASURED (native capabilities)

Host: Apple M2 Pro / UTM 4.7.5, guest Windows 11 ARM64 build 26100, run headless
through the QEMU guest agent — no GUI, no keystrokes, no screen.

### The inner loop works

```
$ irgo-winvm run -vm irgo-win11 hello-arm64.exe alpha beta
hello from windows/arm64
args: [alpha beta]
```

10.8 seconds end to end. The binary was cross-compiled on macOS with plain
`GOOS=windows GOARCH=arm64` and **no toolchain at all** — which is what cgo-free
buys, and what irgo cannot currently do because mingw pins it to amd64.

Exit codes propagate: a binary exiting 3 fails the command rather than passing
silently.

### Native capabilities — windows/arm64, native

| capability | result |
|---|---|
| `clipboard.write` / `clipboard.read` | **OK** — round trip verified |
| `power.preventSleep` | **OK** — acquired and released |
| `singleinstance.acquire` | **OK** — lock held, re-acquire correctly refused |
| `mmap.map` | **OK** — mapped and wrote through |
| `openurl` / `tray` / `filedialog` / `menu` | skipped (see the caveat in PLAN.md — three are not even linked) |
| `notifications`, `keychain`, `fswatch` | missing from the ecosystem |

Identical to the macOS column. Every capability that works on macOS works on
Windows ARM64.

### glaze probes — still outstanding

The two glaze probes open a WebView2 window, and that is where this stopped.
The VM rebooted itself mid-session (Windows Update, disk grew 14 → 27 GB),
dropping the agent with `Port is not connected`, and it has not been brought
back yet.

Two constraints learned in the attempt, both now understood rather than guessed:

- **The guest agent runs without a desktop session.** A GUI app started through
  it has no window station, so the glaze probes need launching into the
  interactive session — a scheduled task with `/it`, not a plain exec.
- **Keystrokes do not reach the VM while the Mac is locked.** Boot recovery
  depends on typing at the UEFI shell, so a locked screen blocks it. This is the
  strongest argument for suspend/resume: resuming restores RAM and never reaches
  the firmware, so it needs no keystrokes and works locked.

## Windows ARM64 — the claims still unverified

An unattended install completed on 11 Aug 2026: Windows 11 ARM64 (build 26100)
reached a logged-in desktop as `dev` with no interaction after the boot command.
That proves the answer file, the disk layout, the display device and the boot
path. The probes themselves have not produced a report yet — the host locked
before the run finished, and driving the guest needs the screen unlocked while
the VM has no guest agent.

### What the install proved

| claim | status |
|---|---|
| ARM64 Windows installs unattended in UTM | **yes** — desktop reached, auto-login as `dev` |
| answer file is read and applied | **yes** — GPT layout matched `DiskConfiguration` exactly |
| `virtio-ramfb-gl` display works | **yes** — Setup and desktop both render |
| NVMe system disk is visible to Setup | **yes** |
| glaze runs on Windows | **not yet measured** |

## Windows ARM64 — outstanding

Pending a working VM. These are the claims that **cannot** be checked on a Mac
and are the whole reason for the VM:

- **The undocumented WebView2 export.** glaze calls
  `CreateWebViewEnvironmentWithOptionsInternal` directly to avoid shipping
  `WebView2Loader.dll`. Microsoft documents that it may change or be removed.
  Both glaze probes exercise it on first `New()`.
- **The hand-written ARM64 ABI code.** `putbounds_arm64.go` passes a 16-byte
  RECT in two registers per AAPCS64, versus by hidden reference on amd64.
  glaze's CI runs `windows-latest`, which is x64 — so this code is compiled but
  never executed anywhere.
- **Native capabilities on their best-covered platform.** Windows has
  implementations for 7 of 8 packages, more than macOS or Linux.
- **x64-under-emulation behaviour.** Whether an amd64 build behaves identically
  on ARM Windows decides if Mac-local testing has any fidelity at all, or
  whether x64 testing must live on x86 hardware.

Run with `probe/run-all.cmd` from the payload image; it writes one report to the
Desktop covering ARM64-native and x64-emulated for every probe.
