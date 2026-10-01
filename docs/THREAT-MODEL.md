# Threat model

Read this before you enable `-http`. It describes what anyone who can reach the
HTTP port can do to your machine.

**In short:** the tool's job is to run an arbitrary binary on your machine.
Anyone who can call `app-create` can run code of their choice in the Windows
guest, on your Mac, with the guest's network and your host's disk within reach
of what the three steps already touch.

That is not a flaw to fix. It is what the tool is for, and it is why the
defaults are strict.

> [!NOTE]
> This page was written before the HTTP transport existed, so that it shaped the
> design rather than justifying it afterwards.

## What an attacker can do

Assume someone can send authenticated MCP calls to the HTTP transport.

| they call | they get |
|---|---|
| `app-create` with an uploaded `.exe` | arbitrary code execution inside the Windows VM, as `dev`, with a desktop session and network |
| `app-create -gui` | the same, on a visible desktop: anything a person at the machine could do |
| `vm-screen` | a picture of that desktop, including whatever you had open in the guest |
| `vm-delete -force` | a destroyed 45-minute install |
| `iso-delete -force -all` | a destroyed 4.2 GB download, from a source that rate-limits |
| `vm-create -install` | a 45-minute job (seconds plus a boot when it clones the golden image); it no longer restarts UTM |
| `doctor` | your username, your paths, and the versions you have installed |

**The guest is disposable.** It is a throwaway VM with a `dev`/`dev` account, so
code running in it reaches nothing valuable.

**The host is not.** The tool writes under your Application Support directory,
reads binaries you point it at, and drives UTM. An attacker who can make it run
a binary of their choice has code execution in the guest and a lever on the
host through everything the three steps already do.

## What is defended

| threat | defence |
|---|---|
| anyone on the network reaching the port | **Loopback by default.** A wider bind needs an explicit flag, and its help text says what it means |
| a browser on your machine tricked into calling it (DNS rebinding) | **The SDK already rejects** a localhost request carrying a non-localhost `Host`, with 403. Don't set `DisableLocalhostProtection` |
| a page on another origin calling it | Cross-origin protection middleware wraps the handler |
| an unauthenticated caller off loopback | **Authentication is mandatory off loopback**, not a warning. A bearer token, compared in constant time |
| an oversized or truncated upload | A body limit that is never disabled, and the hash verified **before** the file is written |
| two clients installing to one VM at once | A lock. A concurrent mutation is refused with a result that says so, never silently queued |

## What is not defended

- **A caller with the token can do everything.** There are no per-tool
  permissions, and there won't be: a tool that can run one binary can run any
  binary, so splitting permissions would protect nothing.
- **The guest is not hardened.** It logs in as `dev` with the password `dev`,
  on purpose: an unattended install needs a plaintext credential that nobody
  types. That is safe only because the guest is disposable and reachable only
  from your Mac. **Don't copy that account to anything network-reachable.**
- **Uploads are trusted once authenticated.** The hash proves the bytes arrived
  intact, not that they are harmless. Nothing inspects what an uploaded `.exe`
  does.
- **A recycled process id** could report a stranger's process as one of this
  tool's jobs. The code that checks is commented with this.

## What to do instead

**Prefer no inbound listener at all.** Cloudflare Tunnel or Tailscale puts
identity at the edge and keeps the server bound to loopback, so there is no
port to find. Opening a port is the fallback, not the plan.

If you do open one:

1. Bind it deliberately, with `-allow-remote`.
2. Set a token in `IRGO_WINVM_TOKEN`.
3. Remember that the machine on the other end can run code on yours.

## The remote job queue

[`serve`](FOR-AGENTS.md#from-another-machine-linux-windows-github) is the way
to let other machines use the Mac **without** an inbound listener: the Mac
connects out to the Worker and asks for work. What it exposes is different
from `-http`, and narrower.

| someone holding | can | cannot |
|---|---|---|
| a caller token (`JOBS_TOKENS`) | run a binary of their choice in a **fresh clone** of the golden image, with `-gui` and up to an hour; read back its output, a picture of that clone's desktop, and its own jobs' files; cancel its own jobs | see another caller's jobs (404, as one that does not exist), list jobs, touch `irgo-win11`, the golden image or any other VM, choose the VM, run anything on the Mac itself |
| the admin token | everything a caller can, for every job, and list them | anything the runner does |
| the runner token | take jobs and report results, so forge a result or read any queued binary | submit or read jobs as a caller |
| nothing | 401 on every queue path; 503 if the queue is not configured | learn which job ids exist |

**What is defended:**

- **Nothing the caller sends runs on the Mac.** `serve` downloads the binary,
  checks its SHA-256 against the spec, and pushes it into a guest that was
  cloned for this job and is deleted afterwards whatever happened. The Mac
  runs only its own commands; there is no argument a caller can pass that
  reaches a host shell. Arguments go to the program in the guest.
- **Each job gets its own VM**, admitted by the same room check as `vm-create`
  (a job that would leave the Mac too little memory is refused with 7), and
  recorded with an owner, so `status` shows it and `vm-reap` removes one a
  crash left. Its name is `job-<id>`; it can never be the owner's VM.
- **Bounded**: the binary 95 MiB, each result 32 MiB and 64 of them, the log
  2 MiB, the run at most 1 h, 20 live jobs per caller, 500 in all, one job at
  a time per Mac. A job nobody picks up expires in 2 h; one whose Mac dies is
  `lost` after 90 s.
- **No public listing, no public bucket.** Job ids are 128 random bits; the
  bucket `irgo-jobs` has no public access and is reached only through the
  Worker, and a lifecycle rule deletes everything in it after 7 days.
- **One token per caller**, so one can be revoked by removing it from
  `JOBS_TOKENS` without touching anyone else.

**What is not defended:**

- **A caller can still run anything in the guest**, as `app-create` can: the
  guest has a network, and `-gui` a desktop. The clone is disposable; its
  network is not filtered. Give tokens only to people and workflows you would
  let run code on a VM of yours.
- **The guest is as weak as ever** (`dev`/`dev`, RDP on), now reachable by
  any program a caller sends. It is still reachable from the network only
  through the Mac's private UTM network.
- **The runner token is the Mac.** Whoever holds it can take every queued
  binary and answer with any result. Keep it on the Mac alone.
- **The Windows licence.** Every clone is a running copy of Windows and needs
  its own licence. This is for your own machines and your own CI; it is not a
  service to offer to others, and `serve` says so when it starts.
