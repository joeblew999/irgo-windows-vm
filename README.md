# irgo-windows-vm

<https://github.com/joeblew999/irgo-windows-vm>
· [Docs site](https://joeblew999.github.io/irgo-windows-vm/)

A Windows 11 ARM64 VM on Apple Silicon, and your Go binaries running inside it.

Build on the Mac, run on Windows, read the output back. No GUI, no manual
install, nothing to click.

## Get it

**[All releases →](https://github.com/joeblew999/irgo-windows-vm/releases)** —
every version, with checksums.
The [latest](https://github.com/joeblew999/irgo-windows-vm/releases/latest) is
the one you want.

Download the binary for your Mac — `arm64` for Apple Silicon — then:

```sh
chmod +x irgo-winvm-darwin-arm64
xattr -d com.apple.quarantine irgo-winvm-darwin-arm64
./irgo-winvm-darwin-arm64
```

The second line is macOS refusing anything downloaded from the internet;
without it Gatekeeper reports the binary as damaged.

macOS on Apple Silicon. It installs UTM itself if you do not have it.

## Try it

Three commands, in this order. Each is cheap to repeat — if it is already done
it says so and stops.

```sh
irgo-winvm iso-create -fetch    # the Windows installer
irgo-winvm vm-create -install   # a VM, with Windows installed on it
irgo-winvm app-create your.exe  # your binary, run in that VM, output back
```

| | cost | |
|---|---|---|
| `iso-create -fetch` | **4.2 GB**, ~250 s | downloaded once; ~40 s to rebuild afterwards |
| `vm-create -install` | **about 45 minutes** | an estimate, not a measurement — unattended, you click nothing |
| `app-create` | **10.8 s** | measured 12 Aug 2026, cross-compiled on the Mac with no toolchain |

Flags for all of them are in the
[command reference](https://joeblew999.github.io/irgo-windows-vm/reference.html),
captured from the binary.

## What it is for

This is the VM system for **[Irgo](https://github.com/stukennedy/irgo)** — a
hypermedia-driven framework for building apps in Go with Datastar that run on
iOS, Android, **desktop** and the web, with no JavaScript framework involved.

Desktop is the hard word in that sentence. An app that runs on one desktop is
not a desktop app; it has to work on the platforms you did not write it on. The
desktop half rests on
[glaze](https://github.com/crgimenes/glaze) and
[native](https://github.com/crgimenes/native) — cgo-free Go libraries for a
webview and the OS integration around it — and *"it works on my Mac"* is not
evidence about any of the others.

Windows is the one that cannot be checked by reading the code. It is a
different windowing system, a different webview, a different set of things that
fail silently, and none of it is visible from macOS. So this exists to check it
the only way that counts: install a real Windows on a real VM, run the binary
there, and read back what it actually did.

Everything else here is in service of that: getting the media, making the VM,
and running a binary on it.

Once that is dependable, it belongs to Irgo rather than to a repository beside
it — the VM machinery is meant to be ported in, so that verifying a desktop
build on every platform is part of building one.

## Three steps

| step | what it gets you |
|---|---|
| **`iso-create`** | the Windows installer |
| **`vm-create`** | a VM with Windows on it, answering |
| **`app-create`** | your `.exe` running in that VM, output back |

They run in that order. Each one is cheap to repeat: if it is already done, it
says so and stops. Each has an undo — `iso-delete`, `vm-delete`, `app-delete` —
so a step that fails can be cleaned and re-run rather than leaving the machine
somewhere between two states.

Two more, for when something is wrong: **`vm-screen`** photographs the VM, and
**`doctor`** reports what is here. Neither changes anything.

Your `.exe` is anything built with `GOOS=windows GOARCH=arm64 CGO_ENABLED=0`.
That is the whole contract.

Every command that takes flags explains itself with `-h`, and `irgo-winvm help`
explains the sequence. This file lists no flags and so cannot go stale about
them — the **[command reference](https://joeblew999.github.io/irgo-windows-vm/reference.html)**
on the site is captured from the binary at build time.

## What it exits with

`utmctl` exits 0 when it fails, which this repository has been bitten by more
than once. So this tool is the only honest signal a caller gets, and it says
something specific:

| code | meaning |
|---|---|
| **0** | it worked — including `-h`, and an undo that found nothing to undo |
| **1** | your program ran and failed |
| **2** | the command was called wrongly |
| **3** | that VM does not exist |
| **4** | the VM is there, the guest agent is not answering |
| **5** | refused — a destructive command without `-force` |

**1 is your program, not this tool.** The guest's own exit code is *not* passed
through: a binary exiting 3 exits `app-create` **1**, and names its real code in
the message. That is deliberate — a failing program and a missing VM must not
look the same to a script.

**4 is the one worth retrying.** Windows Update takes the agent away for minutes
at a time; the VM is fine and will answer again. `app-create` already waits and
tries to recover before giving up, which is why it can take several minutes to
reach that code.

`-detach` exits 0 once the program is running, since it is for windows nobody
intends to close.

## What it costs

| | size | |
|---|---|---|
| the `.esd` from Microsoft | **4.2 GB** | downloaded once, from a source that rate-limits |
| scratch to build the ISO | **12 GiB** | free space `iso-create` requires |
| the built ISO | **~4.9 GB** | hardlinked into the VM, not copied |
| the installed VM | **~30 GiB** | on a 64 GiB sparse disk |

About **33 GB** once installed. `iso-delete` keeps the `.esd` unless you pass
`-all`, because rebuilding the ISO from it takes ~40 seconds with no network,
while losing it means downloading 4.2 GB again.

## Linux

Out of scope here. This repository is the Windows VM system: Windows is the
platform whose behaviour cannot be checked by reading the code from a Mac, and
everything in it — the answer file, the ISO mastering, the guest agent, the
session model — is Windows-specific.

Linux would need its own guest image and its own path, and it is not built
here. The `linux` builds in CI exist only so the tool compiles for a developer
on another OS, not because it can drive a Linux guest.

## What it looks like

Not mock-ups. A Mac built the installer, installed Windows on it unattended,
and photographed the result — including the failure that put three Bing tabs on
the desktop.

Every one of these was taken by the tool itself, named for the stage that
produced it. Nothing was staged, cropped, or copied across by hand —
`mise run vm:shots` publishes the newest shot of each stage.

**`booting-1`** — UEFI firmware, before Windows has started

![booting-1](docs/screens/vm/booting-1.png)

**`booting-2`** — Windows starting

![booting-2](docs/screens/vm/booting-2.png)

**`ready`** — the guest agent answers, and the VM is usable

![ready](docs/screens/vm/ready.png)

**`copying`** — an unattended install, mid-flight. Nobody clicked anything

![copying](docs/screens/vm/copying.png)

**`finalising`** — the copy is done, first logon

![finalising](docs/screens/vm/finalising.png)

**`stalled-1`** — an install that stopped moving, photographed so you can see why

![stalled-1](docs/screens/vm/stalled-1.png)

**`running-no-agent`** — the failure it now refuses to cause: keystrokes meant
for a boot prompt, landing in a logged-in desktop

![running-no-agent](docs/screens/vm/running-no-agent.png)

`booting-N` repeats every few seconds until the agent answers, so a boot that
hangs leaves a picture of exactly where it stopped.

Every stage photographs itself as it runs, because from the host a stuck boot
and a working one look identical. Those go to `shots/` outside the repository;
the few kept here as evidence are in `docs/screens/`.

## A glaze or native bug is fixed at crgimenes, not here

**Non-negotiable.** When a probe fails, the fix goes to
[crgimenes/glaze](https://github.com/crgimenes/glaze) or
[crgimenes/native](https://github.com/crgimenes/native) — never into a
workaround in this repository.

That is the whole point. This repo exists to *find* what breaks in glaze and
native on Windows, and a bug worked around in an example is a bug still shipped
to everyone using those libraries. Worse, the workaround hides it: the probe
goes green, the report says the capability works, and the next person to hit it
starts from nothing.

## The rest

- **[CONTRIBUTING.md](CONTRIBUTING.md)** — how to set up, what to run, how to
  land a change.
- **[AGENTS.md](AGENTS.md)** — read before changing any of this. How the code is
  organised, and every trap that cost hours.
- **[RESULTS.md](RESULTS.md)** — what has been measured, dated.
- **[UPSTREAM.md](UPSTREAM.md)** — what was found and where it was fixed.

## Reading this with a machine

The whole documentation is published as one file:

- **[llms-full.txt](https://joeblew999.github.io/irgo-windows-vm/llms-full.txt)**
  — every page, one request, ~67 KB of markdown.
- **[llms.txt](https://joeblew999.github.io/irgo-windows-vm/llms.txt)** — the
  index, if you would rather choose first.

Prefer those over fetching the `.md` files from this repository. Five of the six
pages are here as markdown; the sixth, the command reference, **has no source
file** — it is captured from the compiled binary at build time so that no flag
or default is ever transcribed. Fetching the raw markdown gets you documentation
that looks complete with no flag reference in it.

MIT licensed — see [LICENSE](LICENSE).
