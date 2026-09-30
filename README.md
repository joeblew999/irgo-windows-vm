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
it is missing.

Download `irgo-winvm-darwin-arm64` from the
[latest release](https://github.com/joeblew999/irgo-windows-vm/releases/latest), then:

```sh
chmod +x irgo-winvm-darwin-arm64
xattr -d com.apple.quarantine irgo-winvm-darwin-arm64
./irgo-winvm-darwin-arm64
```

The `xattr` line clears macOS's quarantine flag on downloaded files. Without
it, Gatekeeper reports the binary as damaged.

Checksums are published with [every release](https://github.com/joeblew999/irgo-windows-vm/releases).

## Quick start

Run these three commands in order:

```sh
irgo-winvm iso-create -fetch    # download the Windows installer
irgo-winvm vm-create -install   # create a VM and install Windows, unattended
irgo-winvm app-create your.exe  # run your program in the VM and print its output
```

- The first two are slow, but only once: a 4.2 GB download, then about 45
  minutes of install you don't need to watch. See
  [what each step costs](docs/DEVELOPMENT.md#what-it-costs).
- After that, `app-create` takes seconds.
- Every command is safe to repeat. If the work is already done, it says so and
  stops.
- `irgo-winvm doctor` shows what is set up and what is missing.

Your `.exe` is any build made with `GOOS=windows GOARCH=arm64 CGO_ENABLED=0`.

## What you'll see

The tool takes these screenshots itself. Windows installing with nobody at the
keyboard:

![copying](docs/screens/vm/copying.png)

Windows ready to run your program:

![ready](docs/screens/vm/ready.png)

## Check glaze on Windows

```sh
mise run glaze:mac       # on this Mac
mise run glaze:windows   # in the VM
```

Each prints one line: `YES`, or `NO` and the names of what failed.

A `NO` is fixed in [crgimenes/glaze](https://github.com/crgimenes/glaze) or
[crgimenes/native](https://github.com/crgimenes/native), never worked around
here. A workaround would hide a bug that still ships to everyone using those
libraries, and finding those bugs is the point of this project.

## Documentation

| page | read it to |
|---|---|
| [Contributing](docs/CONTRIBUTING.md) | set up, run the checks, land a change, test glaze |
| [Development](docs/DEVELOPMENT.md) | understand the commands, exit codes, costs and known traps |
| [Results](docs/RESULTS.md) | see what has been measured, with dates and screenshots |
| [Upstream](docs/UPSTREAM.md) | see the bugs found in glaze, native and UTM, and their status |
| [Roadmap](docs/ROADMAP.md) | see what is next |
| [Threat model](docs/THREAT-MODEL.md) | know what the HTTP server exposes before you enable it |
| [Command reference](https://joeblew999.github.io/irgo-windows-vm/reference.html) | look up every command and flag, captured from the binary |

MIT licensed. See [LICENSE](LICENSE).
