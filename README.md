# irgo-windows-vm

Run irgo's **Windows** desktop build on a Mac, unattended, so glaze and its
native capabilities can be verified on both platforms from one machine.

Pure Go. No `hdiutil`, no `plutil`, no shell scripts — clone, build, run.

## Why

A developer on an Apple Silicon Mac can test **both** desktop targets: macOS
natively, Windows in a VM. A developer on Windows can only test Windows. That
asymmetry decides who should own the desktop app, and `irgo-winvm targets`
prints it for the machine you are on.

Some things simply cannot be checked from macOS, and they are the reason this
exists:

- glaze calls the **undocumented** `CreateWebViewEnvironmentWithOptionsInternal`
  WebView2 export to avoid shipping `WebView2Loader.dll`. Microsoft may remove it.
- `putbounds_arm64.go` passes a 16-byte RECT in two registers per AAPCS64.
  glaze's CI runs `windows-latest`, which is x64 — **that code is never executed
  anywhere**.
- Windows is the best-covered platform for native capabilities: 7 of 8 packages,
  more than macOS or Linux.

## Install

```sh
go install github.com/joeblew999/irgo-windows-vm/cmd/irgo-winvm@latest
irgo-winvm doctor        # installs UTM via Homebrew if missing
```

## Use

```sh
irgo-winvm doctor                                # UTM version, guest tools, disk space
irgo-winvm targets                               # what this machine can test
irgo-winvm verify -iso win11-arm64.iso           # ARM64? can it boot unattended?

# The usual entry point: create, restart UTM, boot past the UEFI shell.
irgo-winvm up -iso win11-arm64.iso -name dev-win -probes ./out

irgo-winvm status -vm dev-win                    # state, IP, whether the agent answers
irgo-winvm probe  -vm dev-win                    # run the probes, print the report
irgo-winvm delete -vm dev-win -force             # stop and reclaim the space
```

`up` is `create` + `RestartUTM` + `boot`. The steps stay separate underneath
because each fails differently and is worth retrying alone.

Two behaviours that look like bugs and are not:

**UTM must be restarted after `create`.** It enumerates its bundle directory
only at launch, so a VM written while UTM is running does not exist as far as
`utmctl` or the UI are concerned — with no error saying so. `up` does this for
you.

**Every boot needs driving.** UTM's aarch64 firmware never auto-selects a boot
entry; it drops to the interactive UEFI shell. `boot` types the path the way a
person would, and must open a display window first: `utmctl start` powers the VM
on headless, and UTM routes keyboard input through the display, so keystrokes to
a windowless VM are accepted and discarded.

`create` writes a bundle whose install needs no input. A generated ISO9660 CD
carries `autounattend.xml`, `startup.nsh` and your probe binaries, and the UTM
guest tools CD is attached so the QEMU agent exists afterwards.

It has to be a CD, not a removable FAT disk. Attached as a disk, Windows Setup
did not read `autounattend.xml` and fell back to an interactive install with no
error; as a CD it is read and applied.

## Things that cost hours, encoded so they cannot regress

Each is covered by a test in `utmvm/config_test.go`, because UTM rejects a bad
config with one generic *"cannot import this VM"* that names no field.

| trap | what happens |
|---|---|
| `virtio-gpu-pci` display | no framebuffer on aarch64, no legacy VGA — guest boots **invisibly** and looks hung |
| VirtIO system disk | Windows ARM64 has no inbox driver; Setup reports no drive found. Use **NVMe** |
| `virtio-net-pci` without guest tools | no inbox driver — **no network at all** in the guest |
| missing `PS2Controller` | non-optional decode, no default; whole document rejected |
| `UsbBusSupport: "USB3_0"` | the enum is `"2.0"` / `"3.0"` |
| `CPUFlags` | the keys are `CPUFlagsAdd` and `CPUFlagsRemove` |
| reading UTM's schema from `main` | `main` was v5.0.4 while the app was v4.7.5, and they disagree. Read the **tag** |
| `efi\boot\bootaa64.efi` | prints "Press any key to boot from CD" and times out. Use `cdboot_noprompt.efi` |
| no `startup.nsh` | UTM's firmware does not auto-boot; it sits in the UEFI shell |
| Windows ISOs are **UDF**, not ISO9660 | `install.wim` exceeds ISO9660's 4 GB limit, so ISO9660 readers fail on every path |
| answer file on a FAT disk | Setup ignores it and runs interactive. Use an ISO9660 **CD** |
| ISO padded past its declared volume size | mounts fine on macOS, ignored by Setup. Trim to the PVD size |
| Joliet disabled | `autounattend.xml` becomes `AUTOUNAT.XML`, which Setup never looks for |
| `start utm-guest-tools-*.exe` | `start` does not expand wildcards; the installer silently never runs |
| `utmctl start` then keystrokes | headless VM has no display, and UTM routes input through it — keystrokes vanish |

## Layout

```
cmd/irgo-winvm/   CLI
utmvm/            bundle generation, FAT images, UTM control, cleanup
utmvm/assets/     embedded autounattend.xml and startup.nsh
probe/            native capability probe (clipboard, power, mmap, …)
glaze-probes/     glaze app:// origin and Events bridge proofs
```

See [RESULTS.md](RESULTS.md) for measured results per platform.

## Requirements

macOS on Apple Silicon, UTM 4.7.x, and a Windows 11 **ARM64** ISO. Get the ISO
with [CrystalFetch](https://github.com/TuringSoftware/CrystalFetch), which is
what UTM's own authors ship for the purpose.
