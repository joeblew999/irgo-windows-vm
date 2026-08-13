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
consolidating, not by testing.

**Nothing depends on this repository yet** — no published API, no other
consumer, no released binary anyone has installed. So this deletes rather than
deprecates, renames without aliases, and cuts the command surface instead of
preserving it. The only contract is what `RESULTS.md` records as measured.

This plan is organised by **the properties the code must have**, not by which
files to move. Moving files is the last and least interesting part.

---

---

## Why not start a new repository beside this one

It is the obvious question given how much is changing, and the answer is no —
except for one part, where it is yes. The deciding measurement:

| area | lines | comment lines | density |
|---|---|---|---|
| `cmd/irgo-winvm` | 1144 | 138 | **12%** |
| `utmvm` | 5579 | **1418** | **25%** |

**The value of this repository is not its code. It is roughly 1400 lines of
recorded findings**, each of which cost hours and none of which is recoverable
by reading the code they annotate: why the display must be `virtio-ramfb-gl`,
why ESD image 3 needs `--boot`, why the keystroke count is bounded at eight
(surplus presses reached Setup's UI and destroyed an install), why the answer
file must be a CD and not a FAT disk, why `%q` must not be re-escaped for
AppleScript, why `utmctl exec` output cannot be trusted.

A rewrite either loses those or copies them across — and copying them *is* the
refactor, minus the compiler checking that each one still sits beside the code
it explains.

Three further costs a new repo pays:

- **The evidence stops being true.** `RESULTS.md` records measurements — 400 ms
  resume, a self-built ISO installing unattended, every native capability on
  Windows ARM64 — taken against *these* binaries. A rewrite invalidates all of
  it until re-measured, and re-measuring costs 45-minute installs.
- **The tests encode traps, not behaviour.** `config_test.go` exists because UTM
  rejects a bad config with one generic "cannot import this VM" naming no field.
  Porting those is work with no gain.
- **Git history is provenance.** Which commit discovered which fact is currently
  answerable, and would not be.

And the refactor is mostly *mechanical* — move, rename, extract — where the
compiler and the existing tests carry you. The genuinely new work (`context`,
the `runner` seam, typed errors, one reporting interface) is additive and small.
A rewrite reaches "compiles and looks nicer" quickly and "actually boots Windows
unattended" slowly, because the hard part was never the structure.

**The exception, and it is worth taking:** `cmd/irgo-winvm` is 1144 lines at 12%
density — flag plumbing with almost nothing learned embedded in it. Phase 5
should therefore be **written fresh against the new `utmvm` API and the old file
deleted**, rather than carefully split. Untangling boilerplate is slower than
replacing it, and there is nothing there to lose.

So: refactor `utmvm` in place, rewrite the CLI, keep the repo.

---

## The properties, and where they are violated today

### 1. Idempotency is a bolted-on layer, not a property of operations

Every expensive operation here is one you will re-run: a 4.2 GB download, a
45-minute install, a VM that reboots itself mid-session. So re-running must be
safe and cheap. Today exactly **one** function has that property — `Setup` — and
it gets it by wrapping primitives that actively refuse:

| operation | re-run today |
|---|---|
| `Create` | **fails** — `"already exists; remove it or choose another name"` (`bundle.go:86`) |
| `BuildISO` | **fails** — `"already exists — this never overwrites"` (`isobuild.go:255`) |
| `Download` | **fails** if the destination exists (`fetch.go:159`); resumes only via `.part` |
| `boot` | **unsafe** — re-driving a live Setup sends keystrokes into its UI, which has destroyed an install |
| `Setup` | idempotent, by doing everyone else's state checks itself |

That is backwards. `Setup` is 338 lines largely because it re-implements
"is this already done?" for four subsystems that should each answer for
themselves.

**Target:** every operation takes `ensure` semantics — already done is success,
not an error — and reports which it was. The refusals that exist for *safety*
(`CheckWritable` on immutable or hardlinked media) stay; they are a different
thing from refusing to repeat work.

```go
// The shape every operation converges on.
type Outcome struct {
    Done    bool   // work was performed
    Detail  string // what, or why it was skipped
}
func EnsureX(...) (Outcome, error)
```

`Setup`'s stage reporting already has exactly this shape — lift it out of
`setup.go` and make it the package's vocabulary.

### 2. DRY of *facts*, not just of code

Duplicate function bodies are gone — a machine scan returns nothing. What is
duplicated now is knowledge, which is worse, because the copies drift silently.

**The live bug:** guest temp files are named in `run.go` by string concatenation
(`irgo-i-`, `irgo-io-`, `irgo-ir-`, `irgo-l-`), and `cleanup.go:121-125`
separately lists those prefixes so `Prune` can find them. Two sources of truth
for one naming scheme. Add a prefix and `Prune` silently stops cleaning it —
with no test that would notice.

| fact | copies | where |
|---|---|---|
| guest temp-file naming | **2 schemes** | `run.go` builds them, `cleanup.go` lists them |
| `win11-arm64.iso` | 5 | `paths.go:83`, `external.go:90,99`, `setup.go:257`, comments |
| default VM name `irgo-win11` | 3 | `setup.go:87`, `mise.toml`, `external.go:99` |
| timeouts | **8 distinct literals** | `10m×3, 45m×2, 2m×2, 60m, 15m, 5m, 10s` — no policy anywhere |

**Target:** one declaration per fact. A `guestFile(kind, stamp)` constructor that
`Prune` also consumes, so the two cannot disagree. Named timeout constants with
the reason attached (`agentPoll`, `installLimit`, `bootStall`), because
`45*time.Minute` at a call site tells you nothing about why 45.

### 3. One retry primitive

Six hand-written poll/retry loops: `bootassist.go:110,196`, `control.go:202,269`,
`install.go:136`, `cleanup.go:93`. Each picks its own interval and its own idea
of what a timeout means. This is where the 10-second-poll bug lived.

**Target:** one `retry(limit, interval, fn)` — or `WaitForAgentEvery`'s shape
generalised — used by all six. The interval becomes a decision made once, with a
reason, rather than a literal typed six times.

### 4. Atomicity and resumability

Partly right already, and worth keeping deliberately rather than by accident:

- `Download` writes `.part` and renames — **good**, keep. It also keeps the file
  on a hash mismatch rather than deleting 4 GB — **good**.
- `BuildISO` removes a partial ISO on failure — **good**: a half-written image is
  the right size to look plausible and fails later as an unbootable VM.
- `ExpandESD` has **no** resume: a failure at image 6 of 8 redoes all six.
- `Delete` removes files 30 s after asking QEMU to stop, whether or not it did.

**Target:** state the guarantee for every operation that writes, and make
`ExpandESD` skip images already exported.

### 5. One seam per external thing

22 `exec.Command` sites across 8 files. `osascript` is driven from both
`bootassist.go` and `control.go` under different escaping assumptions — and the
trap table records that `%q` double-escaping made every Go-driven boot silently
fail. Two implementations is how that bug returns.

**Target:** `utmctl(...)`, `osascriptRun(...)` — each the only place that knows
how to quote for its tool. `brew` and the ISO tools are already consolidated.

### 6. The CLI and `mise.toml` are two interfaces to one thing, with no rule

There is no source of truth today, and it has already drifted. Measured:

| | count |
|---|---|
| mise tasks that are **thin wrappers** over a CLI command | 20 |
| mise tasks containing **real shell logic** | 7 (`probes` 13 lines, `upstream:link` 24, `vm:iso-test` 10, …) |
| already broken | `vm:up` (line 227) calls `irgo-winvm up`, which Phase 4 deletes |

The thin wrappers are drift surface: 20 places that restate a command's name and
flags, each free to fall behind.

Worse, the logic tasks hide a **product gap**. `probes` is 13 lines that
cross-compile every Windows probe binary — and it exists *only* in mise. A
developer who runs `go install github.com/…/irgo-winvm@latest`, exactly as the
README tells them to, **cannot build probes at all**. The CLI has `probe` (run
them in the VM) and no way to produce them. The task runner has been quietly
carrying product functionality, so the binary is incomplete and nothing said so.

**The rule:**

> **`mise` is a maintainer tool for this repository. Nothing else.**
>
> A person using the binary never installs mise. A developer working on a
> project that *uses* this tool never installs mise. Only someone changing this
> repository does — to build, test, lint, and work on the upstream clones.
>
> Everything else is a CLI subcommand, because the binary is the product.

This deletes most of the file. Of 30 tasks, **~20 are wrappers that exist for
people who already have the binary** — `mise run vm:status` over
`irgo-winvm status -vm irgo-win11` buys a default VM name and costs a drift
surface. They go.

| task | becomes |
|---|---|
| `probes` (13 lines) | **`irgo-winvm probes build -o <dir>`** — product functionality that was hiding in the task runner |
| `vm:iso-test` (10 lines) | deleted — it is `setup` with a throwaway name |
| `vm:*`, `iso:*`, `setup*`, `doctor`, `example*` | deleted — all are CLI commands already |
| `check`, `fuzz`, `lint`, `upstream:*` | **kept** — these are maintainer work and belong nowhere else |

`mise.toml` ends up around eight tasks. The `IRGO_*` env block stays, since the
Go code reads those variables directly and a maintainer wants them defaulted;
a user sets them or accepts the defaults without mise existing at all.

The README must change with it — and further: **it should not list CLI commands
at all.** Every command and flag written into prose is a copy that will fall
behind the code, exactly as `mise run vm:up` already has. The binary's own
`-h` output is generated from the flag definitions and cannot drift.

So the README explains *why the project exists*, the concepts a newcomer needs
(the ISO is hardlinked; every boot needs driving; UTM rescans only at launch),
and the one command to discover the rest:

```sh
irgo-winvm -h
```

Worked examples that must stay accurate — the `setup` flow, the suspend/resume
timing — belong in `RESULTS.md`, where they are recorded as *measurements with a
date* rather than as instructions. A measurement that goes stale is a fact about
the past; an instruction that goes stale is a lie.

**Enforced**, because a convention this specific will not survive on trust: a
test parses `mise.toml` and fails if a task name matches the CLI's subcommand
list, or if a task outside the maintainer allowlist exists at all. Drift becomes
a red build rather than a discovery six weeks later.

### 7. Architectural gaps the duplication was hiding

Deduplication is the shallow layer. These are structural, and several explain
*why* the bugs happened rather than merely recording that they did.

**No `context.Context` anywhere — zero occurrences.** Operations here run for 45
minutes (install), 4.2 GB (download), and poll for minutes (boot). None can be
cancelled, none carries a deadline, and Ctrl-C leaves a half-written ISO or a VM
mid-boot. For a tool whose unit of work is an hour, this is the largest missing
piece. Every long-running exported function should take a `ctx` and honour it.

**Nothing VM-related is unit-testable, and that is structural.** 22
`exec.Command` calls are made directly against `utmctl`, `osascript`, `plutil`,
`hdiutil`, `ditto`, `bsdtar`, `wimlib-imagex` and `xorriso`. There is no seam, so
the only way to exercise any of it is a real VM — which is why coverage is thin.
It is not laziness. One narrow interface (`type runner interface { run(ctx,
name string, args ...string) (string, error) }`) makes most of the package
testable with a fake, and Phase 5's consolidation is the natural moment to
introduce it.

**Errors are strings, so callers cannot branch.** One sentinel exists
(`ErrUTMNotInstalled`). Everything else is `fmt.Errorf`, so nothing can
distinguish *VM not found* from *agent not ready* from *UTM not running* from
*out of space*. `Setup` therefore cannot decide anything on failure — it aborts.
Typed errors for the handful of states that matter would let `setup` recover
rather than stop.

**Four progress mechanisms, and a library that prints.** `progress func(done,
total int64)`, `progress func(step string)`, `Log io.Writer`, `log
func(string)`, plus direct `fmt.Printf` and `os.Stderr` writes *inside* `utmvm`.
A library writing to stdout cannot be used by anything but this CLI, and rules
out machine-readable output for CI. One reporting interface; the CLI decides the
format.

**`Ensure*` means five different things, and that caused a real bug.**
`EnsureUTM` installs an app, `EnsureGuestTools` downloads a file, `EnsureReady`
boots a VM and waits, `EnsureWork` makes a directory and checks free space,
`EnsureISOTools` installs two binaries. `setup` called `EnsureReady` where it
needed `RunInstall` precisely because the name promises "make it ready" — which
is what the caller wanted, and not what it does. Verbs need fixed meanings:
`Ensure` = idempotent make-it-so, `Fetch` = network, `Build` = produce an
artefact, `Run` = drive something to completion.

**No concurrency safety at all — zero locks.** Two `irgo-winvm` processes can
create, boot or delete the same VM simultaneously. `setup` is idempotent but not
re-entrant, and its checks are classic time-of-check-to-time-of-use. A lockfile
per VM bundle is cheap and honest.

**Config precedence is implicit, undocumented and untested.** `IRGO_*`
environment variables, flags and defaults interact with no stated order — and
`mise.toml` *sets* those variables, so the same command behaves differently
under `mise run` than run directly. That is a footgun the maintainer-only rule
above reduces but does not remove: precedence needs stating and a test.

**The platform guard is applied inconsistently.** `CanCreateVMs()` exists and
its own comment says callers should prefer it over comparing `runtime.GOOS` —
and `bundle.go:15` compares `runtime.GOOS` anyway.

---

---

## Phases

Ordered so every `utmvm` API change lands before the CLI is rewritten against
it. Sizes are rough; *VM* marks phases that cannot be verified by CI.

| # | phase | size | verify | why here |
|---|---|---|---|---|
| 1 | Lint baseline, then delete what it finds | S | CI | `unused` finds the dead code for you |
| 2 | One source of truth for facts | S | CI | fixes a live bug; no dependencies |
| 3 | One retry primitive | S | CI | no dependencies |
| 4 | Decide probe distribution | S | decision | blocks 10 |
| 5 | Reporting seam + `runner` interface | L | CI | **unlocks unit tests — everything after is testable** |
| 6 | Idempotency contract | M | CI + twice-run test | needs 5's tests to be safe |
| 7 | `context.Context` through long operations | M | **VM** | signature change, after 5 and 6 settle |
| 8 | Verbs, typed errors, locking | M | CI | last API change before the CLI is written |
| 9 | Group `utmvm` by subject (`git mv`) | S | CI | move files only once content is final |
| 10 | Rewrite the CLI | L | **VM** | against the now-final API, written once |
| 11 | Shrink the exported surface | S | CI | needs the CLI's real usage to know what is unused |
| 12 | Tighten enforcement | M | CI | thresholds set from the finished code |

The rule the order follows: **every `utmvm` API change lands before the CLI is
written against it** (5–8 before 10), and **the phase that makes testing
possible comes before the phases that need testing** (5 before 6–8).

**1 — Lint baseline, then delete what it finds.** Land `.golangci.yml` with only
the checks the code already passes, and turn on `unused` — which identifies the
dead code rather than trusting a grep. Then delete it: Eight symbols with no caller: `BuildFATImage`,
`GuestToolsInstallCommand` (still carries a `start`-wildcard bug already fixed
in the answer file), `OpenDisplay`, `BootAssist`, `IfaceVirtIO`,
`SchemaConfigurationVersion`, `EnsureISOTools`, `SuspendToDisk`. The last is a
footgun that reports success while power-cutting the guest; its finding stays in
`RESULTS.md` and the trap table, which is where it is useful.

**2 — One source of truth for facts.** `guestFile()` shared by `run.go` and
`Prune`, fixing the live drift bug; named timeout constants carrying their
reason; ISO name and default VM name declared once in `paths.go`.

**3 — One retry primitive.** Collapse the six hand-written loops.

**4 — Decide probe distribution.** Embed, download, or maintainer-only. Blocks
phase 10, because it decides whether `probes` is a CLI command at all. A
decision, not code — do it early so phase 10 is not blocked by it.

**5 — Reporting seam and `runner` interface.** One reporting mechanism instead
of four; every `fmt.Printf`/`os.Stderr` write moves out of `utmvm` into the CLI.
The `runner` seam lands here because both are about who may talk to the outside
world. **This is the phase that makes the package testable without a VM**, which is
why it comes before idempotency, `context` and the renames rather than after:
every phase from here on can be verified by a test instead of by hand.

**6 — Idempotency contract.** `Ensure` semantics on `Create`, `BuildISO`,
`Download`; the `Outcome` type lifted out of `setup.go`; `ExpandESD` skipping
images already exported. `Setup` shrinks as its subsystems answer for
themselves.

**7 — `context.Context`.** `Download`, `ExpandESD`, `BuildISO`, `RunInstall`,
`EnsureReady`, `WaitForAgent*`, `Setup`. Ctrl-C should stop a 45-minute install
cleanly instead of leaving partial state. *VM* — cancellation during a real
install is the only honest test.

**8 — Verbs, typed errors, locking.** Fix `Ensure`'s five meanings:
`EnsureReady` → `BootInstalled`, which is what it does and would not have been
mistaken for `RunInstall`. Typed errors for the states `setup` should act on. A
lockfile per VM bundle. Route the last `runtime.GOOS` through `CanCreateVMs`.

**9 — Group `utmvm` by subject** with `git mv`: `media_*`, `deps_*`, `vm_*`,
`host_*`. **No sub-packages** — the parts are coupled and splitting would force
the exported surface to stay large, defeating phase 11.

**10 — Rewrite the CLI.** Written fresh against the now-final `utmvm` API, old
file deleted; `up` and `start` simply not carried over. 1144 lines at 12%
comment density is flag plumbing with nothing learned in it. New shape:
`main.go` (dispatch only) plus `cmd_{setup,vm,boot,guest,media}.go`, with one
helper behind the 18 flagsets and 10 copies of *resolve-find-handle*. *VM*.

**11 — Shrink the exported surface.** Grep `cmd/` for each of the 134 exported
symbols; unexport what is absent. Expect 40–60 to go.

**12 — Enforcement.** See below.

### If it stops early

Phases 1–3 are cheap, self-contained and fix a live bug; they are worth doing
even if nothing else happens. **Phase 5 is the highest-value single phase** —
without a `runner` seam this codebase cannot be tested at all, and every later
phase has to be verified by hand against a real VM. Phases 9–11 are cosmetic by
comparison: stop before them without loss.

### Phase 12 in detail — enforcement

A cleanup that is not enforced decays back. This phase is the reason the others
are worth doing.

**Most of this mess was made by an AI agent that did not read what already
existed** — three wait-for-agent loops next to a `WaitForAgent` method, a fourth
byte formatter, `EnsureReady` where `RunInstall` was needed. Prevention has to
work on that failure mode specifically: a convention nobody reads prevents
nothing, so it goes in the file agents load automatically.

### 12a. A linter that catches each mistake actually made

`golangci-lint` is already available through mise. Enable exactly the checks
that would have caught the bugs in this repo's history — not a default preset:

| linter | the bug it would have caught |
|---|---|
| `unused` | all 8 dead symbols in phase 1, at the moment each lost its caller |
| `dupl` | the 3 wait-for-agent loops, the 3 batch-file blocks, the 4 byte formatters |
| `goconst` | `win11-arm64.iso` ×5, `irgo-win11` ×3 |
| `mnd` | the 8 unexplained timeout literals |
| `funlen`, `gocognit` | `runUp` at 86 lines doing five things |
| `errcheck`, `gosec` | already clean; keep them clean |

Land a **minimal config in phase 1** with only the checks the code already
passes, so the refactor itself is gated while churn is highest. Tighten the
thresholds in phase 12 once the code is final — saving all of it for the end
leaves the noisiest period ungoverned.

### 12b. Wire it into the gate, not just the docs

`.golangci.yml` at the root; `mise run check` gains a `lint` step; the existing
`.github/workflows/check.yml` gains the same. A rule that only lives in prose is
a rule that gets skipped at 2am.

### 12c. Tests that encode the invariants

Three properties in this codebase are invisible to the compiler and have each
already broken once:

```go
TestGuestFileNamesArePrunable  // generate via guestFile(), assert Prune claims it
TestOperationsAreIdempotent    // Download/BuildISO/Prune twice; second is a no-op
TestExportedSurfaceBudget      // count exported symbols; fail if it grows
```

The last is unusual and deliberate: 134 exported symbols accumulated because
nothing ever objected. A budget makes growth a decision someone takes rather
than a thing that happens.

### 12d. `AGENTS.md` — the file that gets read

Rules for contributors, human and AI, in the location agents load without being
asked. `CLAUDE.md` points at it so Claude Code picks it up too. It states the
things this repo has learned the hard way:

- **Search before you write.** `WaitForAgent`, `HumanBytes`, `Tool.resolve`,
  `pushScript` all exist because someone eventually noticed the third copy.
- **The comments are findings, not decoration.** Do not compress them.
- **Idempotency is a contract**, not a feature of `setup`.
- **Verify against the VM**, not the compiler, for anything touching boot, run
  or media — those paths have no unit coverage and fail silently.
- **One fact, one declaration.**

---

## Verification

**Every phase:**

```sh
mise run check && mise run lint
for t in darwin/arm64 linux/amd64 windows/amd64 windows/arm64; do
  GOOS=${t%%/*} GOARCH=${t##*/} CGO_ENABLED=0 go build -o /dev/null ./...
done
```

**Idempotency — run it twice.** The check this repo does not have; phase 6 adds
it as a test (phase 6). Most of it needs no VM: `Prune` twice, `Download` twice to an
existing file, `BuildISO` twice against a fixture.

```sh
irgo-winvm setup && irgo-winvm setup       # second run: every stage skipped
irgo-winvm iso -protect && irgo-winvm iso -protect
```

**Phases 7 and 10 (*VM*)** — a green build means "compiles", not "works", and
these paths fail silently:

```sh
irgo-winvm run -timeout 3m -vm irgo-win11 .bin/nativeprobe-arm64.exe
irgo-winvm run -gui -timeout 4m -vm irgo-win11 .bin/glaze-verifyevents-arm64.exe
irgo-winvm suspend -vm irgo-win11 && irgo-winvm resume -vm irgo-win11
```

The last must still report **~400 ms**. Seconds means a poll interval was lost —
exactly how that bug happened the first time.

**Once, at the end — a fresh install.** Nothing above exercises it, phases 6 and
7 both touch it, and it is the only path whose failure costs 45 minutes to
observe:

```sh
irgo-winvm setup -vm refactor-test -install     # ~45 min, unattended
irgo-winvm delete -vm refactor-test -force
```

Never against `irgo-win11`.

**Rollback.** Each phase is one `--no-ff` merge, so a regression found late is
`git revert -m 1 <merge>` — one concern, not a hand-unpick. `pre-refactor` tags
the last commit whose `RESULTS.md` measurements were actually taken.

---

## Git strategy

Each phase is a branch off `master`, merged only once verified. The reason is
not ceremony: **CI cannot prove these phases correct.** Phases touching boot,
run, media or setup have no unit coverage, so a green build means "compiles",
not "works" — and the failures are silent.

```sh
git switch -c refactor/01-facts master
# … work, verify …
git switch master && git merge --no-ff refactor/01-facts
```

- **`--no-ff`**, so each phase is one revertable merge commit. When a VM-only
  regression surfaces three phases later, `git revert -m 1 <merge>` takes back
  exactly one phase.
- **One phase per branch**, never two. The point of the sequence is that a
  bisect lands on a single concern.
- **Do not merge on CI alone** for phases marked *VM* below. Run the checks in
  Verification first and put the output in the merge commit body — that is the
  only durable record that it was actually run.
- **Tag `pre-refactor` at `master` now.** A single known-good point that predates
  everything, since `RESULTS.md`'s measurements were taken there.

---

## What not to do

- **Do not "tidy" the comments.** They record findings that cost hours and are
  not recoverable from the code: why the display is `virtio-ramfb-gl`, why image
  3 needs `--boot`, why `--save-state` must never be called.
- **Do not make `boot` idempotent by making it re-drive.** Re-sending keystrokes
  into a live Setup has destroyed an install. Idempotent here means *detecting
  that it need not act*, not repeating the action.
- **Do not touch `assets/`, the answer file, or the plist template.** UTM rejects
  a bad config with one generic "cannot import this VM" naming no field;
  `config_test.go` exists because that failure is undebuggable.
- **Do not do this in one commit.**

---

---

## Appendix: this plan's own flaws, and how they were fixed

Written down because the plan was assembled incrementally and inherited exactly
the failure it describes.

**The ordering was wrong.** Phase 6b renamed the `utmvm` API *after* Phase 5
rewrote the CLI against it — writing the CLI twice. **Every `utmvm` API change
now lands before the CLI is touched.**

**A whole phase was wasted work.** "Cut `up` and `start`" was a separate phase
*before* the CLI rewrite. If the CLI is being written fresh, you simply do not
write them. Folded in.

**The numbering (0,1,2,3,4,4b,4c,5,6,6b,7,8) was itself the evidence** — the
`b`/`c` suffixes are where things were bolted on instead of re-planned.
Renumbered.

**`probes build` in the CLI is probably wrong, and it exposes a deeper hole.**
A `go install`ed binary has no `probe/`, `glaze-probes/` or `examples/` source
tree, so it cannot compile them — the command cannot exist there. Worse, the
whole probe story is already incoherent: `create -probes <dir>` bakes binaries
into the payload CD, and an installed binary has no way to obtain those
binaries. So `irgo-winvm probe` is broken for anyone who did not clone.

That is a **distribution decision, not a placement one**, and it must be made
before Phase 4:

- **embed** the cross-compiled probes with `go:embed` — a self-contained binary,
  at the cost of ~20 MB and a release process that cross-compiles first;
- **download** them from a GitHub release, like the guest tools already are;
- **accept** that probes are maintainer-only, and say so — then mise *is* their
  correct home and the "product gap" identified above is not one.

Until this is decided, `probes` stays in mise and the README stops implying
otherwise.
