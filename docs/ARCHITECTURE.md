# Architecture

How `irgo-winvm` is built: its stages, packages, locks and data, and how the
golden image, the private cache and binary pushes work underneath. Read it,
with [Conventions](CONVENTIONS.md) and [Known traps](TRAPS.md), before changing
code. What each command does for its user is in [Using it](USING.md); the
Cloudflare Worker has [its own page](WORKER.md).

## Overview

`irgo-winvm` is a command-line tool for macOS on Apple Silicon. It builds a
Windows 11 ARM64 virtual machine in [UTM](https://mac.getutm.app), runs a Go
`.exe` inside it, and returns the program's output and exit status. It also
serves the same commands to agents over the Model Context Protocol.

The work is split into three stages, run in order. Each is one command with a
matching undo (see [The three steps](USING.md#the-three-steps)):

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
internal/         the CLI's packages (see Packages). internal/ so nothing outside can import them
examples/         conformance, the glaze and native test suite (mise run glaze:mac /
                  glaze:windows), drive, which its interaction tests click and
                  type with, and glaze-all, the demo you drive by hand
site/             renders docs/ into the website
worker/           the Cloudflare Worker: the site, live glaze status, golden-image links
docs/             every document. AGENTS.md and CLAUDE.md at the root only point here
.plans/           work in progress, one file per plan
mise.toml         tools, environment, one-line tasks. They run .bin/irgo-winvm, built by go:tool
mise-tasks/       every task longer than a line, one script each; the path is the name
Casks/            the Homebrew cask, committed by each release
```

### Go modules

There are four modules. The split controls what reaches the shipped binary.

| module | why it is separate |
|---|---|
| root | the tool. `go list -deps ./cmd/irgo-winvm` is what actually reaches a user |
| `examples` | builds against **glaze and native**, the libraries under test, which must never reach the shipped binary |
| `site` | needs a markdown parser the tool has no business carrying |
| `worker` | the [Cloudflare Worker](WORKER.md), on workers-go and built to Wasm by TinyGo |

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
  utm-releases.json   doctor's cache of UTM's latest releases, trusted for 12 hours
  golden-pull/  what vm-golden-pull downloaded: the bundle, its golden.json,
                manifest.json once finished, .parts/ while it is not
```

VMs live where UTM keeps them, because UTM reads nowhere else. Screenshots
chosen as documentation are committed under `docs/screens/`, separate from
`shots/`, so the record is not buried in per-run noise.

## Packages

| package | holds |
|---|---|
| `internal/utmvm` | all three stages and everything they touch. **Do not split it**: iso, vm and app are coupled, and separating them means one reaching into another's paths |
| `internal/command` | which commands exist, and nothing about what they do. Imported by anything that must know the list in-process |
| `internal/mcpserver` | the MCP surface, with **no behaviour of its own** |
| `internal/job` | work that outlives the caller that started it. Not in `utmvm`, because all three stages start such work and its owner must be able to report a **dead** process |
| `internal/glazecheck` | whether glaze works: build `examples/conformance` into a test binary, run it here or through `app-create`, record every test from its test2json events. Needs a checkout of this repository, so it is not in `utmvm`, which must work on a machine that has never seen it |
| `cmd/irgo-winvm` | wiring: one file per concern (`iso.go`, `vm.go`, `app.go`, `doctor.go`, `status.go`, `mcp.go`, `glaze.go`, `help.go`, `report.go`), each command's flags beside its run func; `main.go` holds dispatch and the table joining `command.All` to those funcs; `exit.go` maps errors to exit codes |

### Dependency direction

Dependencies run one way:

- `vm` and `app` use the ISO API; nothing calls back into them from `iso`.
- `mcpserver` depends on `utmvm` and `command`; neither depends on it.
- `doctor` reports on all three stages by calling into them, not by holding its
  own copy of where anything is.

### Adding a command

Two edits: declare it in `command.All` (name, summary, undo, whether it
mutates, the locks it takes), and add a `<name>Flags` func and a `run<Name>`
func in the matching file in `cmd/irgo-winvm`, joined by one row in the table in
`main.go`. The binary panics at start if the two lists disagree, and the MCP
tool, its schema, the usage text and the site's reference follow on their own.

Two commands exist for tooling rather than for people. **`irgo-winvm
commands`** prints one command name per line; the reference generator and the
documentation test both read it, so neither scrapes the usage text or drifts
from what the binary accepts. **`irgo-winvm version`** prints the version
stamped in at build time, or `dev` when built by hand; `doctor` shows the same
value in its first row.

## The MCP server

`irgo-winvm mcp` serves the same commands over the Model Context Protocol, on
stdin/stdout or over HTTP (`-http`, loopback by default). How an agent uses it
is in [For agents](FOR-AGENTS.md); how it is built:

- **Tools are generated from the command list** in `internal/command`, so they
  are the commands and nothing else. A tool's description is the command's
  summary plus what the declaration says an agent must know first: that it
  returns a job, that it needs `-force`, its undo (`describe`). Each tool's
  flags are typed schema properties generated from the same `flag.FlagSet` the
  command line parses (`cmd/irgo-winvm/flags.go`).
- **The server sends instructions** with its initialize response
  (`internal/mcpserver/instructions.go`): the order of the steps, jobs, which
  failures to retry. `irgo-winvm mcp -h` prints them after how to register the
  server, and the site's MCP page captures that, so the three cannot differ. A
  test fails if they name a command or status that does not exist.
- **The server holds no logic.** Behaviour reachable only over MCP is a second
  answer to a question already answered, and nobody tests it: the cycle tests
  and developers both drive the CLI. If a tool needs logic, it goes in `utmvm`,
  where both callers get it. The reasons, and why nothing may print to stdout
  while it runs, are in `internal/mcpserver/doc.go`.
- **The reference resource** `irgo-winvm://reference` is generated from the
  running binary's flag definitions; why it is generated rather than embedded is
  in `internal/mcpserver/resource.go`.
- **Over HTTP** it is stateless Streamable HTTP with cross-origin protection in
  middleware, a 10 s read-header timeout and DNS-rebinding protection left on;
  binding wider than loopback requires `-allow-remote` and `IRGO_WINVM_TOKEN`.
  Read the [threat model](THREAT-MODEL.md) first; the SDK options this depends
  on are in [the roadmap's notes](ROADMAP.md#notes-for-whoever-works-on-the-http-transport).
- **In this repository**, `.mcp.json` registers `irgo-winvm mcp` for any agent
  working here, rebuilding `.bin/irgo-winvm` first. mise's output goes to
  `/dev/null`, because stdout is the JSON-RPC channel. Before 30 Sep 2026 there
  was no `.mcp.json`, so no agent working here was connected to the server.

## Jobs

`vm-create -install` (about 45 minutes), `iso-create -fetch` and
`vm-golden-create`, `vm-golden-push` and `vm-golden-pull` (always) start the
work and return a job id instead of blocking on a connection that would time
out. Over MCP, `glaze-check -windows` is a job too. The work runs in its own
process group and outlives the client that started it; `status` reports what is
running, what finished and how long it took. Whether a job is alive is answered
by asking the operating system, not by reading a file that says so. The same
command with the same arguments returns the job already running instead of
starting a second.

Job records live in `jobs/` under the runtime data directory. Twenty finished
jobs are kept, older ones pruned with their logs; running jobs are never
touched, and `doctor` reports the size. A recycled process id could report a
stranger's process as a job: accepted, and documented where the check lives, in
`job.alive`. Why long work is its own package is in `internal/job/doc.go`.

## The mutation locks

Every command that changes state takes the locks it declares in
`command.All` (`Locks`), from three kinds (`internal/utmvm/lock.go`):

| lock | guards | taken by |
|---|---|---|
| machine (`mutation.lock`) | the media and the golden image | `iso-*`, `vm-golden-*`, and `vm-create` only while it writes or clones the bundle |
| per VM (`mutation-vm-<name>.lock`) | that VM | `vm-create`, `vm-delete`, `vm-repair`, `app-create`, `app-delete`, `glaze-check -windows` |
| stage (`mutation-stage.lock`) | `bin/`, the staged binaries | `app-upload`, `app-delete` |

So `app-create` on two VMs runs side by side, and a second mutation of the same
VM is **refused, not queued**, with exit code 6 and a message naming the busy
lock. The locks are taken in `runTool`, including in a detached job's child,
with a probe before forking; a lock whose state cannot be read refuses. The VM
is read from the command's own `-vm` flag, its name case-folded and a UUID
resolved to the name, so every spelling lands on one lock. A lock is released
when its holder dies (flock on macOS; nothing to lock elsewhere, hence
`lock_darwin.go` and `lock_other.go`), and its file is never deleted: unlinking
a flock file someone holds lets a third process lock a new file of that name.

## Every command logs its exit

Every command an agent can run logs its exit (`msg=exit`, with the code, its
outcome name, its arguments cut to 80 characters, and the error at level ERROR)
through `logExit` in `runTool`, into `logs/`. So a failure reached over MCP is
recorded as well as one on a terminal, and `report` can show the last five
commands; before this, an error reached stderr and nothing else.

## How a binary gets into the guest

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
leaves them on after the share is removed ([traps](TRAPS.md#host-utm-and-the-iso)).
`LocalAccountTokenFilterPolicy` is not set, because it only matters for admin
shares (`C$`). `dev` reaches `irgo-drop` with its filtered network token,
through the grants above.

## The golden image: sealing and cloning

What the golden image is for, and its commands, are in
[Using it](USING.md#the-golden-image).

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

**Cloning** (`CloneFromGolden`) takes the machine lock for the clone itself,
seconds, so it cannot race `vm-golden-delete`, and runs the boot under the new
VM's lock only.

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
  The licence terms are in `.plans/2026-09-30_1700_vm-golden-image.md`.

The research, and what is measured and what is not, is in
`.plans/2026-09-30_1700_vm-golden-image.md` and [RESULTS.md](RESULTS.md).

## The private R2 cache: storage and transfer

How to use and set up the cache, and the licence that keeps it private, are in
[Using it](USING.md#the-private-r2-cache).

**One format, two transports.** Through [the Worker](WORKER.md#the-api)
(`internal/utmvm/vm_golden_worker.go`, when `IRGO_GOLDEN_URL` is set) or R2's
S3 API with an access key (`vm_golden_r2.go`). Both are one `goldenStore`
interface under the same code: the same manifest, the same checks, the same
resume and delta, and every test in `vm_golden_cache_test.go` runs through both.
Either reads what the other wrote: the compressed SHA-256 is the object's
`zsha256` metadata both ways.

**In the bucket**, under `golden/` (`internal/utmvm/vm_golden_cache.go`):

- `chunks/<sha256>.zst`: one 64 MiB region of one file, zstd, named by the
  SHA-256 of its **uncompressed** bytes. Equal regions are one object, and an
  all-zero region is not stored, so holes stay holes when it comes back.
  Regions are at fixed offsets, not content-defined: a disk image's blocks do
  not move.
- `manifests/<sha256>.json`: the files (path, size, mode, the chunk per
  region), each chunk's compressed SHA-256 and size, golden.json, the tool
  version and the date. Named by the SHA-256 of its own bytes, so a manifest
  that does not hash to its name is refused.
- `latest`: the newest manifest's id.

**Push** sends only chunks the bucket lacks (a `HEAD` per region; the
compressed digest is kept in the object's metadata), so a new image moves its
delta. Through the Worker each upload also carries its SHA-256, and R2 refuses
a body that does not match it, so a chunk damaged on the way is never stored.
It checks every chunk the manifest names is there at its recorded size, writes
the manifest, reads it back, then moves `latest`.

**Pull** fetches every chunk through `isoDownload` (the ISO downloader, now
taking a SHA-256 and request headers), from the Worker with the read token or
from a 30-minute presigned URL, and resumes a `.part` with `Range` and renames
only after the compressed SHA-256 matches. A mismatch is fetched once more,
then fails. Chunks already in `.parts/` are not fetched again. It then rebuilds
each file at its offsets, checks each region's uncompressed SHA-256, and reads
the file back for its **tree hash** (the SHA-256 of its regions' SHA-256s, so
40 GB of holes cost a zero check, not a hash). Only then is the bundle renamed
into place and `manifest.json` written beside it, last, as the mark of a
finished pull. Up to `-parallel` chunks move at once (4, about 100 MB of memory
each).

**`-delete` on push** removes the manifest, moves `latest` to the newest
remaining manifest (or removes it), and removes every chunk no remaining
manifest needs, including what an interrupted push left.

**Libraries.** The S3 client is
[aws-sdk-go-v2](https://github.com/aws/aws-sdk-go-v2) `service/s3`, which
Cloudflare documents for R2. It and minio-go both add about 2 MB to the binary
(measured 30 Sep 2026, a stripped HEAD-and-presign program: 7.80 MB against
7.59 MB, from 5.63 MB with neither), but aws-sdk-go-v2 brings only AWS's own
modules, where minio-go brings 18 from other owners. Checksums are sent only
when an operation requires them: by default the SDK uploads as `aws-chunked`
with a trailing CRC, and the chunk's SHA-256 is the check that matters. zstd is
[klauspost/compress](https://github.com/klauspost/compress), already in the
build for go-diskfs.
