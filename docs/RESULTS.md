# Results

What has been measured, newest first. Each entry keeps the date it was measured
and the numbers it found. If a later run changes a number, correct it here too.

The goal is parity: the same probes, against the same glaze version, on both
platforms. A pass on one OS proves nothing on its own. Entries up to 30 Sep
2026 name the probes they ran: `examples/probe/` (native capabilities), and
`examples/verify` and `examples/verify-events` (glaze's `app://` scheme and its
Events bridge). Since then the same checks are the test suite
`examples/conformance`, and [GLAZE-STATUS.md](GLAZE-STATUS.md) holds its latest
run per platform.

## At a glance

| date | result |
|---|---|
| 2 Oct 2026 | [the request that launches UTM hangs every later start: an AppleScript to a closed UTM, 4 of 4; opened first, 0 of 3. `capacity` did it to itself 2 of 2 before the fix, 0 of 2 after](#the-request-that-launches-utm-hangs-every-later-start--measured-2-oct-2026) |
| 2 Oct 2026 | [a Linux VM that claude-rig rigs over SSH: `vm-create -os linux` 1 min 42 s with the download, `vm-ssh-create` 2.7 s, the rig's second run changed nothing; a Windows clone behaved as before](#a-linux-vm-that-claude-rig-rigs-over-ssh--measured-2-oct-2026) |
| 2 Oct 2026 | [a Linux guest by hand, before any code: Ubuntu's cloud image boots untyped, cloud-init reads the seed on a VirtIO CD and not on a USB one, the guest agent answers 34 s after the first start](#a-linux-guest-by-hand-before-the-code--measured-2-oct-2026) |
| 2 Oct 2026 | [SSH into a clone: `vm-ssh-create` took 9 min 46 s the first time and 16 s on a repeat; a key login worked; `vm-ssh-delete` closed the port](#ssh-into-a-clone--measured-2-oct-2026) |
| 1 Oct 2026 | [VM capacity: a clone wrote 0.28 GiB of its own in 1.5 h of work; a 4 GiB clone beside irgo-win11 passed glaze-check and vm-check; the first prune freed 103 MB](#vm-capacity-what-a-clone-really-costs--measured-1-oct-2026) |
| 1 Oct 2026 | [several callers on one Mac: an agent refused the owner's VM, a second clone refused for memory, a busy clone kept and an idle one reaped](#several-callers-on-one-mac--measured-1-oct-2026) |
| 1 Oct 2026 | [the drive tests on GitHub's Windows ARM64 runner: what took the foreground, and three green runs in a row](#the-drive-tests-on-githubs-windows-arm64-runner--measured-1-oct-2026) |
| 1 Oct 2026 | [the real golden image through the private R2 cache: 8.4 GB, pulled byte-identical in 4 min 37 s](#the-real-golden-image-through-the-private-r2-cache--measured-1-oct-2026) |
| 1 Oct 2026 | [a VM of your own in 23 s: install 12 min 24 s once, seal 2 min 49 s, then `vm-create` clones and boots](#a-vm-of-your-own-in-23-s--measured-1-oct-2026) |
| 1 Oct 2026 | [a glaze app driven by real OS input in the background: type, click, click-at, scroll, all `isTrusted`, frontmost app unchanged](#a-glaze-app-driven-by-real-os-input--measured-1-oct-2026) |
| 30 Sep 2026 | [pushes go over SMB: 49 MB in 1.6 s instead of 1 min 17 s; `glaze:windows` 31 s → 24 s](#pushes-go-over-smb--measured-30-sep-2026) |
| 30 Sep 2026 | [GoReleaser rebuilds v0.4.1 byte for byte](#goreleaser-reproduces-the-published-v041-byte-for-byte--verified-30-sep-2026) |
| 16 Aug 2026 | [an agent uploaded and ran a binary over HTTP](#an-agent-uploaded-pushed-and-ran-a-binary-over-http--verified-16-aug-2026) |
| 14 Aug 2026 | [an agent drove every step over MCP](#an-agent-drove-the-whole-thing-over-mcp--verified-14-aug-2026) |
| 13 Aug 2026 | [the ISO architecture check went from 77.2 s to 0.0 s](#the-iso-scan-verdict-is-recorded-at-build-time--13-aug-2026) |
| 12 Aug 2026 | [an ISO this repo built installs Windows](#a-self-built-iso-installs-windows--verified-12-aug-2026) |
| 12 Aug 2026 | [suspend and resume takes 400 ms](#suspend-and-resume--400-ms-verified-12-aug-2026) |
| 12 Aug 2026 | [glaze and native work on Windows ARM64](#glaze-and-native-on-windows-arm64--measured-12-aug-2026), with one upstream bug |
| 11 Aug 2026 | [Windows installs unattended](#the-unattended-install--verified-11-aug-2026) |
| — | [the macOS baseline](#macos--verified) |
| not yet | [x64 under emulation](#still-to-measure-x64-under-emulation) |

## The request that launches UTM hangs every later start — measured 2 Oct 2026

**Result:** a UTM that an AppleScript had to launch answers that script and
then never answers a VM start, until it is quit and reopened. `vm-create` and
`capacity` begin with an AppleScript, so run with UTM closed they did it to
themselves: that is the two-minute `AppleEvent timed out. (-1712)` of the
same morning. Opening UTM with `open -g -a` first prevents it. Why is not
known ([UPSTREAM.md](UPSTREAM.md#utm-stops-answering-start-requests)).

M2 Pro, 16 GiB, macOS 27.0.1, UTM 4.7.5, the screen locked throughout. One
VM, `claude-rig-test`, a 4 GiB clone of the golden image (Windows 11 ARM64);
`irgo-win11` and `irgo-golden` stopped and never touched.

**Method.** Each trial: stop the VM, `osascript -e 'quit app "UTM"'`, wait
until `pgrep` no longer finds UTM's process, sleep 2 s. Then the first request
in the table, then `utmctl start` with a 15 s limit, then `utmctl status`
every 0.2 s for 3 s. "Hung" is no answer from the start in 15 s and the VM
still `stopped`; "worked" is `started`, each time 2.7 to 2.9 s after the
start request. After a hang, AppleScript's `activate` and `start` was tried
in the same UTM with a 20 s limit, and hung too, 7 of 7. Two sessions the
same day: the first ran `utmctl` as found on `PATH`, which is Homebrew's
symlink `/opt/homebrew/bin/utmctl`; the second ran both that and
`/Applications/UTM.app/Contents/MacOS/utmctl`, alternating, after the tool's
own runs did not show the hang the first session predicted.

| UTM before the first request | first request, and when | later starts hung |
|---|---|---|
| closed | `osascript -e 'tell application "UTM" to count virtual machines'` | **3 of 3** |
| closed | `osascript`, `start virtual machine named …` itself (30 s limit) | **1 of 1**, and once in the first session |
| closed | `utmctl list` through the symlink | **2 of 2** |
| closed | `utmctl list` by its path in UTM.app | 0 of 4 (one followed by the AppleScript start instead: running 4.6 s later) |
| closed | `utmctl start` by its path in UTM.app | 0 of 2: the VM was running 3.0 and 3.1 s later |
| `open -g -a UTM` | the same `osascript` count, at once | 0 of 3 |
| `open -g -a UTM` | `utmctl list` by its path, at once, every 0.1 s until it lists the VM | 0 of 4 |
| `open -g -a UTM` | `utmctl` through the symlink, at once (`list` 9 times, `status` once) | **10 of 10** |
| `open -g -a UTM` | `utmctl list` through the symlink after 0.3, 0.6, 1.0 or 1.5 s | 0 of 8 |
| `open -g -a UTM` | nothing for 1 to 8 s, then the start through the symlink | 0 of 9 |

In the first session the same held with `open -a UTM`, in the foreground.
`utmctl` resolves the symlink before it looks for UTM.app, so the two ways of
running it differ in something else; it was not found.

**How long things take.** Whether UTM is running: `pgrep` 0.01 s, `osascript
-e 'application id "com.utmapp.UTM" is running'` 0.04 s (0.07 s the first
time), against 0.14 s for `utmctl status`; asked with UTM closed it answered
`false` and no UTM process existed a second later, 7 of 7. `quit app "UTM"`
until the process is gone: 0.1 to 0.2 s. `open -g -a UTM` until `utmctl list`
shows the VM: 0.7 to 1.3 s.

**The tool, before and after.** Binaries built from `origin/main` (c6000d2)
and from the branch that added `utmCommand`, each from UTM closed.

| command | before | after |
|---|---|---|
| `capacity`, then `utmctl start` by its path | exit 0 in 4.0 and 2.9 s; the start **hung, 2 of 2** | exit 0 in 4.8 and 5.0 s, saying `UTM is not running: opening /Applications/UTM.app in the background, then waiting 2s before asking it anything` on stderr; the start worked, 2 of 2 |
| `app-create -vm claude-rig-test hello.exe` on the stopped VM, which boots it through `StartWithDisplay` | exit 0 in 36.2 s, once: its first request is `utmctl list` by its path, which does no harm | exit 0 in 32.9, 38.4, 33.3 and 34.0 s. The line about opening UTM at 0.0 to 0.1 s, the next line at 2.7 s, the start answered by 10.1 s, Windows answering 17.7 s after that |

One more run after the change did not finish: the start was answered and the
VM was `started` at 10.1 s, and Windows had not answered six minutes later.
The VM had been power-cut with `utmctl stop` before each of the first three
runs; stopped and started by hand it answered in 66 s. The later runs shut
Windows down from inside first.

With UTM already open the new binary printed nothing about opening it and
sent the start at once (0.5 s in, answered by 8.7 s). Windows then took 64 s
to answer, not 17.7 s, so two runs under a 60 s limit were cut off waiting for
it and a third finished in 102.2 s. The old binary took 100.1 s for the same,
so that is not from this change; why a VM boots more slowly in a UTM that has
already run it was not looked at.

Not measured: `recoverUTM` and `utmApp.restart` against a UTM in this state
(unit tests against a fake only); two commands racing to a closed UTM (the
lock is tested with real `flock`, against a fake UTM); `vm-create` from UTM
closed, which was the original failure (on `claude-rig-test`, an existing
clone, it stops at "no media" before any start); any UTM but 4.7.5.

## A Linux VM that claude-rig rigs over SSH — measured 2 Oct 2026

**Result:** `vm-create -os linux` made an Ubuntu Server 24.04 VM from nothing
in 1 min 42 s, `vm-ssh-create` let the Mac's key in in 2.7 s, and
[claude-rig](https://github.com/joeblew999/claude-rig)'s `push` rigged it and,
run again, changed nothing. M2 Pro, 16 GiB, UTM 4.7.5, a 4 GiB Windows clone
of another caller's running throughout. The binary was built from the branch
that added `-os`; the key was the Mac's own `~/.ssh/id_ed25519.pub`. One VM,
`linux-dev-a1`, deleted after. The Mac's screen was locked throughout.

| step | command | what happened |
|---|---|---|
| create | `vm-create -os linux -vm linux-dev-a1 -install -golden=false` | **1 min 42 s**, exit 0. The image downloaded in 43 s (620,224,512 bytes, SHA-256 as pinned); converted to a raw disk, the seed written and the bundle imported by UTM in 5.0 s; started and answering in 48 s, 7 s of it the start request; the check 1.5 s: `cloud-init: ok (status: done)`, `account: ok (dev, sudo without a password, password locked)`, `ssh: ok (off: nothing listens on port 22 until vm-ssh-create)`, `/` 61G with 2.0G used, hostname `linux-dev-a1`. The guard counted it at 2.0 GiB of memory and 8.0 GiB of disk |
| again, running | `vm-create -vm linux-dev-a1` | 3.2 s, four steps, the same check; no `-os` needed |
| again, stopped | `vm-create -vm linux-dev-a1`, after `shutdown -h now` in the guest | 37.5 s: answering 32.8 s after the start, then the check |
| SSH on, first run | `vm-ssh-create -vm linux-dev-a1` | **2.7 s**, 3 changes: passwords refused (`/etc/ssh/sshd_config.d/00-irgo-winvm.conf`), key authorized (`/home/dev/.ssh/authorized_keys`), `ssh.socket` enabled and started. Host keys were already there, from cloud-init. Port 22 answered `SSH-2.0-OpenSSH_9.6p1 Ubuntu-3ubuntu13.19`; exit 0 |
| login | `ssh dev@192.168.64.59 'whoami; sudo -n id -un'` with `BatchMode=yes` | `dev`, `root`: the key was accepted and sudo asked nothing |
| a password | the same with `PubkeyAuthentication=no` | `Permission denied (publickey)`: the server offers no password method. `ssh root@…` with the key: the same refusal |
| repeat | `vm-ssh-create -vm linux-dev-a1` | 2.2 s, `ssh: already on, nothing changed`; exit 0 |
| the rig, looking | `mise run push -- dev@192.168.64.59 --known-hosts /dev/null --dry-run`, in claude-rig | `bootstrap: ok git, curl, bash`, three `would` lines, `Nothing was changed`; exit 0 |
| the rig, first run | the same without `--dry-run` | exit 0, **10 `change` lines**: the tool list, the tools, `PATH` in `.bashrc` and `.profile`, Claude Code 2.1.287 by the native installer, and five for the Claude config. `Login` and `Session` each print `skip`: there is no terminal to ask on |
| the rig, second run | the same again | exit 0, **no `change` line**: every row `ok`, `apply: already up to date`, the same two `skip` lines |
| a reboot inside | `ssh … 'sudo reboot'`, then `ssh` 20 s later | logged in again: `ssh.socket` is enabled, so it comes back by itself |
| undo | `vm-ssh-delete -vm linux-dev-a1` | 3.9 s: sshd stopped and disabled, 2 `authorized_keys` files and the configuration file removed, port 22 no longer answers (and `nc -z` from the Mac got no connection); exit 0 |
| undo again | `vm-ssh-delete -vm linux-dev-a1` | 3.4 s, exit 0, 0 files removed |
| on again | `vm-ssh-create -vm linux-dev-a1` | 3.5 s, 3 changes, key login worked |
| Windows-only commands | `app-create`, `vm-repair` with `-vm linux-dev-a1`; `vm-create -vm linux-dev-a1 -os windows` | each exit 2, naming the VM and its system; nothing was run in the guest |
| delete without `-force` | `vm-delete -vm linux-dev-a1` | exit 5: "5.6 GB of VM, and the Linux on it" |
| delete | `vm-delete -vm linux-dev-a1 -force` | 3.9 s, 5.6 GB reclaimed, the record gone; again: nothing to delete, exit 0. The three other VMs untouched |

- **M8, the rig in 2 GiB:** it installed and nothing was killed for memory
  (no `oom-kill` in the kernel log). Afterwards 263 MiB in use, 1689 MiB
  available, no swap configured; `claude --version` and the mise tools ran;
  `/home/dev` held 1.5 GB and the VM's disk 5.6 GiB. **Not measured:** a
  logged-in Claude session at work in it, which needs a terminal for the
  login.
- **The image's allocated size:** 2.5 GiB as converted, 4.3 GiB after a first
  boot and three more ([by hand](#a-linux-guest-by-hand-before-the-code--measured-2-oct-2026)),
  5.6 GiB with the rig in it.
- **`vm-screen` on a Linux VM: not verified.** It exits 0 and writes a
  picture, and with the Mac locked the picture is an empty window, as it is
  for any VM. The boot photographs `vm-create` takes failed or were empty for
  the same reason. Whether `virtio-ramfb` shows Linux's console in UTM's
  window is still unknown.
- **Not measured:** the first boot on a slow or absent network; a VM name
  that is not already a hostname; two Linux VMs at once.

**Windows, after the same changes.** The guest description and the template
are shared, so Windows was run too, on one clone, `linux-dev-wincheck`
(Windows 11 ARM64, 4 GiB), deleted after:

| check | what happened |
|---|---|
| the plist a Windows `vm-create` writes | **byte for byte what it was**: the SHA-256 of `Config.Plist()` for a fixed name, UUID and MAC, with and without GPU acceleration, is the same before and after the template gained its four values (`e021b9a1…5fc1`, `f5cff3f1…6a25`). No Windows install was run: it needs 30 GiB and the Mac had 31 free |
| `vm-create -vm linux-dev-wincheck` | cloned in 1.7 s, answering in 26 s, 32.5 s in all; its record says `windows` |
| `app-create` of the conformance suite, `-test.short` | 4.7 s, PASS, exit 0; `app-delete` twice, exit 0 both times |
| `vm-ssh-create -vm linux-dev-wincheck`, first run | 7 min 22 s, the same 5 changes [as before](#ssh-into-a-clone--measured-2-oct-2026): capability installed, sshd started and set automatic, our firewall rule opened, Windows' own turned off, key authorized. Port 22 answered `SSH-2.0-OpenSSH_for_Windows_9.5`; exit 0 |
| `ssh dev@192.168.64.60 "whoami & ver"` | `win11arm\dev`, 10.0.26100.4349 |
| `vm-ssh-delete -vm linux-dev-wincheck` | 17.1 s, port 22 no longer answers; exit 0 |
| `vm-delete -vm linux-dev-wincheck -force` | 12.8 GB reclaimed |

**Cut short.** The owner needed UTM, so the session ended there, and these
were not run:

- on Windows, the repeat of `vm-ssh-create` and the second `vm-ssh-delete`;
- on Linux, anything after two changes made late: `vm-create`'s check takes
  `-New` and no longer fails on a VM that exists and has SSH on (before, a
  `vm-create` after `vm-ssh-create` would have failed its check: found by
  reading, never seen), and `guestOf` remembers a VM's system by UUID only.
  Both are unit-tested and neither was run in a guest. The runs above used
  the binary from before them; for a VM just made the check is the same.

## A Linux guest by hand, before the code — measured 2 Oct 2026

The five questions the Linux plan (`.plans/2026-10-02_1950_linux-vms.md`)
says must be answered before code is written around them. M2 Pro, 16 GiB, UTM
4.7.5, a 4 GiB Windows clone running beside each VM. Three disposable VMs
(`linux-dev-m1`, `-m2`, `-m3`), one at a time, each deleted after. Nothing here
used the tool except `vm-screen`: the image was converted and the seed built by
a scratch Go program with the same libraries and options the tool has, the
bundle was written by hand from `config.plist.tmpl`, UTM imported it through
`utm-import.applescript`, and the guest was reached with `utmctl` alone.

The VM: `noble-server-cloudimg-arm64.img` of **20260926** (620,224,512 bytes,
SHA-256 `1d6bffe6…cefc55`, checked against Ubuntu's `SHA256SUMS`), converted
from qcow2 to a raw sparse file and extended to 64 GiB, as a **VirtIO** disk;
a seed CD of 57,344 bytes, label `cidata`, holding `user-data` and
`meta-data`; `virtio-ramfb`, no TPM, clock in UTC, 2048 MiB, 4 CPUs. UTM
imported that configuration without complaint.

| # | question | answer |
|---|---|---|
| M1 | Does UTM's firmware boot the cloud image's disk on a fresh VM with nothing typed? | **Yes.** First boot: `BdsDxe: starting Boot0002 "UEFI Misc Device 2"`, the VirtIO disk, through the image's fallback loader. The image then registers its own entry, and every later boot is `Boot0005 "Ubuntu"`, `\EFI\ubuntu\shimaa64.efi`. No UEFI shell was seen in seven boots of three VMs; `startup.nsh` and `bootAssist` are not needed |
| M2 | Does cloud-init read a seed CD made by go-diskfs? | **Yes on a VirtIO CD, no on a USB one.** The same image bytes both times, written as `isoBuildImage` writes them (ISO9660, Joliet and Rock Ridge on, trimmed to the volume size), label `cidata`. As a **USB** CD (what the Windows CDs are, and UTM's own default), cloud-init never ran: no line from it on the console, hostname left as `ubuntu`, no network configuration at all, `systemd-networkd-wait-online` failing after its two minutes, then a login prompt nobody can use. Twice, on two VMs. As a **VirtIO** CD it is `/dev/vdb`: `Datasource DataSourceNoCloud [seed=/dev/vdb]`, hostname and the `dev` account as asked. Why USB fails was not isolated. The likely reason: cloud-init decides whether to run at all from a systemd generator, 0.8 s into the first boot, and a USB disc may not be there that early. The plan asked about Joliet without Rock Ridge; `isoBuildImage` already writes both, so that was not the question |
| M3 | Do `utmctl ip-address`, `exec`, `file push` and `file pull` work against Linux's `qemu-guest-agent`? | **Yes, all four.** `file push` of a 1 KiB script 0.25 s, `exec --cmd /bin/sh <script>` 0.16 s, `file pull` 0.19 s, `ip-address` 0.14 s. `exec` returned nothing and exited 0, as on Windows, so the script wrote its output and exit code to files that were pulled. It runs as root. The package is not in the image: cloud-init installed it (`packages: [qemu-guest-agent]`) |
| M4 | Time from start to an answering agent | **First boot 33.7 s** (cloud-init finished at 29.3 s of uptime: account, `apt-get update`, the agent installed and started, root partition grown to 63 GiB). **Later cold boots 24.1 s and 24.4 s.** Polled every second; the start request itself returns in 2 to 3 s. Until the agent is installed `utmctl ip-address` first fails at once with `OSStatus error -2700`, then hangs, as it does for Windows |
| M10 | Does a `sudo reboot` inside the guest come back with the agent answering, untyped? | **Yes, in 10.0 s** from the request, checked by the guest's own `uptime -s`. A shutdown from inside (`shutdown -h now`) leaves UTM reporting `stopped` in 4 to 6 s |

Also seen, and not what the plan assumed:

- **SSH is on in the image.** `ssh.socket` is enabled and active after the
  first boot, port 22 is listening on every address, and cloud-init generated
  host keys. Nobody can log in (`PasswordAuthentication no` is in the image's
  `60-cloudimg-settings.conf`, `dev` has a locked password and no key), but
  the port answers. The plan had it "stopped and disabled". So `vm-create`
  has to turn it off for `vm-ssh-delete` to mean the same thing on both
  systems.
- **The netplan file cloud-init writes matches the first boot's MAC**
  (`match: macaddress: "52:54:00:83:8d:0a"`, `set-name: enp0s1`). That is the
  plan's M6, for the phase that clones.
- **Disk.** The converted image holds 2.5 GiB on APFS before its first boot
  (qcow2 virtual size 3.5 GiB, converted in 3.4 to 3.7 s) and 4.3 GiB after a
  first boot, a reboot and two more boots. The guest reports 2.0 GiB used of
  61 GiB.
- **Memory.** 275 MiB used of 1952 MiB at rest.
- **Not seen: the screen.** The Mac's screen was locked for the whole
  session. `vm-screen` wrote a picture of the VM's window each time, and the
  window was empty in every one, a Linux VM at its login prompt included, and
  macOS would not capture the window of the Windows clone beside it at all. So
  whether `virtio-ramfb` shows the Linux console in UTM's window is **not
  measured**. What the guest was doing was read from a serial port added to
  the hand-made VMs for this (`interface: tcp`), which is how the USB failure
  above was found.

## SSH into a clone — measured 2 Oct 2026

A fresh clone of the golden image (Windows 11 ARM64 26100.4349, 4 GiB), with
another 4 GiB clone running beside it. The binary was built from the branch
that added the commands; the key was the Mac's own `~/.ssh/id_ed25519.pub`.

| step | command | what happened |
|---|---|---|
| the clone | `vm-create -vm vm-ssh-test` | cloned, booted and answering in 28.5 s |
| first run | `vm-ssh-create -vm vm-ssh-test` | 9 min 46 s, nearly all of it Windows installing the OpenSSH Server capability. 5 changes: capability installed, sshd started and set automatic (it was Manual, Stopped), our firewall rule opened (TCP 22, local subnet, every profile), Windows' own rule turned off (it was for profile Private), key authorized. Port 22 answered `SSH-2.0-OpenSSH_for_Windows_9.5`; exit 0 |
| login | `ssh dev@192.168.64.57 "whoami & ver"` with `BatchMode=yes` | `win11arm\dev`, 10.0.26100.4349: the key was accepted with no password |
| repeat | `vm-ssh-create -vm vm-ssh-test` | 15.9 s, "already on, nothing changed"; exit 0 |
| undo | `vm-ssh-delete -vm vm-ssh-test` | 19.9 s: sshd stopped and disabled, the rule and every key removed, port 22 no longer answers (checked from the Mac with `nc` as well); exit 0 |
| undo again | `vm-ssh-delete -vm vm-ssh-test` | 16.7 s, exit 0. It reports "removed" again rather than saying it was already off |
| clean up | `vm-delete -vm vm-ssh-test -force` | 14.9 GB reclaimed; `irgo-win11`, `irgo-golden` and the other clone untouched |

Not measured: the first run on a guest that already has the capability (a
golden image sealed with it would skip the ten minutes), a guest account that
is not an administrator, and an x64 guest.

## VM capacity: what a clone really costs — measured 1 Oct 2026

**Result:** a clone of the golden image writes very little of its own: 0.28 GiB
after a boot, a full `glaze-check -windows`, twenty `app-create`s and 90 idle
minutes, flat for the last hour. APFS's private size, read with `getattrlist`
on the disk image in UTM's container (where `ls` is refused and this call is
not), is what deleting a VM gives back. M2 Pro, 16 GiB, 460 GiB volume, UTM
4.7.5, `irgo-win11` running throughout and never touched; one disposable clone,
`cap1`, made by `vm-create -vm cap1` and deleted after. Sampled every 30 s
(`ATTR_CMNEXT_PRIVATESIZE`, `st_blocks`, `df`, `vm.swapusage`).

| when | cap1's own bytes | `st_blocks` | notes |
|---|---|---|---|
| cloned and booted, 24.5 s | 0.03 GiB | 10.06 GiB | `df` used +36 MB across the create |
| after `glaze-check -windows` (45 s, KNOWN BUGS ONLY) | 0.10 GiB | 10.08 GiB | |
| after 20 `app-create`s of a 19 MB binary (28 s each, 10 min) | 0.26 GiB | 10.12 GiB | about 8 MB per run, the pushed binary and Windows' own writes |
| 60 and 90 minutes later, idle | 0.27, 0.28 GiB | 10.13 GiB | flat |
| `vm-delete -vm cap1 -force` | — | — | `df` free went up **0.29 GiB**: the private size, not `st_blocks` |

- **`st_blocks` and `du` overcount clones.** A clone reads 10 GiB from the
  moment it exists, because it counts the blocks it shares with the golden
  image. `golden-export/`, a copy of the golden image made by hand, read
  10.1 GB to `du` and has a private size of **0**: the golden image's disk and
  it share every block. Summing `du` over the runtime folder and the VMs counts
  those 10 GB twice.
- **The clone id does not name a family.** `ATTR_CMNEXT_CLONEID` was equal for
  the golden image and `golden-export` and different for `cap1`, cloned by UTM
  from the same image. So `capacity` counts shared blocks once, as the most any
  one file shares (there is one golden image), rather than per family.
- **Memory, not disk, is what limits this Mac.** With `cap1` (8 GiB, made with
  `-overcommit` by the build before this change) and `irgo-win11` both running,
  16 GiB configured on 16 GiB, swap went from 7.5 GB to between 9.6 and 10.6 GB
  and stayed there; every run still passed.
- **A 4 GiB clone is enough.** `cap4`, cloned with 4096 MiB (read back from
  UTM) beside `irgo-win11`, needed no `-overcommit`: the guard answered yes,
  16 − 8 − 4 = 4 GiB left for macOS. It answered in 22 s, passed
  `glaze-check -windows` in 59 s (KNOWN BUGS ONLY: §1b) and `vm-check` in 94 s
  (YES, 44 passed, 0 skipped), and a second clone was refused. Swap used went
  from 10.3 to 11.8 GB over the runs. Deleting it freed 151 MB, its private
  size.
- **What it changed:** `cloneReserveBytes`, the space a clone is promised, is
  4 GiB, fourteen times the measured growth, in place of the 10 GiB guess
  (`cloneHeadroomBytes`); and space promised to existing VMs now counts against
  a new one. A Windows cumulative update on a clone was not seen in this session
  and is the reason for the margin.
- **The machine on the day**, from `irgo-winvm capacity`: 38.2 GiB free of
  460 GiB; `irgo-win11` 20.6 GiB of its own (it was installed, not cloned);
  the golden image 10.1 GiB, shared; media 9.1 GiB; `shots/` 0.3 GiB; the VMs
  and the tool's data 40.2 GiB of the 422 GiB in use on the volume. Room for 7
  more clones by disk, none by memory while `irgo-win11` runs.
- **prune**, first run: 32 runtime screenshots from 13 to 15 Aug, past 14 days,
  **103 MB** by APFS's count; `du` of `shots/` fell from 270,236 KB to
  164,868 KB, 103 MB.
- **vm-delete** had reported "— reclaimed" for every VM: it walked the bundle,
  which macOS refuses in UTM's container. It now reports the private size.

## Several callers on one Mac — measured 1 Oct 2026

**Result:** the guards in [Sharing one Mac](DEVELOPMENT.md#sharing-one-mac)
behave on the real machine. M2 Pro, 16 GiB, UTM 4.7.5, with `irgo-win11`
running throughout and never touched; one disposable clone, `z1`.

| step | command | measured |
|---|---|---|
| what a running VM holds | `footprint` on its QEMULauncher, `sysctl vm.swapusage` | `irgo-win11` (8192 MiB configured): **8327 MB footprint, 8051 MB dirty**; swap 6.3 of 7 GB in use |
| UTM's memory per VM | AppleScript `memory of configuration` | 8192 for both VMs, in 0.6 s, no Full Disk Access |
| a clone while `irgo-win11` runs | `vm-create -vm z1` | **exit 7** in 0.7 s: 16 GiB, 8 running, 8 for z1, 0 left, want 4; nothing made, no record |
| the same, deliberately | `vm-create -vm z1 -overcommit` | made and answering in **26 s**; disk read through `statfs` on UTM's volume: 41.9 GiB free, want 20 |
| a second clone | `vm-create -vm z2` (another owner) | **exit 7**, naming `irgo-win11` and `z1` as the 16 GiB already running |
| an agent without `-vm` | `IRGO_WINVM_OWNER=… app-create x.exe` | **exit 2**, told to `vm-create -vm <name>` |
| an MCP client without `-vm` | stdio, client `agent-x`, `app-create` | `code 2, status usage`, the caller named as `agent-x/apple@…:repo-b (from MCP client)` |
| staging | `app-upload` from clients `agent-y` and `agent-x`, then `app-delete` from `agent-x` | each staged under its own `bin/agent-…-<hash>/`; agent-x's delete left agent-y's file |
| a run on the clone | `app-create -vm z1 zhello.exe` | output back in 4.1 s (SMB, 1.9 s); the record's last use moved |
| reaping, in lease | `vm-reap -stale 1h` | `keep z1 — in lease`, exit 0 |
| reaping, while `vm-repair -vm z1` ran | `vm-reap -stale 1s -force` | `keep z1 — in use: a command holds its lock`; nothing deleted |
| reaping, idle | `vm-reap -stale 1s -force` | `z1` stopped and deleted through UTM in 4.9 s, record removed; `irgo-win11` and `irgo-golden` untouched |
| a left-over record | a record for a VM UTM does not have, and one for `irgo-win11` | `forget` and `keep — protected`; `-force` removed only the first |

Not measured: how much a clone grows over a working day. `df` fell by about
1.1 GiB across z1's clone, boot, one run and a `vm-repair`, with the rest of the
Mac writing too, so `cloneHeadroomBytes` stayed an estimate (measured since:
[VM capacity](#vm-capacity-what-a-clone-really-costs--measured-1-oct-2026)).

## The drive tests on GitHub's Windows ARM64 runner — measured 1 Oct 2026

**Result:** `TestDriveType`, `TestDriveClick`, `TestDriveClickAt` and
`TestDriveScroll` pass on the `windows-11-arm` job three runs in a row, with no
retry, no foreground change and every OS step `isTrusted`:
[36810013494](https://github.com/joeblew999/irgo-windows-vm/actions/runs/36810013494),
[36810234766](https://github.com/joeblew999/irgo-windows-vm/actions/runs/36810234766),
[36810464096](https://github.com/joeblew999/irgo-windows-vm/actions/runs/36810464096)
(branch `drive-windows-reliable`; macOS green in each). Image
`windows-11-vs2026-arm64`, native at fork PR #2 (`2fbbf2d`), glaze v0.0.61.

Before: run 36804946289 failed `TestDriveClick` ("no matching event (9 so
far)", then "the frontmost app was window 0x40270 (pid 6808) before this test
and window 0x30284 (pid 11052) after it, which this test does not own"). Two
causes, each found by tracing the foreground at every step:

| cause | how it showed | fix |
|---|---|---|
| glaze created the app's window, and on Windows glaze's window activates itself | with the old window and the new trace (branch `drive-windows-diag`, run 36808088081) every drive test failed the same way: the app's window was the foreground window from "window up", +0.5 s, to the close. Closing it handed the foreground to some other window, which is why the old check blamed a stranger. The `TestDriveClick` screenshot of run 36804946289 shows its title bar drawn active | `drive` makes the window itself with `WS_EX_NOACTIVATE` and `SW_SHOWNOACTIVATE`, as native's testwin does; the tests now fail if the app's window is ever in front |
| WSL's updater, relaunched for the whole job | `WindowsTerminal.exe "C:\Windows\system32\wsl.exe"` became the foreground in the middle of `TestDriveScroll` (run 36808085142). A process-start trace (run 36809190167) showed `provjobd.exe`, a child of the runner's `hosted-compute-agent`, running `wsl.exe` every 30 s; WSL is not installed, so the inbox stub starts `wsl.exe --update --confirm --prompt-before-exit` in a new Windows Terminal window whenever none is running, and that waits about 60 s. Stopping it brought it back within 30 s; `wsl --update` exits 1 ("not installed") | `runner-desktop.ps1` makes `wsl.exe` run `cmd /c exit 1` (Image File Execution Options) for the job; the trace then showed `provjobd` starting `cmd.exe` every 30 s and no window (run 36809643314). Upstream: [actions/runner-images#14264](https://github.com/actions/runner-images/issues/14264) |

Also on that desktop at job start, every run: the full-screen "Microsoft
account" prompt (`WWAHost.exe`), Start and Search open under it (on one runner
both reopened 3 s after their hosts were stopped, so they are now closed until
they stay closed), a "System Properties" dialog, Widgets, and OneDrive's
first-run setup starting OneDrive a minute in. The keyboard retries in
`TestDriveType` never fired in these runs: with focus moved into the page on
load, the field took focus on the first click every time.

## The real golden image through the private R2 cache — measured 1 Oct 2026

**Result:** the sealed golden image (Windows 11 Pro 26100.4349) went up to the
private bucket `irgo-golden` through the Worker and came back **byte-identical**:
SHA-256 of `Data/disk.img`, `efi_vars.fd`, `tpmdata` and `config.plist` equal to
the UTM export. A machine without a golden image gets one in under 5 minutes
instead of a 12-minute install plus a 3-minute seal.

| step | measured |
|---|---|
| UTM export of `irgo-golden` (AppleScript `export`) | 0.3 s (APFS clone; no extra disk) |
| privacy check before push | r2.dev off, 0 custom domains |
| push, 4 at a time, from this Mac | 64 GB of files, 19.1 GB of data → **310 chunks, 8.4 GB** zstd, 9 min 14 s |
| pull, 4 at a time | **8.4 GB in 3 min 13 s**, rebuild 1 min 20 s, total **4 min 37 s** |
| compare | all four files' SHA-256 identical |

## A VM of your own in 23 s — measured 1 Oct 2026

**Result:** after one unattended install and one seal, `vm-create -vm <name>`
gives a new, answering Windows VM in **23 s**, without restarting UTM, while
`irgo-win11` keeps running. Two clones run side by side with their own MAC and
address, take `app-create` at the same time, and pass `glaze-check -windows`.

M2 Pro, 16 GiB, macOS 27.0, UTM 4.7.5, Windows 11 Pro 26100.4349, run by an agent
session with no Full Disk Access. Every VM below was disposable (`g1`, `a1`, `a2`);
`irgo-win11` stayed `started` throughout and ran a program afterwards.

| step | command | measured |
|---|---|---|
| install, end to end | `vm-create -vm g1 -install -golden=false` | **743.8 s (12 min 24 s)**, nothing typed: bundle written and imported by UTM in 0.95 s, Setup to desktop, agent, first-logon commands, shutdown, medium out, boot, agent |
| installed disk | `stat` | 8.99 GB allocated of 64 GiB (NTFS: 21.5 GB used) |
| BitLocker after install | seal's `facts` | **status 0, 0 % encrypted**: `PreventDeviceEncryption` in specialize worked (irgo-win11, installed without it: 100 % encrypted) |
| seal, end to end | `vm-golden-create -vm g1` | **168.7 s**: vm-repair 45 s, facts 15 s, decrypt (nothing to do) 12 s, hibernation off 9 s, DISM `/ResetBase` 31 s, TRIM 15 s, shutdown 12 s |
| TRIM on the host | `stat` around `Optimize-Volume -ReTrim` | 10,894,462,976 → 10,811,899,904 bytes: **QEMU on macOS does punch holes** (83 MB here; NTFS used fell 22.6 → 19.2 GB over the seal) |
| golden image | `doctor` | **10.1 GB allocated** of 64 GB, only the NVMe disk |
| clone (golden → new VM) | AppleScript `duplicate` | **0.76–1.7 s**; `df` unchanged |
| clone boot to agent | `vm-create` | **21 s**, three times (verification clone, a1, a2) |
| `vm-create -vm a1` from the golden image | | **22.9 s**; a2 23.4 s |
| two clones at once | `utmctl ip-address`, UTM's configuration | MACs `52:54:00:B4:BD:7D` / `:8F:37:DB` (golden `:E3:D3:CD`), addresses 192.168.64.43 / .44 (irgo-win11 .40) |
| `app-create` on both at once | two 20 s programs | **27 s for both**; each pushed over SMB to its own clone (1.9 s, 1.5 s), so the share came with the image. The same VM a second time: **exit 6**, "VM a1 is held by another command" |
| `glaze-check -windows -vm a1` | | **KNOWN BUGS ONLY in 29 s** (irgo-win11: 32 s) |

What it took to get there, each found by running it:

- **A long comment in the answer file made Setup ignore it.** The first install
  stopped at "Select language settings". The bundle was intact (`unattend.iso`
  81,920 bytes in UTM's folder; UTM's scripting reports sizes in whole MiB, so it
  said 0). A/B, each judged by whether the disk grew within 35 s: the answer
  file from before 30 Sep installs; today's without the new specialize
  component installs; the component under a one-line comment installs. Its
  twelve-line comment was the only one in the file with `%` in it; which part
  triggers it was not isolated.
- **The install's eject stopped Windows mid first logon.** The firmware booted
  Windows' own boot entry with the self-booting install CD still attached, so
  the desktop was up while the guest tools were still installing. The stall
  check read the quiet disk as a reboot, hard-stopped the VM (the guest tools
  never installed, so no agent and no network), and after the restart typed
  `fs2:\efi\microsoft\boot\bootmgfw.efi` into the Start menu's search box. Now
  nothing is stopped or typed once Windows is on the disk; the medium comes
  out after the agent answers and `C:\unattend-complete.txt` exists, by a
  shutdown from inside.
- **`drives of (configuration of vm)` fails in AppleScript** (-1700); the
  configuration has to be fetched into a variable first.
- **`New-Item -Force` on the existing BitLocker key fails** ("Cannot delete a
  subkey tree"), so the seal creates it only when missing.
- **This process can neither read nor write UTM's container**, so `vm-create`
  writes the bundle under `vm/staging/` and UTM imports it (0.95 s), and the
  guest tools ISO is downloaded to `vm/` (5.3 s). The guest-tools ISO carries an
  `Autounattend.xml` of UTM's own; ours won on every install here.

Not measured: how much a clone grows over a working day (`cloneHeadroomBytes`,
10 GiB, is still an estimate; `df` did not move by a visible amount for two
clones' boots and runs), and the compressed size of the golden image (phase 2).
## A glaze app driven by real OS input — measured 1 Oct 2026

macOS 27 arm64, the owner's Mac while in use, `examples/drive` with native
from the fork (`feat/input-screen-darwin`, `93363eb`). The four
`TestDrive*` tests of the conformance suite: a glaze app in its own process,
Prohibited activation policy, window behind every other window.

| interaction | real OS input? | measured |
|---|---|---|
| `Click("#name")`, then `Type("héllo wörld 👋!")` | yes (`CGEventPostToPid`) | `mousedown` on `input#name` and every `input` event `isTrusted`; the field holds the text exactly, emoji included |
| `Press(KeyBackspace)` | yes | trusted `keydown` `Backspace`; the `!` removed |
| `Click("#inc")` ×3 | yes | three trusted `click` events on `button#inc`; label `count: 3` |
| the same click by script (`.click()` through `Eval`) | no, on purpose | arrives with `isTrusted` false: the control |
| `ClickAt` 17,23 into `#pad` | yes | `mousedown` on `div#pad` at exactly that point; with the 32-point title-bar offset dropped it lands 32 points higher, on `body` |
| `Scroll(0, -5)` | yes | trusted `wheel`, `deltaY` positive, `scrollY` > 0 — on the first post in `glaze:mac`, on the second when run straight after another test's input ([UPSTREAM §6](UPSTREAM.md#6-nativeinput--the-first-background-scroll-to-a-new-process-is-dropped)) |
| element lookup, `WaitFor*`, reading values | no: the JavaScript bridge | — |
| `Screenshot` | `screen.CaptureWindow` (ScreenCaptureKit) | the window's content while it sits behind other windows |

Each test about 1 s. The frontmost app (System Events) was the same before and
after every test that nobody else interrupted; twice during this work it
changed mid-run to UTM and to VS Code, both brought forward by other programs
on the machine, and the check failed the test as it should.

## Pushes go over SMB — measured 30 Sep 2026

**Result:** `Push` over the guest's SMB share is 11× to 48× faster than the
zipped `utmctl file push`, and the Mac needs no change.

**Method:** `irgo-win11` (Windows 11 Pro, build 26100) on UTM's shared network,
guest `192.168.64.40`. The share was opened by `vm-repair`. Each file was pushed
to `C:\Windows\Temp` by `pushZipped` and then by `pushShared`, and by `Push` to
`C:\Users\Public`. Every SMB time includes the guest round trip that moves the
file into place and checks its SHA-256 with `certutil`. The files were real Go
binaries: the conformance test binary, and four windows/arm64 binaries
concatenated. Zipping such binaries helps, but random bytes would not compress.

| file | zipped `utmctl` push | SMB (`pushShared`) | `Push` |
|---|---|---|---|
| conformance test binary, 8.1 MB | 14.18 s, then 11.92 s | 1.34 s, then 1.07 s | 1.15 s, then 1.05 s |
| four binaries, 50.9 MB | **1 min 16.81 s** | **1.67 s** | 1.58 s |

About 1.1 s of each SMB push is the move-and-hash round trip. The 50 MB transfer
itself took about half a second.

`mise run glaze:windows` (conformance suite, 5 MB, `-gui`): **24.35 s**, down from
31 s on the old path the same afternoon. The push took 1.09 s. Most of what is
left is the two desktop resets (about 8.5 s each) and the tests (about 6 s).
Verdict: KNOWN BUGS ONLY, as before. After main's windowed-test screenshots were
merged in, the same gate took 32.5 s. The push was unchanged at 1.13 s. The test
phase grew from about 6 s to 12.4 s, and pulling the seven pictures added 1.3 s.

**Checked along the way:**

- **The fallback.** With the share removed (`vm-repair -share=false`),
  `app-create` said `pushing 8 MB compressed through utmctl, because the SMB
  share did not work (… The specified share name cannot be found …)`. It then
  pushed in 11.95 s and ran the program.
- **Negative control for the hash check.** With the comparison broken on
  purpose, every push fell back, naming both hashes. Restored.
- **Windows opens more than it is asked to.** After `New-SmbShare`, the rule
  `File and Printer Sharing (Restrictive) (SMB-In)` was enabled, Public profile,
  remote address Any. It was still enabled after `Remove-SmbShare`, which is
  why the share was still reachable (and answered "share name cannot be found")
  with our own rule gone. `file-share.ps1` now turns it off. Afterwards the only
  enabled inbound rule for 445 was ours (`LocalSubnet`), and pushes still went
  over SMB.
- **The Mac.** Nothing was changed. The connection out to the guest's port 445
  worked the first time, and nothing on the Mac asked for a permission.

## GoReleaser reproduces the published v0.4.1 byte for byte — verified 30 Sep 2026

**Result:** moving the release build from a shell loop in `mise.toml` to
`.goreleaser.yaml` changed nothing a user downloads.

**Method:** in a scratch clone, check out v0.4.1, add only `.goreleaser.yaml`,
re-tag it locally, and run `goreleaser release --clean --skip=publish`
(GoReleaser 2.18.2, and Go 1.26.5 as that tag's `mise.toml` pins).

| file | published SHA256SUMS | GoReleaser rebuild |
|---|---|---|
| `irgo-winvm-darwin-arm64` | `ce0f9f0a…3f29c2` | `ce0f9f0a…3f29c2` |
| `irgo-winvm-darwin-amd64` | `55f2de63…3496c5` | `55f2de63…3496c5` |

**Negative control:** the same commit tagged `v9.9.9` hashed differently (arm64
`3f058b88…`), because the version is compiled in. So the match above depends on
the tag and the source, and the comparison can fail.

Two snapshot builds of one commit, each with an empty `GOCACHE`, also produced
identical `SHA256SUMS`.

## An agent uploaded, pushed, and ran a binary over HTTP — verified 16 Aug 2026

**Result:** a client with no shared filesystem can upload a binary and run it in
the VM. The chunked, content-addressed `app-upload` was driven end to end over
the HTTP transport (`irgo-winvm mcp -http :8129`), not the spawned stdio client.

| step | what happened |
|---|---|
| `app-upload` | `probe.exe`, 3,320,832 bytes, SHA-256 `7fe27641…cf130`, sent as 4 chunks of ≤1 MiB. The final chunk verified the digest and committed `bin/7fe27641….exe` |
| `app-create` | pushed and ran on windows/arm64: 5 capabilities OK, 3 missing, 12.6 s. The same report as the 14 Aug stdio run: an upload feeds the same path a local file does |
| `app-delete` | cleared `bin/` on the host and the binary in the guest, so the undo covers both sides |

The hash is verified before the committed file exists, so a truncated or
corrupted upload is removed, never run. Unverified downloads are the oldest
category of bug this repository keeps re-fixing.

Scope: this entry proves the binary-staging half against a real guest. The
transport framing (SSE over HTTP) and bearer authentication are covered by
`mcpserver`'s tests.

## An agent drove the whole thing over MCP — verified 14 Aug 2026

**Result:** a real MCP client, spawning `irgo-winvm mcp` as a subprocess on this
machine, ran every kind of call against the installed VM. No test doubles.

| call | what happened |
|---|---|
| connect | 9 tools listed |
| `doctor` | the full report came back **as the tool result** |
| `vm-screen` | a **4,447,777-byte PNG** with a valid header: a live Windows 11 desktop |
| `iso-create -fetch` | detached, returned job `iso-create-20260814-151113`, and survived the client exiting |
| `app-create probe.exe` | pushed and ran on **windows/arm64**: 5 capabilities OK, 3 missing, 12.7 s |

What each row proves:

- **`doctor`** proves the output capture works. Over stdio, stdout is the
  JSON-RPC channel, and every command prints its progress. Had one line reached
  stdout, the client would have failed with a parse error. The whole report
  arriving as a result shows `utmvm.Capture` doing its job.
- **`vm-screen`** was looked at, not just measured: Windows 11 logged in as
  `dev`, Start menu open, `unattend-complete` still under Recommended from the
  unattended install. No test can check this, because the test would have to
  supply the pixels it verifies.
- **`app-create`** is the point of the repository, reached from an agent: a Go
  binary cross-compiled on a Mac, pushed into real Windows on ARM64, run, and
  its answers returned. The three missing capabilities (`native/notify`,
  keychain, fswatch) are planned upstream but not built, so they are not
  failures of this path.
- **The job** survived the client exiting, which is what jobs are for. Its log
  shows the real command running and stopping because the media was already
  there, so idempotency holds through the detached path too.

**Not verified: a long job.** `iso-create -fetch` finished in under a second
because the ISO was already built. `vm-create -install`, the 45-minute case jobs
were written for, has not been run over MCP. The mechanism is proven; the
duration is not. See the [roadmap](ROADMAP.md).

## The ISO scan verdict is recorded at build time — 13 Aug 2026

**Result:** checking whether an ISO is ARM64 went from 77.2 s to 0.0 s.

The check reads the whole file. On a 4.9 GB ISO that took **77 seconds**, on
every `iso-create`, with nothing printed while it ran. The verdict is now cached
beside the ISO, keyed by size and mtime, and written by the build itself, which
knows the answer because it just mastered the ISO from an ARM64 `.esd`.

| | |
|---|---|
| first check of a fresh ISO, before | 77.2 s |
| same check, after | **0.0 s** |
| rebuild from a kept `.esd`, no network | 39.5 s |
| full fetch + expand + master from nothing | 250 s |

The rebuild figure is why `iso-delete` keeps the `.esd` by default: the ISO
costs 39 seconds of local work to recreate, while the `.esd` costs 4.2 GB from a
source that rate-limits.

**Not covered by a test:** that the build still records the verdict. That path
needs a real `.esd`, so deleting the call leaves the unit tests green. This
measurement is the check.

## A self-built ISO installs Windows — verified 12 Aug 2026

**Result:** macOS can master bootable Windows ARM64 media with `xorriso` alone,
so CrystalFetch is no longer needed.

![Windows 11 installing from an ISO this repo built](screens/vm/copying.png)

That is UTM booted from an ISO mastered by `irgo-winvm iso-create`, installing
unattended.

| question | answer |
|---|---|
| can macOS master bootable Windows ARM64 media? | **yes**, with `xorriso` |
| does `hdiutil` work? | **no.** Two images, one hiding everything from ISO9660 and one hiding nothing, both enumerate as `FS0: /CDROM(0x0)` and both refuse to boot |
| is UDF required for the 4.099 GiB `install.wim`? | **no.** ISO9660 level 3 multi-extent is enough: Setup read it and installed |
| so is `cdrtools` needed? | **no.** `xorriso` alone, one Homebrew formula |
| does `efisys_noprompt.bin` skip "Press any key to boot from CD"? | **yes**, straight into Setup |

The last row matters most. Booting otherwise depends on typing
`\efi\boot\bootaa64.efi` at the UEFI shell with eight keypresses over six
seconds. That hack cost hours, and once destroyed an install when surplus
presses reached Setup's UI. Media built with the no-prompt loader doesn't need
it.

The two failed attempts, and why they failed, are in the trap table in
[Known traps](TRAPS.md).

## Suspend and resume — 400 ms, verified 12 Aug 2026

**Result:** a suspended VM resumes in 400 ms with its state intact. A cold boot
takes **59 seconds**.

Three consecutive cycles on Windows 11 ARM64, each timed until the guest agent
answered, using the guest's own boot time as the fingerprint:

```
baseline  System Boot Time: 8/12/2026, 10:33:36 AM
cycle 1   resumed in 400ms   boot time unchanged -> STATE PRESERVED
cycle 2   resumed in 400ms   boot time unchanged -> STATE PRESERVED
cycle 3   resumed in 400ms   boot time unchanged -> STATE PRESERVED
```

The boot time is the proof, not the speed. It changes on a reboot and not on a
resume, so an unchanged value means the guest continued rather than quietly
restarting.

- A cold boot also has to be driven through the UEFI shell with eight
  keypresses, which needs an unlocked Mac and a visible display window.
- `irgo-winvm vm-create` resumes a suspended VM rather than rebooting it, so the
  idempotent path is the fast one.

**The state lives in memory** and doesn't survive quitting UTM. The durable
version isn't offered: `utmctl suspend --save-state` either refuses (naming GPU
acceleration, then NVMe) or *reports success and power-cuts the guest*: exit 0,
no state file, and the next boot goes through "Diagnosing your PC". See the trap
table in [Known traps](TRAPS.md).

## glaze and native on Windows ARM64 — measured 12 Aug 2026

**Result:** every capability that works on macOS works on Windows ARM64, and
glaze's Events bridge works. The `app://` scheme works except for absolute
sub-resource URLs, an upstream bug.

Host: Apple M2 Pro, UTM 4.7.5. Guest: Windows 11 ARM64 build 26100, run headless
through the QEMU guest agent, with no GUI, keystrokes or screen.

### The inner loop works

```
$ irgo-winvm app-create -vm irgo-win11 hello-arm64.exe alpha beta
hello from windows/arm64
args: [alpha beta]
```

- **10.8 seconds** end to end.
- Cross-compiled on macOS with plain `GOOS=windows GOARCH=arm64` and **no
  toolchain at all**. That is what being cgo-free buys, and what irgo can't do
  today, because mingw pins it to amd64.
- A failing guest binary fails the command, but the host does **not** exit with
  the guest's code. A binary exiting 3 exits `app-create` **1**, with "exited 3
  in the guest" in the message. The two must differ, because a missing VM exits
  3 and a busy guest agent exits 4. See the contract in
  [What it exits with](USING.md#what-it-exits-with).

### Native capabilities — windows/arm64, native

| capability | result |
|---|---|
| `clipboard.write` / `clipboard.read` | **OK**: round trip verified |
| `power.preventSleep` | **OK**: acquired and released |
| `singleinstance.acquire` | **OK**: lock held, re-acquire correctly refused |
| `mmap.map` | **OK**: mapped and wrote through |
| `notifications`, `keychain`, `fswatch` | missing from the ecosystem |

Identical to the [macOS results](#macos--verified).

### The windowed half — `examples/glaze-all`, windows/arm64, `-gui`

These rows used to read *skipped*. Run in the VM with
`irgo-winvm app-create -vm irgo-win11 .bin/glaze-all-arm64.exe`, exit code 0:

| capability | windows/arm64 | darwin/arm64 |
|---|---|---|
| `openurl.Open` (`file://`) | **OK** | **OK** |
| `openurl.Open` refusing a custom scheme | **OK** | **OK** |
| `openurl.Reveal` | **OK** | **OK** |
| `menu.Set` (native menu bar) | **OK** | **OK** |
| `tray.Run` (icon raised and removed) | **OK** | **OK** |
| `glaze.OpenFile` (native file dialog) | **OK** | **OK** |
| `nocapture.Protect` | **OK** | UNSUPPORTED, by design |
| `glaze.SetAppIcon` | UNSUPPORTED, by design | **OK** |

The last two rows are mirror images: each platform lacks exactly what the other
has, and neither is a failure.

- `nocapture` on macOS is right to refuse: Apple removed the API.
- Windows takes an app's icon from the executable's resources, fixed before the
  process starts.

Getting the report to *say* so needed a fix in glaze and native themselves
([UPSTREAM.md](UPSTREAM.md) §2). Every package defined its own `ErrUnsupported`
without wrapping the standard one, so both rows read FAILED and a wholly correct
run exited non-zero.

This is the first time the whole native surface has run together on Windows:
not in this repo before, not in glaze's examples, and not in `crgimenes/native`,
all of which test one capability per binary.

### glaze probes — measured 12 Aug 2026

Both ran on Windows 11 ARM64 via `irgo-winvm app-create -gui`. With these,
everything glaze does is measured on both platforms, which was the project's
stated goal.

**Events bridge: fully working.**

```
PASS: JS -> Go   : "js-listener-installed"
PASS: Go -> JS   : 3 unsolicited pushes delivered
PASS: round trip : received=["tick:1","tick:2","tick:3"] domChildren=3
```

**`app://` scheme: works, with one serious upstream bug.**

```
origin:        https://app.localhost      (macOS reports app://home)
secureContext: true
relative sub-resources:  served
absolute app:// sub-resources:  NEVER REQUESTED
```

The handler works and the origin is secure, but **absolute `app://` URLs inside
a page don't load on Windows**, silently, with no error. glaze emulates the
scheme there with a virtual host, so the document loads from
`https://app.localhost/`, and an absolute `app://home/app.js` names a scheme
WebView2 doesn't know.

So a glaze app that works on macOS loses every stylesheet and script on
Windows, with nothing to say why.

- **Workaround for app authors:** reference assets relatively and both
  platforms work. `verifyevents` now does this, which is why it passes.
- **Fix:** WebView2's real custom-scheme registration, written up in
  [UPSTREAM.md](UPSTREAM.md) §1b.

This is exactly the class of bug the project was built to find: invisible from a
Mac, invisible in glaze's own CI (`windows-latest` is x64 and has no ARM64
desktop), and silent when it happens.

### Two constraints learned getting there

Both were guessed at first, then understood:

- **The guest agent runs without a desktop session.** A GUI app started through
  it has no window station, so the glaze probes must be launched into the
  interactive session with a scheduled task using `/it`, not a plain exec.
- **Keystrokes don't reach the VM while the Mac is locked.** Boot recovery
  depends on typing at the UEFI shell, so a locked screen blocks it. This is the
  strongest argument for suspend and resume: resuming restores RAM and never
  reaches the firmware, so it needs no keystrokes and works while locked.

## macOS — verified

The baseline the Windows results are compared against.

Host: Apple M2 Pro, macOS 26.5, `glaze v0.0.47`, `CGO_ENABLED=0`.

### Native capabilities

| capability | result |
|---|---|
| `clipboard.write` / `clipboard.read` | OK: round trip verified, original clipboard restored |
| `power.preventSleep` | OK: acquired and released |
| `singleinstance.acquire` | OK: lock held, re-acquire correctly refused |
| `mmap.map` | OK: mapped and wrote through |
| `openurl` / `tray` / `filedialog` / `menu` / `nocapture` / app icon | covered by `examples/glaze-all`; see the [windowed half](#the-windowed-half--examplesglaze-all-windowsarm64--gui), which lists both platforms |
| `notifications`, `keychain`, `fswatch` | **missing from the ecosystem** (`native/notify` is planned, not built) |

### glaze `app://` scheme

```
origin:        app://home
secureContext: true
cryptoSubtle:  true
localStorage:  true
pushState:     /deep/link/route
css:           32px
```

- **`pushState` succeeding** means client-side routing works. The same probe over
  `file://` fails with `SecurityError`, which is why the `app://` handler
  matters and `file://` is no substitute.
- **`css: 32px`** is `getComputedStyle` returning the stylesheet's `2rem`. The
  asset was fetched through the Go handler *and* applied by the engine, not
  merely served.

### glaze Events bridge

```
PASS: JS -> Go   : "js-listener-installed"
PASS: Go -> JS   : 3 unsolicited pushes delivered
PASS: round trip : received=["tick:1","tick:2","tick:3"] domChildren=3
sockets during run: NONE
```

`domChildren=3` shows server-initiated pushes actually changed the DOM. With
zero sockets, this is a genuine SSE substitute for a desktop app. That matters
because glaze's `SchemeResponse` is a buffered `[]byte` on the UI thread, so it
can't stream SSE itself.

## The unattended install — verified 11 Aug 2026

**Result:** Windows 11 ARM64 (build 26100) reached a logged-in desktop as `dev`
with no interaction after the boot command. That proves the answer file, the
disk layout, the display device and the boot path.

| claim | status |
|---|---|
| ARM64 Windows installs unattended in UTM | **yes**: desktop reached, auto-login as `dev` |
| answer file is read and applied | **yes**: GPT layout matched `DiskConfiguration` exactly |
| `virtio-ramfb-gl` display works | **yes**: Setup and desktop both render |
| NVMe system disk is visible to Setup | **yes** |
| glaze runs on Windows | **yes**: measured 12 Aug, [above](#glaze-probes--measured-12-aug-2026) |

### What it looked like

The tool took every one of these itself and named each for the stage that
produced it. Nothing was staged, cropped or copied in by hand. Two more,
`copying` (mid-install, nobody clicking) and `ready` (the guest agent answers),
are on the [front page](../README.md).

**`booting-1`**: UEFI firmware, before Windows has started

![booting-1](screens/vm/booting-1.png)

**`booting-2`**: Windows starting

![booting-2](screens/vm/booting-2.png)

**`finalising`**: the copy is done, first logon

![finalising](screens/vm/finalising.png)

**`stalled-1`**: an install that stopped moving, photographed so you can see why

![stalled-1](screens/vm/stalled-1.png)

**`running-no-agent`**: the failure the tool now refuses to cause. Keystrokes
meant for a boot prompt landed in a logged-in desktop and put three Bing tabs on
it.

![running-no-agent](screens/vm/running-no-agent.png)

Every stage photographs itself as it runs, because from the host a stuck boot
and a working one look identical. `booting-N` repeats every few seconds until
the agent answers, so a hung boot leaves a picture of exactly where it stopped.
Those go to `shots/`, outside the repository. The few kept as evidence are in
`docs/screens/`, published by `mise run vm:shots`.

## Still to measure: x64 under emulation

Every Windows result on this page is ARM64-native. Nothing has been run as an
amd64 build under emulation. Last evidence gathered 13 Aug 2026, all of it
ARM64.

**Why it matters:** whether an amd64 build behaves identically on ARM Windows
decides whether testing on a Mac has any fidelity for x64, or whether x64
testing must happen on x86 hardware.

**What's missing** is the run and its record, not the means. `app-create` runs
any build, so an x64 `examples/probe` can be pushed the same way as the ARM64
one. (`probe/run-probe.cmd`, the hand-run script this entry once named, was
removed 30 Sep 2026.)

**Exercised but not recorded:** glaze's hand-written ARM64 ABI code
(`putbounds_arm64.go`, which passes a 16-byte RECT in two registers per AAPCS64,
not by hidden reference as on amd64). Every `-gui` run above executes it, since
a window can't be positioned without it. No run has reported on it
specifically, so this page makes no claim either way.

Open work that isn't a measurement is on the [roadmap](ROADMAP.md#known-gaps).
