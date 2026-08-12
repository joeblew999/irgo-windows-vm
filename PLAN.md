# Seamless local Windows testing — status and plan

## Start here

One command, idempotent. It checks each stage and skips what is already done, so
running it twice is safe and the second run takes seconds:

```sh
irgo-winvm setup                    # media, VM, everything — from any state
irgo-winvm setup -fetch -install    # ...including a 4.2 GB download and a 45-minute install
```

On a machine that already has a VM it prints six skipped stages and stops. After
a restart the VM is simply off; `setup` starts it and waits for the agent.

```sh
mise run check     # build, vet and test all four modules
mise run probes    # cross-compile every Windows probe into .bin/
mise run doctor    # UTM, tools, and every file this needs that git does not have
```

The probes live in `.bin/` (gitignored) and survive a restart. Nothing depends
on `~/Downloads`, and the ISO is immutable — see `irgo-winvm iso`.

## What is proven

### The inner loop works

```
$ irgo-winvm run -vm irgo-win11 hello-arm64.exe alpha beta
hello from windows/arm64
args: [alpha beta]
```

**10.8 seconds**, no GUI, no keystrokes, no screen. Cross-compiled with plain
`GOOS=windows GOARCH=arm64` and **no toolchain** — which is what cgo-free buys,
and what irgo cannot do today because mingw pins it to amd64. Exit codes
propagate: a binary exiting 3 fails the command.

### Measured on Windows 11 ARM64

| capability | result |
|---|---|
| `clipboard` write/read | **OK** — round trip verified |
| `power.preventSleep` | **OK** |
| `singleinstance.acquire` | **OK** — re-acquire correctly refused |
| `mmap.map` | **OK** — wrote through |

Identical to macOS. Detail in `RESULTS.md`.

### Commands that work

`doctor` · `targets` · `verify` · `create` · `install` (unsupervised, **both**
boot phases) · `boot` · `run` (+`-gui`) · `exec` · `status` (reports install
phase with no agent) · `screenshot` · `list` · `delete` · `prune` · `up`

## Facts discovered the hard way

Each cost real time. None is guessable from documentation.

| finding | consequence |
|---|---|
| `%q` already escapes for AppleScript | an escaping helper double-escaped, and every Go-driven boot silently failed |
| `cdboot_noprompt.efi` returns without booting from the shell | use `\efi\boot\bootaa64.efi` plus a keypress |
| one keypress misses the ~5s CD prompt | 8 presses over ~6s — **bounded**, because surplus presses reach Setup's UI and destroyed an install |
| `utmctl exec` never returns output and always exits 0 | capture to a file in the guest and pull it, or a suite that ran nothing looks like one that passed |
| `cmd.exe` mangles quoted command lines through exec | write a batch file, push it, run it by path |
| `utmctl start` is headless; UTM routes input via the display | keystrokes to a windowless VM are accepted and discarded — use `StartWithDisplay` |
| the agent runs as **SYSTEM in session 0** | GUI apps fail with "webview2: environment/controller creation failed" — **not** a missing runtime; 151.0.4129.78 was installed and healthy |
| `schtasks /it` + `/rp` are contradictory | registers the task, then "ERROR: Element not found", which reads like a missing session |
| `C:\Windows\Temp` is not executable by the interactive user | task exits `Last Result: 1`; use `C:\Users\Public` |
| Windows reboots itself (Windows Update) | the agent drops with "Port is not connected"; nothing may assume the VM is still reachable |
| `BootInstalled` on `fs0:` is the install CD | the ESP is on NVMe at a varying fs number, so it must search |

## Done since this list was written

2. ~~**Run the glaze probes**~~ — both measured on Windows 11 ARM64. The Events
   bridge passes completely. The `app://` scheme works, and exposed a serious
   upstream bug: absolute `app://` URLs silently do not load on Windows, so a
   glaze app that works on macOS loses every stylesheet and script there.
   [UPSTREAM.md](UPSTREAM.md) §1b. **This was the project's stated goal.**
6. ~~**`fetch-iso`**~~ — done, and verified end to end. Microsoft's Media
   Creation Tool catalog (not the Sentinel-blocked download API) gives a direct
   URL, a size and a SHA-1; `fetch-iso` downloads and verifies, `build-iso`
   turns the .esd into a bootable ISO, and an ISO built that way **booted UTM
   straight into Windows Setup and installed unattended**. Two external tools,
   both what CrystalFetch bundles: `wimlib` and `xorriso`. Detail and the two
   failed approaches are in [PLAN-fetch-iso.md](PLAN-fetch-iso.md).
