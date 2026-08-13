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

---

## Phases

Each is one commit, independently verifiable. Ordered so the properties land
before the file moves, and the VM-dependent work comes last.

**Phase 0 — delete.** Eight symbols verified to have no caller: `BuildFATImage`,
`GuestToolsInstallCommand` (still carries a `start`-wildcard bug already fixed in
the answer file), `OpenDisplay`, `BootAssist`, `IfaceVirtIO`,
`SchemaConfigurationVersion`, `EnsureISOTools`, `SuspendToDisk`. The last is
kept today only so its finding is checkable — and the finding is that calling it
can silently power-cut the guest. Do not ship a footgun; the measurement lives in
`RESULTS.md`.

**Phase 1 — one source of truth for facts.** `guestFile()` shared by `run.go`
and `Prune`; named timeout constants; ISO name and default VM name declared once
in `paths.go`. Fixes the live prune bug.

**Phase 2 — one retry primitive.** Collapse the six loops.

**Phase 3 — idempotency contract.** `Ensure*` semantics on `Create`, `BuildISO`,
`Download`; the `Outcome` type lifted out of `setup.go`; `ExpandESD` skips
completed images. `Setup` should shrink substantially as its subsystems start
answering for themselves.

**Phase 4 — cut the command surface.** Delete `up` (86 lines duplicating
`setup`, the largest remaining duplication) and `start` (powers a VM on
headlessly, yielding a UEFI prompt nobody can type at — a footgun with a
friendly name). Update `mise.toml`'s `vm:up` and `vm:iso-test` in the same
commit.

**Phase 5 — split the CLI.** 1144 lines → `main.go` (dispatch only) plus
`cmd_{setup,vm,boot,guest,media}.go`. Pure movement. Then collapse the boilerplate:
18 flagsets and 10 copies of *resolve-find-handle* behind one helper, so an
unknown VM reads the same from every command — which it currently does not.

**Phase 6 — group `utmvm` by subject** with `git mv` so history follows:
`media_*`, `deps_*`, `vm_*`, `host_*`. **No sub-packages** — the parts are
genuinely coupled and splitting would force most of the 134 symbols to stay
exported, defeating Phase 7.

**Phase 7 — shrink the exported surface.** 134 exported symbols for one
consumer. Grep `cmd/` for each; unexport what is absent. Expect 40–60 to go.

---

## Verification

Every phase:

```sh
mise run check
for t in darwin/arm64 linux/amd64 windows/amd64 windows/arm64; do
  GOOS=${t%%/*} GOARCH=${t##*/} CGO_ENABLED=0 go build -o /dev/null ./...
done
```

**Idempotency is verified by running things twice.** This is the check the repo
does not currently have, and Phase 3 should add it as a test:

```sh
irgo-winvm setup && irgo-winvm setup       # second run: every stage skipped
irgo-winvm iso -protect && irgo-winvm iso -protect
irgo-winvm build-iso -esd X -o Y && irgo-winvm build-iso -esd X -o Y   # must succeed
irgo-winvm probes && irgo-winvm probes
```

A unit test can cover most of it without a VM: `Prune` twice, `Download` twice
to an existing file, `BuildISO` twice against a fixture directory.

**The prune bug gets a regression test in Phase 1** — generate a guest filename
through `guestFile()` and assert `isOurArtefact` recognises it, so the two
cannot drift again.

Phases 4 and 5 need the real VM:

```sh
irgo-winvm run -timeout 3m -vm irgo-win11 .bin/nativeprobe-arm64.exe
irgo-winvm run -gui -timeout 4m -vm irgo-win11 .bin/glaze-verifyevents-arm64.exe
irgo-winvm suspend -vm irgo-win11 && irgo-winvm resume -vm irgo-win11
```

The last must still report **~400 ms**. Seconds means a poll interval was lost —
exactly how the bug happened the first time.

---

## Phase 8 — make the mess impossible to repeat

A cleanup that is not enforced decays back. This phase is the reason the others
are worth doing.

**Most of this mess was made by an AI agent that did not read what already
existed** — three wait-for-agent loops next to a `WaitForAgent` method, a fourth
byte formatter, `EnsureReady` where `RunInstall` was needed. Prevention has to
work on that failure mode specifically: a convention nobody reads prevents
nothing, so it goes in the file agents load automatically.

### 8a. A linter that catches each mistake actually made

`golangci-lint` is already available through mise. Enable exactly the checks
that would have caught the bugs in this repo's history — not a default preset:

| linter | the bug it would have caught |
|---|---|
| `unused` | all 8 dead symbols in Phase 0, at the moment each lost its caller |
| `dupl` | the 3 wait-for-agent loops, the 3 batch-file blocks, the 4 byte formatters |
| `goconst` | `win11-arm64.iso` ×5, `irgo-win11` ×3 |
| `mnd` | the 8 unexplained timeout literals |
| `funlen`, `gocognit` | `runUp` at 86 lines doing five things |
| `errcheck`, `gosec` | already clean; keep them clean |

Thresholds are set from the code *after* Phase 7, so the gate starts green and
any regression is a new failure rather than pre-existing noise.

### 8b. Wire it into the gate, not just the docs

`.golangci.yml` at the root; `mise run check` gains a `lint` step; the existing
`.github/workflows/check.yml` gains the same. A rule that only lives in prose is
a rule that gets skipped at 2am.

### 8c. Tests that encode the invariants

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

### 8d. `AGENTS.md` — the file that gets read

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
