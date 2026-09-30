# Development

How `irgo-winvm` is built: its structure, the conventions the code follows,
the contracts it publishes, and the traps already found. Read it before
changing code.

Setup, the commands to run and how to land a change are in
[CONTRIBUTING.md](CONTRIBUTING.md). They are not repeated here.

## Overview

`irgo-winvm` is a command-line tool for macOS on Apple Silicon. It builds a
Windows 11 ARM64 virtual machine in [UTM](https://mac.getutm.app), runs a Go
`.exe` inside it, and returns the program's output and exit status. It also
serves the same commands to agents over the Model Context Protocol.

The work is split into three stages, run in order. Each is one command with a
matching undo (see [The commands](#the-commands)):

1. **iso** (`iso-create`) — Windows installation media, downloaded from
   Microsoft and mastered with `xorriso`.
2. **vm** (`vm-create`) — a UTM VM with Windows installed from that media.
3. **app** (`app-create`) — your `.exe` running in the VM, with its output
   copied back.

Each stage owns its own paths and constants, in its own files:

- **iso** knows nothing about UTM and works on a machine that has never had a
  hypervisor. It once called into UTM's bundle directory to decorate an error
  message, which made fetching media require UTM to be installed.
- **vm** owns UTM entirely: finding and installing it, `utmctl`, the bundle
  layout and the guest tools.
- **app** drives the guest through the vm stage's `utmctl` wrapper and owns the
  guest-side paths.

A change that makes one stage reach into another's paths is a design error.

### Requirements

- macOS on Apple Silicon.
- UTM. `vm-create` installs it from its signed `.dmg` if it is missing: the
  newest release GitHub does not mark as a pre-release, never a beta
  (`latestStableUTMDMG`). UTM 5.0.x are betas; see
  `.plans/2026-09-30_2000_utm-5.md`.
- `wimlib` and `xorriso`, only when building media from scratch. `iso-create`
  installs them and `iso-delete` removes them.
- macOS Automation permission to control UTM. This is granted once, in a system
  dialog, and nothing can grant it for you. `vm-create` checks it before doing
  anything expensive: without it a boot cannot be driven, and the failure would
  otherwise arrive forty minutes into an install as a timeout that does not
  mention permissions.

### Scope: Windows only

Windows is the platform whose behaviour cannot be checked by reading code on a
Mac, and everything here — the answer file, ISO mastering, the guest agent, the
session model — is Windows-specific. Linux guests would need their own image
and path and are not built. The `linux` builds in CI exist only so the tool
compiles for a developer on another OS.

## Repository layout

Each top-level directory has one job:

```
cmd/irgo-winvm/   the CLI: flags, handlers, exit codes. The one thing users install
internal/         the CLI's packages (see Architecture). internal/ so nothing outside can import them
examples/         conformance, the glaze and native test suite (mise run glaze:mac /
                  glaze:windows), and glaze-all, the demo you drive by hand
site/             renders docs/ into the website
docs/             every document. AGENTS.md and CLAUDE.md at the root only point here
.plans/           work in progress, one file per plan
mise.toml         tools, environment, one-line tasks. They run .bin/irgo-winvm, built by go:tool
mise-tasks/       every task longer than a line, one script each; the path is the name
```

### Go modules

There are three modules. The split controls what reaches the shipped binary.

| module | why it is separate |
|---|---|
| root | the tool. `go list -deps ./cmd/irgo-winvm` is what actually reaches a user |
| `examples` | builds against **glaze and native**, the libraries under test, which must never reach the shipped binary |
| `site` | needs a markdown parser the tool has no business carrying |

Verify the split with `go list -deps`, not by reading imports. The site module
requires goldmark, its extensions and the chroma highlighter, and nothing else.
That is why the generated MCP page is captured from the binary rather than
produced by importing the server: importing it would pull the protocol SDK's
dependency graph into the documentation generator.

### Runtime data

Everything the tool writes goes in one fixed place, with nothing to configure:

```
~/Library/Application Support/irgo-winvm/
  media/    the ISO, the .esd it was built from, and scratch
  bin/      binaries staged into a VM
  logs/     every command, appended across runs
  shots/    a screenshot per stage of every run
  jobs/     long-running work, so a 45-minute install survives a disconnect
  utm-releases.json   doctor's cache of UTM's latest releases, trusted for 12 hours
```

VMs live where UTM keeps them, because UTM reads nowhere else. Screenshots
chosen as documentation are committed under `docs/screens/`, separate from
`shots/`, so the record is not buried in per-run noise.

## Architecture

### Packages

| package | holds |
|---|---|
| `internal/utmvm` | all three stages and everything they touch. **Do not split it**: iso, vm and app are coupled, and separating them means one reaching into another's paths |
| `internal/command` | which commands exist, and nothing about what they do. Imported by anything that must know the list in-process |
| `internal/mcpserver` | the MCP surface, with **no behaviour of its own** |
| `internal/job` | work that outlives the caller that started it. Not in `utmvm`, because all three stages start such work and its owner must be able to report a **dead** process |
| `internal/glazecheck` | whether glaze works: build `examples/conformance` into a test binary, run it here or through `app-create`, record every test from its test2json events. Needs a checkout of this repository, so it is not in `utmvm`, which must work on a machine that has never seen it |
| `cmd/irgo-winvm` | wiring: one file per concern (`iso.go`, `vm.go`, `app.go`, `doctor.go`, `status.go`, `mcp.go`, `glaze.go`, `help.go`), each command's flags beside its run func; `main.go` holds dispatch and the table joining `command.All` to those funcs; `exit.go` maps errors to exit codes |

### Dependency direction

Dependencies run one way:

- `vm` and `app` use the ISO API; nothing calls back into them from `iso`.
- `mcpserver` depends on `utmvm` and `command`; neither depends on it.
- `doctor` reports on all three stages by calling into them, not by holding its
  own copy of where anything is.

### The MCP server

`irgo-winvm mcp` serves the same commands over the Model Context Protocol, on
stdin/stdout or over HTTP (`-http`, loopback by default). Its purpose: an agent
writing a Go desktop app on a Mac cannot otherwise find out whether the app
works on Windows. Through this server it can ask, get an answer from a real
Windows guest, and see the screen when the answer is that the app hung.

Adding a command is two edits: declare it in `command.All` (name, summary,
undo, whether it mutates), and add a `<name>Flags` func and a `run<Name>` func
in the matching file in `cmd/irgo-winvm`, joined by one row in the table in
`main.go`. The binary panics at start if the two lists disagree, and the MCP
tool, its schema, the usage text and the site's reference follow on their own.

It needs macOS on Apple Silicon, and UTM, which `vm-create` installs from its
signed `.dmg` if it is missing. `wimlib` and `xorriso` are installed by
`iso-create` and removed by `iso-delete`, only when building media from scratch.

- **Tools are generated from the command list** in `internal/command`, so they
  are the commands and nothing else.
- **The server holds no logic.** Behaviour reachable only over MCP is a second
  answer to a question already answered, and nobody tests it: the cycle tests
  and developers both drive the CLI. If a tool needs logic, it goes in `utmvm`,
  where both callers get it.
- **Uploads.** Over HTTP, an agent with no shared filesystem can send a
  cross-compiled `.exe` in chunks with `app-upload`. It is staged
  content-addressed under `bin/`, verified by SHA-256 before it is committed,
  and then passed to `app-create` by path.
- **Remote access.** Binding wider than loopback requires `-allow-remote` and
  `IRGO_WINVM_TOKEN`. Read the [threat model](THREAT-MODEL.md) first.
- **In this repository**, `.mcp.json` registers `irgo-winvm mcp` for any agent
  working here, rebuilding `.bin/irgo-winvm` first. mise's output goes to
  `/dev/null`, because stdout is the JSON-RPC channel. Before 30 Sep 2026 there
  was no `.mcp.json`, so no agent working here was connected to the server.

### Jobs

`vm-create -install` (about 45 minutes) and `iso-create -fetch` start the work
and return a job id instead of blocking on a connection that would time out.
Over MCP, `glaze-check -windows` is a job too. The work outlives the client that
started it; `status` reports what is running, what finished and how long it
took. Whether a job is alive is answered by asking the operating system, not by
reading a file that says so. Job records live in `jobs/` under the runtime data
directory.

### The mutation lock

Every command that changes state takes one shared lock (`internal/utmvm/lock.go`).
There is one lock for all three stages, not one per stage, because their
mutations touch the same machine. A second mutation is **refused, not queued**,
with exit code 6. The lock is released when its holder dies; the primitive that
does that differs per platform, hence `lock_darwin.go` and `lock_other.go`.

## Building, testing and linting

The commands are in [CONTRIBUTING.md](CONTRIBUTING.md#before-you-push):
`mise run go:check` and `mise run go:lint` before pushing, `mise run ci:watch`
after. The cycle tests that need UTM and real media are in
[Run the cycle tests](CONTRIBUTING.md#run-the-cycle-tests), and the glaze gates
in [Does glaze work?](CONTRIBUTING.md#does-glaze-work).

What the checks cover, and what they do not:

- **`go:check` cross-compiles** for Linux and Windows as well as macOS. Deleting
  a function from `sysfile_other.go` once passed every check being run, because
  they all ran on darwin.
- **`go:lint` pins `GOOS=darwin`**, so a Mac and the Linux CI runner lint the
  same code. On Linux `statfsAvailable` is a stub that always errors, and
  staticcheck rightly reports `fErr == nil && free < n` in `iso_create.go` as
  never true (SA4023). That failed CI on every push from e1a4243 to 79f8a55,
  while the same command passed on the Mac that wrote it.
- **Unit tests cover the iso stage well**, and the vm and app stages only at the
  edges. Anything touching a real guest is proven by running it, because those
  paths fail silently. Use a disposable VM: running a binary pushes it into the
  guest and executes it, and a 45-minute install is not worth losing to a test.
- **Wait for CI with `mise run ci:watch`**, never a hand-written `sleep` loop.
  There were five such loops before the task existed; two were wrong in the same
  way. It watches every workflow the commit started, not just `check`, exits
  non-zero if any failed, and takes `SHA=<commit>` for a commit other than HEAD.

### mise tasks

`mise tasks` lists them. `mise.toml` holds the tools, the environment and the
one-line tasks. Anything longer is an executable script in `mise-tasks/`, where
the path is the name (`mise-tasks/go/check` is `go:check`), `#MISE` lines at the
top declare its description and dependencies, and findings are comments beside
the lines they explain. Until 30 Sep 2026 all of it was shell inside TOML
strings: 516 lines that no editor, `bash -n` or shellcheck could see.

A task exists only for what the binary cannot do on its own: checks, builds,
the glaze gates, upstream work and the create-and-delete cycles. No task merely
wraps a command; call the command.

- **Tools are pinned in `mise.toml` and nowhere else**, so CI installs what a
  maintainer has; `jdx/mise-action` reads it. Every `go.mod` says `go 1.27.1` to
  match. mise's reading of the Go version from `go.mod` is deprecated (removed
  in 2026.11.0), hence the pin.
- **`go:tool` is the one build of the tool.** Every task runs `.bin/irgo-winvm`
  rather than `go run ./cmd/irgo-winvm`, and `.bin` is on `PATH` in this
  directory, so `irgo-winvm doctor` works by hand. `sources` and `outputs` let
  mise skip the build when nothing under `cmd/` or `internal/` changed. From
  977136e to 30 Sep 2026 the task built `./irgo-winvm` while `outputs` named
  `.bin/irgo-winvm`, so every task and `.mcp.json` ran whatever stale binary
  `.bin` held (in a fresh clone, none). mise warned on every run — `did not
  generate expected output` — and nobody read it. After editing `cmd/` or
  `internal/`, run any task or `mise run go:tool` before calling the binary by
  hand.
- **`site:build` renders the markdown and never hand-written pages**, so the
  site cannot drift from the repository. **`site:serve` builds and serves in one
  command** on purpose: a separate server can be pointed at a stale `dist`.
- **`vm:shots` copies the newest screenshot of each stage** from `shots/` into
  `docs/screens/vm` under its stage name. The originals carry timestamps, so no
  document can point at them.
- **`UPSTREAM_DIR`** is where the local glaze and native clones live, read by the
  `upstream:*` tasks. It was restored from the `mise.toml` deleted in e533764.

## Engineering conventions

Each of these exists because its absence caused a real defect here.

### One way to do each thing

Every operation has exactly one implementation, and every question (where media
lives, how a binary runs in the guest) has one answer. A second route is a
second answer that drifts. Example: there were once four ways to run a binary in
the guest, differing only in where it landed and which session ran it, and
three answers to where media lives, all in use at once.

### Nothing reports success it did not verify

A function returns success only after checking that the effect happened. An
operation that cannot report failure also cannot be undone, because it does not
know what it did. Example: a download was renamed into place without checking
its length. The same defect appeared as `ExitCode: 0` when the output could not
be fetched, `nil` after five failed boots, an error value that could never be
non-nil, and an ISO built and never checked. Closing a file you *wrote* can fail
— that is where a full disk shows up — so that error is checked; closing a file
you read cannot.

### Commands come in do/undo pairs

Every command that changes state has an undo, so a failed step can be cleaned up
and re-run. The undo must work from any starting point, and deleting nothing is
success, so it can run twice. Example: `cmd`'s `del` exits 1 on a glob that
matches nothing, so an undo built on it succeeded while there was something to
remove and failed as soon as there was not.

### Guards answer yes, no, or cannot tell

A guard written `if ok && bad { refuse }` allows the action when the question
cannot be answered. Every guard here protects something destructive, so that is
backwards. Guards return three answers — yes, no, *could not determine* — and
the caller handles the third explicitly, refusing by default. Example:
`glaze-status` reports each recorded verdict as current, stale or cannot tell,
and `glaze-check` ends with `YES`, `NO` or `CANNOT TELL` when the guest agent
went away.

### Every check has a negative control

A test that cannot fail is not a test. When writing one, break the code it
covers, watch the test fail, and restore it; record the control in the test's
comment. Example: a test for the scan cache passed against a mutation that
disabled the check it covered, because its test case also changed the other
field.

If a property can only be verified by measurement, record the measurement with
a date in [RESULTS.md](RESULTS.md) rather than writing a test that looks like
coverage; a test for "the build records its verdict" is marked as not proving
that, because deleting the build's call leaves it green. Controls are run by
hand, not automated: a mise task that applied eight mutations matched exact
source text, broke on the first rename, once left a mutated file in a commit,
and was removed.

### Measure, do not assert

Behaviour of UTM, Windows and the filesystem is established by running it, not
by reasoning. Example: a length check added to the downloader was unreachable,
because `net/http` already rejects a short body — proven by disabling the check
and watching the test still pass. The other measured surprises are in
[Known traps](#known-traps). For code, use the compiler and analysers rather
than grep, which counts comment mentions as call sites and gave three wrong
answers in one afternoon: delete the symbol, rebuild, run the tests, and put it
back if either fails. `mise run go:lint` finds what grep does not.

### Say what is happening, and where

A command that prints nothing for fifty seconds cannot be told apart from one
that has hung. Announce each step before doing it, name every path, and print
elapsed time; "not found" without a location cannot be checked. Example: the
77-second ARM64 scan was always there and was found only once the tool said so.

### Comments follow Go norms

A doc comment says what the thing does and, in a sentence or two, the
non-obvious why. A measured trap or a warning stays in the code, tightly worded:
why the display is `virtio-ramfb-gl`, why ESD image 3 needs `--boot`, why
`utmctl suspend --save-state` must never be called. The story of how it was
found belongs in [RESULTS.md](RESULTS.md) or the traps table below, not in the
code. If a comment is wrong, fix the fact, and look for any other copy of a
measurement you correct.

### Upstream bugs are fixed upstream

A bug in glaze or native is fixed in [crgimenes](https://github.com/crgimenes),
not worked around here. A workaround in an example still ships the bug to every
user of those libraries and hides it. Example: `examples/conformance` (and
`glaze-all`) carry one marked stand-in for the `ErrUnsupported` fix, to be
deleted when a release contains it. A test for an upstream bug is never
skipped to make a run green: it fails, and `glazecheck.KnownUpstream` names it
as a known upstream bug (see [the conformance suite](#the-conformance-suite)). [UPSTREAM.md](UPSTREAM.md) is the ledger.

### Do not

- Split `internal/utmvm`. Its parts are coupled.
- Touch the assets, the answer file or the plist template without running a real
  install. UTM rejects a bad config with one generic *"cannot import this VM"*
  that names no field.
- Export anything nothing uses.
- Put logic in a task, whether in `mise.toml` or `mise-tasks/`. Tasks call the
  binary; anything more belongs in the binary.
- Land a refactor in one commit. One concern per commit, each verified.

## The commands

| step | what it gets you | undo |
|---|---|---|
| **`iso-create`** | the Windows installer | `iso-delete` |
| **`vm-create`** | a VM with Windows on it, answering | `vm-delete` |
| **`app-create`** | your `.exe` running in that VM, output back | `app-delete` |

They run in that order, and each is cheap to repeat: if the work is already
done, it says so and stops.

Three commands change nothing: **`vm-screen`** photographs the VM, **`doctor`**
reports what is installed and where, and **`status`** lists long-running
[jobs](#jobs). `doctor` also names the installed UTM, the latest stable and
pre-release on GitHub, and whether an update is available. It answers from a
12-hour cache, else GitHub within 3 seconds, else an older cache marked as
such, and offline it says "cannot tell" rather than failing.

Two work only **in a checkout of this repository**, because they build and read
`examples/`:

- **`glaze-check`** builds [the conformance suite](#the-conformance-suite) into
  one test binary and runs it, natively on this machine (macOS, or Windows —
  which is how CI runs it) or, with `-windows`, in the VM through
  `app-create -gui`. It records the verdict in [GLAZE-STATUS.md](GLAZE-STATUS.md),
  a generated file: when, which commit, which glaze and native were built
  against (from `go list -m`, so a `go.work` pointing at local clones is named
  with the clone's branch and commit), and every test's PASS, FAIL or skip with
  the first message it wrote. The Mac and Windows sections are separate, so one
  run never erases the other. Each run keeps its full output as
  `glaze-<target>-<stamp>.log`, and the test2json events as `.json` beside it,
  in the log directory `doctor` names, and prints both paths first and last.
  With `-import <dir>` it runs nothing and records downloaded CI artifacts
  instead — what `pages.yml` does, on Linux, before building the site.
- **`glaze-status`** prints the recorded file and says, per section, whether it
  still describes the tree: current, stale (and why), or cannot tell.

Outside a checkout both exit 2 and say where they looked. They ship in the
binary anyway because an MCP tool can only be a command, and the agent most
likely to ask "does glaze work on Windows?" is the one `.mcp.json` starts in
this repository; the reasoning is in `internal/glazecheck/doc.go`. glaze and
native still never reach the binary: the suite is built by running `go`.
Over MCP, `glaze-check -windows` is a job: call `status`, then `glaze-status`.

Your `.exe` is anything built with `GOOS=windows GOARCH=arm64 CGO_ENABLED=0`.
That is the whole contract.

Every command that takes flags documents them with `-h`, and `irgo-winvm help`
explains the sequence. No document lists flags, so none can go stale: the
[command reference](https://joeblew999.github.io/irgo-windows-vm/reference.html)
is captured from the binary at build time.

## What it exits with

`utmctl` exits 0 when it fails (see [UPSTREAM.md](UPSTREAM.md#utm)), so this
tool's exit code is the only reliable signal a caller gets. Each code means one
thing:

| code | meaning |
|---|---|
| **0** | it worked — including `-h`, and an undo that found nothing to undo |
| **1** | your program ran and failed |
| **2** | the command was called wrongly |
| **3** | that VM does not exist |
| **4** | the VM is there, the guest agent is not answering |
| **5** | refused — a destructive command without `-force` |
| **6** | refused — another mutation is in progress |

**1 is your program, not this tool.** The guest's exit code is *not* passed
through: a binary exiting 3 makes `app-create` exit **1**, and the message names
the real code. A failing program and a missing VM must not look the same to a
script.

**4 and 6 are worth retrying.** Windows Update takes the guest agent away for
minutes at a time while the VM is fine. `app-create` already waits and tries to
recover before giving up, which is why it can take several minutes to return 4.
6 means another mutation holds the [lock](#the-mutation-lock); the holder
finishes on its own schedule.

`-detach` exits 0 once the program is running, since it is for windows nobody
intends to close.

`cmd/irgo-winvm/docs_test.go` reads this table, so a code declared in
`command.Outcomes` and not explained here fails the build.

## What it costs

| step | time | |
|---|---|---|
| `iso-create -fetch` | minutes, and the 4.2 GB `.esd` below | downloaded once; a rebuild from the kept `.esd` needs no network ([measured](RESULTS.md#the-iso-scan-verdict-is-recorded-at-build-time--13-aug-2026)) |
| `vm-create -install` | **about 45 minutes** | an estimate, not a measurement — unattended, you click nothing |
| `app-create` | seconds | [measured](RESULTS.md#the-inner-loop-works), cross-compiled on the Mac with no toolchain |

| on disk | size | |
|---|---|---|
| the `.esd` from Microsoft | **4.2 GB** | downloaded once, from a source that rate-limits |
| scratch to build the ISO | **12 GiB** | free space `iso-create` requires |
| the built ISO | **~4.9 GB** | hardlinked into the VM, not copied |
| the installed VM | **~30 GiB** | on a 64 GiB sparse disk |

About **33 GB** once installed. `iso-delete` keeps the `.esd` unless you pass
`-all`, because rebuilding the ISO from it is local work, while losing it means
downloading 4.2 GB again.

## The VM and the dev account

The VM's shape is fixed and not settable by a flag, because a VM that differs
between two machines gives results that cannot be compared. Changing it means
editing `setDefaults` in `internal/utmvm/vm_create.go`. Nothing in the tree
records *why* these particular numbers were chosen, only that they are fixed.

| | value | |
|---|---|---|
| name | `irgo-win11` | `utmvm.DefaultVMName`; `-vm` overrides, for a disposable VM |
| disk | **64 GiB, sparse** | costs kilobytes until the guest writes; see [what it costs](#what-it-costs) |
| RAM | **8192 MiB** | |
| CPUs | **4** | `CPU` is `host` — the guest sees the Mac's cores |

The guest logs itself in as **`dev`**, an administrator, with the password
**`dev`** in plaintext in `internal/utmvm/assets/autounattend.xml`, auto-logon
enabled for 999 logons, and RDP switched on. VMs created now have a `dev`
password that never expires.

> [!WARNING]
> The `dev` password is deliberate, not a leaked credential, and is not to be
> "fixed". Setup needs it in plaintext to create the account and log in with
> nobody typing, which is the point of an unattended install. It guards a
> throwaway VM with no inbound route except from this Mac, and it is obvious so
> nobody mistakes it for a secret. **Do not copy that answer file to anything
> reachable from a network you do not control.**

### Why `-gui` exists

The QEMU guest agent runs as `NT AUTHORITY\SYSTEM` in **session 0**, which has
no window station. Anything that opens a window fails there, confusingly: glaze
reports `webview2: environment/controller creation failed`, which reads like a
missing WebView2 runtime. It is not — the runtime was present and healthy
(151.0.4129.78) while that failure persisted.

`-gui` runs the program through a scheduled task with `/it`, as the logged-in
user in their session, which has a desktop. Auto-logon guarantees that session
exists. It also stages the binary in `C:\Users\Public` rather than
`C:\Windows\Temp`, because the interactive user must be able to execute it.

Headless programs need no flag; anything with a window needs `-gui`. The
operating system enforces that split.

### When `-gui` stops working on an old VM

- **Expired password.** Windows expires local passwords after 42 days. AutoLogon
  then stops, there is no desktop session, and every `-gui` run has nowhere to
  go. Affects VMs created before the never-expiring password.
- **Stale WebView2 registration.** An interrupted WebView2 update can leave its
  registration naming a deleted folder, and glaze then reports WebView2 as
  missing ([glaze#34](https://github.com/crgimenes/glaze/issues/34)).

`irgo-winvm vm-repair -reboot` fixes both, running as SYSTEM. `app-create -gui`
refuses up front, naming the problem, when nobody is logged in, instead of
waiting out its timeout.

## The conformance suite

`examples/conformance` is a `go test` suite, and the only thing that answers
"does glaze work?". It is the same tests everywhere: `go test` on the Mac, the
same package compiled with `go test -c` for Windows ARM64 and run on the VM's
desktop by `app-create -gui`, and the CI job `conformance` on GitHub's
`macos-latest` and `windows-11-arm`. What each test proves, and what it
deliberately does not, is in its comment.

| tests | library | needs |
|---|---|---|
| `TestClipboard`, `TestPowerPreventSleep`, `TestSingleInstance`, `TestMmap` | native | nothing: headless |
| `TestOpenURL` | native — the scheme allow-list and Reveal's refusal only | nothing: no side effect |
| `TestAppScheme`, `TestEvents` | glaze — the portless `app://` path, the Events bridge | a desktop session |
| `TestTray`, `TestMenu`, `TestNoCapture`, `TestAppIcon`, `TestFileDialog` | native + glaze | a desktop session |

How it is built, and why:

- **The main thread.** AppKit takes UI work only on the main OS thread, and
  the testing package runs each test on a goroutine of its own. `init` locks
  the main goroutine to the main thread, `TestMain` runs `m.Run` elsewhere and
  then serves a queue, and a test opens its window through that queue — so a
  test is an ordinary test, with `t.Fatal` and subtests, while the window's run
  loop has the main thread.
- **Nothing is left open.** Every window is torn down in `t.Cleanup`; the tray
  is stopped; the file dialog is found through the OS (the modal panel, the
  owned popup) and cancelled. `openurl.Open` and `Reveal` on a real target are
  not in the suite at all: they open a window in another process that the test
  cannot close, and "the call returned nil" is all it could assert. Drive them
  by hand in `glaze-all`.
- **A skip says why, and there are two reasons.** `-short` skips what needs a
  desktop (it is what `go:check` and `app:test` run). And a package's own
  `ErrUnsupported` skips only on an OS where the capability is documented as
  unsupported (`nocapture` on macOS, `SetAppIcon` on Windows); the same error
  anywhere else is a failure.
- **Known upstream bugs fail.** `TestAppScheme/absolute_subresources` fails on
  Windows until glaze fixes [§1b](UPSTREAM.md). `glazecheck.KnownUpstream`
  lists it, so a run whose only failures are known answers `KNOWN BUGS ONLY`
  and exits 0 — the VM gate and the CI job stay green for changes that broke
  nothing — and a known failure that starts passing answers `UNEXPECTED PASS`
  and exits non-zero, because the list is then out of date.
- **Every windowed test photographs its window**, when the binary is given
  `-conformance.shots=<dir>` (or `$CONFORMANCE_SHOTS`), at the moment that
  shows what it checked: the page loaded, the tray up, the menu installed, the
  dialog open. Windows uses `PrintWindow` with `PW_RENDERFULLCONTENT` (WebView2
  is composited by DWM, and a plain `WM_PRINT` or `BitBlt` gets black); macOS
  uses `screencapture -l` with the `NSWindow`'s `windowNumber`, after
  `CGPreflightScreenCaptureAccess` says the process may capture — without
  Screen Recording permission `screencapture` returns the wallpaper where the
  window should be. A black or one-colour frame is a failed capture and is not
  written. A capture never fails a test: it logs `screenshot: <os>/<Test>.png`
  or `screenshot not captured: <why>`, and `glaze-check` reads those lines.
  On macOS the tray item and the menu bar are drawn outside the process and a
  capture of them shows the desktop behind (measured on macOS 27), so those two
  tests photograph their window instead and say why beside the picture.
- **The pictures go where the verdict goes.** `glaze-check` copies them into
  `docs/screens/conformance/<target>/` with a `shots.json` manifest (pulled
  from `C:\Users\Public\irgo-conformance-shots` with `utmvm.Pull` for the VM
  run), emptying the directory first, and `GLAZE-STATUS.md` ends in a
  Screenshots table — each windowed test, the Mac and Windows side by side,
  each with its result. The CI job uploads each runner's pictures with its
  record; `pages.yml` downloads the latest completed conformance run on main
  and runs `glaze-check -import` on it before building the site, so the Glaze
  status page shows CI's pictures, and what is committed when CI has none.

`examples/glaze-all` is the demo: every capability is a button, and
`mise run glaze:hands` leaves it on the VM's desktop to drive by hand. It is
not a test and nothing reads its output.

## Desktop hygiene

A check leaves nothing on a screen: not on the owner's Mac, where `glaze:mac`
runs natively, and not on the VM's desktop, which every screenshot shows.
Before 30 Sep 2026 each run left two file-manager windows (five Finder windows
onto `$TMPDIR` had piled up on the Mac, six `explorer.exe` processes on the VM),
a "Location is not available" box, and a file dialog that went only because
the process exited under it.

**Tests do not open what they cannot close.** The conformance suite checks
openurl's refusals only, which ask the OS for nothing; `Open` and `Reveal` on a
real target hand it to Finder or Explorer, whose window the test does not own
(`TestOpenURL` says why). `TestFileDialog` cancels its dialog and requires
`OpenFile` to return. Opening a real folder is for `glaze-all`, by hand.

**The VM's desktop is reset around every Windows check.** `utmvm.DesktopReset`
runs `internal/utmvm/assets/desktop-reset.ps1` in dev's session, through the
same `/it` scheduled task as `app-create -gui`. SYSTEM is in session 0 and
cannot see dev's windows. It closes Explorer windows and Explorer's error
boxes, stops Windows Update's restart prompt and its requester, dismisses
notification toasts, closes an open Start or Search pane, then checks again and exits 1 if any is still there or if
the taskbar is gone. It never kills `explorer.exe`: when that was tried, the
taskbar went with it and Windows did not restart the shell. There is no
command of its own. `glaze-check -windows` runs it before the first program and
after the last, printing what it closed, so a program that left something open
is named in the log. `vm-repair` runs it at the end unless it reboots.
`docs/screens/vm/desktop-before-reset.png` is the VM's desktop before any of
this, with two Explorer windows and the restart prompt;
`docs/screens/vm/desktop-after-glaze-check.png` is the same desktop straight
after a full `glaze-check -windows`, with nothing on it.

**At the source**, the answer file and `vm-repair` turn off Windows Update
notifications, restart warnings included (`SetUpdateNotificationLevel` 1 with
`UpdateNotificationLevel` 2, and `SetAutoRestartNotificationDisable`), turn
off OneDrive (`DisableFileSyncNGSC`) and notification toasts
(`NoToastApplicationNotification`, a per-user policy written into dev's hive),
and remap both Windows keys to nothing (`Scancode Map`). OneDrive's "Turn On
Windows Backup" toast arrived a minute after the reboot that installed the
pending update. UTM forwards the Mac's
Command key as the Windows key, so every Cmd-Tab on the Mac opened Start in the
guest. The remap is read at boot. `vm-repair` says whether it is in effect,
needs a reboot, or cannot tell (set by the answer file with no record of when).
UTM's only related settings are app-wide, not per VM: `IsCtrlCmdSwapped`
(Settings, Input: swap Control and Command) and the input-capture options
(`WindowFocusAutoCapture`, `FullScreenAutoCapture`). None of them simply stops
forwarding Command, and this tool does not change UTM's preferences.

**What a `vm-screen` shows is current.** It captures UTM's window, and the
taskbar clock in each capture advanced minute by minute. A capture taken inside
the guest (`CopyFromScreen` in dev's session) matched it pixel for pixel,
update prompt included. When a window seems to survive being killed, the
process that owns it is not the one that was killed. See the traps below.

## Known traps

Each of these fails silently or misleadingly. UTM rejects a bad config with one
generic *"cannot import this VM"* that names no field; a wrong boot command
produces a prompt nobody sees; a truncated ISO produces a VM that will not boot.

One line each. The `utmctl` rows are **defects in UTM**, written up with
severity, reproduction and status in [UPSTREAM.md](UPSTREAM.md#utm); keep the
detail there and only the reminder here.

### Host, UTM and the ISO

| trap | symptom | what to do |
|---|---|---|
| `virtio-gpu-pci` display | no framebuffer on aarch64 and no legacy VGA; the guest boots **invisibly** and looks hung | use `virtio-ramfb-gl` |
| VirtIO system disk | Windows ARM64 has no inbox driver; Setup reports no drive found | use **NVMe** |
| `virtio-net-pci` without guest tools | no inbox driver, **no network at all** in the guest | install the guest tools |
| missing `PS2Controller` | non-optional decode with no default; the whole config is rejected | include it |
| `UsbBusSupport: "USB3_0"` | config rejected | the enum is `"2.0"` / `"3.0"` |
| `CPUFlags` | config rejected | the keys are `CPUFlagsAdd` and `CPUFlagsRemove` |
| UTM's schema read from `main` | `main` was v5.0.4 while the app was v4.7.5, and they disagree | read the schema at the **tag** of the installed version |
| `gh run list --commit` with a short SHA | an empty list, not an error — indistinguishable from "not started yet"; it matches the **full 40 characters only** | use `mise run ci:watch` |
| reading a Windows ISO as ISO9660 | every path fails: the ISOs are **UDF**, because `install.wim` exceeds ISO9660's 4 GB limit | read it as UDF |
| answer file on a FAT disk | Setup ignores it and runs interactively | put it on an ISO9660 **CD** |
| ISO padded past its declared volume size | mounts on macOS, ignored by Setup | trim to the PVD size |
| Joliet disabled | `autounattend.xml` becomes `AUTOUNAT.XML`, which Setup never looks for | keep Joliet on |
| El Torito marked BIOS (`-b`) | correctly sized and named, **does not boot** | UEFI needs `-e` |
| `start utm-guest-tools-*.exe` | `start` does not expand wildcards; the installer silently never runs | expand the name with `for` first, as `autounattend.xml` does |
| `utmctl start`, then keystrokes | a headless VM has no display, UTM routes input through it, and the keystrokes vanish | start it through UTM itself so a display opens (`StartWithDisplay`) |
| driving a boot on a VM that is already running | it may be a working desktop, not a UEFI shell; keystrokes land in whatever has focus (`docs/screens/vm/running-no-agent.png`: three Bing tabs searching for the EFI path) | never type at a VM this code did not just start; look at `vm-screen` |
| `utmctl delete` | prints its failure and **exits 0** | check that the bundle is gone afterwards |
| bundle removed behind UTM's back | `utmctl` will not drop a registry entry whose bundle is gone, leaving a phantom it cannot recover from | delete through `utmctl`. To recover a phantom, recreate an empty stub at the expected path so UTM has something to remove |
| `utmctl exec` | never returns the guest's output and always exits 0 | run a batch file that captures output to a file, then pull the file |
| `utmctl exec` with a whole command line as one string | the agent looks for a file by that entire name and answers "No such file or directory" — indistinguishable from a dead agent | pass arguments separately |
| `utmctl suspend --save-state` | **reports success and power-cuts the guest**: no state file, VM left `stopped`, next boot goes through "Diagnosing your PC" | use plain `suspend` |
| `cmd` `del` on a glob matching nothing | **exits 1**, so an undo fails as soon as there is nothing left to undo | treat "nothing matched" as success |
| `dir` and `del` report a missing file differently | `dir` says "File Not Found", `del` says "Could Not Find"; handling only one prints the other's text as if it were a filename | handle both |
| `ln` to an immutable file | `EPERM`, so protecting the ISO silently turned a hardlink into a 5 GB copy | clear the flag first, restore it after |
| `rm` on a bundle holding that hardlink | `EPERM`, directory left behind, so every VM built from a protected ISO was undeletable | clear the flag first, restore it after |
| a length check on a download | unreachable: `net/http` already rejects a short body | do not add one; it was proven dead by disabling it |

### In the guest programs

| trap | symptom | what to do |
|---|---|---|
| each package defines its **own** `ErrUnsupported` | none wrap `errors.ErrUnsupported`, so a check against that alone matches nothing, and a platform behaving as documented reports **FAILED** with a non-zero exit. `glaze.SetAppIcon` is unsupported on Windows by design | check each package's sentinel until the [upstream fix](UPSTREAM.md#2-native--glaze--errunsupported-sentinels-do-not-wrap-errorserrunsupported) is released |
| `tray.Run` **blocks**, driving the event loop until `Stop` | waiting on it deadlocks | post it and leave it; `Stop` is safe from any goroutine |
| the tray started **before** the window | glaze's `New` runs a temporary `[NSApp run]` that ends only when `applicationDidFinishLaunching` fires, once per process. A tray started first consumes it and `glaze.New` blocks forever, with no window and nothing printed | create the window first |
| `menu.Set` with no `Options.Window` | returns an error naming it, on Windows, where the HWND is required | pass the window |
| `menu.Set` with `Options.Dispatch` set **before** `Run` | `Set` blocks until its UI work has run, and nothing drains the queue until the run loop starts | call it after `Run` starts |
| `file://` URL built by concatenation | Windows paths are `C:\dir`; the URL needs `file:///C:/dir`. Without the leading slash `net/url` writes `file:C:/dir` and ShellExecuteW rejects it | build it with `net/url` and a leading slash |
| `openurl.Open != nil` as a capability check | a function value is never nil, so it checks nothing; `go vet` says so | call it and check the error |
| Windows Update's restart prompt, "We've got an update for you" | killing `MoNotificationUx.exe` leaves it on screen: that process only requests it. The window is a `Shell_SystemDialogProxy` owned by `PickerHost.exe`, and `WM_CLOSE` to the proxy removes the window but leaves its picture | stop `PickerHost.exe`, then the requester, as `desktop-reset.ps1` does |
| `EnumWindows` or UI Automation to find what is on the Windows screen | both list only the desktop's own z-order band. The taskbar, Start, Search and the update prompt live in others, so all of them were missing while on screen | walk top-level windows with `FindWindowEx(NULL, prev, NULL, NULL)`, and treat a cloaked window as hidden |
| checking the foreground window for an open Start menu | the check runs in a console of its own, which has the foreground, while Start stays open behind it | look for an uncloaked `CoreWindow` of `StartMenuExperienceHost` or `SearchHost` |
| a notification toast (OneDrive's "Turn On Windows Backup") | an uncloaked `CoreWindow` of `ShellExperienceHost` titled "New notification". Stopping that host brings it straight back | stop the sending app and clear its notification history (`ToastNotificationManager.History.Clear`) |
| `$null` passed to a `string` parameter of a .NET method from PowerShell | PowerShell passes `""`, so `FindWindow('Shell_TrayWnd', $null)` asks for an empty title and `FindWindowEx(0, h, $null, $null)` finds nothing | call from C# (`Add-Type`) or pass `[NullString]::Value` |
| the Mac's Command key | UTM forwards it as the Windows key, so Cmd-Tab on the Mac opens Start in the guest, over screenshots and `-gui` windows | `Scancode Map` remaps both Windows keys (answer file, `vm-repair`); a reboot applies it |
| `glaze.New` called after other work on the main goroutine, on macOS | SIGTRAP inside `[NSApp run]` in about 1 run in 7: the goroutine had moved off the main OS thread, and glaze pins the thread in `New`, not in an `init` ([UPSTREAM.md §5](UPSTREAM.md#5-glaze--new-crashes-if-the-main-goroutine-has-moved-thread)) | call `glaze.New` before anything slow on the main goroutine |
| an absolute `app://` URL for a sub-resource on Windows | glaze emulates the scheme with a virtual host, so the document loads from `https://app.localhost/` and an absolute `app://` URL names a scheme WebView2 does not know. No error, no console message, no stylesheet | reference assets relatively ([UPSTREAM.md §1b](UPSTREAM.md#1b-glaze--absolute-app-urls-silently-do-not-load-on-windows)) |
