# Refactoring plan

## Context

This project grew by accretion. Each capability — create a VM, drive a boot, run
a probe, fetch media, build an ISO, set the machine up — was added where it was
convenient rather than where it belonged, and several were added by someone who
did not first look at what already existed. The result works and is measured,
but it is not a codebase anyone can safely change.

The specific harm is not aesthetic. Twice now, duplication has hidden a real
bug: `setup` called `EnsureReady` where it needed `RunInstall` (a fresh VM would
have sat at a UEFI prompt until timeout), and three separate wait-for-agent
loops meant a 400 ms resume was measured with a 10-second poll. Both were found
by consolidating, not by testing. There will be more.

This plan is ordered so that each phase is independently shippable and
independently verifiable. Nothing here changes behaviour; if behaviour changes,
that is a bug in the refactor.

## What the audit found

Measured 13 Aug 2026, not estimated:

| | |
|---|---|
| `cmd/irgo-winvm/main.go` | **1144 lines, one file, 25 subcommands** |
| longest functions there | `runUp` 86, `runISO` 73, `runBuildISO` 71, `runCreate` 65, `runFetchISO` 62 |
| repeated CLI boilerplate | 18 × `flag.NewFlagSet`, 10 × `vmRef(fs, args)`, 8 × `utmvm.Find`, 6 × `utmvm.Named` |
| `utmvm` package | **25 files, 134 exported symbols**, flat, no internal boundary |
| host commands | **22 × `exec.Command`** scattered across 8 files; `osascript` driven from two of them |

Duplicate *function bodies* are already gone — a machine scan returns nothing.
What remains is structural: repeated shapes, misplaced responsibility, and an
API surface far larger than its single consumer needs.

## Principles

1. **Behaviour is frozen.** Every phase ends with the same observable output.
   The comments in this codebase record findings that cost hours — they move
   with their code, they do not get "tidied".
2. **The safety net is thin.** Unit tests cover config, ISO parsing, the
   catalog and prune. Everything else is only verified by running a real VM. So
   each phase must be verifiable by a command, and the VM-touching phases are
   deliberately last.
3. **Smaller exported surface.** 134 exported symbols for one consumer is not an
   API, it is an accident. Unexport what the CLI does not use.
4. **One seam per external thing.** `utmctl`, `osascript`, `brew`, and the ISO
   tools are four different conversations with the host; each should have one.

---

## Phase 1 — Split the CLI by concern

**Problem:** 1144 lines, 25 commands, one file. Nothing tells you which commands
are related, and a change to media handling sits three screens from a change to
VM lifecycle.

**Do:** split `cmd/irgo-winvm/main.go` into files grouped by what a developer is
trying to do. Pure code movement — no logic edits.

```
main.go        usage, dispatch, and nothing else (~120 lines)
cmd_setup.go   setup, doctor, targets
cmd_vm.go      create, up, delete, list, status, prune
cmd_boot.go    start, boot, suspend, resume, install
cmd_guest.go   run, exec, probe, screenshot
cmd_media.go   iso, fetch-iso, build-iso, verify
```

**Verify:** `go build ./... && go vet ./...`, and `git diff --stat` should show
only moves — every deleted line in `main.go` appearing verbatim elsewhere.

## Phase 2 — Kill the command boilerplate

**Problem:** 18 flagsets, and 10 commands repeat *resolve a VM reference, look
it up, get a handle*. Each does its own error wrapping, so the same failure
reads differently depending on which command hit it.

**Do:** introduce one small helper in the CLI layer (not in `utmvm` — this is
presentation):

```go
// cmd_common.go
type cmd struct { fs *flag.FlagSet }
func newCmd(name string) *cmd
func (c *cmd) vm(args []string) (utmvm.Entry, utmvm.VM, error)  // resolve+find+handle, once
```

`vmRef` already exists and does part of this; extend it rather than adding a
second path. Ten call sites collapse from four lines to one.

**Verify:** build, vet, and run one command from each group against the VM —
`status`, `run`, `iso`. Error text for an unknown VM must be identical before
and after.

## Phase 3 — Fold `up` into `setup`

**Problem:** `runUp` is 86 lines duplicating what `utmvm.Setup` does: inspect
the ISO, create, restart UTM, drive the install. Two orchestrations of the same
sequence is exactly how the `EnsureReady`/`RunInstall` bug happened.

