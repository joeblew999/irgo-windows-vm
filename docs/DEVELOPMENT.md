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
- UTM. `vm-create` installs it from its signed `.dmg` if it is missing.
- `wimlib` and `xorriso`, only when building media from scratch. `iso-create`
  installs them and `iso-delete` removes them.
- macOS Automation permission to control UTM. This is granted once, in a system
  dialog, and nothing can grant it for you. `vm-create` checks it before doing
  anything expensive: without it a boot cannot be driven, and the failure would
  otherwise arrive forty minutes into an install as a timeout that does not
  mention permissions.
- **Not** Full Disk Access, or access to other apps' data. The tool never reads
  or writes UTM's container itself; it writes bundles under its own directory
  and has UTM import, clone, reconfigure and delete them through AppleScript.

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
  vm/       the UTM guest tools ISO, and staging/ for bundles until UTM imports them
  golden.json            what is known about the golden image
  mutation*.lock         the mutation locks, one machine-wide, one per VM, one for bin/
  net/      the guest address the last SMB push reached, one file per VM
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

`vm-create -install` (about 45 minutes), `iso-create -fetch` and
`vm-golden-create` (always) start the work and return a job id instead of
blocking on a connection that would time out.
Over MCP, `glaze-check -windows` is a job too. The work outlives the client that
started it; `status` reports what is running, what finished and how long it
took. Whether a job is alive is answered by asking the operating system, not by
reading a file that says so. Job records live in `jobs/` under the runtime data
directory.

### The mutation locks

Every command that changes state takes the locks it declares in
`command.All` (`Locks`), from three kinds (`internal/utmvm/lock.go`):

| lock | guards | taken by |
|---|---|---|
| machine (`mutation.lock`) | the media and the golden image | `iso-*`, `vm-golden-*`, and `vm-create` only while it writes or clones the bundle |
| per VM (`mutation-vm-<name>.lock`) | that VM | `vm-create`, `vm-delete`, `vm-repair`, `app-create`, `app-delete`, `glaze-check -windows` |
| stage (`mutation-stage.lock`) | `bin/`, the staged binaries | `app-upload`, `app-delete` |

So `app-create` on two VMs runs side by side, and a second mutation of the same
VM is **refused, not queued**, with exit code 6 and a message naming the busy
lock. The VM is read from the command's own `-vm` flag, its name case-folded
and a UUID resolved to the name, so every spelling lands on one lock. A lock is
released when its holder dies (flock on macOS; nothing to lock elsewhere, hence
`lock_darwin.go` and `lock_other.go`), and its file is never deleted: unlinking
a flock file someone holds lets a third process lock a new file of that name.

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

