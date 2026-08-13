# Working on this repository

Read this before writing code, not after. It is the approach, not an index —
any list of where things live goes stale the day someone moves them.

## Three stages, isolated

`iso-create` gets the Windows media. `vm-create` makes a VM from it.
`app-create` puts a binary on that. Each has an undo, and each owns its own
paths and constants in its own files:

- **iso** knows nothing about UTM. It is a download from Microsoft and an ISO
  built with `xorriso`, and it works on a machine that has never had a
  hypervisor. It once called into UTM's bundle directory to decorate an error
  message, which meant fetching media required UTM to be installed.
- **vm** owns UTM entirely: finding it, installing it, `utmctl`, the bundle
  layout, the guest tools.
- **app** drives the guest through `vm`'s `utmctl`, and owns the guest-side
  paths.

Dependencies run one way: `vm` and `app` use the ISO API, nothing comes back.
`doctor` reports on all three and calls into them rather than holding its own
copy of where anything is.

If you are about to make one stage reach into another's paths, stop.

## One way to do each thing

There were four ways to run a binary in the guest, differing only in where it
landed and which session ran it, and every caller had to pick. There were three
answers to where media lives, all live at once. A second route is a second
answer.

## Nothing returns success without checking it did the thing

Nearly every bug here was this: a download renamed without verifying its length,
`ExitCode: 0` when the output could not be fetched, `nil` after five failed
boots, an error value that could never be non-nil, an ISO built and never
checked.

Closing a file you *wrote* can fail, and that is where a full disk shows up.
Closing one you read cannot.

An operation that cannot report failure also cannot be undone, because it does
not know what it did.

## Every action owes an undo

The commands come in pairs. The undo is what lets a failed step be fixed and
re-run, and it must work from any starting point — stopping early because half
the work was already gone leaves the other half behind for good.

Deleting nothing is success, not an error, or the undo cannot be run twice.

## "Cannot tell" is not "safe"

Guards are written `if ok && bad { refuse }`, so a question that cannot be
answered does not refuse — it allows. Every guard here stands in front of
something destructive, so that is backwards.

Answer three ways: yes, no, and *could not determine*. The caller handles the
third explicitly, refusing by default.

## Check before you claim

Everything below was asserted wrongly first, then measured:

- `ln` to an immutable file is `EPERM`, so protecting the ISO silently turned a
  hardlink into a 5 GB copy.
- `rm` on a bundle holding that hardlink fails, so every VM built from a
  protected ISO was undeletable.
- `utmctl delete` prints its failure and **exits 0**.
- `utmctl` will not drop a registry entry whose bundle is gone, so removing a
  bundle behind its back makes a phantom it cannot then recover from.
- `net/http` already rejects a short body, so a length check added to the
  downloader was unreachable — proven by disabling it and watching the test
  still pass.

Grep counts comment mentions as call sites; it gave three wrong answers in one
afternoon. **Use the compiler and the analysers**: delete the symbol, rebuild,
run the tests, put it back if either fails. `mise run go:lint` finds what grep
does not.

And check on every platform. Deleting a function from `sysfile_other.go` passed
every check that was running, because they were all darwin. `mise run go:check`
cross-compiles.

## A test that cannot fail is not a test

Every assertion needs a negative control: **break the thing, watch the test go
red, put it back.** Ten seconds, by hand, when you write the test. There was a
mise task automating this across eight mutations; it was fifty lines of shell
matching exact source text, so it broke on the first rename and once left a
mutated file in a commit. The habit catches what matters — a test born vacuous —
and the machinery only caught tests weakened later, which did not happen.

This is not theory. A test for the scan cache passed against a mutation that
disabled the check it was testing, because the case it built also changed the
other field. And a test for "the build records its verdict" is marked as not
proving that, because deleting the build's call leaves it green.

If a property can only be verified by measurement, record the measurement in
`RESULTS.md` with a date rather than writing a test that looks like coverage.

## Say what is happening, and where

A command that prints nothing for fifty seconds is indistinguishable from one
that has hung. Announce each step *before* doing it, name every path, and print
elapsed time. "Not found" without a location cannot be checked.

That is how the 77-second ARM64 scan was found: it was always there, and nothing
said so.

## The comments are findings

Long comments record what cost hours and is not recoverable from the code: why
the display is `virtio-ramfb-gl`, why ESD image 3 needs `--boot`, why
`utmctl suspend --save-state` must never be called, why `%q` must not be
re-escaped for AppleScript.

Move them with their code. Do not compress or tidy them. If one is wrong, fix
the fact — do not delete the explanation. When you correct a measurement, look
for the other copy.

## Verify against the VM

Unit tests cover the ISO stage well, the VM and run stages only at the edges.
Anything touching a real guest is proven by running it, and those paths fail
silently.

Use a disposable VM. Running a binary pushes it into the guest and executes it,
which is a mutation; losing a 45-minute install to a test is not a trade worth
making.

