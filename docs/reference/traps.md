---
title: Known traps
nav_order: 1
parent: Reference
---

# Known traps

Each of these fails silently or misleadingly. UTM rejects a bad config with one
generic *"cannot import this VM"* that names no field; a wrong boot command
produces a prompt nobody sees; a truncated ISO produces a VM that will not boot.

One line each. The `utmctl` rows are **defects in UTM**, written up with
severity, reproduction and status in [Upstream bugs](upstream.md#utm); keep the
detail there and only the reminder here. The Worker's TinyGo and tooling traps
are [on its page](../worker.md#traps).

## Host, UTM and the ISO

| trap | symptom | what to do |
|---|---|---|
| `virtio-gpu-pci` display | no framebuffer on aarch64 and no legacy VGA; the guest boots **invisibly** and looks hung | use `virtio-ramfb-gl` |
| VirtIO system disk | Windows ARM64 has no inbox driver; Setup reports no drive found | use **NVMe** |
| `virtio-net-pci` without guest tools | no inbox driver, **no network at all** in the guest | install the guest tools |
| missing `PS2Controller` | non-optional decode with no default; the whole config is rejected | include it |
| `UsbBusSupport: "USB3_0"` | config rejected | the enum is `"2.0"` / `"3.0"` |
| `CPUFlags` | config rejected | the keys are `CPUFlagsAdd` and `CPUFlagsRemove` |
| UTM's schema read from `main` | `main` was v5.0.4 while the app was v4.7.5, and they disagree | read the schema at the **tag** of the installed version |
| `gh run list --commit` with a short SHA | an empty list, not an error — indistinguishable from "not started yet"; it matches the **full 40 characters only** | use `mise run ci:watch` |
| reading a Windows ISO as ISO9660 | every path fails: the ISOs are **UDF**, because `install.wim` exceeds ISO9660's 4 GB limit | read it as UDF |
| answer file on a FAT disk | Setup ignores it and runs interactively | put it on an ISO9660 **CD** |
| ISO padded past its declared volume size | mounts on macOS, ignored by Setup | trim to the PVD size |
| Joliet disabled | `autounattend.xml` becomes `AUTOUNAT.XML`, which Setup never looks for | keep Joliet on |
| El Torito marked BIOS (`-b`) | correctly sized and named, **does not boot** | UEFI needs `-e` |
| `start utm-guest-tools-*.exe` | `start` does not expand wildcards; the installer silently never runs | expand the name with `for` first, as `autounattend.xml` does |
| `utmctl start`, then keystrokes | a headless VM has no display, UTM routes input through it, and the keystrokes vanish | start it through UTM itself so a display opens (`StartWithDisplay`) |
| a request that has to launch UTM: `osascript` with UTM closed, which is what `vm-create` and `capacity` sent first; also `utmctl` through Homebrew's symlink, closed or in the first 0.3 s after `open` | that request is answered, and **every VM start after it hangs** for as long as that UTM process lives: `utmctl list` and `status` go on answering at once, `start` and `ip-address` fail with `OSStatus error -1712`, and the AppleScript `start` waits two minutes for `AppleEvent timed out. (-1712)`. `utmctl` by its path inside UTM.app did not do it, closed or at 0 s. Why is not known ([measured 2 Oct 2026](../findings.md#the-request-that-launches-utm-hangs-every-later-start--measured-2-oct-2026), [Upstream bugs](upstream.md#utm-stops-answering-start-requests)) | never let a request be what launches UTM, and never poll UTM to see whether it is ready: that poll is a request. Ask macOS whether it is running (`application id "com.utmapp.UTM" is running`), and if not `open -g -a` it and send nothing for 2 s. Every request goes through `utmCommand`, which does that. By hand, open UTM before `utmctl` |
| UTM already in that state (something else launched it with a request) | the same hang, on the first start | quit UTM and open it again, which stops every VM it runs: `StartWithDisplay` does it once, only when UTM lists every VM as stopped (`recoverUTM`), and otherwise says which are up |
| driving a boot on a VM that is already running | it may be a working desktop, not a UEFI shell; keystrokes land in whatever has focus (`docs/screens/vm/running-no-agent.png`: three Bing tabs searching for the EFI path) | never type at a VM this code did not just start; look at `vm-screen` |
| `utmctl delete` | prints its failure and **exits 0** | check that the bundle is gone afterwards |
| bundle removed behind UTM's back | `utmctl` will not drop a registry entry whose bundle is gone, leaving a phantom it cannot recover from | delete through `utmctl`. To recover a phantom, recreate an empty stub at the expected path so UTM has something to remove |
| `utmctl exec` | never returns the guest's output and always exits 0 | run a batch file that captures output to a file, then pull the file |
| `utmctl exec` with a whole command line as one string | the agent looks for a file by that entire name and answers "No such file or directory" — indistinguishable from a dead agent | pass arguments separately |
| `utmctl suspend --save-state` | **reports success and power-cuts the guest**: no state file, VM left `stopped`, next boot goes through "Diagnosing your PC" | use plain `suspend` |
| `cmd` `del` on a glob matching nothing | **exits 1**, so an undo fails as soon as there is nothing left to undo | treat "nothing matched" as success |
| `dir` and `del` report a missing file differently | `dir` says "File Not Found", `del` says "Could Not Find"; handling only one prints the other's text as if it were a filename | handle both |
| `ln` to an immutable file | `EPERM`, so protecting the ISO silently turned a hardlink into a 5 GB copy | clear the flag first, restore it after |
| `rm` on a bundle holding that hardlink | `EPERM`, directory left behind, so every VM built from a protected ISO was undeletable | clear the flag first, restore it after |
| a length check on a download | unreachable: `net/http` already rejects a short body | do not add one; it was proven dead by disabling it |
| testing holes on a small file | APFS keeps an 8 MiB file fully allocated whatever its holes; at 64 MiB they are holes (measured 30 Sep 2026) | make a sparse test file 64 MiB or more |
| aws-sdk-go-v2 `PutObject` with default settings | sends `aws-chunked` bodies with a trailing CRC, which a plain S3 server or fake does not expect | `RequestChecksumCalculation: WhenRequired` |
| reading or writing UTM's container | `Operation not permitted` for `ls`, `cat`, `touch`, even unsandboxed (macOS App Data protection); `stat` on a known path works | have UTM do it through AppleScript; `vm-golden-push -bundle` takes a copy UTM exported, not the bundle in place |
| a bundle written into UTM's folder | UTM only rescans at launch, and restarting it stops every running VM | write it elsewhere and `import` it |
| `utmctl clone` / `duplicate` | keeps the source's MAC unless a global setting (default off) says otherwise; two clones fight over one DHCP lease | set the MAC in the same `duplicate ... with properties` |
| an APFS clone of an immutable file | the clone is immutable too (`copyfile` copies BSD flags), so UTM cannot delete it later | clear the flag on the clone |
| a long comment in `autounattend.xml` | Setup ignored the **whole** answer file and stopped at "Select language settings"; the same element under a one-line comment installed (30 Sep 2026; that comment was the only one with `%` in it, the trigger was not isolated). Unit tests pass either way | keep comments in the answer file short; prove any change to it with an install |
| Windows 11 24H2 left alone | encrypts the disk on its own (Device Encryption), so a copy of it does not compress | `PreventDeviceEncryption` in specialize; decrypt before sealing |
| `utmctl file push` | about **0.4 MB/s**; a 50 MB file took 1 min 17 s even zipped | `Push` goes over the guest's SMB share (see [How a binary gets into the guest](../concepts/architecture.md#how-a-binary-gets-into-the-guest)) |
| the guest connecting to a server on the Mac | hangs: the Mac's firewall is in stealth mode and drops incoming connections | connect from the Mac to the guest instead, never ask for a firewall change |
| creating an SMB share on Windows 11 24H2 | Windows enables `File and Printer Sharing (Restrictive) (SMB-In)` itself, open to **any** address, and leaves it on after the share is removed | `file-share.ps1` turns it off both ways, and its own rule allows only the local subnet |
| Windows' own firewall rule for OpenSSH Server | the guest's network is filed as Public, and connections to port 22 were dropped until the rule was set for every profile (by hand, 2 Oct 2026, build 26100.4349) | `vm-ssh.ps1` adds its own rule for every profile, from the local subnet, and turns Windows' off |
| an administrator's SSH key in `~\.ssh\authorized_keys` | sshd does not read it: for an administrator the keys are in `C:\ProgramData\ssh\administrators_authorized_keys`, and that file is ignored unless only Administrators and SYSTEM can write it | write that file and restrict it with `icacls`, as `vm-ssh.ps1` does |
| `Add-WindowsCapability` for `OpenSSH.Server` | minutes the first time, with nothing printed | `vm-ssh-create` says so before it starts and allows 20 minutes |
| a cloud-init seed on a **USB** CD | cloud-init never runs: no account, no network configuration at all, two minutes in `systemd-networkd-wait-online`, then a login prompt with the hostname `ubuntu` that nobody can use. From the host it is a VM that started and never answers (twice, 2 Oct 2026; the cause was not isolated) | attach the seed as a **VirtIO** CD, where it is `/dev/vdb` |
| Ubuntu's cloud image and port 22 | `ssh.socket` is enabled and listening from the first boot, on every address | the seed turns it off, and `vm-create` checks nothing listens |
| `utmctl ip-address` on a Linux VM's first boot | fails at once with `OSStatus error -2700`, then hangs, until cloud-init has installed `qemu-guest-agent`; the same as a VM that will never answer | wait with a deadline; `vm-create` gives a first boot ten minutes |
| the Mac's screen locked | `vm-screen` and every boot photograph show an empty window, or fail with `could not create image from window`; nothing says the lock is why | unlock the Mac to see a VM. A locked Mac still boots a Linux VM, which needs nothing typed |
| `fstrim -a` in a Linux VM | `/boot/efi` (vfat, on the VirtIO disk) fails with `FITRIM ioctl failed: Input/output error` and the command exits 1, after trimming the rest (3 Oct 2026) | trim the ext4 filesystems by name, as `vm-golden-seal.sh` does |
| telling a VM's disk from its CD in UTM's AppleScript | there is no such property, and `removable` is `false` for both: a Linux VM's seed CD and its disk are each a VirtIO drive that is not removable (3 Oct 2026) | keep the first drive on the system disk's interface; every bundle this tool writes lists the disk first (`utm-clone.applescript`) |
| a running VM and an idle Mac | a VM holds no power assertion: `irgo-win11` started and `pmset -g assertions` listed nothing from UTM or QEMU (1 Oct 2026), so the Mac sleeps on its idle timer and every VM stops with it | the keeper holds `caffeinate -i -s` while a VM runs on AC power ([The keeper](../guides/using.md#the-keeper-vms-that-stay-up)) |
| `utmctl list` from a process macOS has not allowed to control UTM (pitchfork's supervisor, before its Automation prompt is answered) | prints `Error from event: ... (OSStatus error -1743.)` on stderr, the header alone on stdout, and **exits 0**: it reads as UTM having no VMs (3 Oct 2026) | `List` treats that stderr as an error; answer the prompt with Allow, or turn the process on in System Settings > Privacy & Security > Automation |
| a VM's record written by a version of the tool from before a field | the field is dropped: the record is decoded into the old struct and written whole, so v0.7.0 touching a VM erases its `keep_running` | mark it again (`vm-keep-create`), or run one version |
| a Go file named `*_linux.go` | compiled only for `GOOS=linux`, so on a Mac everything in it is `undefined`, with no word about the file name | name it otherwise (`linux_vm.go`) |

## Driving a window on Windows, and the hosted runner

| trap | symptom | what to do |
|---|---|---|
| letting glaze create the window on Windows | glaze shows it with `SW_SHOW` and moves focus into WebView2, so it became the foreground window as it started, in every drive test (traced on `windows-11-arm`, 1 Oct 2026); a background click also activates it (Chromium focuses its window on mouse-down) | create the window with `WS_EX_NOACTIVATE`, show it with `SW_SHOWNOACTIVATE`, and pass it as `glaze.Options.Window` (`drive`'s `backgroundWindow`) |
| comparing the foreground only before and after | an app that took the foreground hands it to another window when it closes, so the takeover was reported as "something else switched apps" (TestDriveClick, run 36804946289) | record the foreground at every step and fail on the app's own window (`Session.TookForeground`) |
| the foreground named by HWND and pid | "window 0x30284 (pid 11052)" says nothing a day later | name the executable and the window title (`drive.Frontmost`) |
| keys to a WebView2 window that is never activated | dropped whenever the page does not have focus | click into the field first, check it is the active element, and resend only keys that were dropped whole, logging each retry (`TestDriveType`) |
| the `windows-11-arm` desktop at job start | a full-screen "Microsoft account" prompt (`WWAHost.exe`) over everything, Start open under it, a "System Properties" dialog; Chromium drops wheel events aimed at a covered window | `.github/scripts/runner-desktop.ps1 -Clear`, which lists, clears and checks |
| stopping Start and Search once | they came back open 3 s later on one runner | Escape and stop their hosts until they stay closed, then check |
| stopping WSL's updater (`wsl.exe --update --confirm --prompt-before-exit`) | back within 30 s in a new Windows Terminal window that takes the foreground, every ~90 s for the whole job ([runner-images#14264](https://github.com/actions/runner-images/issues/14264)): the runner's `provjobd.exe` runs `wsl.exe` every 30 s and the inbox stub (WSL is not installed) starts the updater whenever none is running; `wsl --update` exits 1, "not installed" | an Image File Execution Options `Debugger` makes `wsl.exe` run `cmd /c exit 1` for the rest of the job (traced with `runner-desktop.ps1 -Watch`) |
| a native command last in a `pwsh` step | the step fails with that command's exit code (GitHub's wrapper exits with `$LASTEXITCODE`), though the script carried on | end the script with an explicit `exit 0` |

## In the guest programs

| trap | symptom | what to do |
|---|---|---|
| each package defines its **own** `ErrUnsupported` | none wrap `errors.ErrUnsupported`, so a check against that alone matches nothing, and a platform behaving as documented reports **FAILED** with a non-zero exit. `glaze.SetAppIcon` is unsupported on Windows by design | check each package's sentinel until the [upstream fix](upstream.md#2-native--glaze--errunsupported-sentinels-do-not-wrap-errorserrunsupported) is released |
| `tray.Run` **blocks**, driving the event loop until `Stop` | waiting on it deadlocks | post it and leave it; `Stop` is safe from any goroutine |
| the tray started **before** the window | glaze's `New` runs a temporary `[NSApp run]` that ends only when `applicationDidFinishLaunching` fires, once per process. A tray started first consumes it and `glaze.New` blocks forever, with no window and nothing printed | create the window first |
| `menu.Set` with no `Options.Window` | returns an error naming it, on Windows, where the HWND is required | pass the window |
| `menu.Set` with `Options.Dispatch` set **before** `Run` | `Set` blocks until its UI work has run, and nothing drains the queue until the run loop starts | call it after `Run` starts |
| `file://` URL built by concatenation | Windows paths are `C:\dir`; the URL needs `file:///C:/dir`. Without the leading slash `net/url` writes `file:C:/dir` and ShellExecuteW rejects it | build it with `net/url` and a leading slash |
| `openurl.Open != nil` as a capability check | a function value is never nil, so it checks nothing; `go vet` says so | call it and check the error |
| Windows Update's restart prompt, "We've got an update for you" | killing `MoNotificationUx.exe` leaves it on screen: that process only requests it. The window is a `Shell_SystemDialogProxy` owned by `PickerHost.exe`, and `WM_CLOSE` to the proxy removes the window but leaves its picture | stop `PickerHost.exe`, then the requester, as `desktop-reset.ps1` does |
| `EnumWindows` or UI Automation to find what is on the Windows screen | both list only the desktop's own z-order band. The taskbar, Start, Search and the update prompt live in others, so all of them were missing while on screen | walk top-level windows with `FindWindowEx(NULL, prev, NULL, NULL)`, and treat a cloaked window as hidden |
| checking the foreground window for an open Start menu | the check runs in a console of its own, which has the foreground, while Start stays open behind it | look for an uncloaked `CoreWindow` of `StartMenuExperienceHost` or `SearchHost` |
| a notification toast (OneDrive's "Turn On Windows Backup") | an uncloaked `CoreWindow` of `ShellExperienceHost` titled "New notification". Stopping that host brings it straight back | stop the sending app and clear its notification history (`ToastNotificationManager.History.Clear`) |
| `$null` passed to a `string` parameter of a .NET method from PowerShell | PowerShell passes `""`, so `FindWindow('Shell_TrayWnd', $null)` asks for an empty title and `FindWindowEx(0, h, $null, $null)` finds nothing | call from C# (`Add-Type`) or pass `[NullString]::Value` |
| the Mac's Command key | UTM forwards it as the Windows key, so Cmd-Tab on the Mac opens Start in the guest, over screenshots and `-gui` windows | `Scancode Map` remaps both Windows keys (answer file, `vm-repair`); a reboot applies it |
| `glaze.New` called after other work on the main goroutine, on macOS | SIGTRAP inside `[NSApp run]` in about 1 run in 7: the goroutine had moved off the main OS thread, and glaze pins the thread in `New`, not in an `init` ([Upstream bugs §5](upstream.md#5-glaze--new-crashes-if-the-main-goroutine-has-moved-thread)) | call `glaze.New` before anything slow on the main goroutine |
| an absolute `app://` URL for a sub-resource on Windows | glaze emulates the scheme with a virtual host, so the document loads from `https://app.localhost/` and an absolute `app://` URL names a scheme WebView2 does not know. No error, no console message, no stylesheet | reference assets relatively ([Upstream bugs §1b](upstream.md#1b-glaze--absolute-app-urls-silently-do-not-load-on-windows)) |
