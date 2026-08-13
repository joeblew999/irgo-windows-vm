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

Every capability exposes exactly three entry points:

```go
Check()  (State,   error)   // pure. No installs, no downloads, no writes.
Ensure() (Outcome, error)   // acts — and is built on Check
Clean()  error              // removes what Ensure created
```

- **DO** calls `Ensure`.
- **SEQUENCE** calls the *same* `Ensure`. This is what makes `setup` thin, and
  what makes reproducing a `setup` failure by hand actually reproduce it.
- **REPORT** calls `Check` **only**, so a diagnostic *cannot* mutate rather than
  being trusted not to.

`Clean` is the third because it is what makes a failed stage recoverable:

> **fix the code → delete what the stage did → run it again**

That loop is the whole recovery story for an unattended run, and it needs each
stage to know what it created. Without it, a failure leaves debris that the
retry then trips over — which is not hypothetical, it is three live bugs:
`RunInteractive` leaves an `irgo-l-*.bat` in the guest on **every** call,
`Create` leaves a half-built bundle when it fails (and cannot remove it, because
the ISO hardlink is immutable), and `Download` leaves a `.part` that makes a
subsequent 416 permanent. Each is a missing `Clean`.

`Clean` also completes idempotency rather than duplicating it. `Ensure` makes
re-running safe when the previous run *succeeded*; `Clean` makes it safe when
the previous run *failed halfway* — which this project's own notes call the
normal case.

Everything below is a consequence of this: the idempotency contract, the thin
`setup`, the single `VMState`, the primitives kept as a recovery toolkit.

### Two verbs on top, the chain underneath

What a developer actually wants is what an iOS developer wants from a simulator:
**give me a machine, run my binary on it.** Nobody "sets up" a simulator. So the
surface is two verbs, and `setup` is not a concept a developer ever meets — it
*is* the first verb, named for what it does.

```
vm    →  EnsureHost → EnsureMedia → EnsureBundle → EnsureInstalled → EnsureRunning → EnsureAgent
run   →  Ensure(vm ready)  then execute
```

The chain is **implicit to the developer and explicit in the code**. That is the
whole design:

- **Every node is also a primitive command.** That is what makes the chain
  addressable, and it is why primitives are never trimmed for being low-level.
- **`vm` is idempotent; `run` is not.** Already-ready is success. Running a
  binary twice runs it twice — correct, and not to be "fixed" into caching.
- **`doctor` is the sum of every `Check`. `nuke` is the sum of every `Clean`.**
  Neither is a command to maintain separately; both fall out of the chain.

**The AI and the developer call the same two verbs.** This matters more than it
looks. When a stage fails the AI drops to that node, `Clean`s it, fixes the
code, re-runs *that node* — and then goes back to typing `vm` and `run`, exactly
as a developer would, until the next failure. So the developer-facing command
*is* the integration test, and a stage is not done until the thing a developer
types works.

That also means the verification harness is not separate apparatus. It is these
two verbs plus assertions on their output — which is why the harness needs
`-json` on the same commands rather than commands of its own. Same verbs,
machine-readable results.

**`nuke` is load-bearing, not a convenience.** It is the only way back to
nothing, and "a developer on a fresh machine" cannot be tested without it. It is
also the fallback when `Clean` is incomplete, which during a refactor it will
be. Note it **cannot work today**: the immutable-hardlink defect makes bundle
removal fail with `EPERM`, so `nuke` depends on the phase 7 `Delete` fix.

## The second pattern: "cannot tell" silently means "allow"

Reading the package line by line turned up a defect class the three-consumers
frame does not cover, and it is the more dangerous of the two, because it
disables the machinery this project exists to have.

Every destructive-operation guard is written `if ok && bad { refuse }`:

```go
if flags, ok := fileFlags(abs); ok && flags&uchgFlag != 0 { return ...immutable }
if _, nlink, ok := inodeInfo(abs); ok && nlink > 1     { return ...shared    }
```

So when the question **cannot be answered**, the guard does not fire and the
write proceeds. "Unknown" and "safe" are the same value. The same shape recurs
in `bundle.go:80` (`err == nil && !sp.OK` — a space check that vanishes when it
errors), `isoguard.go:62` (a stat failure reports `Protected == false`, which
`setup.go:131` reads as *not protected*), and `paths.go:171` (any stat error
returns "writable", skipping both checks below it).

Two things make this more than a nitpick.

**The comments claim the opposite, in the file whose entire job is this.**
`sysfile_other.go:25` says reporting `ok=false` *"makes callers treat every file
as potentially shared, which is the safe direction."* It is the unsafe
direction: every caller reads `ok=false` as *not shared* and proceeds. Twenty
lines above, the same file states the principle it breaks — *"a wrong answer
about whether two files share blocks, or whether one is protected, is worse than
no answer: it would let a destructive operation proceed on the grounds that
nothing said otherwise."* That is a precise description of the bug, sitting
directly above it.

**On any non-darwin build, all of it is off at once.** `sysfile_other.go` returns
`ok=false` for `inodeInfo` and `fileFlags` unconditionally, so `CheckWritable`
degrades to the VM-directory test alone. And `hasDotDot` (`paths.go:187`)
hardcodes `"../"`, while `filepath.Rel` on Windows returns `..\x` — so even that
last guard inverts, reporting every outside path as inside.

The fix is a tri-state, not a boolean. A guard must distinguish *no*, *yes* and
*could not determine*, and the caller decides which way "could not determine"
falls — refusing by default, with an explicit override. Whichever is chosen, it
must be **stated at the call site** rather than encoded in a dropped `ok`.

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
| timeouts | **19 in `utmvm`, 29 with `cmd/`** | `10m×3, 45m×2, 2m×2, 60m, 15m, 5m, 10s` — no policy |

The first is a live bug: add a guest-file prefix and `Prune` silently stops
cleaning it.

### Six retry loops, 22 exec sites, no seam

Poll/retry is hand-written at `bootassist.go:110,196`, `control.go:202,269`,
`install.go:136`, `cleanup.go:93`, each with its own interval. This is where the
10-second-poll bug lived.

22 `exec.Command` calls across 9 files reach `utmctl`, `osascript`, `plutil`,
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

### This is two systems wearing one coat

The largest structural problem in the repo, and the one that made the probe question come out
wrong the first time.

| | **System A — VM management** | **System B — the probe suite** |
|---|---|---|
| what | Windows VMs under UTM on Apple Silicon: create, install unattended, boot, run a binary, suspend, screenshot, build ISOs | Windows programs exercising glaze/native capabilities, plus the harness that runs them and records what works |
| audience | any Go developer needing Windows-on-ARM64 | glaze/native users, and crgimenes upstream |
| artefacts | `utmvm`, `irgo-winvm` | `probe/`, `glaze-probes/`, `examples/`, `RESULTS.md`, `UPSTREAM.md` |
| depends on | nothing here | **System A** |

B depends on A. **A must not know B exists** — and today it does:

- `Options.ProbeDir`, `PayloadOptions.ProbeDir`, `SetupOptions.ProbeDir` —
  the generic "stage files onto the payload medium" feature is named after one
  caller's use for it.
- `external.go:104` lists *"probe binaries"* as a dependency, with the fix
  `"mise run probes"` — a VM library telling you to run a task in this repo.
- `external.go:113–124` lists **glaze and native clones** as dependencies of
  the VM library.
- `paths.go:203` hardcodes `~/workspace/go/src/github.com/crgimenes` as
  `IRGO_UPSTREAM_DIR`'s default. **A library for running Windows VMs contains
  one person's GitHub org.**
- `config.go:117` justifies the GPU choice by *"running console probes and a
  WebView2 window"* — a correct finding, filed under the wrong system.

The rule that follows: **nothing in `utmvm` may name a probe, glaze, native or
crgimenes.** `ProbeDir` becomes `StageDir` and means what it does — files to
place on the payload medium. The upstream-clone inventory and `IRGO_UPSTREAM_DIR`
move to System B, which is the only thing that has ever wanted them.

### One repo. DECIDED.

