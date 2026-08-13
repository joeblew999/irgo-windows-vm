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

### This is two systems wearing one coat

The largest structural problem in the repo, and the one that made the phase 5
decision come out wrong the first time.

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

Phase 13 gives the separation that matters, and gives it *harder* than a split
would: a test that fails if `utmvm` names a probe, glaze, native or crgimenes. A
repo boundary is enforced by nothing.

**What would change the answer**, on evidence rather than taste:

- someone other than the maintainer depends on System A, so it needs its own
  release cadence and issue tracker;
- System A is published or announced as a standalone tool;
- the two genuinely stop co-evolving — System A stable for months while System B
  churns.

None hold today. If one does later, the split costs a `git mv` and a `go.mod`,
because phase 13 will already have removed every symbol that crosses.

**Two module defects to fix while there** (phase 13, since both are boundary
facts): `probe/go.mod` declares `module nativeprobe` — unqualified, unlike its
two siblings, so it cannot be referred to by path; and the four modules declare
three Go versions (`1.25.0` at root, `1.26.5` elsewhere), which makes CI's
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
| files with **no test at all** | **22 of 26** |

The untested list is every file that touches a VM, media or setup: `control`,
`run`, `install`, `setup`, `bundle`, `fetch`, `isobuild`, `isoguard`, `ensure`,
`paths`, `brew`, `cleanup`, `host`, `screenshot`, `diskspace`, `payload`,
`external`, `installutm`, `iso`, `fatimage`, and both `sysfile_*`.

Only `config`, `isoimage`, `catalog`, `assets`, `bootassist` and the prune
regression have any. This is the strongest argument for phase 6: **without the
`runner` seam, 22 files cannot be tested at all**, and every phase after it is
verified by hand against a 45-minute install.

### Four modules, three Go versions, no policy

`go.mod` declares `1.25.0` at the root and `1.26.5` in `probe`, `glaze-probes`
and `examples`. CI's `go-version-file: go.mod` therefore installs 1.25 and makes
Go download a second toolchain to build the other three.

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
written against it** (7–12 before 14), and **the phase that makes testing
possible comes before the phases that need testing** (6 before everything
risky).

The reading pass added three phases (4, 8, 9). Phase 4 moved early because it is
live data-loss and needs no VM. Phase 8 is the *most severe* defect found and
deliberately does **not** come first: proving that no keystroke is sent to a
running guest requires observing what was sent, which is precisely the `runner`
seam phase 6 installs. Fixing it before then means verifying by hand against a
45-minute install, which is how these bugs survived.

| # | phase | size | verify | why here |
|---|---|---|---|---|
| **0** | **The harness — make every check an assertion** | **L** | self | **nothing else can run unattended until this exists** |
| 1 | Lint baseline, then delete what it finds | S | CI | `unused` finds the dead code for you |
| 2 | One source of truth for facts | S | CI | fixes a live bug; no dependencies |
| 3 | One retry primitive | S | CI | no dependencies |
| 4 | **Guards become tri-state** | M | CI | **live data-loss; needs no VM, so it goes early** |
| 5 | Decide probe distribution | S | decision | blocks 14 |
| 6 | Reporting seam + `runner` interface | L | CI | **unlocks unit tests — everything after is testable** |
| 7 | `Check`/`Ensure` split, `VMState`, pure `doctor` | L | CI | **the keystone; fixes the live `doctor` bug** |
| 8 | **Boot driver correctness** | M | **VM** | **most severe defect** — but needs 6's seam to be provable |
| 9 | **Download and media integrity** | M | CI + **VM** | needs 6's seam; independent of 7 |
| 10 | Idempotency through `Ensure`; `setup` becomes thin | M | CI + twice-run | needs 7's `Ensure` to exist |
| 11 | `context.Context` through long operations | M | **VM** | signature change, after 7–10 settle |
| 12 | Verbs, typed errors, locking | M | CI | last API change before the CLI |
| 13 | **Separate the two systems** | M | CI | **decides what the CLI is**; after the API work, before 15 |
| 14 | Group `utmvm` by subject (`git mv`) | S | CI | move files only once content is final |
| 15 | Rewrite the CLI | L | **VM** | against the now-final API, written once |
| 16 | Shrink the exported surface | S | CI | needs the CLI's real usage to know what is unused |
| 17 | Tighten enforcement | M | CI | thresholds set from the finished code |

