# Seamless local Windows testing — status and plan

## Start here after a restart

```sh
cd ~/workspace/go/src/github.com/joeblew999/irgo-windows-vm
go build ./... && go test ./utmvm/...      # should be clean

irgo-winvm status -vm irgo-win11           # the VM survives reboots; it will be off
irgo-winvm boot -vm irgo-win11 -installed  # boot the installed Windows
irgo-winvm status -vm irgo-win11           # wait for: agent: yes
```

Then rebuild the probes — they live in `/tmp` today and do **not** survive a
restart (see "Next up", item 1):

```sh
mkdir -p /tmp/probes
cd probe           && for a in arm64 amd64; do CGO_ENABLED=0 GOOS=windows GOARCH=$a go build -o /tmp/probes/nativeprobe-$a.exe .; done
cd ../glaze-probes && for p in verify verifyevents; do for a in arm64 amd64; do CGO_ENABLED=0 GOOS=windows GOARCH=$a go build -o /tmp/probes/glaze-$p-$a.exe ./$p; done; done
```

**The single next action** is one command. Everything it needs is committed:

```sh
irgo-winvm run -gui -timeout 3m -vm irgo-win11 /tmp/probes/glaze-verify-arm64.exe
```

That is the last unmeasured claim in the project.

The Windows ISO is hardlinked at `.cache/win11-arm64.iso` (gitignored, zero
extra bytes), so nothing depends on `~/Downloads` any more.

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

## Next up, in order

1. **`probes build -o <dir>`** — cross-compile both probe modules. They are
   hand-built into `/tmp` today, so a restart loses them. First job, because
   everything else needs probes.
2. **Run the glaze probes** (`-gui`) and record in `RESULTS.md`. One command;
   see the top of this file. This closes the project's actual goal.
3. **`suspend` / `resume`** — resuming restores RAM and never reaches firmware,
   so it needs no keystrokes and works with the Mac locked. This makes the loop
   genuinely fast and removes the boot problem from daily use.
4. **`probe/gui`** — tray, menu, file dialog, app icon. Nothing currently tests
   glaze's dialogs or menu on any platform.
5. **Drop the fake `SKIPPED` rows** for `tray`/`filedialog`/`nocapture`, which
   are not imported: the report implies checks that do not exist.
6. **`fetch-iso`** — removes the CrystalFetch step. Microsoft serves the ARM64
   image through the same gated API quickget automates; verified by fetching the
   official page (HTTP 200, product edition `3324`, session-permit and SKU
   endpoints present).
7. **Build tags** — `utmvm` does not compile on Linux or Windows
   (`inode_darwin.go`, `syscall.Statfs_t`), so `targets` cannot reach the
   Windows developer it exists to inform.
8. **Delete dead code** — `BuildFATImage`, `OpenDisplay`, `BootAssist`,
   `SchemaConfigurationVersion`, `IfaceVirtIO`, and `GuestToolsInstallCommand`,
   which still carries the `start`-wildcard bug already fixed in the XML.
9. **`Prune` and `Delete` safety** — `Prune` removes any `*.img`/`*.dmg` in
   `os.TempDir()` regardless of owner; `Delete` removes files after 30s whether
   or not QEMU actually stopped.

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
