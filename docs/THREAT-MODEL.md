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
| `vm-create -install` | a 45-minute job that restarts UTM, stopping anything else you run in it |
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