**0 — The harness.** The whole plan is to run unattended, and exactly one thing
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
| Setup got no stray keys | *(white-box; arrives with phases 6 and 8)* |

Probes already report `OK`/`UNSUPPORTED`/`ERROR` per capability — they need a
machine-readable mode, not new logic.

**Safety interlocks, in code rather than prose.** Unattended plus destructive
plus a working VM the user cares about is the one combination that must not be
governed by a sentence in a document:

- Refuse to operate on any VM whose name does not match the disposable test
  pattern. `irgo-win11` is unreachable by construction, not by discipline.
- Refuse to unprotect or overwrite the working ISO. The `uchg` flag already
  says so; the harness must not be the thing that clears it.
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
On a fresh machine the run stops at a dialog nobody is there to see. Phase 0
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
running `irgo-win11`.** Phase 0: refuse to restart UTM while any VM outside the
test pattern is started.

**3. Protecting the ISO silently defeats the hardlink, then blocks cleanup.**
Measured, not reasoned:

```
ln  → immutable inode   ⇒ EPERM   so linkOrCopy silently COPIES 5.27 GB
rm  → bundle holding an immutable hardlink ⇒ EPERM, directory left behind
```

So every VM created after protection costs ~35 GB rather than ~30, and any
bundle created *before* it cannot be deleted at all — `cleanup.go` never calls
`UnprotectISO`. With **49 GiB free** that is one test VM, while phase 8 wants
twelve sequential trials. Phase 0 must unprotect-delete-reprotect around
teardown, and assert free space against the *copy* cost before each trial.

**4. A run that dies between `upstream:link` and `unlink` poisons every later
build**, invisibly, because `go.work` is gitignored and absent from
`git status`. Phase 0 restores it on exit, including the failure path, and
asserts it is absent before merging anything.

**This is black-box only, deliberately** — end-to-end assertions over the real
CLI. The white-box checks (*was any keystroke sent while the agent was up?*)
need the phase 6 seam, which does not exist yet. Building phase 0 on it would be
circular; building it black-box now is what lets phases 1–5 run unattended
tonight.

**And it makes the expensive experiments affordable for the first time.** Phase
8 must settle three contradictions by experiment — how many keypresses the
prompt needs, which EFI loader boots, whether Setup reads the answer file from
FAT. Each costs a 45-minute install per trial, which is why nobody has run them
and why the code asserts both answers. A human will not sit through twelve
installs. A machine does not care.

#### Install Windows once, then use the commands you already have

The 45-minute install is the plan's real cost, and an early draft of this
section spent it twelve times in phase 8 and then proposed a new `clone`
primitive to avoid that. Both were wrong, and wrong in the way this whole
document is about: **reaching for new machinery instead of the commands that
already exist.**

`setup` is idempotent — re-running it against an existing VM skips every stage.
And the phases do not each need their own Windows. They need *an* installed
Windows that answers. So:

> **One test VM, created once, reused across all seventeen phases.**

That is the whole strategy. It requires no new code, and phase 10 is what makes
the idempotency it leans on trustworthy.

The work splits three ways, and only one part is expensive:

| what a phase needs | what it costs |
|---|---|
| bundle operations — `create`, `delete`, `config`, `prune`, `iso` | an empty bundle. **Seconds.** No Windows involved |
| guest operations — `run`, `exec`, `suspend`, `resume`, probes | **one** installed VM, reused. Paid once |
| install operations — phase 8's three experiments | real installs, **aborted at first signal** |

Most of what looked like it needed cloning never needed Windows installed at
all. And phase 8's experiments do not need the install to *finish*: boot success
is observable in about two minutes from disk growth or the agent, so each trial
stops once the answer is known. Twelve trials come in under an hour.

**Between phases, suspend rather than reinstall.** Resume is ~400 ms and — per
`setup.go:172` — works with the Mac's screen locked, because it restores RAM and
never reaches firmware. Keeping the test VM suspended between phases means the
TCC and unlocked-screen preconditions bind only on the phases that genuinely
drive a boot, instead of on the whole run.

