---
title: Threat model
nav_order: 2
parent: Concepts
---

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
| `vm-ssh-create` with a key of theirs | a shell in that VM as `dev`, an administrator, for as long as the VM lives or until `vm-ssh-delete`: it outlasts the call ([below](#ssh-into-a-guest)) |
| `vm-screen` | a picture of that desktop, including whatever you had open in the guest |
| `vm-delete -force` | a destroyed 45-minute install |
| `iso-delete -force -all` | a destroyed 4.2 GB download, from a source that rate-limits |
| `vm-create -install` | a 45-minute job (seconds plus a boot when it clones the golden image); it restarts UTM only if UTM does not answer and no VM is running |
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

[`serve`](../guides/agents.md#from-another-machine-linux-windows-github) is the way
to let other machines use the Mac **without** an inbound listener: the Mac
connects out to the Worker and asks for work. What it exposes is different
from `-http`, and narrower.

| someone holding | can | cannot |
|---|---|---|
| a caller token (`JOBS_TOKENS`) | run a binary of their choice in a **fresh clone** of the golden image, with `-gui` and up to an hour; read back its output, a picture of that clone's desktop, and its own jobs' files; cancel its own jobs | see another caller's jobs (404, as one that does not exist), list jobs, touch `irgo-win11`, the golden image or any other VM, choose the VM, run anything on the Mac itself |
| the admin token | list every job (owner, state, spec, exit code), and read any job's result files (its output, test2json events, pictures of its clone's desktop) | read a job's log or binary, submit, cancel, or anything the runner does |
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

## SSH into a guest

[`vm-ssh-create`](../guides/using.md#ssh-into-a-vm) opens a port in a guest that had
none listening there. What that adds, and what it does not:

**What it opens.** TCP 22 in that one VM, by a firewall rule that allows the
guest's local subnet only, and `sshd` started and set to start at boot. It
stays open across reboots until `vm-ssh-delete` or the VM's deletion. In a
[Linux VM](../guides/using.md#a-linux-vm) there is no firewall and none is added: the
port is open on every address the guest has.

**Who can reach it.** The VM is on UTM's Shared Network: a private subnet on
the Mac (the guest's address is `192.168.64.x`, the Mac's `192.168.64.1`)
behind the Mac's own address translation. So:

- **the Mac can**, and so can anything running on it, including every user of
  the Mac and whatever they run;
- **other VMs on the same shared network should be expected to**: they are on
  the same subnet, and the rule allows that subnet. Every clone and
  `irgo-win11` are on it. Not measured;
- **the rest of your network and the internet cannot start a connection to
  it**: nothing forwards a port from the Mac to the guest, and this tool adds
  no forward. That is how UTM documents the mode; it has not been probed from
  another machine.

**What lets someone in.** The command adds one thing, a public key for `dev`.
It does not write `sshd_config`, so it neither turns password login on nor
off: that stays whatever Windows' sshd defaults to, which is to accept
passwords, and `dev`'s password is `dev` (not checked on a guest by this
command). **Treat the port as open to anyone who can reach it**, exactly as
RDP and the file share already are on this VM. The key is so that you can log
in without typing; it is not what keeps others out. What keeps others out is
that only the Mac and its VMs can reach the port.

**A Linux VM is stricter, and here the key is what keeps others out.** The
script writes `PasswordAuthentication no` and
`KbdInteractiveAuthentication no` into a file of its own under
`sshd_config.d`, and fails unless `sshd -T` then reports passwords refused;
`dev`'s password is locked, so there is none to guess; and root has no key. A
login attempt with a password was refused with `Permission denied
(publickey)` (2 Oct 2026). The account can `sudo` without a password, so
whoever holds the key is root in that VM.

**What is defended:**

- **No private key leaves the Mac, or is read at all.** `-key` takes a public
  key file; anything containing `PRIVATE KEY` is refused before it is parsed,
  and only the one parsed public line is pushed. Nothing generates or stores a
  key pair.
- **The authorized-keys file is writable only by Administrators and SYSTEM**,
  which is also the condition under which sshd will read it. On Linux it is
  the account's own, mode 600 in a 700 directory.
- **A new Linux VM has the port closed.** Ubuntu's image listens on 22 from
  its first boot; `vm-create` turns that off and fails unless nothing is
  listening.
- **One caller per VM.** The command takes the VM's lock and is refused the
  owner's VM by default, like `app-create`; ownership is a label, not
  authentication, as everywhere here.
- **The undo closes it and checks.** `vm-ssh-delete` stops `sshd`, ends its
  sessions, removes the rule and every key, and fails if port 22 still answers
  from the Mac.

**What is not defended:**

- **Whoever can call `vm-ssh-create` gets a shell that outlasts the call.**
  They could already run anything with `app-create`; this makes it
  interactive and persistent. Over `-http` it is one more reason for the token.
- **The host's identity.** A clone makes its host keys when its `sshd` first
  starts; nothing tells the caller their fingerprint out of band, so the first
  connection trusts whatever answers at that address.
- **A golden image sealed with SSH on.** Its clones would all carry the same
  host keys and the same authorized keys. Seal a VM that never had
  `vm-ssh-create` run on it. The Linux seal removes the host keys, every
  `authorized_keys` and `vm-ssh-create`'s configuration, and empties the
  machine-id, so a Linux clone has keys of its own once `vm-ssh-create` makes
  them (two clones' fingerprints differed, 3 Oct 2026); the Windows seal does
  not yet.
- **A Linux VM trusts Ubuntu's archive on its first boot.** The guest agent is
  installed from it by `apt`, with `apt`'s own signature checks and nothing
  more; the cloud image itself is checked against a pinned SHA-256.
