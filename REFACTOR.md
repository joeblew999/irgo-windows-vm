# Refactoring plan

## Context

This project grew by accretion. Each capability — create a VM, drive a boot, run
a probe, fetch media, build an ISO, set the machine up — was added where it was
convenient rather than where it belonged, and several were added without first
looking at what already existed. It works and it is measured, but it is not a
codebase anyone can safely change.

**Nothing depends on this repository yet** — no published API, no other
consumer, no released binary anyone has installed. So this deletes rather than
deprecates and renames without aliases. The only contract is what `RESULTS.md`
records as measured.

---

## The pattern underneath every bug

Each defect found here looked unrelated to the others. They are one thing.

**Every capability has three consumers**, and each has been re-deriving the
others' knowledge:

| | consumer | commands |
|---|---|---|
| **DO** | the primitive | `create`, `boot`, `fetch-iso`, `build-iso`, `suspend` |
| **SEQUENCE** | the orchestration | `setup` |
| **REPORT** | the diagnosis | `doctor`, `status`, `targets`, `verify`, `list` |

Sorted by which pair drifted:

| defect | what happened |
|---|---|
| `EnsureReady` used where `RunInstall` was needed | **SEQUENCE re-implemented DO**, and picked the wrong one |
| `Prune` silently stops cleaning new guest files | **REPORT re-implemented DO's naming** in a second list |
| nine ways to decide "is this VM usable" | **all three**, in five combinations, two disagreeing on case |
| **`doctor` installs UTM** | **REPORT called DO's function** — a diagnostic that mutates |

The last is live, and was introduced during this session's own work: `runDoctor`
calls `EnsureUTM` and `EnsureGuestTools`, both of which install. On a machine
without UTM, *"let me check what is wrong"* installs an application and
downloads 120 MB. Nobody wrote that on purpose — `doctor` predates those
functions gaining side effects, and nothing objected when they did.

### The fix: separate the query from the action

Every capability exposes exactly two entry points:

```go
Check()  (State,   error)   // pure. No installs, no downloads, no writes.
Ensure() (Outcome, error)   // acts — and is built on Check
```

- **DO** calls `Ensure`.
- **SEQUENCE** calls the *same* `Ensure`. This is what makes `setup` thin, and
  what makes reproducing a `setup` failure by hand actually reproduce it.
- **REPORT** calls `Check` **only**, so a diagnostic *cannot* mutate rather than
  being trusted not to.

Everything below is a consequence of this: the idempotency contract, the thin
`setup`, the single `VMState`, the primitives kept as a recovery toolkit.

---

## Two decisions taken up front

### Refactor `utmvm` in place; rewrite the CLI

| area | lines | comment lines | density |
|---|---|---|---|
| `cmd/irgo-winvm` | 1144 | 138 | **12%** |
| `utmvm` | 5579 | **1418** | **25%** |