**Do:** make `up` a thin wrapper over `utmvm.Setup` with `ISO` set and
`Install: true`, keeping its `-replace` flag. Delete the duplicated body.

**Verify:** this one needs a real VM. `up -replace -name up-test` against a
throwaway name, driven to a working install. **Do not run against
`irgo-win11`.** Delete the test VM afterwards.

## Phase 4 — Group `utmvm` by subject

**Problem:** 25 flat files. Seven are about media (`iso`, `isoimage`,
`isobuild`, `isoguard`, `fatimage`, `catalog`, `fetch`), three about acquiring
dependencies (`ensure`, `installutm`, `brew`), the rest about VM lifecycle.
Nothing in the layout says so.

**Do:** rename for subject prefix — no package split, which would force a large
exported surface between them:

```
media_catalog.go  media_fetch.go   media_build.go  media_guard.go
media_iso.go      media_answer.go  (was fatimage/isoimage)
deps_utm.go       deps_tools.go    (was ensure/installutm/brew)
vm_control.go     vm_create.go     vm_boot.go      vm_run.go  vm_clean.go
host_paths.go     host_sysfile_{darwin,other}.go
setup.go
```

**Verify:** `go build ./...` for all six target platforms, and `git log
--follow` should still trace each file (use `git mv`).

## Phase 5 — One seam per external tool

**Problem:** 22 `exec.Command` sites. `osascript` is driven from both
`bootassist.go` and `control.go` with different escaping assumptions — and the
trap table already records that `%q` double-escaping made every Go-driven boot
silently fail. That class of bug returns whenever there are two implementations.

**Do:** one internal helper per external program, each the only place that
knows how to quote for it:

```go
func utmctl(args ...string) (string, error)     // exists as VM.run; make it the only path
func osascriptRun(script string) (string, error)
// brew already consolidated: BrewInstall / BrewPath
// ISO tools already consolidated: Tool.resolve / Tool.Ensure
```

**Verify:** boot the VM and drive it — `boot -installed` exercises the
`osascript` path that the escaping bug lived in. Then `run -gui`, which uses the
scheduled-task path.

## Phase 6 — Shrink the exported surface

**Problem:** 134 exported symbols; the CLI is the only consumer.

**Do:** unexport everything the CLI does not reference. Mechanical: for each
exported symbol, grep `cmd/`; if absent and not used in a test, lowercase it.
Expected to remove 40–60 from the public surface.

**Verify:** `go build ./... && go test ./...`. The CLI compiling unchanged *is*
the proof.

---

## Verification

No phase is done until this passes:

```sh
mise run check                 # build, vet, test — all four modules
for t in darwin/arm64 linux/amd64 windows/amd64 windows/arm64; do
  GOOS=${t%%/*} GOARCH=${t##*/} CGO_ENABLED=0 go build -o /dev/null ./...
done
```

Phases 3 and 5 additionally require the VM, because they touch code with no
unit coverage:

```sh
irgo-winvm setup                                   # must skip every stage
irgo-winvm run -timeout 3m -vm irgo-win11 .bin/nativeprobe-arm64.exe
irgo-winvm run -gui -timeout 4m -vm irgo-win11 .bin/glaze-verifyevents-arm64.exe
irgo-winvm suspend -vm irgo-win11 && irgo-winvm resume -vm irgo-win11
```

The last must still report **~400 ms**. If it reports seconds, a poll interval
was lost in the move.

## What not to do

- **Do not split `utmvm` into sub-packages.** Its parts are genuinely coupled
  (media informs create, create informs boot), and a split would force most of
  the 134 symbols to stay exported — the opposite of Phase 6.
- **Do not "tidy" the comments.** They record findings that cost hours and are
  not guessable from the code: why the display is `virtio-ramfb-gl`, why image 3
  needs `--boot`, why `--save-state` is never called. Move them with their code.
- **Do not refactor `assets/`, the answer file, or `config.go`'s plist
  template.** UTM rejects a bad config with one generic "cannot import this VM"
  that names no field; `config_test.go` exists precisely because that failure is
  undebuggable.
- **Do not do this in one commit.** One phase per commit, each verified.