7. ~~**Build tags**~~ — `utmvm` now compiles for darwin, linux and windows on
   both architectures, so `targets` can reach the Windows developer it exists to
   inform. The macOS-only syscalls live behind `sysfile_darwin.go` /
   `sysfile_other.go`, which report "unknown" rather than guessing — a wrong
   answer about whether two files share blocks is worse than none.

1. ~~**`probes build -o <dir>`**~~ — `mise run probes` cross-compiles all four
   probe binaries for arm64 and amd64 into `.bin/`, which is gitignored and
   survives a restart. Still worth folding into the CLI eventually; the task is
   what unblocked everything else.
4. ~~**`probe/gui`**~~ — `examples/nativeall` runs **every** native capability
   in one windowed program: tray, menu, file dialogs, app icon, no-capture,
   open-url. Measured on macOS **and** Windows 11 ARM64; see `RESULTS.md`.
5. ~~**Drop the fake `SKIPPED` rows**~~ — gone. They were backed by
   `openurl.Open != nil`, which `go vet` rejects because a function value is
   never nil, so the report named capabilities nothing had checked.

Two upstream bugs were found doing it, both fixed at crgimenes rather than
worked around here — see [UPSTREAM.md](UPSTREAM.md).

## Next up, in order

3. ~~**`suspend` / `resume`**~~ — **done for the case that works, and the other
   case turns out to be a trap.**

   `irgo-winvm suspend` / `resume` pause to memory and restore. **Measured at
   300–500 ms** to a live guest agent, against about two minutes for a cold
   boot that additionally has to be driven through the UEFI shell with eight
   keypresses — which needs an unlocked Mac and a visible display window.
   That removes the boot problem from daily use, which was the point.

   The state is in MEMORY, so it does not survive quitting UTM or rebooting the
   Mac. The durable version, `utmctl suspend --save-state`, is **not offered**,
   because it does one of two things and the caller cannot tell which:

   - refuses honestly, naming a device — `Suspend is not supported when GPU
     acceleration is enabled`, and once that is removed (`create -no-gpu`),
     `...when an emulated NVMe device is active`. NVMe is not removable:
     Windows ARM64 Setup has no inbox VirtIO storage driver and finds no drive
     without it;
   - **or reports success and power-cuts the guest** — exit 0, no state file,
     VM left `stopped`, next boot through "Diagnosing your PC".

   Making a durable suspend possible means switching the system disk to VirtIO
   and injecting the driver into `boot.wim` — which is now actually reachable,
   because `build-iso` already drives wimlib over the media. That is the
   follow-up, and it belongs with the ISO builder rather than here.
8. **Delete dead code** — `BuildFATImage`, `OpenDisplay`, `BootAssist`,
   `SchemaConfigurationVersion`, `IfaceVirtIO`, and `GuestToolsInstallCommand`,
   which still carries the `start`-wildcard bug already fixed in the XML.
9. **`Delete` safety** — removes files after 30s whether or not QEMU actually
   stopped. (`Prune` is fixed: it matched any `*.img`/`*.dmg` in the system temp
   directory regardless of owner, which on a shared /tmp is somebody else's
   half-built VM. It now matches only this project's own prefixes, guarded by a
   test that asserts a `disk.img` and an `Xcode.dmg` survive.)

## Scope

**No irgo integration until this works standalone with glaze.** Deliberate:
integrating a tool that does not yet work makes the framework absorb its
failures.

Recorded for when that gate opens:

- irgo's `verify` never builds a desktop target at all — `app build all` is
  iOS+Android only (`cmd_app_build.go:65`), so the mingw path it documents is
  never exercised
- the artifact-check precedent is `verifyAndroidArtifact`
  (`cmd/irgo/app_android_verify.go`); there is no Windows equivalent
- `desktopTargets` (`cmd/irgo/cmd_project_ci_data.go:42`) is the single source
  of truth for generated projects' CI and has no arch field

**Architecture:** windows/arm64 native is the primary target. glaze is cgo-free
so it costs nothing, and ARM64 is where the only never-executed code lives —
`putbounds_arm64.go` hand-writes AAPCS64 register passing while glaze's CI runs
x64. Real x64 fidelity belongs on a `windows-latest` runner, not on emulation.

**When irgo does adopt glaze**, the headline is that it removes the mingw
toolchain requirement *and* unlocks native ARM64 Windows builds in one move.

## Verification

- `go build ./...`, `go vet ./...`, `go test -race ./utmvm/...`, and both fuzz
  targets clean
- `GOOS=linux go build ./...` and `GOOS=windows go build ./...` — **fail today**;
  that is the test for item 7
- End to end: cross-compile a glaze app for windows/arm64 with plain
  `GOOS`/`GOARCH`, `irgo-winvm run -vm dev app.exe`, and see its output on the Mac