## A glaze or native bug is fixed at crgimenes

Non-negotiable, and the reason this project exists. A bug worked around in an
example still ships to everyone using those libraries, and the workaround hides
it. `UPSTREAM.md` is the ledger.

## Where things go

One place, fixed, nothing to configure:

```
~/Library/Application Support/irgo-winvm/
  media/    the ISO, the .esd it was built from, and scratch
  bin/      binaries staged into a VM
  logs/     every command, appended across runs
  shots/    a screenshot per stage of every run
```

VMs go where UTM keeps them, because UTM reads nowhere else. Committed
screenshots — evidence chosen for documentation — are `docs/screens/`, kept
apart from `shots/` so the record does not drown in the noise.

It needs macOS on Apple Silicon, and UTM, which `vm-create` installs from its
signed `.dmg` if it is missing. `wimlib` and `xorriso` are installed by
`iso-create` and removed by `iso-delete`, only when building media from scratch.

One thing nothing can install for you: macOS asks, once, in a dialog, whether
this may control UTM. `vm-create` checks that before doing anything expensive,
because without it a boot cannot be driven and the failure arrives forty minutes
into an install as a timeout that mentions nothing about permissions.

## The four programs it runs

Split by what a capability *needs*, with exactly one owner each. A capability
probed in two places is two things to fix when upstream changes and two reports
to reconcile when they disagree.

| program | library | needs |
|---|---|---|
| `probe/` | native — clipboard, power, single-instance, mmap | headless; runs under the guest agent in session 0 |
| `examples/glaze-all` | native + glaze — openurl, tray, no-capture, menu, file dialogs, app icon | `-gui` |
| `glaze-probes/verify` | glaze — the portless `app://` path | `-gui` |
| `glaze-probes/verifyevents` | glaze — the Events bridge | `-gui` |

`glaze-all` opens its window and waits by default, because it is an example
before it is a test. `-probe` is the unattended report, and the mise task passes
it.

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
| `utmctl exec` | never returns the guest's output and always exits 0. Everything that needs output goes through a batch file that captures it to a file the host then pulls |
| `utmctl exec` with a whole command line as one string | the agent looks for a file by that entire name and answers "No such file or directory" — indistinguishable from a dead agent |
| `cmd` `del` on a glob matching nothing | **exits 1**. So an undo succeeds while there is something to undo and fails the moment there is not |
| `dir` and `del` disagree on the message | `dir` says "File Not Found", `del` says "Could Not Find". Handling one and not the other prints the other's error text as if it were a filename |
| `utmctl suspend --save-state` | **reports success and power-cuts the guest.** No state file, VM left `stopped`, guest's next boot goes through "Diagnosing your PC". Use plain `suspend` |
| `ln` to an immutable file | `EPERM` — so protecting the ISO silently turns a hardlink into a 5 GB copy |
| `rm` on a bundle holding that hardlink | `EPERM`, directory left behind. Clear the flag first, restore it after |

### In the guest programs

| trap | what happens |
|---|---|
| every package defines its **own** `ErrUnsupported` | none wrap `errors.ErrUnsupported`, so a check against that alone matches nothing and a platform behaving as documented reports **FAILED** with a non-zero exit. `glaze.SetAppIcon` is unsupported on Windows by design — the platform this exists to test |
| `tray.Run` **blocks**, driving the event loop until `Stop` | waiting on it deadlocks. Post it and leave it; `Stop` is safe from any goroutine |
| the tray started **before** the window | glaze's `New` runs a temporary `[NSApp run]` that ends only when `applicationDidFinishLaunching` fires — once per process. A tray started first consumes it and `glaze.New` blocks forever, with no window and nothing printed |
| `menu.Set` with no `Options.Window` | required on Windows (the HWND); it returns an error naming it, on the one platform that matters here |
| `menu.Set` with `Options.Dispatch` set **before** `Run` | Set blocks until its UI work has run, and nothing drains the queue until the run loop starts |
| `file://` URL built by concatenation | Windows paths are `C:\dir`; the URL wants `file:///C:/dir`. Without the leading slash `net/url` writes `file:C:/dir` and ShellExecuteW rejects it |
| `openurl.Open != nil` as a capability check | a function value is never nil. `go vet` says so outright — the check checked nothing |
| an absolute `app://` URL for a sub-resource on Windows | glaze emulates the scheme with a virtual host, so the document loads from `https://app.localhost/` and an absolute `app://` sub-resource names a scheme WebView2 has never heard of. Fails silently: no error, no console message, no stylesheet |

## Do not

- Split the package up. Its parts are coupled.
- Touch the assets, the answer file or the plist template without running a real
  install. UTM rejects a bad config with one generic *"cannot import this VM"*
  that names no field.
- Export anything nothing uses.
- Put logic in `mise.toml`. Tasks call the binary; if a task needs to do more
  than that, it belongs in the binary.
- Land a refactor in one commit. One concern per commit, each verified.
