# Working on this repository

Read this before writing code, not after. It is the approach, not an index —
any list of where things live goes stale the day someone moves them.

## Three stages, isolated

`iso-create` gets the Windows media. `vm-create` makes a VM from it.
`app-create` puts a binary on that. Each has an undo, and each owns its own
paths and constants in its own files:

- **iso** knows nothing about UTM. It is a download from Microsoft and an ISO
  built with `xorriso`, and it works on a machine that has never had a
  hypervisor. It once called into UTM's bundle directory to decorate an error
  message, which meant fetching media required UTM to be installed.
- **vm** owns UTM entirely: finding it, installing it, `utmctl`, the bundle
  layout, the guest tools.
- **app** drives the guest through `vm`'s `utmctl`, and owns the guest-side
  paths.

Dependencies run one way: `vm` and `app` use the ISO API, nothing comes back.
`doctor` reports on all three and calls into them rather than holding its own
copy of where anything is.

If you are about to make one stage reach into another's paths, stop.

## One way to do each thing

There were four ways to run a binary in the guest, differing only in where it
landed and which session ran it, and every caller had to pick. There were three
answers to where media lives, all live at once. A second route is a second
answer.

## Nothing returns success without checking it did the thing

Nearly every bug here was this: a download renamed without verifying its length,
`ExitCode: 0` when the output could not be fetched, `nil` after five failed
boots, an error value that could never be non-nil, an ISO built and never
checked.

Closing a file you *wrote* can fail, and that is where a full disk shows up.
Closing one you read cannot.

An operation that cannot report failure also cannot be undone, because it does
not know what it did.

## Every action owes an undo

The commands come in pairs. The undo is what lets a failed step be fixed and
re-run, and it must work from any starting point — stopping early because half
the work was already gone leaves the other half behind for good.

Deleting nothing is success, not an error, or the undo cannot be run twice.

## "Cannot tell" is not "safe"

Guards are written `if ok && bad { refuse }`, so a question that cannot be
answered does not refuse — it allows. Every guard here stands in front of
something destructive, so that is backwards.

Answer three ways: yes, no, and *could not determine*. The caller handles the
third explicitly, refusing by default.

## Check before you claim

Everything below was asserted wrongly first, then measured:

- `ln` to an immutable file is `EPERM`, so protecting the ISO silently turned a
  hardlink into a 5 GB copy.
- `rm` on a bundle holding that hardlink fails, so every VM built from a
  protected ISO was undeletable.
- `utmctl delete` prints its failure and **exits 0**.
- `utmctl` will not drop a registry entry whose bundle is gone, so removing a
  bundle behind its back makes a phantom it cannot then recover from.
- `net/http` already rejects a short body, so a length check added to the
  downloader was unreachable — proven by disabling it and watching the test
  still pass.

Grep counts comment mentions as call sites; it gave three wrong answers in one
afternoon. **Use the compiler and the analysers**: delete the symbol, rebuild,
run the tests, put it back if either fails. `mise run go:lint` finds what grep
does not.

And check on every platform. Deleting a function from `sysfile_other.go` passed
every check that was running, because they were all darwin. `mise run go:check`
cross-compiles.

Waiting for CI is **`mise run ci:watch`** — never a hand-rolled `sleep` loop.
There were five of those before the task existed and two were wrong, both in the
same way, and both looked like patience rather than a bug. It watches every
workflow the commit started, not just `check`, and exits non-zero if any failed.
`SHA=<commit>` for one other than HEAD.

## A test that cannot fail is not a test

Every assertion needs a negative control: **break the thing, watch the test go
red, put it back.** Ten seconds, by hand, when you write the test. There was a
mise task automating this across eight mutations; it was fifty lines of shell
matching exact source text, so it broke on the first rename and once left a
mutated file in a commit. The habit catches what matters — a test born vacuous —
and the machinery only caught tests weakened later, which did not happen.

This is not theory. A test for the scan cache passed against a mutation that
disabled the check it was testing, because the case it built also changed the
other field. And a test for "the build records its verdict" is marked as not
proving that, because deleting the build's call leaves it green.

If a property can only be verified by measurement, record the measurement in
`RESULTS.md` with a date rather than writing a test that looks like coverage.

## Say what is happening, and where

A command that prints nothing for fifty seconds is indistinguishable from one
that has hung. Announce each step *before* doing it, name every path, and print
elapsed time. "Not found" without a location cannot be checked.

