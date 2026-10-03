---
title: Home
nav_order: 1
permalink: /
---

# irgo-windows-vm

[![latest release](https://img.shields.io/github/v/release/joeblew999/irgo-windows-vm)](https://github.com/joeblew999/irgo-windows-vm/releases/latest)

Build a Go program on your Mac and find out whether it really works on
Windows. The tool (`irgo-winvm`) makes a real Windows 11 ARM64 virtual machine
on an Apple Silicon Mac, with nobody at the keyboard, runs your program in it,
and brings back what it printed, its exit code and a picture of the screen. It
also makes Ubuntu Linux VMs to SSH into (`vm-create -os linux`).

It is for:

- **Developers of Go desktop apps**, first of all [Irgo](https://github.com/stukennedy/irgo)'s,
  whose desktop support is the webview [glaze](https://github.com/crgimenes/glaze)
  and the OS integration [native](https://github.com/crgimenes/native). Passing
  on a Mac says nothing about Windows, and Windows bugs cannot be found by
  reading code, so this runs the program on real Windows and reports what
  happened. When it is dependable it moves into Irgo, so every desktop build is
  checked on every platform.
- **AI agents** writing such apps: `irgo-winvm mcp` offers the same commands
  over the Model Context Protocol ([For agents](guides/agents.md)).
- **Machines that are not Macs:** from Linux, Windows or GitHub Actions, a job
  is sent through the project's Worker to a Mac that runs it
  ([From another machine](guides/agents.md#from-another-machine-linux-windows-github)).
- **[claude-rig](https://github.com/joeblew999/claude-rig)**, which makes its
  Windows and Linux test machines with it ([A Linux VM](guides/using.md#a-linux-vm)).

```sh
irgo-winvm doctor               # what is set up, and the next steps in order
irgo-winvm vm-create -install   # a Windows VM, installed unattended (about 45 minutes, once)
irgo-winvm app-create your.exe  # run your program in it and print its output
```

Start with [Getting started](getting-started.md).

## What is what

| Name | Where | What it is |
|---|---|---|
| The tool | `cmd/irgo-winvm/`, installed as `irgo-winvm` | The one binary: every command, its MCP server, and the client for a remote Mac ([Commands](https://joeblew999.github.io/irgo-windows-vm/reference.html)) |
| The three steps | `iso-create`, `vm-create`, `app-create`, each with an undo | The Windows installer, a VM with Windows on it, your `.exe` running in it ([The three steps](guides/using.md#the-three-steps)) |
| The VM | `irgo-win11` in UTM, unless `-vm` names another | The Windows 11 ARM64 machine the commands use ([The VM and the dev account](guides/using.md#the-vm-and-the-dev-account)) |
| A Linux VM | `vm-create -os linux -vm <name>` | Ubuntu Server 24.04 ARM64, a clone of the Linux golden image or made from Ubuntu's cloud image, reached over SSH ([A Linux VM](guides/using.md#a-linux-vm)) |
| The golden image | `irgo-golden` (Windows), `irgo-golden-linux` (Linux) in UTM | A sealed VM of each system that a new VM is cloned from instead of installed: about 23 seconds for Windows, about 30 for Linux ([The golden image](guides/using.md#the-golden-image)) |
| The private cache | an R2 bucket of yours | The golden image, chunked, for another Mac to pull ([The private R2 cache](guides/using.md#the-private-r2-cache)) |
| The runtime data | `~/Library/Application Support/irgo-winvm/` | Media, logs, screenshots, jobs, locks and VM records ([Runtime data](concepts/architecture.md#runtime-data)) |
| A job | `status` | Long work that outlives the terminal or agent that started it ([Jobs](concepts/architecture.md#jobs)) |
| The MCP server | `irgo-winvm mcp` | The same commands as tools for an agent ([For agents](guides/agents.md)) |
| The Worker | `worker/`, at `https://irgo-windows-vm.gedw99.workers.dev` | The Cloudflare Worker: the remote job queue, the ledger, live glaze status, the cache's front door ([The Cloudflare Worker](worker.md)) |
| A remote job | `remote-submit` on any OS; `serve` on the Mac | A binary from another machine, run on a fresh clone of the golden image ([Remote jobs](concepts/architecture.md#remote-jobs)) |
| The keeper | `keeper`, under pitchfork (`keeper-create`) | Keeps the Mac awake while a VM runs, starts again the VMs marked keep-running, and writes the VMs down for claude-rig, whose report to fleet-api carries them ([The keeper](guides/using.md#the-keeper-vms-that-stay-up)) |
| The glaze suite | `examples/conformance` | The tests that say whether glaze and native work, on the Mac and on Windows ([Testing](guides/testing.md)) |
| The VM suite | `examples/vmconformance` | The tests that say whether a VM has what this project relies on ([The VM conformance suite](guides/testing.md#the-vm-conformance-suite)) |
| The docs site | `docsite/` (the generator), `site/` (this site's config and hooks) | <https://joeblew999.github.io/irgo-windows-vm/>, built from these pages ([The docs site](contributing.md#the-docs-site)) |
| A task | `mise.toml`, `mise-tasks/` | `mise run <task>`: checks, builds, the glaze gates, the cycle tests ([mise tasks](contributing.md#mise-tasks)) |
| A plan | `.plans/` | Work not done yet, one file each |

## What is generated

Never edit these: change the source and run the command.

| Path | Written by |
|---|---|
| `docs/GLAZE-STATUS.md`, `docs/screens/conformance/` | `irgo-winvm glaze-check` (`mise run glaze:mac`, `mise run glaze:windows`) |
| `docs/VM-STATUS.md`, `docs/screens/vm-conformance/` | `irgo-winvm vm-check` |
| `docs/screens/vm/` | `mise run vm:shots` |
| The site's Commands, MCP and Worker API pages | the hooks in `site/`, when `mise run site:build` runs |
| `worker/openapi.json` | `mise run worker:wasm` |
| `Casks/irgo-winvm.rb` | the release ([Releases](contributing.md#releases)) |
| `docs/_config.yml`, `docs/writing.md`, `docs/llms.txt`, `docs/_sass/` | `mise run docs:setup` |

## Every page

| Section | Pages |
|---|---|
| Start | [Getting started](getting-started.md) |
| [Guides](guides.md) | [Using it](guides/using.md) (the commands, exit codes, costs, Linux VMs, the golden image, the private cache, sharing a Mac, the keeper), [For agents](guides/agents.md) (MCP, HTTP, another machine, filing issues), [Testing](guides/testing.md) (does glaze work, driving an app, the cycle tests) |
| [Concepts](concepts.md) | [Architecture](concepts/architecture.md), [Threat model](concepts/threat-model.md) |
| [Reference](reference.md) | [Commands](https://joeblew999.github.io/irgo-windows-vm/reference.html), [MCP](https://joeblew999.github.io/irgo-windows-vm/mcp.html), [Worker API](https://joeblew999.github.io/irgo-windows-vm/api.html), [Known traps](reference/traps.md), [Upstream bugs](reference/upstream.md), [Glaze status](GLAZE-STATUS.md), [VM status](VM-STATUS.md) |
| [This repository](contributing.md) | [Rules](rules.md), [Findings](findings.md), [Roadmap](roadmap.md), [The Cloudflare Worker](worker.md), [Writing docs](writing.md) |

For an agent: [llms.txt](https://joeblew999.github.io/irgo-windows-vm/llms.txt)
lists every page as Markdown, and
[llms-full.txt](https://joeblew999.github.io/irgo-windows-vm/llms-full.txt) is
all of them in one file.
