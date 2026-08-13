# Refactoring plan

## Context

This project grew by accretion. Each capability — create a VM, drive a boot, run
a probe, fetch media, build an ISO, set the machine up — was added where it was
convenient rather than where it belonged, and several were added without first
looking at what already existed. It works and it is measured, but it is not a
codebase anyone can safely change.

The harm is not aesthetic. Twice, duplication has hidden a real bug: `setup`
called `EnsureReady` where it needed `RunInstall` (a fresh VM would have sat at
a UEFI prompt until timeout), and three separate wait-for-agent loops meant a
400 ms resume was measured with a 10-second poll. Both were found by
consolidating, not by testing. There will be more.

**Nothing depends on this repository yet.** No published API, no other consumer,
no released binary anyone has installed. So this is a rewrite where a rewrite is
warranted, not a migration: the plan deletes rather than deprecates, renames
without aliases, and cuts the command surface instead of preserving it. The only
thing that must survive is what `RESULTS.md` records as measured — those numbers
have to still hold at the end.

## What the audit found

Measured 13 Aug 2026:

| | |
|---|---|
| `cmd/irgo-winvm/main.go` | **1144 lines, one file, 23 subcommands** |
| longest functions | `runUp` 86, `runISO` 73, `runBuildISO` 71, `runCreate` 65, `runFetchISO` 62 |
| repeated boilerplate | 18 × `flag.NewFlagSet`, 10 × `vmRef`, 8 × `Find`, 6 × `Named` |
| `utmvm` | **25 flat files, 134 exported symbols** — for one consumer |
| host commands | **22 × `exec.Command`** across 8 files; `osascript` driven from two |
| dead code | 8 symbols with no caller (verified: definition only) |

Duplicate *function bodies* are already gone — a machine scan returns nothing.
What remains is structural.

## Principles

1. **Delete first.** Every phase should end with fewer lines than it started.
   The cheapest code to maintain is the code that is not there.
2. **The measured results are the contract**, not the API. `RESULTS.md` says a
   resume is ~400 ms, a probe passes on Windows ARM64, a self-built ISO
   installs. Those must still be true. Everything else is free to change.
3. **The safety net is thin.** Unit tests cover config, ISO parsing, the catalog
   and prune. Everything else is only verified by running a real VM — so the
   VM-touching phases come last, and each has an explicit check.
4. **One seam per external thing.** `utmctl`, `osascript`, `brew` and the ISO
   tools are four conversations with the host; each gets one implementation.

---

## Phase 0 — Delete the dead code

**Verified definition-only, no caller anywhere:**

| symbol | file |
|---|---|
| `BuildFATImage` | `fatimage.go` |
| `GuestToolsInstallCommand` | `fatimage.go` — still carries the `start`-wildcard bug already fixed in the answer file |
| `OpenDisplay`, `BootAssist` | `bootassist.go` |
| `IfaceVirtIO` | `config.go` |
| `SchemaConfigurationVersion` | `ensure.go` |
| `EnsureISOTools` | `brew.go` — written and never called |
| `SuspendToDisk` | `control.go` — keep the finding, delete the method |

`SuspendToDisk` is the interesting one: it exists only so the finding is
checkable, and the finding is that calling it can silently power-cut the guest.
With no consumers there is no reason to ship a footgun — the measurement lives
in `RESULTS.md` and the trap table, which is where it is useful.

**Verify:** `mise run check`. Deleting something with a caller will not compile.

## Phase 1 — Cut the command surface

23 subcommands, several of which exist only because `setup` had not been written
yet.

**Delete:**
- `up` — `setup` does create + restart UTM + install, and better. Its 86-line
  body is the largest single duplication left.
- `start` — powers a VM on headlessly, which on this firmware yields a UEFI
  prompt nobody can type at. It is a footgun with a friendly name; `boot` and
  `resume` are the two things anyone actually wants.

**Keep, because each fails differently and is worth retrying alone:** `setup`,
`doctor`, `targets`, `create`, `boot`, `install`, `suspend`, `resume`, `run`,
`exec`, `probe`, `screenshot`, `status`, `list`, `delete`, `prune`, `iso`,
`fetch-iso`, `build-iso`, `verify`.