Once one VM has been installed, **`vm-golden-create`** (undo
**`vm-golden-delete`**) seals it into a [golden image](#the-golden-image), and
`vm-create` then clones that instead of installing.

Three commands change nothing: **`vm-screen`** photographs the VM, **`doctor`**
reports what is installed and where, and **`status`** lists long-running
[jobs](#jobs).

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

## The golden image

A golden image is an installed Windows, sealed once, that every new VM is
cloned from instead of installed. It turns "a VM of my own" from about 45
minutes into a clone and a boot, and it lets several developers or agents on
one Mac each have a VM without stopping anybody else's.

| command | what it does | undo |
|---|---|---|
| **`vm-golden-create -vm <disposable>`** | seals that VM and registers the result as `irgo-golden` | `vm-golden-delete` |
| **`vm-create -vm <name>`** | with a golden image: clones it as `<name>` and boots the clone | `vm-delete` |

The first VM is still installed the slow way, under a throwaway name:
`vm-create -vm g1 -install -golden=false`, then `vm-golden-create -vm g1`. With
no golden image, `vm-create` says it is falling back to a full install and
does that; `-golden=false` installs even when there is one.

**Sealing** (`internal/utmvm/vm_golden.go`, and `assets/vm-golden-seal.ps1` in
the guest, as SYSTEM, one step at a time with the disk's allocation printed
after each):

1. boot the source VM and wait for its agent;
2. BitLocker off (and `PreventDeviceEncryption` set), hibernation off,
   `DISM /StartComponentCleanup /ResetBase`, TRIM;
3. shut Windows down from inside and wait for UTM to report it stopped;
4. clone it through UTM as `irgo-golden`, keeping only the NVMe system disk
   (the install, answer-file and guest-tools CDs are dropped);
5. clone the golden image once more, boot that clone until its agent answers,
   and delete it, so an image that does not boot is never reported made;
6. write `golden.json`: source, Windows build, WebView2 version, allocated and
   apparent size, seal and boot times, tool version. `doctor` reports it.

It refuses `irgo-win11` without `-force`, and refuses when it cannot find out
which VM it was given. The source is left sealed and stopped, an ordinary VM
that `vm-delete` removes.

**Cloning** (`CloneFromGolden`) takes the machine lock for the clone itself,
seconds, so it cannot race `vm-golden-delete`, and runs the boot under the new
VM's lock only. It refuses when the golden image is running (it must stay
stopped: UTM will not clone a running VM, and a golden image that has booted is
no longer the one its manifest describes) and when free space is below 10 GiB,
an estimate of how much a clone grows until it is measured.

Why it is built this way:

- **Everything goes through UTM.** macOS App Data protection refuses this
  process `ls`, `cat` and `touch` in `~/Library/Containers/com.utmapp.UTM`, even
  unsandboxed; `stat` on a known path works. UTM can do all of it to its own
  folder, so clone, import, drive changes and delete are AppleScript
  (`assets/utm-*.applescript`), and no Full Disk Access is needed.
- **UTM's clone is `copyfile` with CLONE and DATA_SPARSE**: instant on APFS,
  sparse, free until the clone writes. It always assigns a new UUID and renames
  the bundle with the VM.
- **It keeps the MAC** unless UTM's global `IsRegenerateMACOnClone` is on, which
  defaults to off. Two clones with one MAC compete for one DHCP lease, so every
  clone is given `randomMAC()`, and the MAC UTM reports back is checked.
- **Nothing restarts UTM.** A new bundle is written to `vm/staging/` and UTM
  imports it; the install medium is ejected with `update configuration` on the
  stopped VM. Quitting UTM would stop every VM it runs.
- **Decrypted, because ciphertext does not compress.** Windows 11 24H2 turned
  Device Encryption on by itself; the answer file now prevents it at install,
  and sealing decrypts VMs made before that. This also removes the risk of a
  TPM protector locking a clone out.
- **No sysprep.** It re-runs OOBE and risks the `dev` setup, for a machine SID
  nothing standalone uses. Every clone is `WIN11ARM` on the network.
- **Local only.** The Windows licence forbids passing the image to anyone else,
  and every running clone needs its own licence. There is no public download.

The research, and what is measured and what is not, is in
`.plans/2026-09-30_1700_vm-golden-image.md` and [RESULTS.md](RESULTS.md).

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
6 means another mutation holds a [lock](#the-mutation-locks) this one needs,
and the message names which; the holder finishes on its own schedule.

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
| the built ISO | **~4.9 GB** | cloned into the VM (APFS), not copied |
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

### How a binary gets into the guest

`utmctl file push` moves about **0.4 MB/s** (6.9 MB in 17.8 s), so the bytes,
not the calls, were what made a Windows run slow. The guest's own network is
about 250 times faster, but the Mac cannot be the server: its firewall is in
stealth mode and drops connections *to* it, so a guest `curl` to a host HTTP
server hangs. The owner is not asked to change that. So the connection goes the
other way. **The guest serves an SMB share and the Mac connects out to it.**
Nothing on the Mac changes, and nothing is mounted: the client is pure Go
([cloudsoda/go-smb2](https://github.com/cloudsoda/go-smb2), the maintained fork
of hirochachacha/go-smb2, about 0.5 MB of the binary), so no volume appears in
Finder.

`Push` (`internal/utmvm/app.go`, `app_share.go`), for anything of 256 KiB or more:

1. **Find the guest.** First the address cached in `net/`, then
   `utmctl ip-address` with a 3 s deadline (it sometimes hangs), then `ipconfig`
   run through the agent, only if utmctl gave no answer.
2. **Log in** as `dev`/`dev` over NTLMv2, with signing required. Windows 11 24H2
   requires signing by default anyway (`RequireSecuritySignature: True` on
   build 26100), and requiring it on our side refuses a guest-access fallback.
3. **Write** the file into `\\<guest>\irgo-drop` as `irgo-<name>.part`, hashing
   it as it goes.
4. **Move and hash in the guest**, one batch as SYSTEM: `move /y` to the real
   destination (a rename, on the same volume) and `certutil -hashfile … SHA256`.
   Success means the guest's hash equals the local one.
5. **Otherwise fall back** to the zipped `utmctl` push, and say why in one line:
   `pushing 8 MB compressed through utmctl, because the SMB share did not work
   (…)`. The fast path prints `pushed 8 MB over SMB to 192.168.64.40 in 1.05s`.

Measured 30 Sep 2026: 8 MB in 1.1 s instead of 12–14 s, and 49 MB in 1.6 s
instead of 1 min 17 s, most of the second being the guest round trip for the
move and hash ([RESULTS](RESULTS.md#pushes-go-over-smb--measured-30-sep-2026)).

**The share** is opened by `internal/utmvm/assets/file-share.ps1`, run as SYSTEM.
`vm-repair` runs it on an existing VM, and `-share=false` removes it again. New
VMs run it at first logon from the unattend CD. Running it again changes
nothing. It creates:

- `C:\irgo-drop`, with Modify granted to `dev`. The grant is explicit because a
  network logon is not `INTERACTIVE`.
- the share `irgo-drop` on that folder, Full access for `dev` only.
- the firewall rule `irgo-winvm: SMB from the host`: TCP 445, remote address
  `LocalSubnet` (the UTM shared network, where the Mac is `192.168.64.1`), any
  profile, because Windows files that network as Public.

It also turns **off** the `File and Printer Sharing (Restrictive)` rules. Windows
11 24H2 enables them itself when a share is created, open to any address, and
leaves them on after the share is removed (see the traps).
`LocalAccountTokenFilterPolicy` is not set, because it only matters for admin
shares (`C$`). `dev` reaches `irgo-drop` with its filtered network token,
through the grants above.

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
| reading or writing UTM's container | `Operation not permitted` for `ls`, `cat`, `touch`, even unsandboxed (macOS App Data protection); `stat` on a known path works | have UTM do it through AppleScript |
| a bundle written into UTM's folder | UTM only rescans at launch, and restarting it stops every running VM | write it elsewhere and `import` it |
| `utmctl clone` / `duplicate` | keeps the source's MAC unless a global setting (default off) says otherwise; two clones fight over one DHCP lease | set the MAC in the same `duplicate ... with properties` |
| an APFS clone of an immutable file | the clone is immutable too (`copyfile` copies BSD flags), so UTM cannot delete it later | clear the flag on the clone |
| a long comment in `autounattend.xml` | Setup ignored the **whole** answer file and stopped at "Select language settings"; the same element under a one-line comment installed (30 Sep 2026; that comment was the only one with `%` in it, the trigger was not isolated). Unit tests pass either way | keep comments in the answer file short; prove any change to it with an install |
| Windows 11 24H2 left alone | encrypts the disk on its own (Device Encryption), so a copy of it does not compress | `PreventDeviceEncryption` in specialize; decrypt before sealing |
| `utmctl file push` | about **0.4 MB/s**; a 50 MB file took 1 min 17 s even zipped | `Push` goes over the guest's SMB share (see [How a binary gets into the guest](#how-a-binary-gets-into-the-guest)) |
| the guest connecting to a server on the Mac | hangs: the Mac's firewall is in stealth mode and drops incoming connections | connect from the Mac to the guest instead, never ask for a firewall change |
| creating an SMB share on Windows 11 24H2 | Windows enables `File and Printer Sharing (Restrictive) (SMB-In)` itself, open to **any** address, and leaves it on after the share is removed | `file-share.ps1` turns it off both ways, and its own rule allows only the local subnet |

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
