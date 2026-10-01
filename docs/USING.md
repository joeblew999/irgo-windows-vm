# Using it

What each command does, what it exits with, what it costs, and how to keep VMs
cheap with the golden image. Installing and the first run are in
[Getting started](GETTING-STARTED.md). Every flag is in the
[command reference](https://joeblew999.github.io/irgo-windows-vm/reference.html),
captured from the binary at build time: `irgo-winvm help` explains the
sequence, every command documents its flags with `-h`, and no page here lists
flags, so none can go stale.

## The three steps

| step | what it gets you | undo |
|---|---|---|
| **`iso-create`** | the Windows installer | `iso-delete` |
| **`vm-create`** | a VM with Windows on it, answering | `vm-delete` |
| **`app-create`** | your `.exe` running in that VM, output back | `app-delete` |

They run in that order, and each is cheap to repeat: if the work is already
done, it says so and stops. Every undo works from any starting point, and
deleting nothing is success.

- **`iso-create -fetch`** downloads the 4.2 GB `.esd` from Microsoft and masters
  an ISO from it. `iso-delete` keeps the `.esd` unless you pass `-all`.
- **`vm-create`** makes a VM. With a [golden image](#the-golden-image) it clones
  that; otherwise `-install` runs the unattended install. `-vm <name>` names a
  VM of your own, so several can run at once; anyone but the person at the
  terminal must pass it ([Sharing one Mac](#sharing-one-mac)). It first checks
  there is [room](#is-there-room) for another VM.
- **`app-create <exe> [args]`** pushes your program into the VM, runs it, and
  prints what it wrote. `-gui` runs it on the guest's desktop, which anything
  with a window needs ([why](#why--gui-exists)); `-detach` leaves it running
  and returns. Over HTTP, `app-upload` stages a binary for it
  ([For agents](FOR-AGENTS.md#over-http)).

Long work returns a job instead of blocking: `vm-create -install` (about 45
minutes), `iso-create -fetch`, and the `vm-golden-*` commands. The job outlives
the terminal or client that started it; `status` reports what is running, what
finished and how long it took ([how jobs work](ARCHITECTURE.md#jobs)).

A command that changes something takes a lock on what it changes. A second
change to the same VM is **refused, not queued**, with exit code 6 and a message
naming the busy lock; `app-create` on two different VMs runs side by side
([the mutation locks](ARCHITECTURE.md#the-mutation-locks)).

## Commands that change nothing

- **`vm-screen`** saves a PNG of the VM's screen. From the host, a stuck boot
  and a working one look identical; this is the only way to tell them apart.
- **`doctor`** reports what is installed and where, including the log and
  screenshots of this run. It names the installed UTM, the latest stable and
  pre-release on GitHub, and whether an update is available, answering from a
  12-hour cache, else GitHub within 3 seconds, else an older cache marked as
  such; offline it says "cannot tell" rather than failing. It ends with the next
  steps in order, each with its command (`nextSteps`): UTM, the installer (only
  when the VM will be installed from it), the VM (a clone, a pull from the
  private cache, or an install, with the one-time Automation dialog), then
  `app-create`, and how to register the MCP server. Rows that are optional or
  fetched by the command that needs them (Go, the guest tools, `wimlib` and
  `xorriso`) read `optional` or `not yet`, never `MISSING`.
- **`status`** lists every VM with its owner, last use and idle time, then
  long-running jobs, or one job by its id.
- **`report`** prints a redacted diagnostic block for an issue
  ([Reporting issues](FOR-AGENTS.md#reporting-issues)).

**`vm-reap`** does change something: it removes the clones callers left behind
([Sharing one Mac](#sharing-one-mac)).

Two commands, **`glaze-check`** and **`glaze-status`**, work only in a checkout
of this repository, because they build and read `examples/`. They answer "does
glaze work?" and are described in [Testing](TESTING.md#does-glaze-work).
Outside a checkout both exit 2 and say where they looked.

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
| **7** | refused — another VM would leave this Mac too little memory or disk, or that could not be determined |
| **8** | a remote job did not run to the end — cancelled, no Mac took it in time, or its Mac went away |

**1 is your program, not this tool.** The guest's exit code is *not* passed
through: a binary exiting 3 makes `app-create` exit **1**, and the message names
the real code. A failing program and a missing VM must not look the same to a
script.

**4 and 6 are worth retrying.** Windows Update takes the guest agent away for
minutes at a time while the VM is fine. `app-create` already waits and tries to
recover before giving up, which is why it can take several minutes to return 4.
6 means another mutation holds a [lock](ARCHITECTURE.md#the-mutation-locks)
this one needs, and the message names which; the holder finishes on its own
schedule. **7 is worth retrying once a VM stops**: the message gives the
numbers and names the running VMs, and `status` says whose they are.

**8 is worth submitting again.** A [remote job](FOR-AGENTS.md#from-another-machine-linux-windows-github)
that ran exits with the code its Mac's command exited with, on this same
table, and a code this build does not declare is 1. 8 is the job that never
ran to the end: cancelled, expired in the queue, or its Mac stopped reporting.

`-detach` exits 0 once the program is running, since it is for windows nobody
intends to close.

`cmd/irgo-winvm/docs_test.go` reads this table, so a code declared in
`command.Outcomes` and not explained here fails the build.

## What it costs

| step | time | |
|---|---|---|
| `iso-create -fetch` | minutes, and the 4.2 GB `.esd` below | downloaded once; a rebuild from the kept `.esd` needs no network ([measured](RESULTS.md#the-iso-scan-verdict-is-recorded-at-build-time--13-aug-2026)) |
| `vm-create -install` | **about 45 minutes** | an estimate, not a measurement — unattended, you click nothing |
| `vm-create` from a golden image | **about 23 s** to an answering agent | [measured](RESULTS.md#a-vm-of-your-own-in-23-s--measured-1-oct-2026) |
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
| name | `irgo-win11` | `utmvm.DefaultVMName`, the machine owner's; `-vm` overrides, and every other caller must pass it ([Sharing one Mac](#sharing-one-mac)) |
| disk | **64 GiB, sparse** | costs kilobytes until the guest writes; see [what it costs](#what-it-costs) |
| RAM | **8192 MiB** | `vmMemoryMiB`; committed as the guest runs, which is why a 16 GiB Mac holds one ([Is there room?](#is-there-room)) |
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

Binaries reach the guest over an SMB share the guest serves, in about a second
for 8 MB ([how](ARCHITECTURE.md#how-a-binary-gets-into-the-guest)).

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
waiting out its timeout. `vm-repair` also re-applies the desktop settings
described in [Desktop hygiene](TESTING.md#desktop-hygiene) and the SMB share
(`-share=false` removes it).

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
`vm-create -vm g1 -install -golden=false`, then `vm-golden-create -vm g1`.
`-golden=false` installs even when there is a golden image.

`vm-golden-create` refuses `irgo-win11` without `-force`, and refuses when it
cannot find out which VM it was given. The source is left sealed and stopped,
an ordinary VM that `vm-delete` removes. A clone is refused while the golden
image is running (it must stay stopped: UTM will not clone a running VM, and a
golden image that has booted is no longer the one its manifest describes).
Whether there is memory and disk for another VM is `vm-create`'s question,
asked before any of this ([Is there room?](#is-there-room)). `doctor` reports
the golden image from its `golden.json`.

How sealing and cloning work, and why, is in
[Architecture](ARCHITECTURE.md#the-golden-image-sealing-and-cloning).

### A new VM on a machine with no golden image

What `vm-create` does depends on the [private cache](#the-private-r2-cache)
(`VMCreate`, and `internal/utmvm/vm_golden_import.go`):

| cache (`GoldenCacheFromEnv`) | `vm-create` | `vm-create -install` |
|---|---|---|
| none of its variables set | writes the bundle; says the install is next | installs from the ISO, and says how to make the next VM a clone |
| configured (`IRGO_GOLDEN_URL` + `IRGO_GOLDEN_TOKEN`, or the S3 five) | stops and says `-install` pulls it, writing no bundle | pulls the image, has UTM import it as `irgo-golden`, clones it |
| some set, not all | exit 2 naming what is missing, and `-golden=false` | the same |

- **Only with `-install`**, because a pull is minutes (4 min 37 s for 8.4 GB,
  [measured](RESULTS.md#the-real-golden-image-through-the-private-r2-cache--measured-1-oct-2026))
  and `-install` is what makes `vm-create` a job over MCP. Without it nothing is
  written: a VM that exists is never cloned, so a bundle written now would make
  the next `-install` install after all.
- **Half a configuration is an error**, not a quiet 45-minute install for
  someone who meant to pull.
- **The pull runs under the machine lock**, like `vm-golden-pull`, and asks UTM
  again first, so a golden image another process made meanwhile is cloned, not
  pulled over. It is `GoldenPull` itself, licence notice and privacy check
  included, into `golden-pull/`.
- **The bundle's own name is checked** before the import (`plutil -extract
  Information.Name`): UTM registers a bundle under the name in its
  `config.plist`, so an image pushed from another VM would be left registered
  under that name.
- **The pull is kept** in `golden-pull/`. UTM's import is an APFS clone of it,
  so it costs no more space, and after `vm-golden-delete` the next import needs
  no download. `vm-golden-pull -delete -force` removes it. Its `golden.json` is
  copied to the runtime root, where `doctor` reads it.

The decisions are unit-tested with fakes for UTM, the bucket and the lock
(`vm_golden_import_test.go`); the whole path against the real bucket and UTM is
proven by running it.

## The private R2 cache

An optional, **owner-only** cache of the golden image in a private Cloudflare
R2 bucket, so a machine of yours without one downloads it instead of installing
Windows for 45 minutes: 8.4 GB compressed, pulled byte-identical in 4 min 37 s
([measured](RESULTS.md#the-real-golden-image-through-the-private-r2-cache--measured-1-oct-2026));
55 MB/s was measured from Cloudflare's edge.

| command | what it does | undo |
|---|---|---|
| **`vm-golden-push -bundle <dir>`** | uploads a golden bundle directory | `vm-golden-push -delete -force [-id <manifest>]` |
| **`vm-golden-pull`** | downloads it into `golden-pull/` under the runtime data | `vm-golden-pull -delete -force` |

Both are transport only: a bundle directory goes up, the same bytes come down.
`-bundle` must be a copy this process can read, because macOS refuses it UTM's
container ([traps](TRAPS.md#host-utm-and-the-iso)). Over MCP both always run as
jobs. Do not run `-delete` on push while a push to the same bucket is under way
elsewhere: that push's chunks are unreferenced until its manifest is written.

> [!WARNING]
> The Windows licence forbids redistribution (§2c), and every running clone
> needs its own Windows 11 Pro licence (§2d(iv)). The bucket is for **your own**
> licensed machines and CI. Never make it public, never share it or its
> credentials. Both commands print this on every run.

**Two ways to reach the bucket, one format.** With `IRGO_GOLDEN_URL` set, both
commands go through [the Worker](WORKER.md), which has the bucket bound and
needs no R2 keys; this is the way in use, because the owner's Cloudflare token
can deploy Workers but cannot create R2 API tokens. Without it they use R2's S3
API with an access key. Either reads what the other wrote.

**Private by construction.** Before touching an object, push and pull ask the
Cloudflare API for the bucket's two public routes, the r2.dev development URL
(`domains/managed`) and custom domains (`domains/custom`), and go on only when
both were read and both are off. On or **cannot tell** (no token, a 403, an
answer without `enabled`) refuses. `-delete` does not ask: removing the image
is what you would do if the bucket were public.

Through the Worker the check runs when `IRGO_R2_API_TOKEN` is set (with
`IRGO_R2_ACCOUNT_ID` and `IRGO_R2_BUCKET`). Without it there is nothing to ask,
and the commands say so rather than calling the bucket private: the Worker's
binding is not a public route and every request to it needs a token, but the
bucket's own public routes go unchecked.

What is stored in the bucket, and how push and pull verify every byte, is in
[Architecture](ARCHITECTURE.md#the-private-r2-cache-storage-and-transfer).

### Setting up the bucket

Done once, by the owner. This tool creates no Cloudflare resources.

**Through the Worker** (in use): the bucket `irgo-golden` exists with no public
access, the Worker binds it as `GOLDEN` and holds its two tokens
([Deploying it](WORKER.md#deploying-it)). Then `.env.r2` needs:

```
IRGO_GOLDEN_URL=https://irgo-windows-vm.gedw99.workers.dev
IRGO_GOLDEN_TOKEN=<the Worker's GOLDEN_TOKEN: read>
IRGO_GOLDEN_PUSH_TOKEN=<the Worker's GOLDEN_PUSH_TOKEN: write; only where you push or delete>
IRGO_R2_ACCOUNT_ID=<account id>       # these three are optional: with them the
IRGO_R2_BUCKET=irgo-golden            # bucket's public access is checked too
IRGO_R2_API_TOKEN=<Workers R2 Storage Read>
```

A machine or CI job that only pulls gets the read token and nothing else.

**Through S3** (when you have R2 API tokens), in the Cloudflare dashboard:

1. **R2 > Create bucket**, e.g. `irgo-golden`, location automatic, default
   jurisdiction. In its **Settings**, leave **Public Development URL**
   disabled and connect **no custom domain**.
2. **R2 > Manage API tokens > Create API token** for the data, scoped to that
   bucket only: **Object Read & Write** on the machine that pushes, **Object
   Read only** on machines and CI that only pull. The page shows an Access Key
   ID and a Secret Access Key once.
3. A second token for the privacy check: **Admin Read only** (permission group
   *Workers R2 Storage Read*, account-wide). Bucket settings are visible only
   at account level, and this is the narrowest token that can read them. Use
   its **token value**.
4. The account ID is on the R2 overview page.
5. Put them in **`.env.r2`** at the repository root. It is gitignored, and
   `mise.toml` loads it (`_.file`, redacted), so it is in the environment of
   every `mise run` and of a shell in this directory. Outside the repository,
   export the same variables.

```
IRGO_R2_ACCOUNT_ID=<account id>
IRGO_R2_BUCKET=irgo-golden
IRGO_R2_ACCESS_KEY_ID=<from step 2>
IRGO_R2_SECRET_ACCESS_KEY=<from step 2>
IRGO_R2_API_TOKEN=<token value from step 3>
```

A variable that is missing is named, every one at once, with exit 2, for
either way. In CI, set the same variables as repository secrets.

## Sharing one Mac

One Mac is used by several independent callers at once: the owner at a
terminal, agents working in this repository, and agents from other
repositories through the CLI or `irgo-winvm mcp`. Before 1 Oct 2026 they all
defaulted to the owner's VM, queued on its lock and left their binaries in it;
any of them could create clones until the Mac ran out of memory; `app-delete`
emptied every caller's uploads; and `vm-delete` and `app-delete` exited 0 when
`utmctl` itself had failed. What an agent from another repository must do is in
[For agents](FOR-AGENTS.md#sharing-the-mac).

**Who is calling** (`internal/utmvm/owner.go`) is, in order: `-owner` on the
command, else `IRGO_WINVM_OWNER`, else the MCP client's name from its
`initialize` request followed by `/user@host:repo`, else `user@host:repo` for
the person at the terminal (the repository is the nearest directory up from
the working directory holding `.git`). The repository is added to the MCP name
because every Claude Code session calls itself `claude-code`; over stdio the
server runs in the agent's own repository, so two agents from two repositories
differ. It is a label, not authentication: anyone can claim any name. An MCP
job is started with `-owner` set to its caller, because the job is a new
process with no client.

**The owner's VM is not anyone else's default.** Only the person at the
terminal — no `-owner`, no `IRGO_WINVM_OWNER`, not over MCP — gets
`irgo-win11` by leaving `-vm` out, so `irgo-winvm app-create x.exe` keeps
working. Anyone else who leaves it out is refused with exit 2 before any lock
is taken, and told to make a VM of their own:
`irgo-winvm vm-create -vm <name>`, about 23 s from the golden image. Passing
`-vm irgo-win11` explicitly is allowed; that is a choice, not a default.
`glaze-check` without `-windows` touches no VM and is not affected.

**Every VM `vm-create` makes has a record** in `vms/<name>.json`: owner, where
the identity came from, created, last used, and the pid of the `vm-create`
still making it. It is written before the clone or install starts, so a create
that is killed still leaves an owner, and removed again if no VM came of it.
`app-create`, `app-delete`, `vm-repair`, `vm-screen` and `glaze-check -windows`
move "last used"; `vm-delete` removes the record. A VM without a record —
`irgo-win11`, the golden image, anything made before records — has no known
owner and is never reaped. `status` lists every VM UTM knows with its owner,
last use and idle time, and any record whose VM is gone; `doctor` counts the
records.

**`vm-reap`** removes clones nobody is using (`internal/utmvm/vm_lease.go`).
A VM goes only when all of these hold: it has a record; it is not
`irgo-win11`, `irgo-golden` or the golden image's verification clone (decided
without even taking their locks, so the owner's commands are never refused for
it); its creator is not alive; its lock is free (taken without waiting and held
through the delete, so nothing can start using it in between); UTM lists it; and
its last use is older than `-stale` (default 24 h). A record whose VM UTM says
is gone is forgotten. Anything it cannot tell — an unreadable lock, UTM not
answering, an unreadable record — is kept and said. Without `-force` it lists
the verdicts and exits 5, like every destructive command.

**Staging is per caller.** `app-upload` writes `bin/<caller>/<sha256>.exe`,
under a stage lock of that caller's own, so two agents uploading at once do not
refuse each other, and `app-delete` removes only its caller's directory. The
directory name is the identity made readable, with a short hash when it had to
be changed (`claude-code-apple-mac-repo-1a2b3c4d`). Files directly in `bin/`,
from before this, are cleared only by the owner.

**Undo commands tell "no such VM" from "no answer".** `vm-delete` and
`app-delete` succeed with nothing to do only when UTM answered that there is no
such VM (`ErrNoVM`); when `utmctl` could not be asked they fail and say
nothing was deleted.

### Is there room?

`vm-create` asks before it makes or boots a VM (`internal/utmvm/vm_capacity.go`),
under the capacity lock, and answers yes, no or cannot tell; no and cannot tell
refuse with exit 7, the numbers, and the running VMs by name.

- **Memory:** `hw.memsize`, less the memory UTM says each VM that is not
  stopped is configured with (AppleScript `memory of configuration`, which
  needs no Full Disk Access; paused VMs keep theirs), less the VMs other
  `vm-create`s are still making (their records' live pids), less the new VM's,
  must leave `hostMemoryReserveBytes`, **4 GiB**, for macOS and the owner's own
  work. Configured, not current use, because the guest commits it: on 1 Oct
  2026 `irgo-win11` (8192 MiB) had a footprint of 8327 MB, 8051 MB of it dirty,
  with 6.3 GB of the Mac's 7 GB swap in use. So a 16 GiB Mac holds one VM and
  refuses a second; 32 GiB holds three. `-overcommit` skips the memory half for
  a person who accepts swapping: three VMs did boot and pass `glaze-check` on
  16 GiB for a few minutes ([RESULTS](RESULTS.md#a-vm-of-your-own-in-23-s--measured-1-oct-2026)).
- **Disk:** free space on the volume holding UTM's VMs (`statfs`, which works
  there without Full Disk Access) must cover the new VM's growth plus
  `hostDiskReserveBytes`, **10 GiB**, which keeps macOS, whose swap lives on
  that volume, out of its low-space warnings. The growth is `cloneHeadroomBytes`,
  **10 GiB**, for a clone (still an estimate; a clone's boot and one run moved
  `df` by about 1 GiB) and `installHeadroomBytes`, **30 GiB**, for an install,
  or for pulling the golden image from the private cache when there is none
  here (8.4 GB down, then the bundle rebuilt). So a clone wants 20 GiB free and
  an install 40 GiB. An existing stopped VM being booted needs no disk check.

## Where it keeps things

Everything the tool writes goes under
`~/Library/Application Support/irgo-winvm/`, with nothing to configure; VMs live
where UTM keeps them. `doctor` names every path, and the layout is in
[Architecture](ARCHITECTURE.md#runtime-data).
