# irgo-windows-vm

<https://github.com/joeblew999/irgo-windows-vm>

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

## What it is for

[glaze](https://github.com/crgimenes/glaze) and
[native](https://github.com/crgimenes/native) are cgo-free Go desktop libraries.
Their Windows support is the half that is hardest to check from a Mac, so this
exists to check it — automatically, on a real Windows, from the machine you
already have.

Everything else here is in service of that: getting the media, making the VM,
and running a binary on it.

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

Every command explains itself with `-h`, and `irgo-winvm help` explains the
sequence. This file does not list flags and so cannot go stale about them.

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

- **[AGENTS.md](AGENTS.md)** — read before changing any of this. How the code is
  organised, and every trap that cost hours.
- **[RESULTS.md](RESULTS.md)** — what has been measured, dated.
- **[UPSTREAM.md](UPSTREAM.md)** — what was found and where it was fixed.