That is how the 77-second ARM64 scan was found: it was always there, and nothing
said so.

## The comments are findings

Long comments record what cost hours and is not recoverable from the code: why
the display is `virtio-ramfb-gl`, why ESD image 3 needs `--boot`, why
`utmctl suspend --save-state` must never be called, why `%q` must not be
re-escaped for AppleScript.

Move them with their code. Do not compress or tidy them. If one is wrong, fix
the fact — do not delete the explanation. When you correct a measurement, look
for the other copy.

## Verify against the VM

Unit tests cover the ISO stage well, the VM and run stages only at the edges.
Anything touching a real guest is proven by running it, and those paths fail
silently.

Use a disposable VM. Running a binary pushes it into the guest and executes it,
which is a mutation; losing a 45-minute install to a test is not a trade worth
making.

## A glaze or native bug is fixed at crgimenes

Non-negotiable, and the reason this project exists. A bug worked around in an
example still ships to everyone using those libraries, and the workaround hides
it. `UPSTREAM.md` is the ledger.

## Where things go

One place, fixed, nothing to configure:

```
~/Library/Application Support/irgo-winvm/
  media/    the ISO, the .esd it was built from, and scratch
  bin/      binaries staged into a VM
  logs/     every command, appended across runs
  shots/    a screenshot per stage of every run
  jobs/     long-running work, so a 45-minute install survives a disconnect
```

VMs go where UTM keeps them, because UTM reads nowhere else. Committed
screenshots — evidence chosen for documentation — are `docs/screens/`, kept
apart from `shots/` so the record does not drown in the noise.

In the tree, each top-level directory has one job:

```
cmd/irgo-winvm/   the CLI: flags, handlers, exit codes. The one thing users install
internal/         the CLI's packages (below). internal/ so nothing outside can import them
examples/         the four programs run on Windows and the Mac: probe, verify,
                  verify-events, glaze-all. mise run glaze:mac / glaze:windows
site/             renders docs/ into the website
docs/             every document. AGENTS.md and CLAUDE.md at the root only point here
.plans/           work in progress, one file per plan
mise.toml         tools, environment, one-line tasks. They run .bin/irgo-winvm, built by go:tool
mise-tasks/       every task longer than a line, one script each; the path is the name
```

and one rule decides the packages:

| package | holds |
|---|---|
| `internal/utmvm` | all three stages and everything they touch. **Do not split it** — iso, vm and app are coupled, and separating them means one reaching into another's paths |
| `internal/command` | which commands exist, and nothing about what they do. Imported by anything that must know the list in-process |
| `internal/mcpserver` | the MCP surface, and **no behaviour of its own** |
| `internal/job` | work that outlives the caller that started it — a 45-minute install an MCP client cannot wait on. Not in `utmvm` because all three stages start such work, and whoever owns it must be able to report a **dead** process |
| `internal/glazecheck` | does glaze work: build the four examples, run them here or through app-create, record the verdict. Needs a checkout, so it is not in `utmvm`, which must work on a machine that has never seen this repo |
| `cmd/irgo-winvm` | wiring: flags, handlers, exit codes |

Three Go modules, and the split is load-bearing rather than organisational:

| module | why it is its own |
|---|---|
| root | the tool. `go list -deps ./cmd/irgo-winvm` is what actually reaches a user |
| `examples` | they build against **glaze and native**, which are the things under test and must never reach the shipped binary |
| `site` | needs a markdown parser the tool has no business carrying |

Check it with `go list -deps`, not by reading imports. The site module requires
goldmark, its extensions and the chroma highlighter, and nothing else, which is
why the generated MCP page is captured from
the binary rather than produced by importing the server — importing it would
drag the protocol SDK's dependency graph into the documentation generator.

`mcpserver` depends on `utmvm` and `command`; neither depends on it.
Behaviour that exists only when driven over MCP is a second answer to a question
already answered, and it is the one nobody tests — the cycle tests drive the
CLI and so does a developer. If a tool needs logic, the logic goes in `utmvm`
where both callers get it.

It needs macOS on Apple Silicon, and UTM, which `vm-create` installs from its
signed `.dmg` if it is missing. `wimlib` and `xorriso` are installed by
`iso-create` and removed by `iso-delete`, only when building media from scratch.

