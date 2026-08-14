# Roadmap

**This page is intent, not record.** Every other page here states what is true
now; this one states what is meant to happen next, and it will be wrong in the
ordinary way plans are wrong. What has actually been measured is in
[Results](results.html); what was found upstream is in [Upstream](upstream.html).

It is the working plan in full rather than a summary of one, because a summary
is worth nothing to whoever picks this up next — including a later session of
the agent that wrote it. Everything needed to continue is here.

It lives in `docs/` rather than the repository root on purpose. The check that
keeps documentation honest — every `` `irgo-winvm <command>` `` named in root
markdown must exist in the binary — would fail on a roadmap, because naming
things that do not exist yet is exactly what a roadmap is for. Verified both
ways: this file may name a future command, and the same sentence at the root is
refused.

Last revised 14 August 2026.

---

## Where it is

The three steps work and are used. An MCP server drives them, so an agent
writing a Go desktop app on a Mac can find out whether it works on Windows.

| | state |
|---|---|
| ISO, VM, app — the three steps and their undos | working |
| `irgo-winvm mcp` over stdio, tools generated from one list | v0.3.0 |
| long calls return a job that outlives the client; `status` | v0.3.1 |
| typed flags in the schemas, generated from the CLI's own definitions | v0.4.0 |
| the server serves its own reference as an MCP resource | `c155a99` |

Nine tools. `vm-screen` returns the PNG itself. Failures carry
`{code, status, meaning, retryable}`. The page at [MCP](mcp.html) is generated
by listing a live server, so it cannot describe a tool that does not exist.

---

## Done, with what proved it

### Run it for real — 14 Aug 2026, `075fdd5`

A real client spawning the real binary against the VM on this machine: nine
tools listed; `doctor` returned **as a result**, which is what proves
`utmvm.Capture` works where a unit test could not; `vm-screen` returned a
4,447,777-byte PNG of a live Windows desktop, **looked at**; `iso-create -fetch`
detached and survived the client exiting; `app-create` ran a probe on
windows/arm64 — 5 capabilities OK, 3 missing upstream. `mise run app:test`
re-run green. Measurements dated in [Results](results.html).

### Typed schemas — v0.4.0, `cd4d203`

The `FlagSet` is the source and the schema is generated from it by `VisitAll`,
so the CLI and the schema cannot disagree by construction — not because a test
compares two lists, but because there is one registration and both read it.
8 commands, 18 flags hoisted. `reference.md` byte-identical before and after,
checked after the first command and again after the remaining seven. Verified
against the real VM with a client calling `{"vm":"irgo-win11"}` as a typed
property.

### The documentation resource — `c155a99`

`irgo-winvm://reference`: every command, flag, default and the outcome table,
generated from the same `FlagSet`s the command line parses. Verified against the
real binary — one resource, 4,126 bytes.

Embedding `llms-full.txt` was checked and is impossible: `go:embed` needs the
file when the package compiles and it is generated into gitignored `site/dist`,
so a fresh clone would not build. Committing it makes a second copy that goes
stale; a placeholder overwritten at release means a development build serves an
empty document and says nothing about it; fetching from the site makes an
offline server useless. The binary serves what it knows and names the URL for
the rest.

### Job pruning — `1419a18`

Twenty finished jobs kept, logs pruned with their records, running jobs never
touched however many there are, size reported by `doctor`. `jobs/` had been
growing a record and a log per run forever, which is a disk leak in a tool that
already asks for 33 GB, and it broke the rule that every action owes an undo.

---

## Next: the server over HTTP

Remote access is not built. It is the largest remaining piece and it is
**remote code execution by design** — the product is "run this arbitrary binary
on my machine" — so the threat model is written before the port exists.

**No longer gated on the long job.** The original condition was "phase A works
and has run against a disposable VM", and phase A has now run against a real VM.
What is still missing is the 45-minute *duration*, and nothing in remote access
depends on it.

### What reading the SDK source changed

`StreamableHTTPOptions` in `mcp/streamable.go` at v1.7.0, read rather than
assumed. Two earlier instructions turned out to be wrong:

| field | what it means here |
|---|---|
| **`Stateless bool`** | **exists.** Server-to-client requests are rejected outright and GET/DELETE return 405. Confirms a job handle must be keyed by an argument rather than a connection — which is how it is already built |
| **`DisableLocalhostProtection`** | DNS rebinding protection is **already on by default**: a localhost request with a non-localhost `Host` is rejected 403. The work is **not** to build this. It is to not switch it off while debugging a confusing 403 |
| **`CrossOriginProtection`** | **deprecated.** Cross-origin protection goes in middleware — `http.NewCrossOriginProtection()` wrapping the handler — not in that field |
| `MaxRequestBodyBytes` | configurable, default 4 MiB. A negative value disables the limit entirely; never on an exposed server |
| `JSONResponse` | plain JSON instead of SSE, worth having for a simple client |
| `SessionTimeout` | idle sessions never close at the zero value |