**One snapshot, as insurance.** If a phase corrupts the test VM, the 45 minutes
is lost. The VMs are on APFS, so a copy-on-write snapshot of the known-good
bundle is effectively free — measured here, a 3 GB file clones in 0.003 s and
both copies together occupy 32 KB. Take it once, after the install, and restore
it only if a phase leaves the VM broken.

That is a backup, not a mechanism. It adds one `cp -c` to the harness, not a new
primitive and not a per-test lifecycle.

One full unattended install stays as the final gate, once, at the end.

#### Every assertion needs a negative control

The harness is the only thing standing between an unattended run and 17 phases
merged green on nothing. So the harness itself has to be verified, and **this
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
assert: no keys while agent up      break: drop the AgentReady gate     ⇒ must fail
assert: probe capability = OK       break: point at an unpatched glaze  ⇒ must fail
assert: ISO boots                   break: master it with -b not -e     ⇒ must fail
```

The last two are worth their cost on their own: the first is a standing
regression test for the upstream fixes, and the second finally exercises the
`mkisofs` fallback path, which has produced a non-bootable ISO for its entire
existence with nothing to say so.

This is the same negative control that proved the glaze `New` hang was real
rather than assumed — the fix was stashed, the hang returned, the fix was
restored. It cost two minutes and it is the difference between a measurement and
a belief.

**1 — Lint baseline, then delete.** Land `.golangci.yml` with only the checks
the code already passes, and turn on `unused` so it *identifies* the dead code
rather than a grep being trusted. Then delete the twelve symbols with no caller:
`BuildFATImage`, `copyIntoFS`, `GuestToolsInstallCommand` (still carries a
`start`-wildcard bug already fixed in the answer file), `OpenDisplay`,
`BootAssist`, `BootAssistWatched`, `IfaceVirtIO`,
`SchemaConfigurationVersion`, `EnsureISOTools`, `HumanSize`,
`CatalogURLWindows10`, `SuspendToDisk`.

Three carry a finding worth keeping before the code goes: `SuspendToDisk`
reports success while power-cutting the guest; `EnsureISOTools` exists to fail
fast on both ISO tools at once, and its absence is why you currently discover
the second is missing ten minutes into expanding a 4 GB archive — so phase 9
should call it rather than delete it; and `CatalogURLWindows10` is referenced
only by a test asserting two compile-time constants differ, which cannot fail.
Move each finding to `RESULTS.md` or the relevant comment first.

**2 — One source of truth for facts.** `guestFile()` shared by `run.go` and
`Prune`, fixing the live drift; named timeout constants carrying their reason;
ISO name and default VM name declared once in `Paths`.

**3 — One retry primitive.** Collapse the six loops.

**4 — Guards become tri-state.** `inodeInfo`, `fileFlags`, `diskUsage` and
`statfsAvailable` return an explicit *unknown* that callers must handle, so
`ok &&` cannot silently mean *allow*. `CheckWritable` refuses on unknown by
default; anything choosing otherwise says so at the call site. Fix
`hasDotDot` to use `filepath.Separator`, `isoguard.go`'s `Protected` to be
tri-state, and `bundle.go:80`'s vanishing space check. Rewrite
`sysfile_other.go`'s inverted comment to match what its callers do.

Testable with temp files, hardlinks and `chflags` — **no VM**, which is why it
precedes phase 6 despite being a real fix rather than a cleanup. Tests: a
hardlinked destination is refused; an immutable one is refused; an
*unanswerable* one is refused; and on a `!darwin` build `CheckWritable` still
refuses rather than degrading to the directory test.

**5 — Probe distribution. DECIDED: System B owns its own build; mise sequences
the two systems.**

The first version of this decision put `probe -build` in `irgo-winvm`. That was
wrong, and wrong in the way the section above describes: it accepted the premise
that probes are the VM tool's business. They are not. `irgo-winvm probe` and
`create -probes` are **System B inside System A's CLI** and both go.

So:

- **System B builds its own probes**, from its own source, with its own entry
  point. Not 13 lines of shell in `mise.toml`, which is what made
  `go install …@latest` produce a binary that could not build probes at all.
- **System A gains nothing.** It already has the whole feature:
  `create -stage <dir>` puts files on the payload medium and `run <exe>` runs
  one. Neither needs to know what a probe is.
- **mise sequences them** — build the probes, ensure the VM, run each, collect
  results. That is a maintainer workflow over two tools, which is exactly what
  mise is for.

> **mise may *sequence* the two systems. It may not *implement* either.**
>
> This is the sharpened form of the existing rule, and it is what the earlier
> failure was really about: the harm was not that probe-building lived in mise,
> it was that mise **implemented** it, so the capability existed nowhere else and
> `go install` users silently got less. Sequencing two CLIs breaks nothing,
> because each remains complete on its own.

Rejected, with reasons worth keeping:

- **`go:embed` into `irgo-winvm`** — adds ~22 MB (arm64 only; ~48 MB both
  arches) and commits `.exe` files that drift from their source. Doubly wrong
  now: it would embed System B's artefacts in System A's binary.
- **Download from a release** — right shape for System B once a remote exists.
  Blocked today: no remote, nothing pushed, no release.

Lands here: the decision, `-probes` renamed to `-stage`, and the mise task
rewritten to sequence rather than implement.

**6 — Reporting seam and `runner` interface.** One reporting mechanism instead
of four; every `fmt.Printf`/`os.Stderr` write moves out of `utmvm` into the CLI.
The `runner` seam lands here because both are about who may talk to the outside
world. **This is what makes the package testable without a VM**, which is why it
precedes everything risky: from here on, phases are verified by tests rather
than by hand.

Note the seam must cover `osascript` too, not just `utmctl` — phase 8's central
assertion is *which keystrokes were sent*, and that is unobservable otherwise.

**7 — `Check`/`Ensure`, `VMState`, pure `doctor`.** The keystone. Every
capability gets a pure `Check` and an acting `Ensure` built on it. `VMState`
replaces the nine ad-hoc answers to "is this VM usable". `doctor`, `status`,
`targets` and `verify` are rewired to `Check` only — fixing the live bug where
`doctor` installs UTM. Add the test that no REPORT command reaches an
`Ensure*`/`Fetch*`/`Install*`.

**8 — Boot driver correctness.** The most severe defect found, and the one that
has already destroyed an install. Delete `BootAssistWatched`'s dead `diskPath`
or make it real — do not keep a parameter that names a safety property it does
not have. The installed-Windows loop must not advance to the next candidate
while the guest may be booting, and must return an error when it exhausts them
instead of `nil`. `BootAndWait` gates on `AgentReady` before typing, as
`RunInstall` already does. `diskUsage`'s `ok` is honoured (phase 4 makes it
impossible to ignore).

Then **resolve the three contradictions by experiment, not by editing prose**:
how many keypresses the prompt actually needs, whether `cdboot_noprompt.efi` or
`bootaa64.efi` boots, and whether Setup reads `autounattend.xml` from FAT. Each
is currently asserted in two places with opposite answers, so one of each pair
is a lie and the code cannot say which. Record the answers in `RESULTS.md` with
a date and delete the losing claim.

**VM.** The unit tests assert what was sent through the phase-6 seam — *no
keystrokes when the agent is already up*, *an error when candidates are
exhausted* — but only a real install proves the keypress count.

**9 — Download and media integrity.** `Download` compares `done` to
`ContentLength` and fails on a short read; `f.Sync()` before close; the
destination guard re-checked immediately before `os.Rename`, not hours earlier;
`ContentLength == -1` handled rather than producing a negative total. Delete
416 as a dead end by removing the stale `.part`. Then the missing hashes:
publish or pin one for the UTM `.dmg` and the guest-tools ISO, or state in the
code why unverified is acceptable — and stop printing `verified sha1` when
nothing was verified. Fix `mkisofs`'s `-b` → `-e` and give the `switch` a
`default` that errors.

**CI + VM.** Truncation and rename-clobber are unit-testable against a local
server; the `-b`/`-e` fix is only proven by booting the ISO that fallback path
produces — which has, as far as the record shows, never been done.

**10 — Idempotency, and `setup` becomes thin.** `Ensure` semantics on `Create`,
`BuildISO`, `Download`; `ExpandESD` skipping images already exported. Then
`setup` is rewritten to call *only* what its primitives call — deleting
`ensureMedia`, using `BootAndWait` where `boot` does. 338 lines to roughly 40,
a list of steps and their outcomes. Add the test asserting each stage resolves
to the same entry point as its primitive.

**11 — `context.Context`.** `Download`, `ExpandESD`, `BuildISO`, `RunInstall`,
`BootAndWait`, `WaitForAgent*`, `Setup`. Ctrl-C should stop a 45-minute install
cleanly. *VM* — cancellation during a real install is the only honest test.

**12 — Verbs, typed errors, locking.** Give `Ensure`/`Fetch`/`Build`/`Run` fixed
meanings: `EnsureReady` becomes `BootInstalled`, which is what it does and would
not have been mistaken for `RunInstall`. Typed errors for the states `setup`
should act on. A lockfile per VM bundle. Route `bundle.go:15` through
`CanCreateVMs`. `Delete` resolves the bundle by UUID rather than rebuilding a
path from the display name, and `UnprotectISO`s before removing.

**13 — Separate the two systems.** `utmvm` stops naming anything from System B.
`ProbeDir` becomes `StageDir` on `Options`, `PayloadOptions` and `SetupOptions`
— the feature is *stage these files on the payload medium*, which is what it
always was. `external.go` loses the "probe binaries", "glaze clone" and "native
clone" entries; `paths.go` loses `IRGO_UPSTREAM_DIR` and the hardcoded
`github.com/crgimenes` default. Both move to System B, their only consumer.
`config.go:117`'s GPU finding stays but stops citing probes as the reason.

Then the enforcement, which is the point: **a test that fails if `utmvm`
mentions a probe, glaze, native or crgimenes.** Grep the package's own source at
test time. That boundary is invisible to the compiler and will not hold without
one.

Before the CLI (15) because it decides what the CLI *is* — one binary or two —
and after the API work (7–12) so the rename lands on final signatures.

**14 — Group `utmvm` by subject** with `git mv` so history follows: `media_*`,
`deps_*`, `vm_*`, `host_*`. **No sub-packages** — the parts are coupled and
splitting would force the exported surface to stay large, defeating phase 16.

**15 — Rewrite the CLI.** Written fresh against the now-final API, old file
deleted. Every primitive carried over unchanged; only `up` dropped. New shape:
`main.go` (dispatch only) plus `cmd_{setup,vm,boot,guest,media}.go`, with one
helper behind the 18 flagsets and 10 copies of *resolve-find-handle*, so an
unknown VM reads the same from every command — which it currently does not.

**16 — Shrink the exported surface.** Grep `cmd/` for each of the 134 exported
symbols; unexport what is absent. Expect 40–60 to go.

**17 — Tighten enforcement.** Below.

### If it stops early

Phases 1–4 are cheap, self-contained and fix live bugs — **phase 4 is data-loss
and needs no VM**, so it is the best value per hour in the plan. **Phase 6 is
the highest-value single phase**: without a `runner` seam nothing here can be
tested, and every later phase has to be verified by hand against a real VM.
**Phases 7 and 8 fix bugs that are shipping today**, 8 being the one that has
already destroyed an install. Phases 14–16 are cosmetic by comparison: stop
before them without loss.

---

## Phase 17 in detail — enforcement

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
| `mnd` | the unexplained timeout literals (now counted at 15+) |
| `funlen`, `gocognit` | `runUp` at 86 lines doing five things |
| `errcheck`, `gosec` | already clean; keep them clean |
| `exhaustive` | `switch tool.Name` with no `default`, which would run `xorriso` with no arguments |
| `nilerr` | `Prune` returning `nil` after swallowing `ReadDir`; `BootAssistOn` returning `nil` after five failures |

`unparam` is the newly important one: it is the only check here that catches a
parameter which *names a safety property the body does not implement*, which is
how `diskPath` came to be documented, passed, and ignored.

Land a minimal config in **phase 1** so the refactor itself is gated while churn
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
TestNoKeystrokesWhenAgentUp     // via the phase-6 seam: zero osascript calls
TestShortDownloadFails          // truncated body ⇒ error, and no file at dest
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
mise run check && mise run lint
for t in darwin/arm64 linux/amd64 windows/amd64 windows/arm64; do
  GOOS=${t%%/*} GOARCH=${t##*/} CGO_ENABLED=0 go build -o /dev/null ./...
done
```

