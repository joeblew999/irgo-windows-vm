# For agents

An agent writing a Go desktop app on a Mac cannot otherwise find out whether it
works on Windows. `irgo-winvm mcp` lets it ask, get an answer from a real
Windows guest, and see the screen when the answer is that the app hung.

This page is for agents that **use** the tool, from any repository. An agent
changing this repository's code starts at [AGENTS.md](../AGENTS.md) instead.

## Connect

```sh
claude mcp add irgo-winvm -- irgo-winvm mcp
```

The [MCP guide](https://joeblew999.github.io/irgo-windows-vm/mcp.html) is the
reference for everything the server offers: other clients, a typical session,
every tool and its arguments, how failures and long-running jobs are reported,
and the instructions the server sends a connected agent. It is captured from a
live server at build time, so it cannot describe a tool that does not exist.
`irgo-winvm mcp -h` prints the essentials.

Agents working **in this repository** are already connected: `.mcp.json`
registers the server.

## What to rely on

- **Your `.exe` is anything built with `GOOS=windows GOARCH=arm64
  CGO_ENABLED=0`.** That is the whole contract.
- **Match on the exit code or its `status` name**, never on the wording. The
  codes are in [What it exits with](USING.md#what-it-exits-with): 1 is your
  program failing, with its real code in the message; 4 (`no-agent`) and 6
  (`busy`) are worth retrying, and so is 7 (no room) once a VM stops.
- **`vm-screen` returns the picture itself.** Use it whenever an answer is a
  timeout: from the host, a hung program and a slow one look the same.
- **Long calls return a job.** Ask `status` with the id; asking the same command
  again returns the job already running rather than starting a second.

## Sharing the Mac

The Mac is shared: the owner, agents in this repository and agents from other
repositories all use it at once ([how](USING.md#sharing-one-mac)). The whole
contract for an agent from another repository:

1. Set `IRGO_WINVM_OWNER` (or pass `-owner`) to something that names you, or
   rely on the MCP client name.
2. `irgo-winvm vm-create -vm <name>`: with a
   [golden image](USING.md#the-golden-image), a clone in about 23 seconds.
   Exit 7 means no room: wait, or ask whoever `status` names. `capacity` says
   what holds the disk and memory, and how many more VMs fit; each owner may
   have 2 VMs holding 16 GiB unless the machine says otherwise.
3. Pass `-vm <name>` to `app-create`, `vm-screen` and the rest. Leaving it out
   is refused with exit 2: the default VM is the machine owner's.
4. `irgo-winvm vm-delete -vm <name> -force` when done. If you go away, a clone
   idle for a day is removed by whoever runs `vm-reap -force`.

## Over HTTP

`irgo-winvm mcp -http <address>` serves the same tools over Streamable HTTP,
for example `-http 127.0.0.1:8129`. A bare `:port` means loopback.

- **Uploads.** An agent with no shared filesystem sends a cross-compiled `.exe`
  in base64 chunks with `app-upload` (up to 2 MiB per call). It is staged
  content-addressed as `bin/<caller>/<sha256>.exe` under the runtime data, verified by
  SHA-256 before it is committed, and then passed to `app-create` by path. An
  unchanged binary transfers nothing; `app-delete` clears that caller's stage
  and nobody else's.
- **Remote access.** Binding wider than loopback requires `-allow-remote` and a
  bearer token in `IRGO_WINVM_TOKEN`, compared in constant time. A server that
  would start unauthenticated off loopback is refused outright.

> [!WARNING]
> Anyone who can call `app-create` can run code of their choice on your Mac's
> VM. Read the [threat model](THREAT-MODEL.md) before enabling `-http`, and
> prefer no inbound listener at all.

## Reporting issues

Repositories that use `irgo-winvm` through their agents file issues here when
they need something. This is how to file one that can be acted on without a
round of questions.

**File here** when `irgo-winvm` does the wrong thing (a wrong exit code, a
step that hangs, output that lies), or when your repository needs it to do
something it does not.

**Do not file here** when:

- the bug is in glaze, native or UTM. Read [UPSTREAM.md](UPSTREAM.md) first:
  if it is listed, add what you found to that entry's linked issue. If it is
  not, use the *upstream* kind below, so it is triaged into the ledger rather
  than misfiled as ours. It is upstream only if a correct caller, reading only
  that project's documentation, would hit it.
- your own program failed. Exit 1 from `app-create` is your `.exe` failing,
  with its own exit code named in the message (see
  [What it exits with](USING.md#what-it-exits-with)). Exits 4 and 6 are
  worth retrying before filing.

**Write the body with one command**, right after the failure, so the log still
holds it:

```sh
irgo-winvm report -issue bug > body.md       # or: feature, upstream
```

It prints the issue body with the same headings as the web form, the
diagnostic report already inside it, and the `gh` command that files it at the
top. Over MCP, call the `report` tool with `-issue bug`. Plain
`irgo-winvm report` prints only the report, to paste into an existing issue.
Read it before posting anyway.

Replace every _italic_ line, keep every `###` heading in order, tick the
checks, and file it:

```sh
gh issue create --repo joeblew999/irgo-windows-vm \
  --title "[bug] app-create -gui exits 4 on a fresh clone" \
  --label bug,needs-triage,agent-filed \
  --body-file body.md
```

The forms in `.github/ISSUE_TEMPLATE` work only in a browser; `gh` and the API
skip them, so the body is the template. Use `--label feature,needs-triage,agent-filed`
for a feature and `--label needs-triage,agent-filed` for an upstream bug (add
`upstream-glaze`, `upstream-native` or `upstream-utm` if you are sure). If gh
says a label is not found, file without `--label`. What each label means is in
[Triage](CONTRIBUTING.md#triage-and-labels).

**What makes it actionable:**

- the exact command, every flag, and its full output with the exit code: not
  a paraphrase;
- the report, run after the failure and before anything else, so the last
  commands and the log excerpt are about this failure;
- expected against actual, in a sentence each;
- for a feature, what your repository is trying to get done and how you will
  both know it is done, not only the flag you want;
- which repository and which agent filed it, so a question has somewhere to go.

An issue without the report gets `needs-report` and waits for it.

### What the report contains

`irgo-winvm report` gathers, as one markdown block: the version, macOS and
hardware, free disk, UTM, the golden image, the last five commands and how
they exited, the log around the last error (`-lines`, 40 by default),
glaze-status's verdict lines, and `doctor -json`. Every command an agent can
run logs its exit ([how](ARCHITECTURE.md#every-command-logs-its-exit)), so a
failure reached over MCP is in the report as well as one on a terminal.

Redaction is in `cmd/irgo-winvm/report.go`: values of credential-named and
`IRGO_` environment variables and of `.env.r2`, then credential-shaped strings
(GitHub tokens, bearer headers, AWS key ids, signed URL parameters, emails),
then home directories, which become `~`. It does not redact hashes or module
versions, which triage needs. `report_test.go` plants a secret down each road
and checks none comes out.
