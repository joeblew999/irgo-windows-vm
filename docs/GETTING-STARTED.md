# Getting started

From nothing to your own Go program running on Windows 11 ARM64, on a Mac. The
first VM takes about 45 minutes of unattended work; every run after that takes
seconds.

What each command does in detail is in [Using it](USING.md); driving it from an
AI agent is in [For agents](FOR-AGENTS.md).

## What you need

- **A Mac with Apple Silicon.** Windows 11 ARM64 is the only guest: it is the
  platform whose behaviour cannot be checked by reading code on a Mac
  ([scope](ARCHITECTURE.md#scope-windows-only)).
- **About 33 GB of disk** once Windows is installed
  ([what it costs](USING.md#what-it-costs)).
- **UTM**, the hypervisor. `vm-create` installs it from its signed `.dmg` if it
  is missing: the newest release GitHub does not mark as a pre-release, never a
  beta (`latestStableUTMDMG`). UTM 5.0.x are betas; see
  `.plans/2026-09-30_2000_utm-5.md`.
- **The macOS Automation permission to control UTM.** It is granted once, in a
  system dialog, and nothing can grant it for you. `vm-create` checks it before
  doing anything expensive: without it a boot cannot be driven, and the failure
  would otherwise arrive forty minutes into an install as a timeout that does
  not mention permissions.
- **`wimlib` and `xorriso`**, only when building media from scratch.
  `iso-create` installs them and `iso-delete` removes them.

It does **not** need Full Disk Access, or access to other apps' data. The tool
never reads or writes UTM's container itself; it writes bundles under its own
directory and has UTM import, clone, reconfigure and delete them through
AppleScript.

## Install

The release binary is all you need: no checkout, Go or mise.

```sh
curl -fsSL https://raw.githubusercontent.com/joeblew999/irgo-windows-vm/main/install.sh | sh
```

It downloads the binary for your Mac from the
[latest release](https://github.com/joeblew999/irgo-windows-vm/releases/latest),
checks it against the release's `SHA256SUMS`, and installs it as
`~/.local/bin/irgo-winvm`.

| other ways | |
|---|---|
| Homebrew | `brew tap joeblew999/irgo-windows-vm https://github.com/joeblew999/irgo-windows-vm && brew install --cask irgo-winvm` |
| Go | `go install github.com/joeblew999/irgo-windows-vm/cmd/irgo-winvm@latest` |
| by hand | download `irgo-winvm-darwin-arm64` from the release, `chmod +x` it, `xattr -d com.apple.quarantine` it, and put it on your PATH as `irgo-winvm` |

**Gatekeeper.** The binaries are ad-hoc signed by the Go linker (arm64 requires
a signature to run at all) and not notarized. A file with no quarantine flag
runs; one a browser downloaded is refused until `xattr -d
com.apple.quarantine` clears it. `curl`, `go install` and the cask's hook leave
no flag. Signing and notarizing need an Apple Developer account, which this
project does not have.

`irgo-winvm version` prints the version stamped in at build time, or `dev` when
built by hand.

**On Linux or Windows** the same binary is the client for a Mac elsewhere:
`go install github.com/joeblew999/irgo-windows-vm/cmd/irgo-winvm@latest`, then
`irgo-winvm remote-submit app.exe` with the Mac owner's URL and a token
([how](FOR-AGENTS.md#from-another-machine-linux-windows-github)). The commands
that drive UTM refuse there and say so.

## Your first VM

```sh
irgo-winvm doctor               # what is set up, and the next steps in order
irgo-winvm iso-create -fetch    # the Windows installer: 4.2 GB from Microsoft
irgo-winvm vm-create -install   # a Windows VM, installed unattended
```

- **`doctor` lists every step left**, each with its command, in order. Run it
  whenever you are unsure what is next.
- **The install takes about 45 minutes** and needs nobody at the keyboard. You
  never open the VM's window. `irgo-winvm vm-screen` saves a picture of the
  screen if you want to look.
- **With a [golden image](USING.md#the-golden-image)** a new VM is a clone that
  answers in about 23 seconds instead, and with your own
  [private cache](USING.md#the-private-r2-cache) `vm-create -install` pulls the
  image in minutes rather than installing.
- **Every command is safe to repeat.** If the work is already done, it says so
  and stops, and every command that changes something has an undo
  (`iso-delete`, `vm-delete`, `app-delete`).

## Your first program

Your `.exe` is anything built with `GOOS=windows GOARCH=arm64 CGO_ENABLED=0`.
That is the whole contract.

```sh
GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go build -o app.exe .
irgo-winvm app-create app.exe          # run it in the VM and print its output
irgo-winvm app-create -gui app.exe     # the same, for a program that opens a window
```

`app-create` takes seconds. It exits with your program's result: **0** when
your program succeeded and **1** when it failed, with its real exit code in the
message. Every other code is the tool telling you something; they are listed in
[What it exits with](USING.md#what-it-exits-with).

A program with a window needs `-gui`
([why](USING.md#why--gui-exists)).

## Next

| to | read |
|---|---|
| look up a command or flag | [Commands](https://joeblew999.github.io/irgo-windows-vm/reference.html), captured from the binary |
| keep VMs cheap, share a Mac, fix an old VM | [Using it](USING.md) |
| let an AI agent do all of this | [For agents](FOR-AGENTS.md) |
| check whether glaze works on Windows | [Testing](TESTING.md) |
| change the tool itself | [Contributing](CONTRIBUTING.md) |