**Idempotency — run it twice.** Phase 10 adds it as a test; most needs no VM
(`Prune` twice, `Download` twice to an existing file, `BuildISO` twice against a
fixture).

```sh
irgo-winvm setup && irgo-winvm setup       # second run: every stage skipped
irgo-winvm iso -protect && irgo-winvm iso -protect
```

**Phases 8, 9, 11 and 15 (*VM*)** — a green build means "compiles", not "works", and
these paths fail silently. Phase 0 turns each of these into an assertion with a
negative control; until it exists they are run by hand:

```sh
irgo-winvm run -timeout 3m -vm irgo-win11 .bin/nativeprobe-arm64.exe
irgo-winvm run -gui -timeout 4m -vm irgo-win11 .bin/glaze-verifyevents-arm64.exe
irgo-winvm suspend -vm irgo-win11 && irgo-winvm resume -vm irgo-win11
```

The last must still report **~400 ms**. Seconds means a poll interval was lost —
exactly how that bug happened the first time.

**Once, at the end — a fresh install.** Nothing above exercises it, phases 8,
9 and 10 all touch it, and it is the only path whose failure costs 45 minutes to
observe. Everything before it runs against clones of the golden image, so this
is the single full install in the entire run:

```sh
irgo-winvm setup -vm refactor-test -install     # ~45 min, unattended
irgo-winvm delete -vm refactor-test -force
```

