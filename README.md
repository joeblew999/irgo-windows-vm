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

## Start here

```sh
go install github.com/joeblew999/irgo-windows-vm/cmd/irgo-winvm@latest
brew install wimlib xorriso        # only if you want it to build its own ISO

irgo-winvm setup -fetch -install   # everything, from nothing
```

That downloads Windows from Microsoft (SHA-1 verified), builds a bootable ISO,
creates the VM, and installs Windows unattended. About an hour, mostly waiting.

**It is idempotent.** Every stage checks whether it is already done, so running
it again is safe and takes seconds — which matters, because the two expensive
stages are a 4.2 GB download and a 45-minute install, and a setup command that
redoes either is one nobody runs twice.

```
$ irgo-winvm setup
setting up irgo-win11

  · UTM — already done (4.7.5 at /Applications/UTM.app)
  · guest tools — already done (utm-guest-tools-latest.iso)
  · Windows media — already done (win11-arm64.iso)
  · protect the media — already done (immutable)
  · VM bundle — already done (irgo-win11 (started))
  · Windows — already done (installed, agent answering)

irgo-win11 is ready.
```

Neither `-fetch` nor `-install` is implied: a command that starts a
multi-gigabyte download because you ran it in the wrong directory is a bad
command. Without them it does everything cheap and tells you what remains.

`irgo-winvm doctor` is the other thing to run on a new machine. A clone is about
a megabyte; running any of this needs ~31 GB that git has never seen — UTM, the
guest tools, a Windows ISO, the VM itself — and every one fails a long way from
its cause when absent. A missing guest-tools ISO does not say so; it presents as
a VM with no network.

## Getting the Windows ISO

Two routes. The second is the one being built, and it works.