**Verify:** `mise run` tasks that referenced `up` must be updated in the same
commit — `vm:up` and `vm:iso-test` both call it.

## Phase 2 — Split the CLI by concern

1144 lines in one file, with no signal about which commands are related.

```
main.go        usage and dispatch, nothing else (~110 lines)
cmd_setup.go   setup, doctor, targets
cmd_vm.go      create, delete, list, status, prune
cmd_boot.go    boot, install, suspend, resume
cmd_guest.go   run, exec, probe, screenshot
cmd_media.go   iso, fetch-iso, build-iso, verify
```

Pure movement. `git diff --stat` should show every line deleted from `main.go`
reappearing verbatim elsewhere.

## Phase 3 — Kill the command boilerplate

Ten commands repeat *resolve a reference, look it up, get a handle*, each with
its own error wrapping, so the same failure reads differently depending on which
command hit it.

```go
// cmd_common.go
func newCmd(name string) *flag.FlagSet
func vmFrom(fs *flag.FlagSet, args []string) (utmvm.Entry, utmvm.VM, error)
```

`vmRef` already does part of this — extend it rather than adding a second path.
Ten call sites collapse from four lines to one.

**Verify:** the error text for an unknown VM must be identical from every
command, which it currently is not.

## Phase 4 — Group `utmvm` by subject

25 flat files. Seven are about media, three about acquiring dependencies, the
rest about VM lifecycle — and nothing in the layout says so. Rename with `git
mv` so history follows:

```
media_catalog.go  media_fetch.go  media_build.go  media_guard.go  media_iso.go
deps_utm.go       deps_tools.go                  (ensure/installutm/brew)
vm_control.go     vm_create.go    vm_boot.go     vm_run.go       vm_clean.go
host_paths.go     host_sysfile_{darwin,other}.go
setup.go
```

**No sub-packages.** The parts are genuinely coupled — media informs create,
create informs boot — and splitting would force most of the 134 symbols to stay
exported, which is the opposite of Phase 6.

## Phase 5 — One seam per external tool

22 `exec.Command` sites. `osascript` is driven from both `bootassist.go` and
`control.go` under different escaping assumptions — and the trap table records
that `%q` double-escaping made every Go-driven boot silently fail. That bug
returns whenever there are two implementations.

```go
func utmctl(args ...string) (string, error)      // VM.run today; make it the only path
func osascriptRun(script string) (string, error) // one escaping rule
// brew and the ISO tools are already consolidated
```

**Verify:** `boot -installed` exercises the `osascript` path the escaping bug
lived in; `run -gui` exercises the scheduled-task path.

## Phase 6 — Shrink the exported surface

134 exported symbols for one consumer is not an API, it is an accident. For each
exported symbol, grep `cmd/`; if absent and not used in a test, unexport it.
Expect 40–60 to go. The CLI still compiling is the proof.

---

## Verification

Every phase:

```sh
mise run check                 # build, vet, test — all four modules
for t in darwin/arm64 linux/amd64 windows/amd64 windows/arm64; do
  GOOS=${t%%/*} GOARCH=${t##*/} CGO_ENABLED=0 go build -o /dev/null ./...
done
```

Phases 1 and 5 additionally need the VM, because they touch code with no unit
coverage:

```sh
irgo-winvm setup                                   # must skip every stage
irgo-winvm run -timeout 3m -vm irgo-win11 .bin/nativeprobe-arm64.exe
irgo-winvm run -gui -timeout 4m -vm irgo-win11 .bin/glaze-verifyevents-arm64.exe
irgo-winvm suspend -vm irgo-win11 && irgo-winvm resume -vm irgo-win11
```

The last must still report **~400 ms**. Seconds means a poll interval was lost
in the move — which is exactly how the 10-second-poll bug happened the first
time.

## What not to do

- **Do not "tidy" the comments.** They record findings that cost hours and are
  not recoverable from the code: why the display is `virtio-ramfb-gl`, why image
  3 needs `--boot`, why `--save-state` must never be called. They move with
  their code.
- **Do not touch `assets/`, the answer file, or the plist template in
  `config.go`.** UTM rejects a bad config with one generic "cannot import this
  VM" that names no field; `config_test.go` exists because that failure is
  undebuggable.
- **Do not do this in one commit.** One phase per commit, each verified.