The strongest argument for splitting was that a Go developer installing a VM
tool should not drag in glaze. **That is already solved at the module boundary,
deliberately**, and the modules say so themselves:

```
go.mod               → go-diskfs. Zero glaze, zero native, zero crgimenes.
glaze-probes/go.mod  → "Separate module: … should not drag that dependency
                        into the VM tooling."
examples/go.mod      → "Separate module so glaze and native stay out of the
                        VM tooling's dependency graph."
```

`go install …/cmd/irgo-winvm@latest` already resolves a clean graph. The module
split did the work the repo split would have done.

**"Two systems" is an architecture statement; "two repos" is a distribution
statement, and they do not have to agree.** Here they should not, because
**System B is how System A gets tested**. A repo split puts a tag-and-bump
version boundary in the middle of the feedback loop that finds System A's bugs:
every `utmvm` fix a probe discovers would need releasing before the probe could
consume it. For one maintainer that is friction paid daily for a benefit already
held.

Phase 12 gives the separation that matters, and gives it *harder* than a split
would: a test that fails if `utmvm` names a probe, glaze, native or crgimenes. A
repo boundary is enforced by nothing.

**What would change the answer**, on evidence rather than taste:

- someone other than the maintainer depends on System A, so it needs its own
  release cadence and issue tracker;
- System A is published or announced as a standalone tool;
- the two genuinely stop co-evolving — System A stable for months while System B
  churns.

None hold today. If one does later, the split costs a `git mv` and a `go.mod`,
because phase 12 will already have removed every symbol that crosses.

**Two module defects to fix while there** (phase 12, since both are boundary
facts): `probe/go.mod` declares `module nativeprobe` — unqualified, unlike its
two siblings, so it cannot be referred to by path; and the four modules declare
two Go versions (`1.25.0` at root, `1.26.5` in the other three), which makes CI's
`go-version-file: go.mod` install 1.25 and then download a second toolchain.

### Found by reading, not grepping

Everything above came from measurement. These came from reading the code line by
line, which no earlier pass had done — and they are the kind a scanner cannot
find.