**Today:** [CrystalFetch](https://github.com/TuringSoftware/CrystalFetch), then
hardlink it where this repo looks. `irgo-winvm fetch-iso -list` tells you
exactly which build to expect and its SHA-1, so you can check what you got.

**Building your own** — no GUI, no Microsoft account, verified against a
published hash:

```sh
brew install wimlib xorriso        # the only two external tools, ever
irgo-winvm fetch-iso -o win11.esd  # from Microsoft's catalog, SHA-1 checked
irgo-winvm build-iso -esd win11.esd
irgo-winvm iso-create              # download or build the Windows media
```

This is **verified**: an ISO built this way boots UTM straight into Windows
Setup and installs unattended — [RESULTS.md](RESULTS.md) has the screenshot.
Media built here uses the `efisys_noprompt.bin` loader, so it skips the "Press
any key to boot from CD" prompt that the rest of this repo works around with
eight timed keypresses.

Microsoft's catalog is an LZX-compressed CAB, which Go cannot read. It is
extracted with `/usr/bin/bsdtar` — libarchive, which ships with macOS — so this
needs nothing installed beyond the two tools above. Detail, and the two
approaches that failed first, are in [RESULTS.md](RESULTS.md).

## Use

```sh
irgo-winvm setup                                 # everything, idempotent — start here
irgo-winvm doctor                                # UTM, tools, and every file outside git
irgo-winvm targets                               # what this machine can test

irgo-winvm status -vm irgo-win11                 # state, IP, whether the agent answers
irgo-winvm run  -vm irgo-win11                 # run the probes, print the report

# Between runs, suspend instead of stopping: resume is ~400 ms and needs no
# keystrokes, where a cold boot is ~2 minutes of driving the UEFI shell.
irgo-winvm suspend -vm irgo-win11
irgo-winvm resume  -vm irgo-win11

irgo-winvm delete -vm irgo-win11 -force          # stop and reclaim the space
```

`setup` covers what `up` does — create, restart UTM, boot — and everything
either side of it, skipping whatever is already done. `up` and the individual
steps remain, because each fails differently and is worth retrying alone.

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
| `utmctl suspend --save-state` | **reports success and power-cuts the guest.** Exit 0, "suspended to disk", no state file written, VM left `stopped`, and the guest's next boot goes through "Diagnosing your PC". Sometimes it refuses honestly instead (naming GPU acceleration, then NVMe) — you do not get to choose which. Use plain `suspend`: in-memory, resumes in ~400 ms, and does not lie |

## A glaze or native bug is fixed at crgimenes, not here

**Non-negotiable.** When a probe fails, the fix goes to
[crgimenes/glaze](https://github.com/crgimenes/glaze) or
[crgimenes/native](https://github.com/crgimenes/native) — never into a
workaround in this repository.

That is the whole point of the project. This repo exists to *find* what breaks
in glaze and native on Windows, and a bug worked around in an example is a bug
that is still shipped to everyone using those libraries. Worse, the workaround
hides it: the probe goes green, the report says the capability works, and the
next person to hit it starts from nothing.

So, in order:

1. **Decide whose bug it is.** Does a correct consumer, reading only the public
   documentation, hit it? Then it is upstream. Did our code call the API wrongly
   — a missing required option, a call on the wrong thread — then it is ours,
   and it is fixed here.
2. **Upstream ones are reported and fixed upstream**, with the smallest change
   that fixes the cause. [UPSTREAM.md](UPSTREAM.md) tracks every one: what it
   is, how to reproduce it, and where the fix stands.
3. **Only then** does anything defensive appear here, and it is commented with
   the upstream issue it is standing in for, so it can be deleted when the fix
   lands rather than outliving the bug by years.

A rule nobody can act on is a wish, so the loop is a set of tasks. Edit their
code and ours together, and prove the fix on Windows without waiting for a
release, a merge, or a reply:

```sh
the upstream clones      # glaze and native, next to this repo
the upstream clones       # every module now builds against those clones
`go build ./... && go test ./utmvm/...`               # ... edit their code and ours ...
the upstream clones     # their tests, our tests, Windows binaries from the edit
irgo-winvm run .bin/nativeall-upstream-arm64.exe   # the actual proof
the upstream clones       # what you changed — this is the pull request
the upstream clones     # back to the released modules
```

The switch is a gitignored `go.work`, never a `replace` in a `go.mod`: a
`replace` is a tracked file, and one committed by accident points the whole
project at a path on somebody's laptop. `the upstream clones` says which
of the two you are currently building, because a fix that "works" against a
stale module cache is worse than no fix at all.

What is *not* a bug and must not be filed as one: a package returning its own
`ErrUnsupported` on a platform it documents as unsupported. `nocapture` on
macOS is the example — Apple removed the API, and refusing to set it is
correct.

## Layout

```
cmd/irgo-winvm/     CLI
utmvm/              bundle generation, FAT images, UTM control, cleanup
utmvm/assets/       embedded autounattend.xml and startup.nsh
probe/              headless native capability probe — runs under the guest agent
glaze-probes/       glaze app:// origin and Events bridge proofs
examples/nativeall/ every native capability in one windowed program (`-gui`)
```

## Where the big files go

An ISO is 5 GB, a VM is 25 GB, and building an ISO needs room for a copy of one
plus the result. A laptop cannot always host all of that, so none of it is
hardcoded — every directory is overridable, and `irgo-winvm doctor` prints where
each one currently resolves to:

Everything lives under `~/Library/Application Support/irgo-winvm`: `media`
for ISOs, `work` for scratch, `bin` for built binaries, `screens` for
screenshots. VMs go where UTM keeps them, because UTM reads nowhere else.

There is nothing to configure. Environment overrides existed and were
removed: they meant the tool put things somewhere different depending on
which shell launched it, so media built in one shell was invisible to the
next.

This is also a safety boundary. Writing an image refuses three things outright,
because each is a different mistake and each costs a 4 GB re-download from a
rate-limited source: a destination that is **immutable** (somebody protected it
deliberately), one that is **hardlinked** (writing there empties every other
name for the same file — usually including the media a VM boots from), and
anything **inside a VM bundle** (UTM owns that layout).

`irgo-winvm iso` shows every name the working ISO has, and `-protect` makes it
immutable. Do that once; it is free and it is the difference between a slip and
a re-download.

`nativeall` has two modes, and the second is the one to reach for when you want
to *see* any of this rather than read a table:

```sh
irgo-winvm example:try   # on the Mac
irgo-winvm try        # the same window, inside Windows
```

A tray icon you click, all four native file dialogs, a menu bar whose items
report back, a Dock icon that changes colour, and a second copy of the program
handing its arguments to the first. Default (no flags) is the unattended
report, which is what the VM runs.

Four Go modules, deliberately not one: glaze and native stay out of the CLI's
dependency graph, which is what lets it cross-compile with nothing installed.
`go build ./... && go vet ./... && go test ./utmvm/...` walks all four.

## Contributing

**Read [AGENTS.md](AGENTS.md) first** — it is short, and it is aimed at
contributors human and AI alike. Most of the duplication this repo has had to
clean up was written by an agent that did not check what already existed, so the
rules there are about searching before writing, treating the long comments as
findings rather than decoration, and verifying against the VM rather than the
compiler.

See [RESULTS.md](RESULTS.md) for measured results per platform,
[UPSTREAM.md](UPSTREAM.md) for what this repo has found in glaze and native, and
[REFACTOR.md](REFACTOR.md) for the planned cleanup and how it is enforced.

## Requirements

macOS on Apple Silicon, UTM 4.7.x, and a Windows 11 **ARM64** ISO. Get the ISO
with [CrystalFetch](https://github.com/TuringSoftware/CrystalFetch), which is
what UTM's own authors ship for the purpose.
