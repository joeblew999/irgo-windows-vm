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

## Windows ARM64 — not yet run

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