The value of this repository is not its code. It is ~1400 lines of **recorded
findings**, each of which cost hours and none of which is recoverable by reading
the code they annotate: why the display must be `virtio-ramfb-gl`, why ESD image
3 needs `--boot`, why the keystroke count is bounded at eight (surplus presses
reached Setup's UI and destroyed an install), why the answer file must be a CD,
why `%q` must not be re-escaped for AppleScript.

A rewrite either loses those or copies them across — and copying them *is* the
refactor, minus the compiler checking each still sits beside the code it
explains. It would also invalidate `RESULTS.md`, whose measurements were taken
against these binaries, at 45 minutes an install to retake.

**The exception:** `cmd/irgo-winvm` at 12% density is flag plumbing with nothing
learned embedded in it. Untangling it is slower than replacing it, so it is
written fresh against the final API.

### The CLI has two layers; only one may be cut

**Orchestration** — `setup`.
**Primitives** — `create`, `start`, `boot`, `install`, `suspend`, `resume`,
`delete`, `status`, `screenshot`, `exec`, `run`, `probe`, `iso`, `fetch-iso`,
`build-iso`, `verify`, `list`, `prune`, `doctor`, `targets`.

**The primitives are the recovery toolkit and are not surface to be trimmed.**
This project's normal case is failure *mid-sequence*: Windows reboots itself
during Update and the agent drops with "Port is not connected"; the install has
two boot phases with a UEFI shell between them; UTM does not see a new bundle
until relaunched; keystrokes miss the CD prompt. When `setup` dies at stage five
of seven you do not re-run the orchestration — you stand at that point and poke
it.

`start` is the clearest case. An earlier draft of this plan proposed deleting it
as "a footgun with a friendly name" for powering a VM on headlessly and leaving
a UEFI prompt. That judged a diagnostic as a user command: powering on with no
side effects is exactly how you isolate whether the bundle, the firmware or the
boot driver is at fault.

The README already said so, and that draft contradicted it:

> `up` is `create` + `RestartUTM` + `boot`. **The steps stay separate underneath
> because each fails differently and is worth retrying alone.**

**Rule:** a primitive is removed only when another primitive does the same
thing. Never for being low-level or misusable. Only `up` goes — it is
orchestration duplicating `setup`, not a primitive.

---

## What is wrong today

Measured, not estimated.

### Idempotency is bolted on, not a property of operations

Every expensive operation here is one you re-run: 4.2 GB download, 45-minute
install, a VM that reboots itself. Exactly **one** function is idempotent —
`Setup` — and it gets there by wrapping primitives that actively refuse:

| operation | re-run today |
|---|---|
| `Create` | **fails** — "already exists; remove it or choose another name" (`bundle.go:86`) |
| `BuildISO` | **fails** — "already exists — this never overwrites" (`isobuild.go:255`) |
| `Download` | **fails** if the destination exists (`fetch.go:159`) |
| `Setup` | idempotent, by doing everyone else's state checks itself |

Refusals that exist for **safety** — `CheckWritable` on immutable or hardlinked
media — stay. Refusing to *repeat work* is a different thing and goes.

### `setup` is a parallel implementation, not thin

Nineteen entry points, several of which no primitive uses:

| job | `setup` calls | the primitive calls |
|---|---|---|
| boot | `EnsureReady` | **`BootAndWait`** — `setup` never calls it |
| media | its own 80-line private `ensureMedia` | `fetch-iso` → `build-iso` |
| install | `RunInstall` *and* `EnsureReady` | `RunInstall` |
| state | `os.Stat`, `ISOLinks`, `Find`, `AgentReady`, `IsPaused`, `Inspect` | — |

That drift is the mechanism behind the `EnsureReady`/`RunInstall` defect: the
layers had separated far enough that the wrong function looked right.

### Duplicate *responsibility*, which no scanner sees

A scan for duplicate function bodies returns clean. The right question is **"who
else performs this responsibility?"**

**"Is this VM usable?" is decided in nine places, four ways** — `AgentReady()`,
`Status() == "started"`, `IsPaused()`, `BootEntryWritten` — in five
combinations. Two disagree about case for the same question:

```go
install.go:121      if st, _ := vm.Status(); st != "started"
bootassist.go:181   if st, _ := vm.Status(); !strings.EqualFold(st, "started")
```

A latent bug in the open that no duplicate-body scan will ever report, because
the code is not duplicated — the *decision* is.

Same audit: **six files know where the Windows media lives** (`bundle.go`,
`external.go`, `isoguard.go`, `paths.go`, `setup.go`, `main.go`). `Paths` exists
to own that and does not.

### DRY of facts

| fact | copies | where |
|---|---|---|
| guest temp-file naming | **2 schemes** | `run.go` builds them, `cleanup.go:121` lists them for `Prune` |
| `win11-arm64.iso` | 5 | `paths.go:83`, `external.go:90,99`, `setup.go:257` |
| default VM name `irgo-win11` | 3 | `setup.go:87`, `mise.toml`, `external.go:99` |
| timeouts | **8 literals** | `10m×3, 45m×2, 2m×2, 60m, 15m, 5m, 10s` — no policy |

The first is a live bug: add a guest-file prefix and `Prune` silently stops
cleaning it.

### Six retry loops, 22 exec sites, no seam

Poll/retry is hand-written at `bootassist.go:110,196`, `control.go:202,269`,
`install.go:136`, `cleanup.go:93`, each with its own interval. This is where the
10-second-poll bug lived.

22 `exec.Command` calls across 8 files reach `utmctl`, `osascript`, `plutil`,
`hdiutil`, `ditto`, `bsdtar`, `wimlib-imagex`, `xorriso`. `osascript` is driven
from two files under different escaping assumptions — and the trap table records
that `%q` double-escaping made every Go-driven boot silently fail.

**There is no seam, so nothing VM-related is unit-testable.** That is why
coverage is thin: it is structural, not laziness.

### Missing infrastructure

- **`context.Context`: zero occurrences.** Operations run 45 minutes, download
  4.2 GB, poll for minutes. Nothing is cancellable; Ctrl-C leaves partial state.
- **One sentinel error.** Nothing can distinguish *VM not found* from *agent not
  ready* from *out of space*, so `setup` can only abort, never recover.
- **Four progress mechanisms**, and `utmvm` writes to stdout itself — so the
  package is unusable by anything but this CLI and machine-readable output is
  impossible.
- **Zero locks.** Two processes can create or delete the same VM concurrently.
- **`Ensure` means five things** — install an app, download a file, boot a VM,
  make a directory, install two binaries. That ambiguity is what made
  `EnsureReady` look right.
- **`CanCreateVMs()` is bypassed** at `bundle.go:15`, which compares
  `runtime.GOOS` directly despite the helper's own comment saying not to.

### The CLI and `mise.toml` have no boundary

30 mise tasks: **20 thin wrappers** over CLI commands, **7 with real shell
logic**. `vm:up` already invokes `irgo-winvm up`, which this plan deletes.

Worse, `probes` — 13 lines that cross-compile every Windows probe — exists
*only* in mise. Someone who runs `go install …@latest` **cannot build probes at
all**, and `create -probes <dir>` needs binaries they have no way to obtain. So
`irgo-winvm probe` is already broken for anyone who did not clone.

> **`mise` is a maintainer tool for this repository. Nothing else.**
> A person using the binary never installs mise. A developer on a project that
> *uses* this tool never installs mise. Only someone changing **this** repo does.

That deletes the 20 wrappers and leaves ~8 tasks: `check`, `fuzz`, `lint`,
`upstream:*`. The README changes with it — and further: **it should not list CLI
commands at all.** Every command in prose is a copy that falls behind, as
`mise run vm:up` already has. `irgo-winvm -h` is generated from the flag
definitions and cannot drift. Worked examples belong in `RESULTS.md` as **dated
measurements** — a stale measurement is a fact about the past; a stale
instruction is a lie.

---

## Phases

Two rules set the order: **every `utmvm` API change lands before the CLI is
written against it** (6–9 before 11), and **the phase that makes testing
possible comes before the phases that need testing** (5 before everything
risky).

| # | phase | size | verify | why here |
|---|---|---|---|---|
| 1 | Lint baseline, then delete what it finds | S | CI | `unused` finds the dead code for you |
| 2 | One source of truth for facts | S | CI | fixes a live bug; no dependencies |
| 3 | One retry primitive | S | CI | no dependencies |
| 4 | Decide probe distribution | S | decision | blocks 11 |
| 5 | Reporting seam + `runner` interface | L | CI | **unlocks unit tests — everything after is testable** |
| 6 | `Check`/`Ensure` split, `VMState`, pure `doctor` | L | CI | **the keystone; fixes the live `doctor` bug** |
| 7 | Idempotency through `Ensure`; `setup` becomes thin | M | CI + twice-run | needs 6's `Ensure` to exist |
| 8 | `context.Context` through long operations | M | **VM** | signature change, after 6–7 settle |
| 9 | Verbs, typed errors, locking | M | CI | last API change before the CLI |
| 10 | Group `utmvm` by subject (`git mv`) | S | CI | move files only once content is final |
| 11 | Rewrite the CLI | L | **VM** | against the now-final API, written once |
| 12 | Shrink the exported surface | S | CI | needs the CLI's real usage to know what is unused |
| 13 | Tighten enforcement | M | CI | thresholds set from the finished code |

**1 — Lint baseline, then delete.** Land `.golangci.yml` with only the checks
the code already passes, and turn on `unused` so it *identifies* the dead code
rather than a grep being trusted. Then delete the eight symbols with no caller:
`BuildFATImage`, `GuestToolsInstallCommand` (still carries a `start`-wildcard
bug already fixed in the answer file), `OpenDisplay`, `BootAssist`,
`IfaceVirtIO`, `SchemaConfigurationVersion`, `EnsureISOTools`, `SuspendToDisk`.
The last reports success while power-cutting the guest; its finding stays in
`RESULTS.md`.

**2 — One source of truth for facts.** `guestFile()` shared by `run.go` and
`Prune`, fixing the live drift; named timeout constants carrying their reason;
ISO name and default VM name declared once in `Paths`.

**3 — One retry primitive.** Collapse the six loops.

**4 — Decide probe distribution.** Embed with `go:embed`, download from a
release like the guest tools already are, or admit probes are maintainer-only —
in which case mise is their correct home. A decision, not code; do it early so
phase 11 is not blocked.

**5 — Reporting seam and `runner` interface.** One reporting mechanism instead
of four; every `fmt.Printf`/`os.Stderr` write moves out of `utmvm` into the CLI.
The `runner` seam lands here because both are about who may talk to the outside
world. **This is what makes the package testable without a VM**, which is why it
precedes everything risky: from here on, phases are verified by tests rather
than by hand.

**6 — `Check`/`Ensure`, `VMState`, pure `doctor`.** The keystone. Every
capability gets a pure `Check` and an acting `Ensure` built on it. `VMState`
replaces the nine ad-hoc answers to "is this VM usable". `doctor`, `status`,
`targets` and `verify` are rewired to `Check` only — fixing the live bug where
`doctor` installs UTM. Add the test that no REPORT command reaches an
`Ensure*`/`Fetch*`/`Install*`.

**7 — Idempotency, and `setup` becomes thin.** `Ensure` semantics on `Create`,
`BuildISO`, `Download`; `ExpandESD` skipping images already exported. Then
`setup` is rewritten to call *only* what its primitives call — deleting
`ensureMedia`, using `BootAndWait` where `boot` does. 338 lines to roughly 40,
a list of steps and their outcomes. Add the test asserting each stage resolves
to the same entry point as its primitive.

**8 — `context.Context`.** `Download`, `ExpandESD`, `BuildISO`, `RunInstall`,
`BootAndWait`, `WaitForAgent*`, `Setup`. Ctrl-C should stop a 45-minute install
cleanly. *VM* — cancellation during a real install is the only honest test.

**9 — Verbs, typed errors, locking.** Give `Ensure`/`Fetch`/`Build`/`Run` fixed
meanings: `EnsureReady` becomes `BootInstalled`, which is what it does and would
not have been mistaken for `RunInstall`. Typed errors for the states `setup`
should act on. A lockfile per VM bundle. Route `bundle.go:15` through
`CanCreateVMs`.

**10 — Group `utmvm` by subject** with `git mv` so history follows: `media_*`,
`deps_*`, `vm_*`, `host_*`. **No sub-packages** — the parts are coupled and
splitting would force the exported surface to stay large, defeating phase 12.

**11 — Rewrite the CLI.** Written fresh against the now-final API, old file
deleted. Every primitive carried over unchanged; only `up` dropped. New shape:
`main.go` (dispatch only) plus `cmd_{setup,vm,boot,guest,media}.go`, with one
helper behind the 18 flagsets and 10 copies of *resolve-find-handle*, so an
unknown VM reads the same from every command — which it currently does not.

**12 — Shrink the exported surface.** Grep `cmd/` for each of the 134 exported
symbols; unexport what is absent. Expect 40–60 to go.

**13 — Tighten enforcement.** Below.

### If it stops early

Phases 1–3 are cheap, self-contained and fix a live bug. **Phase 5 is the
highest-value single phase** — without a `runner` seam nothing here can be
tested, and every later phase has to be verified by hand against a real VM.
**Phase 6 fixes a bug that is shipping today.** Phases 10–12 are cosmetic by
comparison: stop before them without loss.

---

## Phase 13 in detail — enforcement

A cleanup that is not enforced decays back. **Most of this mess was made by an
agent that did not read what already existed**, so prevention has to work on
that failure mode specifically: a convention nobody reads prevents nothing.

### A linter that catches each mistake actually made

Not a default preset — exactly the checks that would have caught this repo's own
history:

| linter | the bug it would have caught |
|---|---|
| `unused` | all 8 dead symbols, at the moment each lost its caller |
| `dupl` | 3 wait-for-agent loops, 3 batch-file blocks, 4 byte formatters |
| `goconst` | `win11-arm64.iso` ×5, `irgo-win11` ×3 |
| `mnd` | the 8 unexplained timeout literals |
| `funlen`, `gocognit` | `runUp` at 86 lines doing five things |
| `errcheck`, `gosec` | already clean; keep them clean |

Land a minimal config in **phase 1** so the refactor itself is gated while churn
is highest; tighten thresholds here once the code is final.

### Tests that encode the invariants

Five properties invisible to the compiler, each of which has already broken:

```go
TestReportCommandsArePure       // no REPORT command reaches Ensure*/Fetch*/Install*
TestSetupStagesMatchPrimitives  // each setup stage resolves to its primitive's entry point
TestGuestFileNamesArePrunable   // generate via guestFile(), assert Prune claims it
TestOperationsAreIdempotent     // Download/BuildISO/Prune twice; second is a no-op
TestExportedSurfaceBudget       // count exported symbols; fail if it grows
```

The last is deliberate: 134 exported symbols accumulated because nothing ever
objected. A budget makes growth a decision someone takes.

### `AGENTS.md`

The rules live where agents load them without being asked, with `CLAUDE.md`
pointing at it. It carries the three-consumer pattern, the primitives rule,
"search before you write", and the two questions that catch what a scanner
cannot: *who else performs this responsibility*, and *what does a person do when
this fails halfway*.

---

## Verification

**Every phase:**

```sh
mise run check && mise run lint
for t in darwin/arm64 linux/amd64 windows/amd64 windows/arm64; do
  GOOS=${t%%/*} GOARCH=${t##*/} CGO_ENABLED=0 go build -o /dev/null ./...
done
```

**Idempotency — run it twice.** Phase 7 adds it as a test; most needs no VM
(`Prune` twice, `Download` twice to an existing file, `BuildISO` twice against a
fixture).

```sh
irgo-winvm setup && irgo-winvm setup       # second run: every stage skipped
irgo-winvm iso -protect && irgo-winvm iso -protect
```

**Phases 8 and 11 (*VM*)** — a green build means "compiles", not "works", and
these paths fail silently:

```sh
irgo-winvm run -timeout 3m -vm irgo-win11 .bin/nativeprobe-arm64.exe
irgo-winvm run -gui -timeout 4m -vm irgo-win11 .bin/glaze-verifyevents-arm64.exe
irgo-winvm suspend -vm irgo-win11 && irgo-winvm resume -vm irgo-win11
```

The last must still report **~400 ms**. Seconds means a poll interval was lost —
exactly how that bug happened the first time.

**Once, at the end — a fresh install.** Nothing above exercises it, phases 7 and
8 both touch it, and it is the only path whose failure costs 45 minutes to
observe:

```sh
irgo-winvm setup -vm refactor-test -install     # ~45 min, unattended
irgo-winvm delete -vm refactor-test -force
```

Never against `irgo-win11`.

---

## Git strategy

A branch per phase, merged only once verified. **CI cannot prove these phases
correct** — the VM-dependent paths have no unit coverage until phase 5, and
green means "compiles".

```sh
git switch -c refactor/06-check-ensure master
# … work, verify …
git switch master && git merge --no-ff refactor/06-check-ensure
```

- **`--no-ff`**, so each phase is one revertable merge. A regression found three
  phases later is `git revert -m 1 <merge>`, not a hand-unpick.
- **One phase per branch**, so a bisect lands on a single concern.
- **Do not merge on CI alone** for phases marked *VM*. Put the verification
  output in the merge commit body — the only durable record it was run.
- **`pre-refactor` is tagged** at the last commit whose `RESULTS.md`
  measurements were actually taken.

---

## What not to do

- **Do not delete a primitive because it looks like a footgun.** `start` powers
  a VM on with no side effects and yields a UEFI prompt — which is exactly what
  isolates the fault when `setup` has failed.
- **Do not "tidy" the comments.** They record findings that cost hours and are
  not recoverable from the code.
- **Do not touch `assets/`, the answer file, or the plist template** without
  running a real install. UTM rejects a bad config with one generic "cannot
  import this VM" naming no field; `config_test.go` exists because that failure
  is undebuggable.
- **Do not split `utmvm` into sub-packages.**
- **Do not make `boot` idempotent by re-driving.** Re-sending keystrokes into a
  live Setup has destroyed an install. Idempotent here means *detecting it need
  not act*.
- **Do not do this in one commit.**

---

## Appendix: method

How these were found, and why earlier passes missed them.

**A bug is not understood until you name the structure that permitted it.** The
`EnsureReady`/`RunInstall` defect was first recorded as "used the wrong
function" — a slip worth a one-line correction. The real answer is that
orchestration and primitives had drifted into two implementations of one job, so
the wrong function *looked* right. That framing produces a rule, a test and a
phase; the first produced nothing.

**Ask "who else does this job?", not "is this code repeated?"** A duplicate-body
scan returned clean while nine places decided whether a VM is usable, six knew
where the media lives, and two implemented the media pipeline. Syntactic tools
are blind to this by construction.

**Ask what a person does when it fails halfway.** The case for keeping the
primitives — and for `setup` calling down through them — is invisible from
reading code and obvious the moment you picture someone at a VM stuck at a UEFI
prompt. A plan that reasons only about structure will keep proposing to delete
the recovery toolkit.

This document was itself rewritten after each of those realisations. An earlier
draft was numbered `0,1,2,3,4,4b,4c,5,6,6b,7,8` — the suffixes were where things
were bolted on instead of re-planned, which is the same accretion the plan
exists to undo.
