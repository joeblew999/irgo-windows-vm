# What someone who reaches this port can do

This is written before the port exists, because a threat model produced
afterwards is a justification rather than a design input.

Read it before enabling `-http`. It is short and unpleasant, which is the point.

## The one sentence

**The product is "run this arbitrary binary on my machine".** Anyone who can
call `app-create` can execute code of their choosing on a Windows guest, on your
Mac, with the guest's network and your host's disk within reach of what the
three steps already touch.

That is not a flaw to be fixed. It is what the tool is for, and it is why the
defaults are what they are.

## What an attacker gets, concretely

Assume someone can send authenticated MCP calls to the HTTP transport.

| they call | they get |
|---|---|
| `app-create` with an uploaded `.exe` | arbitrary code execution inside the Windows VM, as `dev`, with a desktop session and network |
| `app-create -gui` | the same, on a visible desktop — anything a person at the machine could do |
| `vm-screen` | a picture of that desktop, including whatever you had open in the guest |
| `vm-delete -force` | destroys a 45-minute install |
| `iso-delete -force -all` | destroys a 4.2 GB download from a source that rate-limits |
| `vm-create -install` | starts a 45-minute job that restarts UTM — which stops whatever else you were running in it |
| `doctor` | your username, your paths, the versions you have installed |

The guest is a throwaway VM with a `dev`/`dev` account, so code running in it is
not the crown jewels. **The host is a different matter**: the tool writes under
your Application Support directory, reads binaries you point it at, and drives
UTM. An attacker who can make it run a binary of their choosing has code
execution on the guest and a lever on the host through everything the three
steps already do.

## What is defended, and how

| threat | defence |
|---|---|
| anyone on the network reaching the port | **loopback by default.** A wider bind needs an explicit flag whose help text says what it means |
| a browser on your machine being tricked into calling it — DNS rebinding | **the SDK already rejects** a localhost request carrying a non-localhost `Host` with 403. Do not set `DisableLocalhostProtection` |
| a page on another origin calling it | cross-origin protection middleware wrapping the handler |
| an unauthenticated caller off loopback | **authentication is mandatory off loopback**, not optional with a warning. A bearer token, compared in constant time |
| an oversized or truncated upload | a body limit that is never disabled, and the hash verified **before** the file is written |
| two clients installing to one VM at once | a lock. Concurrent mutation is refused with a result that says so, never serialised silently |

## What is not defended, and why

- **A caller with the token can do everything.** There are no per-tool
  permissions and there will not be: a tool that can run one binary can run any
  binary, so a permission split would be theatre.
- **The guest is not hardened.** It logs itself in as `dev` with the password
  `dev`, deliberately, because an unattended install needs a plaintext
  credential and nobody types one. That is safe only because the guest is
  disposable and reachable only from your Mac. **Do not copy that account to
  anything network-reachable.**
- **Uploads are trusted once authenticated.** The hash proves the bytes arrived
  intact, not that they are harmless. Nothing inspects what an uploaded `.exe`
  does.
- **A recycled process id** could report a stranger's process as one of this
  tool's jobs. Documented where the check lives.

## The recommendation

**Prefer no inbound listener at all.** Cloudflare Tunnel or Tailscale puts
identity at the edge, keeps the server bound to loopback, and means there is no
port to find. The open port is the fallback, not the plan.

If you do open one: bind it deliberately, set a token, and remember that the
machine on the other end can run code on yours.