Never against `irgo-win11`.

---

## Git strategy

A branch per phase, merged only once verified. **CI cannot prove these phases
correct** — the VM-dependent paths have no unit coverage until phase 6, and
green means "compiles". Phase 0's harness is what closes that gap, which is why
it precedes every phase that changes behaviour.

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
| **read-only analysis** | **yes, heavily** | how phases 4, 8 and 13 were found at all — two agents reading 26 files line by line surfaced defects that four measurement passes missed |
| **review of a finished diff** | **yes, always** | an independent reader asking *who else performs this responsibility?* is the check that was missing every time |
| mechanical phases (1, 2, 3, 16) | one at a time | narrow and CI-verifiable, but they overlap on `paths.go` and `run.go`, so concurrent worktrees would spend the saving on merge conflicts |
| **architectural phases (6, 7, 13)** | **no** | one coherent design across every file; a brief narrow enough to delegate is narrower than the problem |
| **VM-verified phases (8, 9, 11, 15)** | **no** | the verification is watching a real Windows install; an agent cannot see the screen |

**Do not run phases concurrently.** They are ordered by dependency, not by
taste: 6 before everything risky, the API before the CLI, 13 before 15. The
table's *why here* column is the constraint, and the wall-clock saving on a
repo of 6,700 lines does not repay breaking it.

