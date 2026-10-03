# irgo-windows-vm

<https://github.com/joeblew999/irgo-windows-vm> · [Docs site](https://joeblew999.github.io/irgo-windows-vm/)

Build a Go desktop program on your Mac and find out whether it really works on Windows.

`irgo-winvm` creates a real Windows 11 ARM64 virtual machine on Apple Silicon,
runs your program in it, and brings back the output. The install is unattended:
you don't click anything, and you never open the VM's window.

`irgo-winvm mcp` offers the same commands to an AI agent, so an agent building
a desktop app on a Mac can test it on real Windows too. It also makes Ubuntu
Linux VMs to SSH into (`vm-create -os linux`), which
[claude-rig](https://github.com/joeblew999/claude-rig) tests on.

## What it is for

This is the Windows test rig for **[Irgo](https://github.com/stukennedy/irgo)**,
a Go + Datastar framework for iOS, Android, **desktop** and the web.

Irgo's desktop support is built on [glaze](https://github.com/crgimenes/glaze)
(the webview) and [native](https://github.com/crgimenes/native) (the OS
integration around it). Passing on a Mac tells you nothing about Windows, and
you can't find Windows bugs by reading the code. So this runs the program on a
real Windows and reports what actually happened.

When it is dependable, it moves into Irgo, so every desktop build is checked on
every platform.

## Install

You need a Mac with Apple Silicon. UTM, the hypervisor, is installed for you if
it is missing.

```sh
curl -fsSL https://raw.githubusercontent.com/joeblew999/irgo-windows-vm/main/install.sh | sh
```

It installs the binary from the
[latest release](https://github.com/joeblew999/irgo-windows-vm/releases/latest)
as `~/.local/bin/irgo-winvm`, after checking it against the release's
`SHA256SUMS`. Homebrew, `go install` and a download by hand are in
[Getting started](docs/getting-started.md#install).

## Quick start

```sh
irgo-winvm doctor               # what is set up, and the next steps in order
irgo-winvm vm-create -install   # a Windows VM, installed unattended
irgo-winvm app-create your.exe  # run your program in it and print its output
```

- `doctor` lists every step left, with the command for each. Before the first
  VM that includes `irgo-winvm iso-create -fetch`, a 4.2 GB download of the
  Windows installer.
- The install is slow, but only once: about 45 minutes you don't need to watch.
  After that `app-create` takes seconds, and with a
  [golden image](docs/guides/using.md#the-golden-image) every new VM is a clone
  that answers in about 23 seconds.
- Every command is safe to repeat. If the work is already done, it says so and
  stops.

Your `.exe` is any build made with `GOOS=windows GOARCH=arm64 CGO_ENABLED=0`.

## For AI agents

```sh
claude mcp add irgo-winvm -- irgo-winvm mcp
```

Other clients, what each tool does, a typical session and the exit codes are in
the [MCP guide](https://joeblew999.github.io/irgo-windows-vm/mcp.html);
`irgo-winvm mcp -h` prints the essentials. Serving it over HTTP and filing
issues from another repository are in [For agents](docs/guides/agents.md).

## What you'll see

The tool takes these screenshots itself. Windows installing with nobody at the
keyboard:

![copying](docs/screens/vm/copying.png)

Windows ready to run your program:

![ready](docs/screens/vm/ready.png)

## Documentation

Every page, and what is what, is on the [docs home page](docs/README.md). To
start:

| page | read it to |
|---|---|
| [Getting started](docs/getting-started.md) | install it, make your first VM and run your first program |
| [Using it](docs/guides/using.md) | understand each command, the exit codes, the costs, Linux VMs and the golden image |
| [For agents](docs/guides/agents.md) | drive it from an AI agent, over MCP or HTTP, and file issues |
| [Command reference](https://joeblew999.github.io/irgo-windows-vm/reference.html) | look up every command and flag, captured from the binary |
| [Testing](docs/guides/testing.md) | find out whether glaze works on Windows, and drive a glaze app with real input |
| [Architecture](docs/concepts/architecture.md) | see how it is built, before you change it |
| [This repository](docs/contributing.md) | set up, run the checks, land a change, cut a release |
| [Findings](docs/findings.md) | see what has been measured, with dates and screenshots |
| [Upstream bugs](docs/reference/upstream.md) | see the bugs found in glaze, native and UTM, and their status |

The [Glaze status](docs/GLAZE-STATUS.md), [Known traps](docs/reference/traps.md),
[Roadmap](docs/roadmap.md) and [Threat model](docs/concepts/threat-model.md) are linked
from those pages.

MIT licensed. See [LICENSE](LICENSE).