One thing nothing can install for you: macOS asks, once, in a dialog, whether
this may control UTM. `vm-create` checks that before doing anything expensive,
because without it a boot cannot be driven and the failure arrives forty minutes
into an install as a timeout that mentions nothing about permissions.

## The commands

| step | what it gets you | undo |
|---|---|---|
| **`iso-create`** | the Windows installer | `iso-delete` |
| **`vm-create`** | a VM with Windows on it, answering | `vm-delete` |
| **`app-create`** | your `.exe` running in that VM, output back | `app-delete` |

They run in that order, and each is cheap to repeat: if it is already done, it
says so and stops. The undo is what lets a step that failed be cleaned and
re-run, rather than leaving the machine somewhere between two states.

### A VM in minutes: the golden image

| step | what it gets you | undo |
|---|---|---|
| **`vm-golden-create -vm <disposable>`** | that VM sealed into `irgo-golden`, which `vm-create` then clones | `vm-golden-delete` |

Installing Windows is the 45 minutes, and nothing used to keep its result. So
make one VM the slow way under a throwaway name (`vm-create -vm g1 -install
-golden=false`), seal it once, and from then on **`vm-create -vm <name>`
clones the golden image and boots the clone** — no media, no install, and no
UTM restart, so every other VM keeps running. Each developer or agent takes its
own name and gets its own VM; `vm-delete` removes it. With no golden image,
`vm-create` says it is falling back to a full install and does that;
`-golden=false` installs even when there is one.