An `MCPGODEBUG` parameter, `allowsessionsinstateless=1`, restores session
handling in stateless mode. Needing an escape hatch is a design signal. Do not
ship one.

### What has to be built

1. **`irgo-winvm mcp -http :port`**, with `Stateless: true`, **bound to
   loopback by default**. A wider bind takes an explicit flag whose help text
   says what it means.
2. **Authentication is mandatory off loopback** — `auth.RequireBearerToken`
   with a constant-time compare against a local secret. Not optional with a
   warning: a server that starts unauthenticated because a token was missing is
   a server that will be run that way. No OAuth unless a specific client demands
   discovery, and that is its own stage with its own reason.
3. **Uploads are content-addressed and chunked.** `app-create` takes a path, and
   a remote agent has a binary it just cross-compiled and no shared filesystem —
   without an upload path the remote server cannot do the one thing this tool is
   for. A Go `.exe` is 5–15 MB against a 4 MiB default, so chunk it rather than
   raising the limit to swallow 20 MB in memory. Pick BLAKE3 or SHA-256 and say
   why. Verify the hash **before** writing: a truncated upload that runs anyway
   is this repository's oldest category of bug. An unchanged binary transfers
   nothing, because the inner loop is 10.8 seconds and an upload must not
   dominate it. Stage into the existing `bin/`, and `app-delete` cleans uploads
   too.
4. **One VM, one mutation.** Two clients cannot install to the same VM at once.
   Concurrent mutation is refused with a result that says so — never serialised
   silently, never interleaved. A lock whose state cannot be read refuses by
   default, because "cannot tell" is not "safe".
5. **The threat model, written down.** One paragraph: what someone who reaches
   this port can do. It is short and unpleasant, which is the point. Prefer no
   inbound listener at all — Cloudflare Tunnel or Tailscale puts identity at the
   edge and lets the server keep binding loopback; document that as the
   recommendation and the open port as the fallback.

---

## Last: the long job, end to end

**The only unverified claim in the system.** `vm-create -install` takes about 45
minutes and returns a handle immediately. The mechanism is proven — a detached
job survived its client exiting — but the *duration* is not: the job that was
driven end to end finished in under a second, because the media it asked for
already existed.

The only honest way to close it: `vm-create -install` over MCP against a
**disposable VM name**, disconnect the client, confirm `status` reports it alive
minutes later, then let it finish and `app-create` a probe into it.

Cost and risk, stated because they decide when this runs: about 45 minutes of
wall clock, and `vm-create` restarts UTM — so the working `irgo-win11` VM must
be shut down first, and losing it costs another 45 minutes. Whoever owns the
machine decides when.

What it would catch that nothing else can: a job whose output stops being
written partway, a `status` that drifts over an hour, a guest agent gap
mid-install, and whether a detached process really survives an hour rather than
a second.

---

## Smaller, and known

- **A recycled process id** could report a stranger's process as a job. Accepted
  and documented where the check lives, in `job.alive`. Storing the start time
  and comparing would close it if it ever matters.
- **Typed schemas cover flags, not positional arguments.** `args` is still a
  list of strings for paths and directories. That is the right shape, but a
  named property would help an agent more.
- **Two compiled binaries were once tracked in git**, 24 MB of them, and are
  gone. History is deliberately **not** rewritten: one blob each against a
  102 MB `.git` does not justify invalidating published tags and every clone.

## How to work on this

- `mise run go:check`, `mise run go:lint` and `go -C site test ./...` after each
  commit; `mise run ci:watch` after each push.
- Every new gate gets a negative control — break it, watch it go red, put it
  back — and the control is named in the commit message.
- The long job is proven by running it and quoting what happened. Never claimed.
- The generated pages change with the behaviour. If [MCP](mcp.html) still
  describes a limitation that has been fixed, it is lying.

## Out of scope

- Rewriting git history for the removed binaries.
- A second list of anything, for any reason.

## Where the decisions actually live

This page is the plan. The reasoning that has to survive is in the code, beside
what it explains:

- `mcpserver/doc.go` — why the server holds no behaviour of its own, and why
  nothing may print to stdout while it runs
- `mcpserver/resource.go` — why the documentation is generated rather than
  embedded, and every option that was rejected
- `job/doc.go` — why long work is its own package, and why reporting a *dead*
  process is the constraint that decides the design
- `cmd/irgo-winvm/flags.go` — why the `FlagSet` is the source and the schema is
  derived from it
- [Agents](agents.html) — how the code is organised, and every trap that cost
  hours