The pattern that does work, per phase:

1. **Read** — an agent reads the files the phase touches and reports what is
   there, including anything the plan got wrong. Phase 5 was decided twice
   because the first answer reasoned from the tangle instead of finding it.
2. **Write** — one agent, or the main session, with `AGENTS.md` and this phase's
   section in context. One concern, one branch.
3. **Review** — a *different* agent reads the diff cold against the phase's
   stated goal, and is told to look for a second implementation of something
   that already exists. This is the adversarial step, and it is the one that
   catches the failure mode this whole document is about.
4. **Verify** — phase 0's harness. Output into the merge commit.

Step 3 is the one to keep if any are dropped.

### The unattended loop

With phase 0 in place the whole plan runs without a person. Per phase, in order,
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

- **The harness proves itself before it gates anything.** Phase 0 merges only
  when every assertion has failed against its own negative control at least
  once. A green harness that has never been red is an untested harness.

**All seventeen phases run unattended.** Nothing in the refactor needs a person
once phase 0 exists — that is the whole point of building it first.

The one act that stays outside the loop is **pushing the prepared glaze and
native fixes to crgimenes**, because it is outward-facing and irreversible: it
puts work under someone else's name in someone else's repository. That is not a
technical limitation and it is not part of these phases — it is a single
standing authorisation, given once, after which the ledger in `UPSTREAM.md` can
be worked automatically like everything else.

**One risk worth stating plainly.** Phases 6, 7 and 13 are design, not
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
