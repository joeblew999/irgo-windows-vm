# Closing the gaps: seamless local Windows testing for irgo desktop apps

## Context

An irgo developer on an Apple Silicon Mac should be able to build their Windows
desktop app and actually run it — in Windows, on their own machine, in seconds.
Today they cannot. irgo cross-compiles `build/desktop/windows/<app>.exe` via
mingw-w64 and that binary has never been executed anywhere except a customer's
machine.

`irgo-windows-vm` was built to close this and gets most of the way: a Go CLI
generates a UTM bundle, and a Windows 11 ARM64 install completes unattended to a
logged-in desktop. What it cannot yet do is the thing the developer wants — take
a freshly built binary, run it in that VM, and hand back the output.

The user's priority is **fast local iteration**: one-time setup may be slow and
may touch the GUI; everything after must be quick and scripted.

## The inner loop we are building toward

```
irgo app build desktop windows      # existing
irgo-winvm run -vm dev <app>.exe    # new: push, execute, stream output back
```

Two capabilities make this achievable without any GUI:

**Suspend/resume sidesteps the boot problem.** UTM exposes `suspend`; `start` on
a suspended VM restores RAM instead of booting, so the UEFI shell is never
reached. Boot driving becomes a one-time setup concern, not a per-run one.

**File transfer and exec are already headless**, needing only the QEMU guest
agent:

```
utmctl file push <vm> <guest-path>   # stdin -> guest file
utmctl file pull <vm> <path>         # guest file -> stdout
utmctl exec <vm> --cmd <c> --env K=V --input
```

There is no snapshot verb (the remote protocol has one; the scriptable surface
does not), so "reset to clean" means `duplicate` from a golden VM — slower, and
only needed when a run dirties the machine.

## The linchpin risk

**Everything depends on the QEMU guest agent, which has never once come up.**
The answer file installs UTM's guest tools at first logon
(`utmvm/assets/autounattend.xml`), but no VM has yet reached a state where
`utmctl exec` works. If that install does not work, the headless story collapses
and we are back to keystrokes.

So step 1 is to prove it, on a throwaway VM, before building anything on top.

## Gaps found

### Blocking the loop

| gap | evidence |
|---|---|
| `up` cannot complete its boot step | `runUp` passes a **name** to `BootAndWait` (`cmd/irgo-winvm/main.go:480`) but `assets/boot.applescript:12` is `virtual machine id %q`, which needs the UUID. `runBoot` passes `e.UUID` and works. This is why my `up` attempts failed. |
| `run-all.cmd` does not exist | `runProbe` executes it (`main.go:305`) and `RESULTS.md:95` documents it, but it is **not in the repo**. It only ever existed in `/tmp`. |
| No command builds the probes | `-probes` consumes a pre-built directory. Producing it means `GOOS=windows GOARCH=… go build` by hand in two separate modules. |
| `probe/run-probe.cmd` ends in `pause` | Blocks forever under `utmctl exec`. |
| No `run`, `suspend` or `resume` | The inner loop has no entry point at all. |
| `exec -cmd` splits on `strings.Fields` | Any quoted argument or path with a space is mangled (`main.go:280`). |

### Blocking the cross-platform claim

**`utmvm` does not compile on Linux or Windows.** `inodeInfo`/`diskUsage` exist
only in `inode_darwin.go`, and `diskspace.go` uses `syscall.Statfs_t`/`Stat_t`
with no build tags anywhere. So `HostCoverage()`'s windows and linux branches
are unreachable, the `targets` subcommand's entire premise is undeliverable, and
`README.md:29`'s `go install …@latest` is false off a Mac.

### Correctness and safety

- **`Prune` deletes every `*.img`/`*.dmg` in `os.TempDir()`** regardless of
  owner (`cleanup.go:119`), while reclaiming nothing of its own — its actual
  staging dirs are already removed by a `defer`.
- **`Delete` waits 30 s then removes files regardless** of whether QEMU stopped —
  precisely the "writing to deleted inodes" hazard its own comment warns about.
- **`BootAssistOn` only ever uses `paths[0]`**; the `bootaa64.efi` fallback is
  unreachable, so `HasNoPromptLoader` is reported by `verify` but never acted
  on — media lacking it fails after the full 45-minute wait.
- **`BootAssistWatched` discards its `diskPath`** argument; the contract is
  fiction.
- **Dead code**, including one copy of a bug we already fixed elsewhere:
  `BuildFATImage`/`copyIntoFS`, `OpenDisplay`, `BootAssist`,
  `SchemaConfigurationVersion`, `IfaceVirtIO`, and `GuestToolsInstallCommand`
  which still emits the broken single-`for` `start` wildcard form.
- **Doc-vs-code**: `payload.go` says probes land in `\probe`; the code puts them
  at the root.

## Architecture decision (user deferred to me)

Exploration settled this, and not as I first assumed.

**irgo cannot produce a windows/arm64 build at all.** `buildDesktopWindows`
(`irgo/cmd/irgo/app_desktop_build.go:235`) hardcodes `GOARCH=amd64` and
`CC=x86_64-w64-mingw32-gcc`; `app_icons.go:124` says outright "we always target
windows/amd64". There is no `aarch64-w64-mingw32` anywhere.