**The README's headline claim is false.** Line 6 says *"Pure Go. No `hdiutil`,
no `plutil`, no shell scripts — clone, build, run."* The code shells out to
`plutil` (`ensure.go:61`), `hdiutil` (`installutm.go:72,79` — added during this
session's own work), `osascript`, `ditto`, `bsdtar`, `wimlib-imagex` and
`xorriso`. This plan has repeatedly reasoned *from* that claim, so parts of it
were built on a false premise.

The defensible version, which is true: **no toolchain is needed to build or
cross-compile, and nothing must be installed for the common path.** Every
external program is either a macOS built-in (`plutil`, `hdiutil`, `ditto`,
`osascript`, `bsdtar`) or optional and only for building your own ISO
(`wimlib`, `xorriso`). Fix the claim; do not quietly keep it.

**`irgo-winvm iso` scans the current working directory** — `main.go:755` calls
`ScanISOs([]string{"."})`. Run it from `$HOME` and it walks everything you own
looking for 1 GB files.

**An unreachable error branch** — `main.go:331`. `runExec` returns early if
`-cmd` is empty, then checks `*cmdline == ""` again inside the `len(argv) == 0`
branch. The helpful message *"give a command after the flags"* can never print.
The comment above it describes an improvement the required-flag check defeats.

**A flag whose documented default does not exist** — `main.go:391` advertises
`-o` as *"(default: under IRGO_CACHE_DIR)"*; no code applies any default, and an
empty `-o` just prints help.

**A fifth byte formatter, in the function next to the ones I fixed** —
`main.go:902`, `fmt.Printf("%.1f MB reclaimed", float64(freed)/(1<<20))`. An
earlier commit in this session claimed "four byte formatters collapse into
`HumanBytes`". It missed the one in `runPrune`.

**Bundle layout leaks into the CLI four times** — `e.Name+".utm"` is
concatenated by hand at `main.go:300,925,1084,1119`, plus `"Data", "disk.img"`.
`utmvm` owns that layout and exposes no `BundlePath(name)`.

**`-replace` swallows its error** — `main.go:964`,
`if _, err := utmvm.Delete(*name, true); err == nil`. A failed delete is
silently ignored, and `Create` then fails with "already exists", which points at
the wrong cause.

**A stale comment on `runISO`** claims `fetch-iso` "does not exist yet and
cannot be reviewed" (`main.go:702`). It exists; this session wrote it.

**A garbled user-facing string** — `main.go:935` prints *"boot took — Setup is
running or the guest agent answered"*.

### The boot driver can type into a running Windows

The most destructive thing in the repo, and its own comments say so: keystrokes
landing in Setup's UI *"wrecked an install that had already partitioned the
disk"* and were found sitting in the Product key field. Three separate paths
reach that state today.

**`BootAssistWatched` ignores the disk it exists to watch.**

```go
func BootAssistWatched(vmRef string, target BootTarget, diskPath string) error {
	return BootAssistOn(vmRef, target, "")   // diskPath never referenced
}
```

Its doc promises progress-checking between candidates. `BootAndWait` passes a
real disk path into it. The parameter is dead, and so is the safety it names.

**The installed-Windows loop types into a booting desktop.** `bootassist.go:105`
tries five filesystems, allowing `4 × 10 s` for `AgentReady()` per candidate.
Windows ARM64 routinely takes longer than 40 s to answer, and with
`Options.NoGuestTools` it *never* answers — so the loop advances and sends
`fs3:`, an EFI path and eight Enters into a live logon screen. The comment above
it asserts the opposite: *"once it boots the loop stops."* It stops only if the
agent answers.

**It then reports success having failed five times** — `bootassist.go:117` is a
bare `return nil` after the loop exhausts every candidate. Both callers treat
`nil` as *the boot took*.

**`BootAndWait` never checks whether the guest is already up** before typing
(`bootassist.go:181`). `RunInstall` does gate on `p.AgentUp`; this does not. So
`irgo-winvm boot -vm X` against a healthy, logged-in Windows sends a boot
command straight into the desktop.

**And the progress check that would catch it is disabled** — `start, _ :=
diskUsage(diskPath)` (`:195`) discards `ok`, so an unreadable path silently
pins the baseline at 0 and the check never fires. Same shape as the pattern
above.

**A comment and its own asset now disagree about a fact learned the hard way.**
`keystrokeDelay` says *"Exactly ONE key may be sent at the 'Press any key' prompt"*;
`assets/boot.applescript` sends **eleven**, and carries its own careful note
explaining why eight is right and forty was not. The asset was corrected and the
constant's comment was not. Under this repo's rule that comments are findings,
one of these two is now a lie, and there is no way to tell which from the code.

`startup.nsh` and `bootassist.go` likewise assert **opposite facts about the
same two loaders** — whether `cdboot_noprompt.efi` or `bootaa64.efi` boots. Two
shipped assets in one package cannot both be right.

### Downloads can produce a plausible, wrong 5 GB file

Three defects in `fetch.go` compose:

- **No length check.** The copy loop breaks on `io.EOF` and renames `.part` into
  place. `done` is never compared to `total`. A connection dropped at 60 % is
  indistinguishable from success.
- **No `fsync` before rename.** After a crash the final name can exist at full
  length with unflushed tail blocks.
- **The destination guard is bypassed.** `refuseUnsafeDest` runs at line 34;
  `os.Rename(part, dest)` at line 128 clobbers unconditionally — hours later for
  a 4.5 GB download, including a `dest` hardlinked into a VM bundle in between.

The only thing standing between these and a corrupt ISO is the SHA-1 — which is
`""` for both the UTM `.dmg` (`installutm.go:61`) and the guest-tools ISO
(`ensure.go:157`), so for those two it is off. `main.go:437` prints
`verified sha1 %s` unconditionally after `Download` returns, so an empty hash
prints *"verified sha1 "* and verified nothing.

Related: the UTM `.dmg` is installed into `/Applications` with no checksum and
`hdiutil -noverify`, which disables the image's own integrity pass. The
justification in the comment — that macOS verifies the signature at launch — is
the only verification claimed anywhere, and nothing runs `codesign` or `spctl`.

### `-b` where the file's own header says `-e`

`isobuild.go:298`: the `mkisofs` fallback marks the El Torito entry **BIOS**,
contradicting both the file header (*"must be marked EFI (platform 0xEF)"*) and
the `xorriso` branch six lines up (*"-b describes a BIOS entry and the firmware
ignores it"*). The fallback yields a correctly sized, correctly named,
**non-bootable** ISO. The `switch` has no `default`, so a third masterer would
run with zero arguments.

### Cleanup and deletion act on guesses

- **`Prune`'s prefix list contains `"irgo-"`**, which is a prefix of every other
  entry and matches anything in `os.TempDir()` starting with it — including the
  batch file a *concurrently running* `pushScript` has open. `main.go:895` calls
  it on `os.TempDir()` with no dry-run.
- Two entries in that list, `irgo-i-` and `irgo-l-`, **can never match**: those
  names only ever exist as *guest* paths under `C:\Users\Public`. Meanwhile the
  guest files they describe are never cleaned — `RunInteractive` leaks an
  `irgo-l-*.bat` on every call.
- **`Prune` declares `error` and cannot return non-nil**; unreadable directories
  are swallowed by `continue`, reporting "0 removed, no error".
- **`freed` is counted before removal and regardless of success**, using the
  directory entry's own size — ~96 bytes for payload trees that are gigabytes.
  That number is what `main.go:902` prints as "MB reclaimed".
- **`Delete` reconstructs the bundle path from the display name**
  (`cleanup.go:39`) and never consults the UUID it already holds. `Create`
  accepts a custom `OutDir`, and UTM permits a display name differing from the
  filename — so this either misses, or removes a same-named bundle belonging to
  a different VM.
- **`Delete` proceeds after a failed stop.** `vm.Stop()`'s error is discarded and
  the wait loop just ends after 30 s, then `RemoveAll` runs on a live QEMU —
  exactly the scenario the doc comment says the ordering exists to prevent.

**A phantom entry cannot be cleared with `utmctl`, and its failure is silent.**
Measured on this machine, not reasoned. `snap-test` was registered in UTM with
no bundle on disk — the exact state `cleanup.go:78` warns of, *"UTM is left with
a phantom entry"*. Deleting it:

```
$ utmctl delete 7D3B3DFF-…
Error from event: The operation couldn't be completed. (OSStatus error -2700.)
"snap-test.utm" couldn't be removed.
$ echo $?
0
```

Two defects in four lines. **`utmctl delete` reports failure and exits 0**, so
`Delete` cannot detect it — the same class as `utmctl exec` always exiting 0,
already recorded for `run.go`. And UTM refuses to drop the registry entry
because it cannot remove a bundle that is not there, so the tool has no path out
of a state its own behaviour produces.

The recovery, which belongs in a comment beside `Delete`: recreate an empty stub
at the expected bundle path, then delete again. UTM removes the stub and drops
the entry. That worked here and left the running VM untouched.

**And protecting the ISO makes the VM undeletable.** `setup` sets `uchg` on the
ISO inode *before* `Create` hardlinks that same inode into the bundle. The flag
is per-inode, so `os.RemoveAll` fails with `EPERM` on any bundle whose ISO was
ever protected, and nothing calls `UnprotectISO` first. The same defeats
`Create`'s own failure cleanup, whose error is discarded — so a half-built
bundle survives a failure the comment promises to clean up.

Setup also `chflags`-es a **user-supplied** `-iso` path immutable as a silent
side effect.

### Two heuristics presented as facts

**`BootEntryWritten`** (`install.go:62`) is a two-file mtime comparison with a
one-minute margin. UTM itself writes `efi_vars.fd` on first power-on, so a VM
merely *booted once* with no Windows on it reports `true` → `PhaseFinalising` at
0 MiB, and `setup` announces *"Windows is installed; booting it"*. Conversely
any settings change in UTM's UI rewrites `config.plist` and flips it back to
false on a genuinely installed VM. This single flag is what `setup.go:215`
uses to choose between recovering and reinstalling, under a comment reading
*"reinstalling would destroy it"*.

**`Inspect` cannot say "I could not find the disk."** `install.go:59` discards
`ok` from `diskUsage`, so a missing disk image and *nothing written yet* are the
same `DiskMiB == 0` — and that is the state that fires keystrokes, up to six
times, into whatever the guest is actually doing.

### Guest command injection

`run.go:269` interpolates the username into a batch file unquoted:

```go
"... /sc once /st 23:59 /ru " + user + " /it /f\r\n"
```

A guest account with a space breaks it; one containing `&` or `>` is injection
into a batch file running in the guest. `quoteForCmd` exists in the same file
for exactly this and is not used. `schtasks`'s own failure is invisible, because
`utmctl exec` always exits 0 — surfacing five minutes later as a timeout.

### Comments that contradict their code

Beyond those already named, reading found a dozen more: `control.go:61` says the
fallback matches on name and it falls back *to* the name from a UUID (and since
every internal caller passes a UUID, this "fallback" is the normal path — every
`StartWithDisplay` runs one guaranteed-failing osascript first); `fatimage.go`
and `isoimage.go` make **flatly opposite claims about the same experiment**
(whether Setup reads `autounattend.xml` from FAT); `GuestToolsInstallCommand`
says it exists *"so the answer file and the drive wiring cannot drift apart"*
and has drifted, still holding the `start`-wildcard bug that `assets_test.go`
was written to prevent; `external.go:82` says guest tools cannot be fetched
while `ensure.go` fetches them; `Prune`'s and `Push`'s doc comments are glued to
the wrong declarations, so `go doc` shows them on a variable and a helper.

`config.go:33` points at `NewConfig`, which does not exist anywhere in the repo.

### Test coverage is thinner than "thin"

| | |
|---|---|
| test functions | **24** |
| exported symbols | **134** |
| files with **no test at all** | **21 of 26** |

The untested list is every file that touches a VM, media or setup: `control`,
`run`, `install`, `setup`, `bundle`, `fetch`, `isobuild`, `isoguard`, `ensure`,
`paths`, `brew`, `cleanup`, `host`, `screenshot`, `diskspace`, `payload`,
`external`, `installutm`, `iso`, `fatimage`, and both `sysfile_*`.

Only `config`, `isoimage`, `catalog`, `assets`, `bootassist` and the prune
regression have any. This is the strongest argument for phase 3: **without the
`runner` seam, 22 files cannot be tested at all**, and every phase after it is
verified by hand against a 45-minute install.

### Four modules, two Go versions, no policy

`go.mod` declares `1.25.0` at the root and `1.26.5` in `probe`, `glaze-probes`
and `examples` — two versions across four modules. CI's `go-version-file: go.mod`
therefore installs 1.25 and makes Go download a second toolchain for the rest.

### The CLI and `mise.toml` have no boundary

30 mise tasks: **20 thin wrappers** over CLI commands, **7 with real shell
logic**. `vm:up` already invokes `irgo-winvm up`, which this plan deletes.

Worse, `probes` — 13 lines that cross-compile every Windows probe — exists
*only* in mise. Someone who runs `go install …@latest` **cannot build probes at
all**, and `create -probes <dir>` needs binaries they have no way to obtain. So
`irgo-winvm probe` is already broken for anyone who did not clone.

**And CI reimplements mise rather than calling it.** `.github/workflows/check.yml`
rebuilds, re-vets and re-tests all four modules in inline shell — the same logic
as `mise run check`, written twice, and **already diverged**: CI writes binaries
to `/tmp/ci-bin`, mise to `.bin/host/`. The cross-compile matrix and the probe
cross-build exist only in CI, so a maintainer cannot run locally what CI will
fail on.

> **mise is the single source of truth for what "checked" means, and CI calls
> it.** CI installs mise and runs `mise run check`, `mise run lint`,
> `mise run cross`. It does not restate a single build step.

That is consistent with mise being maintainer-only rather than in tension with
it: **CI is maintainer infrastructure.** Someone using the binary never runs CI
either.

It does impose one constraint. The tasks CI calls run on Linux and macOS, so
**they must contain nothing macOS-specific** — no UTM, no `osascript`, no
`hdiutil`. That splits `mise.toml` cleanly in two, and the split is worth making
explicit because it is the same boundary as the rest of the plan:

| class | tasks | who runs it |
|---|---|---|
| **portable** | `check`, `lint`, `cross`, `fuzz` | CI, on every OS, and maintainers |
| **macOS-only** | `vm:*`, `iso:*`, `upstream:*` | maintainers, never CI |

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

Phases are sliced **by capability**, not by kind of change.

An earlier draft sliced horizontally — all the linting, then all the guards,
then all the context plumbing — and a cold review found what that shape
produces: phase 1 deleted code phase 9 needed, four phases rewrote a CLI a later
phase deleted, and a dozen recorded defects belonged to no phase at all. Work
was destroyed and defects fell between the slices.

So each capability phase **finishes a thing**: its defects, its
`Check`/`Ensure`/`Clean`, its tests, and its final CLI surface, in one branch.
`main.go` shrinks toward pure dispatch as each capability claims its commands,
so no CLI work is written twice.

Three rules set the order:

1. **Foundation before capabilities.** A capability cannot implement
   `Check`/`Ensure`/`Clean` before that contract exists, or be tested before the
   seam exists.
2. **Capabilities in dependency order**, ending with `setup`, which orchestrates
   all of them and is therefore last.
3. **Nothing is deleted until everything that might need it is done.** Dead-code
   removal moves to the end — that is where the earlier draft went wrong.

| # | | phase | verify | owns |
|---|---|---|---|---|
| 1 | **F** | Harness, lint, and `mise run lint` | self + negative controls | the gate everything else runs through |
| 2 | **F** | Guards and the filesystem foundation | CI | `sysfile_*`, `paths`, `diskspace`, `isoguard` |
| 3 | **F** | The seam: `runner` and reporting | CI | every `exec` and every `Printf` in `utmvm` |
| 4 | **F** | The contract: `Check`/`Ensure`/`Clean` | CI | typed errors, `context`, retry, `VMState` |
| 5 | **C** | Host, dependencies, and the REPORT commands | CI | UTM, brew, guest tools, and `doctor`/`status`/`targets`/`verify` made pure |
| 6 | **C** | Media | CI + VM | catalog, fetch, ESD, `build-iso`, protect, inspect |
| 7 | **C** | Bundle and configuration | CI + VM | `create`, `delete`, plist, payload, `prune` |
| 8 | **C** | Control and observation | CI + VM | `start`, `stop`, `suspend`, `resume`, `list`, `screenshot` |
| 9 | **C** | Boot and install | **VM** | `bootassist`, `install`, and the three experiments |
| 10 | **C** | Guest execution | CI + VM | `run`, `exec`, push/pull, the injection fix |
| 11 | **C** | Setup | **VM** | orchestration made thin by calling down; **deletes `up`** |
| 12 | **Z** | The two systems, and the modules | CI | System B out of `utmvm`; **`probe` leaves the CLI**; module names and versions |
| 13 | **Z** | Dead code and the exported surface | CI | **all deletions happen here, not earlier** |
| 14 | **Z** | Docs, `README`, `RESULTS`, mise | CI | the false claim, the 20 wrappers |
| 15 | **Z** | Enforcement | CI | linters, invariant tests, `AGENTS.md` |

### Every recorded defect has exactly one owner

The cold review's most valuable finding was defects with no phase. This table is
the fix, and it is the checklist a phase closes against:

| defect | phase |
|---|---|
| `sysfile_other.go`'s inverted comment; every `ok &&` guard; `hasDotDot` on Windows | 2 |
| `CheckWritable` stat-error = writable; `isoguard` `Protected` on failure | 2 |
| four progress mechanisms; `utmvm` writing to stdout; five byte formatters | 3 |
| six retry loops; `Ensure` meaning five things; no `context`; one sentinel error | 4 |
| **no timeout policy** — 19 duration literals in `utmvm`, 29 with `cmd/`, none carrying its reason | 4 |
| `doctor` installs UTM; unverified `.dmg`, no `codesign`/`spctl`; guest-tools ISO unchecked | 5 |
| download length/fsync/rename-clobber; `mkisofs` `-b`; `iso` scanning `.` | 6 |
| **`ExpandESD` appends** rather than replacing — re-running against a populated dir duplicates WIM images instead of failing | 6 |
| **`BuildISO` verifies nothing it produced** — stats the output and prints its size; no readback, no mount, no El Torito check | 6 |
| **`verified sha1 %s` printed unconditionally** — an empty hash prints "verified sha1 " having verified nothing | 6 |
| **six files know where the Windows media lives**; `Paths` exists to own that and does not | 6 |
| `ProtectISO` defeating its own hardlink; unprotect-before-delete | 6 + 7 |
| `Delete` by display name not UUID; proceeding after a failed stop | 7 |
| **`randomMAC` discards `rand.Read`'s error** — every VM then gets `52:54:00:00:00:00`, an L2 collision on the shared network | 7 |
| **`CanCreateVMs()` is bypassed** — `bundle.go:15` compares `runtime.GOOS` directly, against its own comment | 7 |
| **`utmctl delete` exits 0 on failure**; a phantom entry is unrecoverable through `utmctl` | 7 |
| **all six `Prune` defects**, including the `"irgo-"` catch-all and mis-counted `freed` | 7 |
| `BundlePath(name)`; bundle layout concatenated in the CLI four times | 7 |
| `RestartUTM` quitting with VMs running | 8 |
| **`WaitForAgentEvery` never probes when `timeout <= 0`** — the deadline is already past on entry, so it returns "did not respond" without one `AgentReady()` call | 8 |
| the five CLI defects — unreachable branch `main.go:331`, phantom `-o` default `:391`, `-replace` swallowing its error `:964`, stale `runISO` comment `:702`, garbled string `:935` | owning capability, as each command moves |
| `BootAssistWatched`'s dead `diskPath`; `nil` after five failures; no `AgentReady` gate | 9 |
| **`BootEntryWritten`** — the heuristic `setup` uses to choose recover vs reinstall | 9 |
| the keypress / loader / FAT contradictions, settled by experiment | 9 |
| **guest command injection** (`run.go:269`); `schtasks` failure invisible; guest litter | 10 |
| **`RunInGuest` reports success when the output pull fails** — `ExitCode: 0`, `err == nil`, so a suite that ran nothing looks like a suite that passed. The harness runs through this path | 10 |
| `setup` as a parallel implementation; `chflags` on a user-supplied `-iso` | 11 |
| `ProbeDir` → `StageDir`; glaze/native/crgimenes out of `utmvm`; `module nativeprobe`; two Go versions | 12 |
| twelve dead symbols; 134-symbol exported surface | 13 |
| README's false headline; CLI commands in prose; 20 mise wrappers; `vm:up` | 14 |
| `control.go:61`'s backwards fallback; doc comments on wrong declarations; `NewConfig` | owning capability |


### Phase 1 in detail — the harness

The whole plan is to run unattended, and exactly one thing
prevents that: four phases are verified by a person watching Windows install.
That is not a property of the work. It is the absence of assertions — and it is
why these bugs survived, because `BootAssistWatched` ignoring the disk it exists
to watch would have been caught the first time anything checked what it did.

Convert every "watch it" into a "assert it":

| verified by eyes today | assertion that replaces it |
|---|---|
| the install completed | `Inspect` reports `PhaseReady` within budget |
| the boot took | agent responds, **or** disk grew past the threshold |
| suspend/resume is fast | timed; fail over 2 s (it measures ~400 ms) |
| the probe ran | probes emit **JSON**; parse and assert per capability |
| Setup got no stray keys | *(white-box; arrives with phases 3 and 9)* |

Probes already report `OK`/`UNSUPPORTED`/`ERROR` per capability — they need a
machine-readable mode, not new logic.

**Safety interlocks, in code rather than prose.** Unattended plus destructive
plus a working VM the user cares about is the one combination that must not be
governed by a sentence in a document:

- **The disposable pattern is `irgo-test-*`.** Named here because the interlock
  is unimplementable without it, and because `-vm ""` is not a safe default:
  `setup.go:87` turns an empty name into **`irgo-win11`**. So the harness passes
  `-vm` explicitly, always, and refuses any invocation that would rely on the
  default.
- Refuse to *mutate* any VM whose name does not match `irgo-test-*`. `irgo-win11` is unreachable by construction, not by discipline —
  except for `suspend`/`resume`, which `RESULTS.md` measures against it and
  which change no persistent state. Reads are always allowed.
- Never clear `uchg` on media the harness did not protect, and never leave it
  cleared. Teardown *must* clear it briefly — the bundle holds a hardlink to the
  same inode, so removal is impossible otherwise — so the rule is
  unprotect-delete-**reprotect**, with the reprotect on the failure path too.
  "Never unprotect" would forbid the teardown the harness requires.
- A wall-clock budget, and **stop on red**. A failed phase leaves its branch
  unmerged and the run halted — never ploughs on, never retries blind.
- Resumable: a run that stopped at phase 9 resumes at phase 9.

#### What this repo puts on the OS, and what that means unattended

The interlocks above cover VMs and media. They are not most of the blast radius.
This tool installs software, holds four macOS permissions, clones other people's
repositories, rewrites the build graph, and leaves files inside the guest:

| what | where | unattended consequence |
|---|---|---|
| **UTM.app** | `/Applications` | installed by brew cask, or an **unverified `.dmg`** via `hdiutil -noverify` + `ditto`, which *merges* into an existing bundle rather than refusing |
| **brew formulae** | system-wide | `wimlib`, `cdrtools` — a system mutation mid-run, needing network |
| **guest tools ISO** | UTM's container | ~120 MB, returned from cache with **no integrity check of any kind** |
| **TCC permissions** | system | **Automation, Accessibility, Screen Recording, Xcode CLT** |
| **upstream clones** | `~/…/github.com/crgimenes` | git clones of someone else's repositories |
| **`go.work`** | repo root, **gitignored** | while it exists every build resolves glaze/native from local clones |
| **guest litter** | `C:\Users\Public` | `irgo-*.bat` + `schtasks` entries, every run, never cleaned |
| **UTM process** | running | `RestartUTM` **quits it unconditionally** |

Four of these break an unattended run outright:

**1. The TCC permissions cannot be granted by a script.** Sending keystrokes,
driving UTM over Apple Events and taking a screenshot are all consent dialogs.
On a fresh machine the run stops at a dialog nobody is there to see. Phase 1
must **probe each permission and refuse to start** — not assume, since the
failure mode is a hang, and a first-run grant is the one thing a person must do.

**Related and equally fatal: a cold boot needs an unlocked screen.**
`setup.go:172` records it — keystrokes route through UTM's display window, so
resume works with the Mac locked and a cold boot does not. An overnight run
needs the display awake and unlocked, and must say so rather than time out.

**2. `RestartUTM` quits UTM with other VMs running.** It discards the quit
error, waits 15 s, and relaunches regardless of the *"a VM is still running"*
modal that is exactly the case it should stop for. `setup` calls it
unconditionally after creating a bundle. **On this machine that would kill a
running `irgo-win11`.** Phase 1: refuse to restart UTM while any VM outside the
test pattern is started.

**3. Protecting the ISO silently defeats the hardlink, then blocks cleanup.**
Measured, not reasoned:

```
ln  → immutable inode   ⇒ EPERM   so linkOrCopy silently COPIES 5.27 GB
rm  → bundle holding an immutable hardlink ⇒ EPERM, directory left behind
```

So every VM created after protection costs ~35 GB rather than ~30, and any
bundle created *before* it cannot be deleted at all — `cleanup.go` never calls
`UnprotectISO`. With **49 GiB free** that is one test VM at a time, which is why
the run reuses one rather than accumulating them. Teardown must unprotect-delete-reprotect around
teardown, and assert free space against the *copy* cost before each trial.

**4. A run that dies between `upstream:link` and `unlink` poisons every later
build**, invisibly, because `go.work` is gitignored and absent from
`git status`. Phase 1 restores it on exit, including the failure path, and
asserts it is absent before merging anything.

**The harness tracks the CLI, capability by capability.** The harness is
black-box over the CLI, and each capability phase rewrites its own commands —
so the harness moves with it, in the same branch, as part of that phase's
definition of done. That is the advantage of slicing by capability: the CLI is
never replaced wholesale under a harness that cannot follow. Primitives are
removed only when another primitive does the same thing, so the surface the
harness asserts on is stable by rule, not by luck.

**Preconditions are numbers, not adjectives.** The harness refuses to start
unless it can state each one:

| precondition | threshold today |
|---|---|
| free space | **≥ 35 GB *when the test VM does not yet exist*; ≥ 8 GB once it does.** 49 GiB free today, and `irgo-win11` already accounts for 27 GB of what is used. A flat "≥ 45 GB" would refuse every phase after the test VM is built, deadlocking the run — the precondition is conditional for that reason |
| no other VM started | `irgo-win11` runs; `snap-test` is a **phantom** — UTM lists it, its bundle is gone, which is the failure `cleanup.go` warns of |
| TCC grants | probed, not assumed — the failure mode is a hang |
| `go.work` | absent, or restored on exit including the failure path |
| screen | unlocked and awake **only** for boot-driving phases |

**This is black-box only, deliberately** — end-to-end assertions over the real
CLI. The white-box checks (*was any keystroke sent while the agent was up?*)
need the phase 3 seam, which does not exist yet. Building phase 1 on it would be
circular; building it black-box now is what lets phases 2–4 run unattended
tonight.

**And it makes the expensive experiments affordable for the first time.** Phase
9 must settle three contradictions by experiment — how many keypresses the
prompt needs, which EFI loader boots, whether Setup reads the answer file from
FAT. Each costs a 45-minute install per trial, which is why nobody has run them
and why the code asserts both answers. A human will not sit through twelve
installs. A machine does not care.

#### Install Windows once, then use the commands you already have

The 45-minute install is the plan's real cost, and an early draft of this
section spent it twelve times in phase 9 and then proposed a new `clone`
primitive to avoid that. Both were wrong, and wrong in the way this whole
document is about: **reaching for new machinery instead of the commands that
already exist.**

`setup` is idempotent — re-running it against an existing VM skips every stage.
And the phases do not each need their own Windows. They need *an* installed
Windows that answers. So:

> **One test VM, created once, reused across all fifteen phases.**

That is the whole strategy. It requires no new code, and phase 11 is what makes
the idempotency it leans on trustworthy.

The work splits three ways, and only one part is expensive:

| what a phase needs | what it costs |
|---|---|
| bundle operations — `create`, `delete`, `config`, `prune`, `iso` | an empty bundle. **Seconds.** No Windows involved |
| guest operations — `run`, `exec`, `suspend`, `resume`, probes | **one** installed VM, reused. Paid once |
| install operations — phase 9's three experiments | real installs, most run **past** the boot signal |

Most of what looked like it needed cloning never needed Windows installed at
all. Phase 9's experiments are the exception, and are budgeted honestly: the
**loader** question is answered in about two minutes by disk growth or the
agent, but the **keypress** question is about surplus presses landing in Setup's
UI *after* boot succeeds, so those trials must run well past the boot signal.
Budget the loader trials in minutes and the keypress trials in installs.

**Between phases, suspend rather than reinstall.** Resume is ~400 ms and — per
`setup.go:172` — works with the Mac's screen locked, because it restores RAM and
never reaches firmware. Keeping the test VM suspended between phases means the
TCC and unlocked-screen preconditions bind only on the phases that genuinely
drive a boot, instead of on the whole run.

**UTM clones natively — and that is better than either option considered here.**
`utmctl clone <id> --name <new>` exists, and it regenerates the UUID and MAC
itself, which was the entire reason an earlier draft proposed writing a `clone`
primitive. There is nothing to write.

**But there are never two VMs.** Cloning per phase means N registry entries, N
UUIDs, N MACs, and an orphan from every phase that failed — and `Delete` is
broken today, so they accumulate, each needing a UTM restart to become visible.
That is a fleet to keep straight in exchange for insurance that one file can
provide.

> **Two bundles, ever: the test VM, and one snapshot of its known-good state.**

The snapshot is not a VM. It is a directory UTM has never heard of, so it has no
registry entry, no UUID, no MAC and no restart. Recovery is **restore in place**
— stop, replace the bundle contents, start — and because the bundle path and
identity never change, UTM does not need to re-enumerate and `newUUID` /
`randomMAC` are not involved at all.

That is why restore-in-place beats clone-per-phase on more than tidiness: it
deletes the entire identity problem rather than solving it.

`cp -c -R` makes it free, and this is measured, not assumed:

```
bundle holding 2 GB of real blocks
cp -c -R  →  0.003 s, 0 MiB consumed, clone reports 2.0G
```

A whole bundle clones recursively, copy-on-write, instantly and free. It costs
only what the clone later *writes*.

So the whole mechanism is two shell operations against a directory:

```sh
snapshot:  cp -c -R  $VM.utm  $SNAP        # after the install succeeds. 3 ms, 0 bytes
restore:   rm -rf $VM.utm && cp -c -R $SNAP $VM.utm   # only when a phase broke it
```

No new primitive, no new command, no identity rewrite, no UTM restart, and at
most two bundles on disk. `utmctl clone` is not used: it refuses on a running
VM, its copy-on-write behaviour is unmeasured so it may duplicate 27 GB, and its
only advantage — regenerating identity — is a problem restore-in-place does not
have.

**One snapshot, as insurance.** If a phase corrupts the test VM, the 45 minutes
is lost. The VMs are on APFS, so a copy-on-write snapshot of the known-good
bundle is effectively free — measured here, a 3 GB file clones in 0.003 s and
both copies together occupy 32 KB. Take it once, after the install, and restore
it only if a phase leaves the VM broken.

That is a backup, not a mechanism. It adds one `cp -c` to the harness, not a new
primitive and not a per-test lifecycle.

One full unattended install stays as the final gate, once, at the end.

#### Going backwards: which commands have an inverse

An unattended run needs to step back when a phase goes wrong, and the command
set only half supports it. Audited:

| forward | inverse |
|---|---|
| `create` | `delete` |
| **`start`** | **missing — `VM.Stop()` exists in `utmvm` and no CLI command exposes it** |
| `suspend` | `resume` |
| `iso -protect` | `iso -unprotect` |
| `boot` | **none possible** — the keystrokes are already in the guest |
| `install` | **none** — delete and recreate |
| `exec` / `run` | **none** — guest state has changed |
| `fetch-iso` / `build-iso` | **none** — overwrites its output |
| `prune` / `delete` | **none** — destructive by design |
| `status`, `list`, `doctor`, `targets`, `verify`, `screenshot` | pure; nothing to undo |

Three of the twenty reverse cleanly (`probe` is omitted below: phase 12 removes it from the CLI). One inverse is missing outright and is a
recovery-toolkit gap by the plan's own rule: **you can power a VM on and not
off**, in a CLI whose primitives exist precisely for standing at a failed stage
and poking it. `delete -force` already calls `Stop` internally, so the
capability exists and is simply not exposed. **Add `stop`** — it is not new
machinery, it is an unexported one.

The rest have no inverse, so they get `Clean` instead — each stage removes what
it created, and the retry loop is:

> **fix the code → `Clean` what the stage did → run it again**

That is cheaper and more precise than restoring a whole VM, and it is why
`Clean` joins `Check` and `Ensure` in the core pattern above rather than being
harness-only scaffolding.

**Snapshots are the fallback for what `Clean` cannot reach**: guest state after
an install, where the damage is inside Windows and no host-side cleanup
describes it. There the APFS measurement pays — 0.003 s and ~0 bytes — so the
harness snapshots before the handful of commands that dirty the guest
irrecoverably, and relies on `Clean` everywhere else.

Two axes of reversibility, and the plan needs both:

- **code** — a branch per phase, `--no-ff`, so any phase is one `git revert -m 1`
- **VM state** — a CoW snapshot before each irreversible command, so a bad
  `boot` or `run` costs seconds instead of 45 minutes

`prune` deserves its own note: it is irreversible, operates on the shared system
temp directory, and its prefix list contains `"irgo-"`, which matches every
other entry including files a concurrently running `pushScript` has open. Until
phase 7 fixes that, the harness must not call it with `os.TempDir()`.

#### Every assertion needs a negative control

The harness is the only thing standing between an unattended run and fifteen
phases merged green on nothing. So the harness itself has to be verified, and **this
repository is the argument for why**:

| check | what it actually does |
|---|---|
| `RunInGuest` | returns `ExitCode: 0`, `err == nil` when the output pull fails — *a suite that ran nothing looks like a suite that passed* |
| `Prune` | declares `error`, can never return non-nil |
| `BuildISO` | stats the output and prints its size. No readback, no mount, no El Torito check |
| `BootAssistOn` | `return nil` after five failed boot attempts |
| `catalog_test.go:121` | asserts two compile-time constants differ — cannot fail |
| every `if ok && bad` guard | does not fire when the question cannot be answered |

Six checks that pass whether or not the thing they check works. That is the
house style being corrected, and a harness written in it would be worse than no
harness, because it would carry authority.

**So: no assertion lands without a negative control — a deliberate break that
must turn it red.** Revert the fix, run the check, watch it fail, restore. An
assertion that stays green against its own mutation is decoration and is deleted
the day it is written.

```
assert: suspend/resume < 2 s        break: remove the poll interval     ⇒ must fail
assert: install reaches PhaseReady  break: skip the answer file         ⇒ must fail
assert: probe capability = OK       break: point at an unpatched glaze  ⇒ must fail
```

Two more controls belong to capabilities rather than to phase 1, because the
fix and its control must land together: *no keystrokes while the agent is up*
(phase 9) and *an ISO mastered with `-b` does not boot* (phase 6). The latter
finally exercises the `mkisofs` fallback, which has produced a non-bootable ISO
for its entire existence with nothing to say so.

The glaze control above is worth its cost on its own: it is a standing
regression test for the upstream fixes.

This is the same negative control that proved the glaze `New` hang was real
rather than assumed — the fix was stashed, the hang returned, the fix was
restored. It cost two minutes and it is the difference between a measurement and
a belief.

**1 — Harness, lint, and the gate.** Detailed above. Two blockers the cold
review found land here: **`mise run lint` does not exist** — no task, no
`.golangci.yml` — while every phase is gated on it, so it is created here or
nothing runs; and phase 1's negative controls must not reference fixes that
arrive later. The two that did (`-b`/`-e`, and the `AgentReady` gate) move to
phases 6 and 9, which is where both the fix and its control belong.

**CI is inverted here too**, because every phase's gate is a mise task and CI
must be running the same one. `check.yml` stops restating build steps and calls
`mise run check`, `mise run lint`, `mise run cross` — the last of which **does
not exist yet** and is created here alongside `lint`. The cross-compile matrix
moves out of the workflow into that portable task, so a maintainer can run
locally exactly what CI runs, and the `/tmp/ci-bin` versus `.bin/host/`
divergence disappears with the duplication that caused it.

**The probe cross-build is not moved here.** It exists in both mise and CI
today, and neither is its home: phase 12 gives it to System B, which owns its
own build. Phase 1 only stops CI restating it; phase 12 is what finally fixes
`irgo-winvm probe` being unbuildable for anyone who ran `go install`.

Keep the two recorded findings in `check.yml`'s comments — why the matrix exists
(a macOS-only syscall broke the Linux build invisibly) and why the catalog job
is `continue-on-error` — by moving them to the tasks, not deleting them.

**2 — Guards and the filesystem foundation.** Tri-state `inodeInfo`,
`fileFlags`, `diskUsage`, `statfsAvailable`: an explicit *unknown* callers must
handle, so `ok &&` cannot silently mean *allow*. `CheckWritable` refuses on
unknown. `hasDotDot` uses `filepath.Separator`. `sysfile_other.go`'s comment is
rewritten to match what its callers actually do.

**Watch CI:** `sysfile_other` returns unknown for everything, so "refuse on
unknown" makes every guarded write refuse on Linux — where `check.yml` runs
`go test ./utmvm/...`. The tests must assert the *refusal*, not skip it.

**3 — The seam.** One reporting mechanism instead of four; every `Printf` and
`os.Stderr` write leaves `utmvm`. Every `exec` — `utmctl`, **`osascript`**,
`hdiutil`, `plutil` — goes through a `runner`. `osascript` matters as much as
`utmctl`: phase 9's central assertion is *which keystrokes were sent*, and that
is unobservable otherwise. **This is what makes the package testable**, so it
precedes every capability.

**4 — The contract.** `Check`/`Ensure`/`Clean` defined once, with the verbs
given fixed meanings — `EnsureReady` becomes `BootInstalled`, which is what it
does and would not have been mistaken for `RunInstall`. Typed errors for the
states `setup` must act on. `context.Context` through the long operations. One
retry primitive replacing six loops. `VMState` replacing nine ad-hoc answers to
*is this VM usable*. A lockfile per bundle.

Nothing here changes behaviour; it defines the shape phases 5–11 implement.

**5–11 — the capabilities.** Each is one branch and closes the same checklist:

1. fix every defect the table above assigns to it;
2. implement its `Check` (pure), `Ensure` (acts), `Clean` (removes what `Ensure`
   created);
3. unit tests through the phase-3 seam, plus the harness assertions and their
   negative controls;
4. write its **final** CLI commands into `cmd_<capability>.go` and delete the
   corresponding block from `main.go`;
5. record any measurement it settles in `RESULTS.md`, dated.

**Done** = its defects closed, `Clean` provably removes what `Ensure` made
(assert a clean tree afterwards), its CLI commands final, harness green.

Notes where a capability is more than its defect list:

- **5 Host** — `doctor`, `status`, `targets` and `verify` become `Check`-only,
  fixing the live bug where a diagnostic installs UTM and downloads 120 MB.
- **6 Media** — `Clean` is the phase's own hardest test: a failed fetch must
  leave no `.part`, a failed build no partial ISO. Settle `-b`/`-e` by booting
  what the `mkisofs` fallback produces, which has never been done.
- **7 Bundle** — `Delete` resolves by UUID and unprotects before removing, which
  is what makes the harness's teardown possible at all. `Prune` gets all six
  fixes; until it does, the harness must not point it at `os.TempDir()`.
- **8 Control** — `stop` is added. `VM.Stop()` exists and no command exposes it,
  so today you can power a VM on and not off.
- **9 Boot** — the most severe defects, and the three experiments. Each trial
  needs a real install, but the keypress question is about *surplus* presses
  landing in Setup's UI **after** boot succeeds, so those trials must run
  **past** the boot signal. Aborting early measures the one thing never in
  doubt.
- **11 Setup** — rewritten to call only what the primitives call. 338 lines to
  roughly 40. The test asserts each stage resolves to its primitive's entry
  point.

**12 — The two systems, and the modules.** `ProbeDir` → `StageDir`; the glaze,
native and probe entries out of `external.go`; `IRGO_UPSTREAM_DIR` and the
hardcoded `crgimenes` default out of `paths.go`. The boundary test greps
`utmvm`'s own source — and must match **identifiers**, not prose, or it fires on
"native ARM64" and forces the comment edits *What not to do* forbids.

Also the module defects: `probe/go.mod` declares `module nativeprobe`,
unqualified unlike its siblings; four modules declare two Go versions. Settle
on one and record why.

**13 — Dead code and the exported surface.** **All deletions happen here**, and
that is the point: the earlier draft deleted `BuildFATImage` in phase 1 and then
needed it for phase 9's FAT experiment. By now every capability is finished, so
what is unused is genuinely unused.

Note `unused` does **not** report exported identifiers, and eleven of the twelve
are exported — the same is true of `unparam`. So this phase cannot lean on the
linter alone: it greps `cmd/`, System B and the harness for each symbol.
`CatalogURLWindows10`'s test goes with it.

**14 — Docs.** The README's opening claim is false and gets corrected, not
quietly kept. CLI commands come out of prose entirely — `-h` is generated and
cannot drift. The 20 mise wrappers go, including `vm:up`, which invokes a
command this plan deletes. `RESULTS.md` gains every measurement phases 6 and 9
settled.

**15 — Enforcement.** Below.

### If it stops early

**Phase 1 is not optional** — it is the gate, and two of its contents are live
blockers. **Phases 2 and 3 are the highest value per hour**: phase 2 is data
loss and needs no VM, phase 3 is what makes anything testable. **Phases 9 and 10
fix what is shipping today** — the boot driver that has already destroyed an
install, and the only security defect in the document.

Phases 12–14 are tidying by comparison; stopping before them costs nothing but
tidiness. Stopping mid-capability is safe by construction, because each owns its
own `Clean`.

## Phase 15 in detail — enforcement

A cleanup that is not enforced decays back. **Most of this mess was made by an
agent that did not read what already existed**, so prevention has to work on
that failure mode specifically: a convention nobody reads prevents nothing.

### A linter that catches each mistake actually made

Not a default preset — exactly the checks that would have caught this repo's own
history:

| linter | the bug it would have caught |
|---|---|
| `unused` | all 12 dead symbols, at the moment each lost its caller |
| `unparam` | **`BootAssistWatched`'s ignored `diskPath`** — a safety parameter that does nothing |
| `dupl` | 3 wait-for-agent loops, 3 batch-file blocks, 5 byte formatters |
| `goconst` | `win11-arm64.iso` ×5, `irgo-win11` ×3, `win11-arm64-built.iso` ×3 |
| `mnd` | the unexplained timeout literals — 19 in `utmvm`, 29 with `cmd/` |
| `funlen`, `gocognit` | `runUp` at 86 lines doing five things — deleted by phase 11, but the threshold stays |
| `errcheck`, `gosec` | already clean; keep them clean |
| `exhaustive` | `switch tool.Name` with no `default`, which would run `xorriso` with no arguments |
| `nilerr` | `Prune` returning `nil` after swallowing `ReadDir`; `BootAssistOn` returning `nil` after five failures |

`unparam` is the newly important one: it is the only check here that catches a
parameter which *names a safety property the body does not implement*, which is
how `diskPath` came to be documented, passed, and ignored.

Land a minimal config in **phase 1** (with the `mise run lint` task that does not
yet exist) so the refactor itself is gated while churn
is highest; tighten thresholds here once the code is final.

### Tests that encode the invariants

Properties invisible to the compiler, each of which has already broken:

```go
TestReportCommandsArePure       // no REPORT command reaches Ensure*/Fetch*/Install*
TestSetupStagesMatchPrimitives  // each setup stage resolves to its primitive's entry point
TestGuestFileNamesArePrunable   // generate via guestFile(), assert Prune claims it
TestOperationsAreIdempotent     // Download/BuildISO/Prune twice; second is a no-op
TestExportedSurfaceBudget       // count exported symbols; fail if it grows
TestGuardsRefuseWhenUnknown     // unanswerable inode/flags ⇒ refuse, on darwin and not
TestNoKeystrokesWhenAgentUp     // via the phase-3 seam: zero osascript calls
TestShortDownloadFails          // truncated body ⇒ error, and no file at dest
TestCleanUndoesEnsure           // per capability: Ensure, then Clean, then assert a clean tree
```

`TestExportedSurfaceBudget` is deliberate: 134 exported symbols accumulated
because nothing ever objected. A budget makes growth a decision someone takes.

### The check no linter can do

Three pairs of comments in this repo assert **opposite facts about the same
experiment**, and every one of them is a fact that cost hours to learn. No tool
can find these, because both halves are prose.

What makes them possible is that a finding is recorded in two places — a Go
comment *and* an asset, or two files that both explain the same discovery. The
rule that follows: **a hard-won fact gets one home, and other sites point at
it.** Where the fact is about an asset's content, its home is next to the asset.
`RESULTS.md` is the index, with dates.

Add to `AGENTS.md`: *when you correct a comment that records a measurement,
grep for the other copy — there is usually one, and leaving it is worse than
having neither, because the reader cannot tell which is current.*

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
mise run check && mise run lint && mise run cross
```

**Everything mutating runs against `$TESTVM`, never `irgo-win11`.** An earlier
draft of this section spelled these recipes out against `irgo-win11` — including
a bare `irgo-winvm setup`, which defaults to exactly that name (`setup.go:87`) —
three paragraphs after declaring it unreachable. `run` pushes a binary into the
guest and executes it; `iso -protect` was measured making every later bundle
undeletable. Neither belongs anywhere near the one installed VM on the machine.

**Idempotency — run it twice.** Phase 4's contract and each capability's `Clean`
make it checkable, and most needs no VM (`Prune` twice, `Download` twice to an
existing file, `BuildISO` twice against a fixture).

```sh
TESTVM=irgo-test-idem            # never empty: "" would resolve to irgo-win11
irgo-winvm vm -vm "$TESTVM" && irgo-winvm vm -vm "$TESTVM"     # 2nd: all skipped
```

**The VM-verified capabilities (6–11)** — a green build means "compiles", not
"works", and these paths fail silently. Phase 1 turns each of these into an
assertion with a negative control; until it exists they are run by hand:

```sh
irgo-winvm run -timeout 3m -vm irgo-test-probe .bin/nativeprobe-arm64.exe
irgo-winvm run -gui -timeout 4m -vm irgo-test-probe .bin/glaze-verifyevents-arm64.exe
irgo-winvm suspend -vm irgo-win11 && irgo-winvm resume -vm irgo-win11   # read-only
```

The last must still report **~400 ms**. Seconds means a poll interval was lost —
exactly how that bug happened the first time.

**Once, at the end — a fresh install.** Nothing above exercises it, phases 9
and 11 both touch it, and it is the only path whose failure costs 45 minutes to
observe. Everything before it runs against the one reused test VM, so this is
the single *fresh* install outside phase 9's experiments:

```sh
irgo-winvm nuke -vm irgo-test-main             # back to nothing first
irgo-winvm vm -vm irgo-test-main -install      # ~45 min, unattended
```

Never against `irgo-win11`.

---

## Git strategy

A branch per phase, merged only once verified. **CI cannot prove these phases
correct** — the VM-dependent paths have no unit coverage until phase 3, and
green means "compiles". Phase 1's harness is what closes that gap, which is why
it precedes every phase that changes behaviour.

```sh
git switch -c refactor/04-contract master
# … work, verify …
git switch master && git merge --no-ff refactor/04-contract
```

- **`--no-ff`**, so each phase is one revertable merge. A regression found three
  phases later is `git revert -m 1 <merge>`, not a hand-unpick.
- **One phase per branch**, so a bisect lands on a single concern.
- **Do not merge on CI alone** for phases marked *VM*. Put the verification
  output in the merge commit body — the only durable record it was run.
- **`pre-refactor` is tagged** at the last commit whose `RESULTS.md`
  measurements were actually taken.

### Should subagents execute this?

Partly — and the plan's own diagnosis sets the limit.

**The central finding of this document is that most of this mess was written by
an agent that did not read what already existed.** Fanning work out to several
agents, each holding a narrow brief and none holding the whole package, is a
faithful reproduction of the conditions that produced the mess. Parallelism is
not free here; the scarce resource is *knowing what already exists*, and that is
exactly what splitting the work destroys.

So the split is by **kind of work**, not by convenience:

| | use an agent | why |
|---|---|---|
| **read-only analysis** | **yes, heavily** | how phases 2, 9 and 12 were found at all — two agents reading 26 files line by line surfaced defects that four measurement passes missed |
| **review of a finished diff** | **yes, always** | an independent reader asking *who else performs this responsibility?* is the check that was missing every time |
| mechanical phases (2, 13, 14) | one at a time | narrow and CI-verifiable, but they overlap on `paths.go` and `run.go`, so concurrent worktrees would spend the saving on merge conflicts |
| **architectural phases (3, 4, 12)** | **no** | one coherent design across every file; a brief narrow enough to delegate is narrower than the problem |
| **VM-verified capabilities (6–11)** | **the agent writes, the harness verifies** | before phase 1 these needed a person watching an install; after it they are assertions with negative controls, which is the entire purpose of building the harness first |

**Do not run phases concurrently.** They are ordered by dependency, not by
taste: the foundation (1–4) before any capability, capabilities in dependency
order ending with `setup`, and every deletion after everything that might need
the code. The
table's *why here* column is the constraint, and the wall-clock saving on a
repo of 6,700 lines does not repay breaking it.

The pattern that does work, per phase:

1. **Read** — an agent reads the files the phase touches and reports what is
   there, including anything the plan got wrong. The probe question was answered twice
   because the first answer reasoned from the tangle instead of finding it.
2. **Write** — one agent, or the main session, with `AGENTS.md` and this phase's
   section in context. One concern, one branch.
3. **Review** — a *different* agent reads the diff cold against the phase's
   stated goal, and is told to look for a second implementation of something
   that already exists. This is the adversarial step, and it is the one that
   catches the failure mode this whole document is about.
4. **Verify** — phase 1's harness. Output into the merge commit.

Step 3 is the one to keep if any are dropped.

### The unattended loop

With phase 1 in place the whole plan runs without a person. Per phase, in order,
never concurrently:

```
branch  →  read  →  write  →  review  →  verify  →  merge --no-ff
                                  │          │
                              findings   red ⇒ STOP
                              feed back   (branch kept, run halts)
```

Four rules make walking away safe:

- **Stop on red, never retry blind.** A failed verify halts the run with the
  branch intact. The next session resumes at that phase. An unattended agent
  that "fixes" a failing verification it does not understand is how a refactor
  becomes a rewrite.
- **Review is blocking, not advisory.** With nobody reading the diffs, step 3 is
  the *only* thing standing where a human reviewer would. It gets a fresh agent,
  the phase's stated goal, `AGENTS.md`, and one instruction above all others:
  *find the second implementation of something that already exists.*
- **One phase per merge, `--no-ff`.** A wrong turn discovered six phases later
  is `git revert -m 1 <merge>`. This is what makes unattended architectural work
  recoverable rather than a bisect through a night's commits.
- **`RESULTS.md` numbers are gates, not decoration.** Suspend/resume drifting
  from ~400 ms to seconds fails the phase. That regression happened once
  already, from a lost poll interval, and only a number caught it.

- **The harness proves itself before it gates anything.** Phase 1 merges only
  when every assertion has failed against its own negative control at least
  once. A green harness that has never been red is an untested harness.

**All fifteen phases run unattended.** No *phase* needs a person once phase 1
exists — that is the whole point of building it first.

What needs a person is the **machine**, once: the TCC grants are consent dialogs
no script can produce, and a boot-driving phase needs the screen unlocked. That
is environment setup, not plan execution, and phase 1 refuses to start rather
than discovering it at 3 a.m.

The one act that stays outside the loop is **pushing the prepared glaze and
native fixes to crgimenes**, because it is outward-facing and irreversible: it
puts work under someone else's name in someone else's repository. That is not a
technical limitation and it is not part of these phases — it is a single
standing authorisation, given once, after which the ledger in `UPSTREAM.md` can
be worked automatically like everything else.

**One risk worth stating plainly.** Phases 3, 4 and 12 are design, not
mechanism, and an unattended agent that designs them wrongly gets the rest of
the plan built on the mistake. The mitigations are real but partial — blocking
review, one revertable merge per phase, stop-on-red, and a harness with negative
controls. If any part of this is worth reading afterwards, it is those three
merges.

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
