# irgo-windows-vm

A Windows 11 ARM64 VM on Apple Silicon, and your Go binaries running inside it.

Build on the Mac, run on Windows, read the output back. No GUI, no manual
install, nothing to click.

## Why

[glaze](https://github.com/crgimenes/glaze) and
[native](https://github.com/crgimenes/native) are cgo-free Go desktop libraries.
Their Windows support is the half that is hardest to check from a Mac, so this
exists to check it — automatically, on a real Windows, from the machine you
already have.

Everything else here is in service of that: getting the media, making the VM,
and running a binary on it.

## Three stages

Each stage has an undo, so a step that fails can be cleaned and re-run rather
than leaving the machine somewhere between two states.

| step | what it gets you | cost | undo |
|---|---|---|---|
| **`iso-create`** | the Windows installer | 4.2 GB, or 40s from an `.esd` you have | `iso-delete` |
| **`vm-create`** | a VM with Windows on it, answering | ~45 min, unattended | `vm-delete` |
| **`app-create`** | your `.exe` running in that VM, output back | seconds | `app-delete` |

They run in that order, and each is cheap to repeat: already-done says so and
stops. `vm-create` does not fetch media — it tells you to run `iso-create`
first, because a stage that runs another stage is not a stage.

Two more, for when something is wrong:

- **`vm-screen`** saves a PNG of the VM's screen. A stuck boot looks identical
  to a working one from the host; this is the only way to see the difference.
- **`doctor`** reports what is installed and what is missing, and changes
  nothing.

## Your .exe

Anything built with `GOOS=windows GOARCH=arm64 CGO_ENABLED=0`. That is the whole
contract — `app-create` pushes it, runs it, and prints what it printed.

`probe/`, `glaze-probes/` and `examples/` hold four such programs, and they are
what this repository runs to find out what breaks in glaze and native on
Windows. `mise run app:create:probe` builds one and runs it, which is the entire
point of the project in one command; `mise tasks` lists the other three.

They are split by what a capability *needs*, with one owner each — `probe/` is
headless, so the guest agent runs it with no desktop at all, and the rest need
`-gui` because a tray icon or a file dialog cannot exist without a window.

`doctor` reports what is present and changes nothing.

Every command explains itself with `-h`, generated from the code, so this file
does not list flags and cannot go stale about them.

## Where things go

One place, fixed, nothing to configure:

```
~/Library/Application Support/irgo-winvm/
  media/    the ISO, the .esd it was built from, and scratch
  bin/      binaries staged into a VM
```

VMs go where UTM keeps them, because UTM reads nowhere else.

## What it needs

macOS on Apple Silicon. UTM, which `vm-create` installs from its signed `.dmg` if it is
missing. `wimlib` and `xorriso`, which `iso-create` installs and `iso-delete` removes,
and only when building media from scratch.

One thing nothing can install for you: macOS asks, once, in a dialog, whether
this may control UTM. `vm-create` checks that before doing anything expensive, because
without it a boot cannot be driven and the failure arrives forty minutes into an
install as a timeout that mentions nothing about permissions.

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

`UPSTREAM.md` is the ledger of what has been found and where it was fixed.

## Things that cost hours

Each of these fails silently. UTM rejects a bad config with one generic *"cannot
import this VM"* that names no field; a wrong boot command produces a prompt
nobody sees; a truncated ISO produces a VM that will not boot.

| trap | what happens |
|---|---|
| `virtio-gpu-pci` display | no framebuffer on aarch64, no legacy VGA — guest boots **invisibly** and looks hung |
| VirtIO system disk | Windows ARM64 has no inbox driver; Setup reports no drive found. Use **NVMe** |
| `virtio-net-pci` without guest tools | no inbox driver — **no network at all** in the guest |
| missing `PS2Controller` | non-optional decode, no default; whole document rejected |
| `UsbBusSupport: "USB3_0"` | the enum is `"2.0"` / `"3.0"` |
| `CPUFlags` | the keys are `CPUFlagsAdd` and `CPUFlagsRemove` |
| reading UTM's schema from `main` | `main` was v5.0.4 while the app was v4.7.5, and they disagree. Read the **tag** |
| Windows ISOs are **UDF**, not ISO9660 | `install.wim` exceeds ISO9660's 4 GB limit, so ISO9660 readers fail on every path |
| answer file on a FAT disk | Setup ignores it and runs interactive. Use an ISO9660 **CD** |
| ISO padded past its declared volume size | mounts fine on macOS, ignored by Setup. Trim to the PVD size |
| Joliet disabled | `autounattend.xml` becomes `AUTOUNAT.XML`, which Setup never looks for |
| El Torito marked BIOS (`-b`) | correctly sized, correctly named, **does not boot**. UEFI needs `-e` |
| `start utm-guest-tools-*.exe` | `start` does not expand wildcards; the installer silently never runs |
| `utmctl start` then keystrokes | headless VM has no display, and UTM routes input through it — keystrokes vanish |
| driving a boot on a VM that is already running | it may be a working desktop, not a UEFI shell. Keystrokes land in whatever has focus — see `docs/screens/keystrokes-into-a-running-desktop.png`, three Bing tabs searching for the EFI path |
| `utmctl delete` | prints its failure and **exits 0** |
| `utmctl suspend --save-state` | **reports success and power-cuts the guest.** No state file, VM left `stopped`, guest's next boot goes through "Diagnosing your PC". Use plain `suspend` |
| `ln` to an immutable file | `EPERM` — so protecting the ISO silently turns a hardlink into a 5 GB copy |
| `rm` on a bundle holding that hardlink | `EPERM`, directory left behind. Clear the flag first, restore it after |

`RESULTS.md` has the measurements, dated. If a number there changes, either
something broke or there is a new measurement to record.

## Layout

```
cmd/irgo-winvm/   the CLI: dispatch and printing, nothing else
utmvm/            iso*.go, vm*.go, run.go — one file group per stage,
                  each owning its own paths and constants
probe/            headless capability probes for native
glaze-probes/     the same for glaze
examples/         runnable examples, including a windowed one
```

`AGENTS.md` is what to read before changing any of it.
