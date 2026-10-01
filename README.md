# irgo-windows-vm

<https://github.com/joeblew999/irgo-windows-vm> · [Docs site](https://joeblew999.github.io/irgo-windows-vm/)

Build a Go desktop program on your Mac and find out whether it really works on Windows.

`irgo-winvm` creates a real Windows 11 ARM64 virtual machine on Apple Silicon,
runs your program in it, and brings back the output. The install is unattended:
you don't click anything, and you never open the VM's window.

`irgo-winvm mcp` offers the same commands to an AI agent, so an agent building
a desktop app on a Mac can test it on real Windows too.

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
it is missing. The release binary is all you need: no checkout, Go or mise.

```sh
curl -fsSL https://raw.githubusercontent.com/joeblew999/irgo-windows-vm/main/install.sh | sh
```

It downloads the binary for your Mac from the
[latest release](https://github.com/joeblew999/irgo-windows-vm/releases/latest),
checks it against the release's `SHA256SUMS`, and installs it as
`~/.local/bin/irgo-winvm`. Or:

- **Homebrew:** `brew install --cask joeblew999/tap/irgo-winvm`
- **Go:** `go install github.com/joeblew999/irgo-windows-vm/cmd/irgo-winvm@latest`
- **By hand:** download `irgo-winvm-darwin-arm64` from the release, then
  `chmod +x` it, `xattr -d com.apple.quarantine` it, and put it on your PATH as
  `irgo-winvm`. The binary is not signed with an Apple Developer ID, so a copy
  downloaded with a browser is refused by Gatekeeper until that flag is cleared.

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
  [golden image](docs/DEVELOPMENT.md#the-golden-image) every new VM is a clone
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
`irgo-winvm mcp -h` prints the essentials.

## What you'll see

The tool takes these screenshots itself. Windows installing with nobody at the
keyboard:

![copying](docs/screens/vm/copying.png)

Windows ready to run your program:

![ready](docs/screens/vm/ready.png)

## Documentation

| page | read it to |
|---|---|
| [MCP guide](https://joeblew999.github.io/irgo-windows-vm/mcp.html) | drive it from an AI agent |
| [Command reference](https://joeblew999.github.io/irgo-windows-vm/reference.html) | look up every command and flag, captured from the binary |
| [Contributing](docs/CONTRIBUTING.md) | set up, run the checks, check glaze on Windows, land a change |
| [Development](docs/DEVELOPMENT.md) | understand the commands, exit codes, costs, the golden image and known traps |
| [Results](docs/RESULTS.md) | see what has been measured, with dates and screenshots |
| [Upstream](docs/UPSTREAM.md) | see the bugs found in glaze, native and UTM, and their status |
| [Roadmap](docs/ROADMAP.md) | see what is next |
| [Threat model](docs/THREAT-MODEL.md) | know what the HTTP server exposes before you enable it |

MIT licensed. See [LICENSE](LICENSE).