So:

- **The developer's app is tested as amd64 under Windows' x64 emulation** — the
  only artifact irgo produces. The tool runs whatever irgo built rather than
  imposing an arch.
- **The glaze probes are tested as arm64 native**, because that is where the
  unique risk is: `putbounds_arm64.go` hand-writes AAPCS64 register passing and
  glaze's CI runs x64, so that code executes nowhere today.

Worth surfacing: **glaze is cgo-free, so a glaze-based app cross-compiles to
windows/arm64 with nothing but `GOOS`/`GOARCH`.** The mingw dependency is exactly
what pins irgo to amd64. Real x64 fidelity should come from a `windows-latest`
CI runner, which generated projects already use.

## Plan

### Phase 1 — Prove the guest agent (do this first, alone)

Nothing else is worth building until this works.

1. Create a throwaway VM with defaults (guest tools attached), install
   unattended, and confirm `utmctl exec` responds.
2. If the tools do not install, diagnose from `C:\unattend-complete.txt` (the
   marker written last, so its absence localises the failure) and from the
   answer file's `FirstLogonCommands` ordering.

**Exit criterion:** `irgo-winvm status -vm <x>` reports `agent: yes`.

### Phase 2 — Build the inner loop

3. Fix the `up` name/UUID bug — resolve via `utmvm.Find` and pass `e.UUID`, the
   way `runBoot` already does.
4. `utmvm/assets/run-all.cmd` as an **embedded asset**, staged by `BuildPayload`
   like the other assets. No `pause`; exit code reflects failure.
5. `irgo-winvm probes build -o <dir>` — cross-compiles both probe modules for
   windows/arm64 and windows/amd64. Removes the hand-built `/tmp` step.
6. `irgo-winvm run -vm <x> <local.exe> [args]` — `file push` to a temp path in
   the guest, `exec`, stream stdout/stderr back, propagate the exit code. This
   is the command the whole plan exists for.
7. `suspend` / `resume` wrappers, and have `run` resume-if-suspended so a warm
   VM answers in seconds.
8. Fix `exec` argument splitting — take `[]string` args after `--` rather than
   `strings.Fields`.

**Exit criterion:** `irgo-winvm run -vm dev ./hello.exe` prints the program's
output on the Mac, with no GUI interaction.

### Phase 3 — Close the actual goal

9. Run the glaze probes in the VM and record real results in `RESULTS.md`:
   the `app://` origin capabilities, the Events bridge, and the native
   capability matrix — the Windows column that has been empty since the start.
10. Compare against the measured macOS column and note every divergence.

### Phase 4 — Make the claims true

11. Build-tag the darwin-only syscalls (`inode_darwin.go` +
    `inode_other.go`, `diskspace_darwin.go` + `diskspace_other.go`) so the
    binary builds on Linux and Windows and `targets` means something.
12. Delete the dead code listed above — especially
    `GuestToolsInstallCommand`, which carries a bug we already fixed in the XML.
13. Make `Prune` remove only artefacts it created; make `Delete` fail rather
    than delete under a still-running QEMU.
14. Act on `HasNoPromptLoader` in `create`/`up` instead of only reporting it, and
    either restore the `bootaa64.efi` fallback or drop the unreachable branch.
15. Fix the `\probe` doc-vs-root mismatch.

### Phase 5 — irgo integration (separate decision)

16. `irgo`'s `verify` task never builds a desktop target at all —
    `app build all` is iOS+Android only (`cmd_app_build.go:65`). Adding
    `app build desktop windows` to `mise.toml`'s `verify` is a one-line fix
    worth doing regardless of the VM.
17. Optionally add `app verify windows`, mirroring the existing
    `verifyAndroidArtifact` precedent (`cmd/irgo/app_android_verify.go`).

Keeping the tool in its own repo for now, per the earlier decision.

## Files

Primary: `cmd/irgo-winvm/main.go`, `utmvm/control.go`, `utmvm/bootassist.go`,
`utmvm/payload.go`, `utmvm/cleanup.go`, plus new `utmvm/assets/run-all.cmd` and
new build-tagged `inode_*.go` / `diskspace_*.go`.

Reuse rather than re-add: `utmvm.Find` (name/UUID resolution), `VM.Exec`,
`VM.WaitForAgent`, `BuildPayload`, `CheckSpace`, and the existing table-driven
test style in `utmvm/config_test.go`.

## Verification

- `go build ./...`, `go vet ./...`, `go test -race ./utmvm/...`, and both fuzz
  targets stay clean.
- **Cross-compile check** — `GOOS=linux go build ./...` and
  `GOOS=windows go build ./...` must succeed. They fail today; that is the test
  for Phase 4.
- New unit tests, following the existing style, for the pure logic currently
  untested: `List`/`Find` parsing, `Prune` against a `t.TempDir()`,
  `HostCoverage`, and an arity test for the `Sprintf` templates in
  `boot.applescript` and `config.plist.tmpl` — a template edit silently ships
  `%!q(MISSING)` today.
- **End to end**, which is the only proof that counts:
  `irgo app build desktop windows` in a scaffolded project, then
  `irgo-winvm run -vm dev build/desktop/windows/<app>.exe`, and see the app's
  output on the Mac.