What sealing does, in the guest as SYSTEM (`assets/vm-golden-seal.ps1`, one
step at a time, the disk's allocation printed after each): BitLocker off and
`PreventDeviceEncryption` set, hibernation off, `DISM
/StartComponentCleanup /ResetBase`, TRIM. Then Windows shuts itself down, UTM
clones it as `irgo-golden` keeping only the NVMe system disk (the install,
answer-file and guest-tools CDs are dropped), a throwaway clone of that is
booted to prove it answers and deleted, and a manifest — Windows build,
WebView2 version, sizes, seal and boot times — goes to `golden.json` under the
application root. `doctor` reports both.

Why it is built this way, each measured on 30 Sep 2026
(`.plans/2026-09-30_1700_vm-golden-image.md`):

- **Windows 11 24H2 encrypts the disk on its own**, and ciphertext does not
  compress: every used block of `irgo-win11`'s disk was XTS-AES. The answer file
  now sets `PreventDeviceEncryption` in specialize, and sealing decrypts VMs
  made before that.
- **This process cannot read or write UTM's container.** macOS App Data
  protection refuses `ls`, `cat` and `touch` in
  `~/Library/Containers/com.utmapp.UTM` even unsandboxed; `stat` on a known path
  works. So every change to a bundle goes through UTM's AppleScript
  (`assets/utm-clone.applescript`), which can, and needs no Full Disk Access.
- **UTM's clone is `copyfile` with CLONE and DATA_SPARSE**: instant on APFS,
  sparse, and costing nothing until the clone writes. It always gives a new
  UUID, but keeps the MAC unless a global UTM setting that defaults to off says
  otherwise — and two clones with one MAC fight over one DHCP lease. So every
  clone gets `randomMAC()`, and the MAC UTM reports back is checked.
- **The clone copies BSD flags**, so a bundle holding the immutable media's
  inode would clone into one UTM cannot then tidy (`EPERM`). Sealing releases
  that one known file for the clone and restores it.
- **No sysprep.** It would re-run OOBE and risk the `dev` setup, for a machine
  SID nothing standalone cares about. Every clone is `WIN11ARM` on the network.
- **Only locally, only for yourself.** The Windows licence forbids passing the
  image to anyone, and every running clone needs its own licence. There is no
  public download of it and there will not be one.

`vm-golden-create` refuses `irgo-win11` without `-force`, and refuses when it
cannot find out which VM it was given. Over MCP it is always a job: sealing is
many minutes even when there is nothing to decrypt.

### Several VMs at once: the locks

A mutation is refused, never queued, while another holds a lock it needs (exit
6). There are three kinds (`internal/utmvm/lock.go`), and each command declares
which it takes in `internal/command`:

| lock | guards | taken by |
|---|---|---|
| machine | the media and the golden image | `iso-*`, `vm-golden-*`, and `vm-create` for the seconds its clone takes |
| one per VM | that VM | `vm-create`, `vm-delete`, `vm-repair`, `app-create`, `app-delete`, `glaze-check -windows` |
| stage | `bin/`, the staged binaries | `app-upload`, `app-delete` |

So `app-create` on two VMs runs side by side, and the same VM twice is refused.
They are `flock`s under the application root, released when the holder dies,
and never deleted: unlinking a lock file someone holds lets a third process lock
a new file of the same name.

The one full-install step that still restarts UTM (to make it see a bundle
written to its folder) refuses while any other VM is running or paused, and when
`utmctl list` cannot answer. Quitting UTM stops every VM it runs; before
30 Sep 2026 nothing in the binary checked.

Three more change nothing: **`vm-screen`** photographs the VM, **`doctor`**
reports what is here, and **`status`** lists long-running work — what is still
going, what finished, and how long it has been. Whether a job is alive is
answered by asking the operating system, not by reading a file that says so.

Two more work only **in a checkout of this repository**, because they build
and read `examples/`: **`glaze-check`** builds the four programs and runs them —
natively on this Mac, or with `-windows` on the VM through `app-create` — and
records the verdict in [GLAZE-STATUS.md](GLAZE-STATUS.md), a generated file:
when, which commit, which glaze and native were actually built against (from
`go list -m`, so a go.work pointing at local clones is named with the clone's
branch and commit), and each program's PASS or FAIL with the first line that
said FAIL. The Mac and Windows sections are separate, so one run never erases
the other's answer. Every run also keeps its complete output as
`glaze-<target>-<stamp>.log` in the log directory `doctor` names, and prints
that path first and last. **`glaze-status`** prints the recorded file and says,
for each section, whether it still describes the tree — current, stale (and
why), or cannot tell. Outside a checkout both exit 2 and say where they looked.
They are in the shipped binary anyway because an MCP tool can only be a command,
and the agent most likely to ask "does glaze work on Windows?" is the one
`.mcp.json` starts inside this repository; the reasoning is in
`internal/glazecheck/doc.go`. glaze and native still never reach the binary:
the examples are built by running `go`. Over MCP, `glaze-check -windows` is a
job, like `vm-create -install`: call `status`, then `glaze-status`.

The calls that take a long time — `vm-create -install`, `iso-create -fetch`
and `vm-golden-create` — start the work and hand back a job id rather than
blocking for 45 minutes on a connection that will time out. The work outlives the client that
asked for it; `status` is how anyone finds out what happened.

Your `.exe` is anything built with `GOOS=windows GOARCH=arm64 CGO_ENABLED=0`.
That is the whole contract.

Every command that takes flags explains itself with `-h`, and `irgo-winvm help`
explains the sequence. No document here lists flags, so none can go stale about
them: the [command reference](https://joeblew999.github.io/irgo-windows-vm/reference.html)
is captured from the binary at build time.

### For an agent: `mcp`

An agent working **in this repository** gets it automatically: `.mcp.json`
registers `irgo-winvm mcp`, rebuilding `.bin/irgo-winvm` first (mise's output
goes to /dev/null, since stdout is the JSON-RPC channel). Before 30 Sep 2026
there was no `.mcp.json`, so the server existed and no agent here was connected
to it.

**`irgo-winvm mcp`** serves the same commands over the Model Context Protocol,
on stdin and stdout or over HTTP (`-http`, loopback by default). It is the point
of the repository pointed at its most likely user: an agent writing a Go desktop
app on a Mac cannot find out whether it works on Windows, and this lets it ask,
get a real answer from real Windows, and see the screen when the answer is that
it hung. The tools are generated from the command list, so they are the commands
and nothing else.

Over HTTP, an agent that has just cross-compiled a `.exe` and has no shared
filesystem with the Mac can send it in chunks with **`app-upload`** — staged
content-addressed under `bin/`, verified by SHA-256 before it is committed —
then hand the staged path to `app-create`. A wider bind than loopback needs
`-allow-remote` and `IRGO_WINVM_TOKEN`; read [the threat model](THREAT-MODEL.md)
before opening one.

## What it exits with

`utmctl` exits 0 when it fails, which this repository has been bitten by more
than once. So this tool is the only honest signal a caller gets, and it says
something specific:

| code | meaning |
|---|---|
| **0** | it worked — including `-h`, and an undo that found nothing to undo |
| **1** | your program ran and failed |
| **2** | the command was called wrongly |
| **3** | that VM does not exist |
| **4** | the VM is there, the guest agent is not answering |
| **5** | refused — a destructive command without `-force` |
| **6** | refused — another mutation is in progress |

**1 is your program, not this tool.** The guest's own exit code is *not* passed
through: a binary exiting 3 exits `app-create` **1**, and names its real code in
the message. That is deliberate — a failing program and a missing VM must not
look the same to a script.

**4 and 6 are the ones worth retrying.** Windows Update takes the agent away
for minutes at a time; the VM is fine and will answer again. `app-create`
already waits and tries to recover before giving up, which is why it can take
several minutes to reach that code. 6 means another mutation holds a lock this
one needs — the message names which, the VM or the machine — and the holder
finishes on its own schedule, so waiting changes the answer.

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

## Linux is out of scope

This repository is the Windows VM system: Windows is the platform whose
behaviour cannot be checked by reading the code from a Mac, and everything in it
— the answer file, the ISO mastering, the guest agent, the session model — is
Windows-specific. Linux would need its own guest image and its own path, and it
is not built here. The `linux` builds in CI exist only so the tool compiles for
a developer on another OS, not because it can drive a Linux guest.

## What the VM is, and who you are inside it

Fixed, and not settable by a flag. Changing one means editing `setDefaults` in
`internal/utmvm/vm_create.go` — there is deliberately no way to ask for a different
shape, because a VM that differs between two machines is a result that cannot
be compared.
Nothing in the tree records *why* these particular numbers, only that they are
fixed.

| | value | |
|---|---|---|
| name | `irgo-win11` | `utmvm.DefaultVMName`; `-vm` overrides, for a disposable VM |
| disk | **64 GiB, sparse** | costs kilobytes until the guest writes; see [what it costs](#what-it-costs) |
| RAM | **8192 MiB** | |
| CPUs | **4** | `CPU` is `host` — the guest sees the Mac's cores |

The guest logs itself in as **`dev`**, an administrator, with the password
**`dev`** in plaintext in `internal/utmvm/assets/autounattend.xml`, auto-logon enabled
for 999 logons, and RDP switched on.

That password is not a leaked credential and is not to be "fixed". Setup needs
it in plaintext to create the account and log in with nobody typing, which is
the entire point of an unattended install. It guards a throwaway VM with no
inbound route from anywhere but this Mac, and it is deliberately obvious so
nobody mistakes it for a secret that matters. **Do not copy that answer file to
anything reachable from a network you do not control.**

### Why `-gui` exists

The QEMU guest agent runs as `NT AUTHORITY\SYSTEM` in **session 0**, which has
no window station. Anything that opens a window fails there — and fails
confusingly: glaze reports it as `webview2: environment/controller creation
failed`, which reads like a missing WebView2 runtime and is not. The runtime was
present and healthy (151.0.4129.78) while that failure persisted.

`-gui` routes through a scheduled task with `/it`, which runs as the logged-in
user in their session, which has a desktop. The auto-logon above is what
guarantees such a session exists. It also stages the binary in `C:\Users\Public`
rather than `C:\Windows\Temp`, because the interactive user must be able to
execute it.

So: headless work needs no flag, anything with a window needs `-gui`, and that
split is enforced by the operating system rather than chosen here.

### When `-gui` stops working on an old VM

Windows expires local passwords after 42 days. AutoLogon then stops, so there
is no desktop session and every `-gui` run has nowhere to go. An interrupted
WebView2 update can also leave its registration naming a deleted folder, and
glaze then reports WebView2 as missing (glaze#34). `irgo-winvm vm-repair -reboot`
fixes both as SYSTEM, and `app-create -gui` refuses up front, naming the
problem, when nobody is logged in, instead of waiting out its timeout. VMs made
now have a `dev` password that never expires.

## The four programs it runs

Split by what a capability *needs*, with exactly one owner each. A capability
probed in two places is two things to fix when upstream changes and two reports
to reconcile when they disagree.

| program | library | needs |
|---|---|---|
| `examples/probe/` | native — clipboard, power, single-instance, mmap | headless; runs under the guest agent in session 0 |
| `examples/glaze-all` | native + glaze — openurl, tray, no-capture, menu, file dialogs, app icon | `-gui` |
| `examples/verify` | glaze — the portless `app://` path | `-gui` |
| `examples/verify-events` | glaze — the Events bridge | `-gui` |

`glaze-all` opens its window and waits by default, because it is an example
before it is a test. `-probe` is the unattended report, and `glaze-check`
passes it.

## Things that cost hours

Each of these fails silently. UTM rejects a bad config with one generic *"cannot
import this VM"* that names no field; a wrong boot command produces a prompt
nobody sees; a truncated ISO produces a VM that will not boot.

One line each, deliberately. The rows about `utmctl` are **defects in UTM**, not
facts of life, and they are written up properly in
[UPSTREAM.md](UPSTREAM.md#utm) — severity, reproduction and status. Keep the
detail there and the reminder here; two full copies is the drift this file warns
about three sections above.

| trap | what happens |
|---|---|
| `virtio-gpu-pci` display | no framebuffer on aarch64, no legacy VGA — guest boots **invisibly** and looks hung |
| VirtIO system disk | Windows ARM64 has no inbox driver; Setup reports no drive found. Use **NVMe** |
| `virtio-net-pci` without guest tools | no inbox driver — **no network at all** in the guest |
| missing `PS2Controller` | non-optional decode, no default; whole document rejected |
| `UsbBusSupport: "USB3_0"` | the enum is `"2.0"` / `"3.0"` |
| `CPUFlags` | the keys are `CPUFlagsAdd` and `CPUFlagsRemove` |
| reading UTM's schema from `main` | `main` was v5.0.4 while the app was v4.7.5, and they disagree. Read the **tag** |
| `gh run list --commit` with a short SHA | matches the **full 40 characters only**. An abbreviated one returns an empty list, not an error — indistinguishable from "not started yet". Use `mise run ci:watch` |
| Windows ISOs are **UDF**, not ISO9660 | `install.wim` exceeds ISO9660's 4 GB limit, so ISO9660 readers fail on every path |
| answer file on a FAT disk | Setup ignores it and runs interactive. Use an ISO9660 **CD** |
| ISO padded past its declared volume size | mounts fine on macOS, ignored by Setup. Trim to the PVD size |
| Joliet disabled | `autounattend.xml` becomes `AUTOUNAT.XML`, which Setup never looks for |
| El Torito marked BIOS (`-b`) | correctly sized, correctly named, **does not boot**. UEFI needs `-e` |
| `start utm-guest-tools-*.exe` | `start` does not expand wildcards; the installer silently never runs |
| `utmctl start` then keystrokes | headless VM has no display, and UTM routes input through it — keystrokes vanish |
| driving a boot on a VM that is already running | it may be a working desktop, not a UEFI shell. Keystrokes land in whatever has focus — see `docs/screens/vm/running-no-agent.png`, three Bing tabs searching for the EFI path |
| `utmctl delete` | prints its failure and **exits 0** |
| `utmctl exec` | never returns the guest's output and always exits 0. Everything that needs output goes through a batch file that captures it to a file the host then pulls |
| `utmctl exec` with a whole command line as one string | the agent looks for a file by that entire name and answers "No such file or directory" — indistinguishable from a dead agent |
| `cmd` `del` on a glob matching nothing | **exits 1**. So an undo succeeds while there is something to undo and fails the moment there is not |
| `dir` and `del` disagree on the message | `dir` says "File Not Found", `del` says "Could Not Find". Handling one and not the other prints the other's error text as if it were a filename |
| `utmctl suspend --save-state` | **reports success and power-cuts the guest.** No state file, VM left `stopped`, guest's next boot goes through "Diagnosing your PC". Use plain `suspend` |
| `ln` to an immutable file | `EPERM` — so protecting the ISO silently turns a hardlink into a 5 GB copy |
| `rm` on a bundle holding that hardlink | `EPERM`, directory left behind. Clear the flag first, restore it after |

### In the guest programs

| trap | what happens |
|---|---|
| every package defines its **own** `ErrUnsupported` | none wrap `errors.ErrUnsupported`, so a check against that alone matches nothing and a platform behaving as documented reports **FAILED** with a non-zero exit. `glaze.SetAppIcon` is unsupported on Windows by design — the platform this exists to test |
| `tray.Run` **blocks**, driving the event loop until `Stop` | waiting on it deadlocks. Post it and leave it; `Stop` is safe from any goroutine |
| the tray started **before** the window | glaze's `New` runs a temporary `[NSApp run]` that ends only when `applicationDidFinishLaunching` fires — once per process. A tray started first consumes it and `glaze.New` blocks forever, with no window and nothing printed |
| `menu.Set` with no `Options.Window` | required on Windows (the HWND); it returns an error naming it, on the one platform that matters here |
| `menu.Set` with `Options.Dispatch` set **before** `Run` | Set blocks until its UI work has run, and nothing drains the queue until the run loop starts |
| `file://` URL built by concatenation | Windows paths are `C:\dir`; the URL wants `file:///C:/dir`. Without the leading slash `net/url` writes `file:C:/dir` and ShellExecuteW rejects it |
| `openurl.Open != nil` as a capability check | a function value is never nil. `go vet` says so outright — the check checked nothing |
| an absolute `app://` URL for a sub-resource on Windows | glaze emulates the scheme with a virtual host, so the document loads from `https://app.localhost/` and an absolute `app://` sub-resource names a scheme WebView2 has never heard of. Fails silently: no error, no console message, no stylesheet |

## Tasks

`mise tasks` lists them. `mise.toml` holds the tools, the environment and the
one-line tasks; anything longer is an executable script in `mise-tasks/`, where
the path is the name (`mise-tasks/go/check` is `go:check`), `#MISE` lines at the
top declare its description and dependencies, and its findings are comments
beside the lines they explain. Until 30 Sep 2026 all of it was shell inside TOML
strings: 516 lines, where no editor, `bash -n` or shellcheck could see it.

A task exists only for what the binary cannot do on its own: checks, builds,
the glaze gates, upstream work, the create-and-delete cycles. No task just wraps
a command — call the command.

The reasons behind the one-liners, which have nowhere else to live:

- **The tools are pinned in `mise.toml` and nowhere else**, so CI installs what a
  maintainer has; `jdx/mise-action` reads it. Every `go.mod` says `go 1.27.1` to
  match. Reading the Go version from `go.mod` is deprecated in mise (removed in
  2026.11.0), hence the pin.
- **`go:tool` is the one build of the tool.** Every task runs `.bin/irgo-winvm`
  rather than `go run ./cmd/irgo-winvm`, and `.bin` is on `PATH` in this
  directory, so `irgo-winvm doctor` works by hand. `sources` and `outputs` let
  mise skip the build when nothing under `cmd/` or `internal/` changed — which
  is also how it went wrong: from 977136e to 30 Sep 2026 the task built
  `./irgo-winvm` while `outputs` named `.bin/irgo-winvm`, so every task and
  `.mcp.json` ran whatever stale binary `.bin` happened to hold (in a fresh
  clone, none). mise said so on every run — `did not generate expected output`
  — and nobody read it. After editing `cmd/` or `internal/`, run any task or
  `mise run go:tool` before calling the binary by hand.
- **`go:lint` pins `GOOS=darwin`**, so a Mac and the Linux CI runner lint the same
  code. The tool only runs on macOS; on Linux `statfsAvailable` is a stub that
  always errors, and staticcheck rightly calls `fErr == nil && free < n` in
  `iso_create.go` never true (SA4023). That failed CI on every push from e1a4243
  to 79f8a55 while the same command passed on the Mac that wrote it.
- **`site:build` renders the markdown, never hand-written pages**, so the site
  cannot drift from the repository: if a page is wrong, the markdown is wrong.
  **`site:serve` builds and serves in one command** on purpose — a separate
  server can be pointed at a stale `dist` from an earlier run, and checking a
  site that no longer matches the markdown is worse than not checking it.
- **`vm:shots` copies, so nothing is copied by hand.** The tool photographs every
  stage into `shots/` as it runs, but those names carry a timestamp, so no
  document can point at one; this copies the newest of each stage into
  `docs/screens/vm` under its stage name, which is what the docs reference.
- **`UPSTREAM_DIR`** is where the local glaze and native clones live, read by the
  `upstream:*` tasks. A bug in either is fixed there, not worked around here
  (`UPSTREAM.md`). It was restored from the `mise.toml` deleted in e533764.

## Do not

- Split the package up. Its parts are coupled.
- Touch the assets, the answer file or the plist template without running a real
  install. UTM rejects a bad config with one generic *"cannot import this VM"*
  that names no field.
- Export anything nothing uses.
- Put logic in a task, in `mise.toml` or `mise-tasks/` alike. Tasks call the
  binary; if a task needs to do more than that, it belongs in the binary.
- Land a refactor in one commit. One concern per commit, each verified.
