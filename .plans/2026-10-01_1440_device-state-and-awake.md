# Device state: one cross-platform reader, reported to the Worker, with awake and lid on top

> **Taken over 3 Oct 2026** (owner: "I'm going bonkers with the fact that UTM is not well
> controlled"; UTM and every VM stopped twice that morning). Written by another session and
> handed over; copied here so the spec is in the repository. What was built from it, on
> `feat/keeper`: `internal/device` (the reader, macOS fully, Linux and Windows host/CPU/memory/disk
> only), the report to **fleet-api** (the schema below became `api/device.go` in
> `joeblew999/fleet-api`, deployed; its Go SDK sends it), and **`keeper`** in place of `awake`:
> the same loop plus keeping marked VMs running (`vm-keep-create`/`-delete`) and pitchfork
> (`keeper-create`/`-delete`). Not built: the lid-closed mode and the sudoers rule (M1 to M8 need
> the owner's hands), the battery-library fork (battery is read from `pmset -g batt` instead),
> `device` as its own command, `capacity` and `doctor` on the reader, `serve`'s per-job hold.
> The schema plan this names, `2026-10-01_1520_device-schema.md`, was not copied: fleet-api's
> `api/device.go` is its source now. Where this text and `docs/` differ, `docs/` is right.

Status: on hold (owner, 1 Oct) until the report's schema is agreed: [`2026-10-01_1520_device-schema.md`](2026-10-01_1520_device-schema.md) · 2026-10-01

## Symptom
1. The Mac sleeps when nobody touches it, and always when the lid closes. Everything on it stops:
   `serve`'s poll loop, a running job (heartbeat every 20 s, lease 90 s, so the Worker marks it
   `lost`), the VMs, and the owner's Claude and VS Code sessions.
2. The Worker knows each machine's VM capacity and nothing else about it: not its battery, power
   source, lid, sleep settings or whether it is still reporting. Nobody away from the machine can
   tell why it went quiet, and the planned web notifications have nothing to announce.
3. What the tool does read about the host (disk, memory) is read its own way, macOS only.

## Evidence (this Mac: Mac14,10, M2 Pro, macOS 27.0.1, measured 1 Oct 2026)
- `pmset -g log`, 30 Sep to 1 Oct: 114 "Entering Sleep" lines, of which 3 `Idle Sleep`,
  2 `Clamshell Sleep`, the rest maintenance re-sleeps. One idle sleep was on AC: 13:52:55, woken
  by hand at 14:27:22, 34 minutes gone.
- `pmset -g custom` before today: `sleep 1` on AC and battery, `displaysleep` 30 / 10. powerd's
  assertion "Prevent sleep while display is on" is what holds it up until the display goes off.
- `irgo-win11` was `started` at 14:29 and `pmset -g assertions` listed nothing from UTM or QEMU:
  **a running VM does not keep the Mac awake.**
- Nothing in this repo touches power: no `caffeinate`, `pmset`, launchd or pitchfork anywhere.
  `serve` is started by hand in a terminal. `sudo` needs a password; `/etc/sudoers.d` is empty.
- The owner set `sudo pmset -c sleep 0` by hand at about 14:30 as a stopgap; `pmset -g` confirms it.
- **Libraries, no cgo needed.** A test program using `shirou/gopsutil/v4` v4.26.9 (disk, mem,
  process, host, cpu) and `distatus/battery` v0.11.0 built with `CGO_ENABLED=0` for darwin/arm64
  and for linux and windows on amd64 and arm64, and ran here. gopsutil's answers were right
  (16 GiB, 21 GiB free, macOS 27.0.1).
- **`distatus/battery` is wrong on this Mac**: `Empty`, NaN percent, while `pmset -g batt` said
  100% charged. It reads top-level `AppleRawCurrentCapacity`, `AppleRawMaxCapacity` and
  `DesignCapacity` from `ioreg -n AppleSmartBattery -r -a`. Here the first two do not exist and
  the capacities are nested under `BatteryData` (`DesignCapacity` 8694, `FullChargeCapacity`
  7563, `RemainingCapacity` 7561); top-level `CurrentCapacity`/`MaxCapacity` are percent (100/100).
  Upstream's last push was 27 Sep 2023, 7 open issues, none about this.
- Nothing cross-platform reads lid state or prevents sleep; each OS has its own mechanism.

## Cause
- Idle sleep is prevented by a power assertion any user process can hold. Nothing here holds one.
- Lid-close sleep ignores assertions (to be confirmed, M3). Only `pmset -a disablesleep 1` stops
  it, and that needs root.
- There is no one place that reads the machine, so there is nothing to report and nothing for a
  power policy to act on.

## Change

### 1. `internal/device`: the one reader (macOS, Linux, Windows)
A `State` read in one call. Every field answers a value, *not applicable* (no battery, no lid:
a desktop, a VM, a CI runner) or *cannot tell* with the reason; never a zero that looks like data.

| field | source |
|---|---|
| OS, version, arch, model, boot time | gopsutil `host` |
| CPUs, load | gopsutil `cpu`, `load` |
| memory total / available | gopsutil `mem` |
| disk free / total for `/` and for the tool's data volume | gopsutil `disk` |
| battery percent, charging state, power source | `distatus/battery` (fixed, see 2) |
| lid open / closed | per OS: `ioreg` `AppleClamshellState`; Linux `/proc/acpi/button/lid/*/state`; Windows: cannot tell until measured |
| sleep: idle timeout, disabled, who prevents it | per OS: `pmset -g` and `-g assertions`; Linux and Windows: cannot tell until measured |

- `irgo-winvm device` prints it as a table, `-json` as JSON, on every OS the tool is released for.
- `capacity`'s own host disk and memory reads move onto this reader in their own commit, with
  capacity's tests as the check, so there is one route and not two.
- `doctor` gets its host rows from it.

### 2. The battery library: fix it in a fork, offer it upstream
Fork `distatus/battery` to `joeblew999/battery`: on darwin read the nested `BatteryData`
capacities, fall back to the percent pair, and return an error rather than NaN when neither is
there. Test with this Mac's `ioreg` output as a fixture and an old-layout fixture as the control.
Open the PR upstream and list it in UPSTREAM.md. `go.mod` uses the fork by `replace` until a
release contains the fix; upstream has been quiet for two years, so that may be for good.

### 3. Devices report to the Worker
The report, the routes, the Worker's conditions and the storage are in
[`2026-10-01_1520_device-schema.md`](2026-10-01_1520_device-schema.md), drafted and checked
against huma. It is its own resource, not a ledger event: a ledger `detail` holds 500 bytes and
this Mac's report is 884.
- `device -watch` is the one loop: read every 10 s, send on any change and every 5 min while
  nothing changes (`next_s` 300), and a `stop` report on the way out. It runs on every OS and
  holds nothing.
- Reports are spooled and resent as ledger events are, and never block or fail a command.

### 4. `awake`: the same loop plus a policy (macOS first)
`irgo-winvm awake` is `device -watch` with a keeper; a Mac runs `awake` and not both.

| power | holds |
|---|---|
| AC | an idle-sleep assertion (a `caffeinate -i -w <own pid>` child, so it dies with the command even on SIGKILL) |
| AC, with `-lid` | the above, plus `pmset -a disablesleep 1` |
| battery | nothing: `disablesleep 0`, and `pmset sleepnow` if the lid is already closed |
| cannot tell | as battery, and it says so |

- On SIGINT/SIGTERM it restores `disablesleep 0`. After every change it re-reads the state through
  `internal/device` and fails if it is not what it set.
- `awake-create` / `awake-delete`, the do/undo pair for the one thing that needs root. Run as
  `sudo irgo-winvm awake-create`: writes `/etc/sudoers.d/irgo-winvm-awake` allowing `$SUDO_USER`
  exactly `/usr/bin/pmset -a disablesleep 0` and `... 1` with NOPASSWD, checked with `visudo -cf`
  before it is moved into place (a bad sudoers file breaks `sudo` for everything), then proved
  with `sudo -n -l`. Not root: print the exact command and exit usage. `awake-delete` removes the
  file and sets `disablesleep 0`; nothing to remove is success.
- `awake -lid` without the rule refuses (usage), naming `awake-create`.
- `serve` holds the same idle assertion for the length of each job, on any power source, through
  the same call. A job started with the lid open finishes on battery. A closed lid on battery
  still sleeps: the bag case wins over the job, which ends `lost` and can be submitted again.
- On Linux and Windows `awake` refuses with "not built for this OS yet"; `device -watch` works.

### 5. pitchfork
`pitchfork = "2.29.0"` in `mise.toml` (the version `auth-proxy` pins; kept out of CI's install)
and a `pitchfork.toml` whose daemons call the binary and nothing else: `awake`
(`irgo-winvm awake -lid`, `retry`, `boot_start`) and `serve` (`depends = ["awake"]`).

### Docs
`device` and `awake` in USING.md; `internal/device` and the event in ARCHITECTURE.md; the
report, the routes and the conditions in WORKER.md; the sudoers rule in THREAT-MODEL.md; the fork in
UPSTREAM.md; every measurement, dated, in RESULTS.md; one line each in TRAPS.md for what fails
silently (a running VM does not keep the Mac awake; the battery library's NaN).

### Commits, one concern each
`wire/device.go` (the schema) · gopsutil + `internal/device` + `device` · battery fork · capacity
onto the reader · the device routes (wire, tool, Worker) · `device -watch` · `awake` · `awake-create`/`-delete` · `serve`'s
per-job hold · doctor rows · pitchfork · docs.

## Measure first (results go in RESULTS.md)
| # | Question | How |
|---|---|---|
| M1 | Is `disablesleep` per power source or system-wide? | `sudo pmset -c disablesleep 1`, read `pmset -g custom` and `pmset -g` |
| M2 | Does `disablesleep 1` on AC survive a closed lid? | close for 10 min with a VM running: guest agent answers every 30 s, no `Clamshell Sleep` in `pmset -g log` |
| M3 | Negative control: does an assertion alone survive it? | same with only `caffeinate -i -s`; expected: it sleeps |
| M4 | Unplug with the lid closed | `awake -lid` running: `disablesleep 0` and asleep within 20 s |
| M5 | Does `pmset sleepnow` work without root? | run it |
| M6 | Do `vm-screen` and `app-create -gui` work with the lid closed and the screen locked? | run both in M2's window |
| M7 | Does pitchfork start `awake` at login, and restart it after `kill -9`? | `pitchfork boot enable`, log out and in; kill it |
| M8 | Does the sudoers rule allow only the two commands? | `sudo -n pmset -a disablesleep 1` works; `sudo -n pmset -a sleep 5` is refused |
| M9 | What does `device -json` answer in a Windows 11 ARM guest? | push the windows/arm64 binary into a disposable clone with `app-create`; expect battery and lid *not applicable*, the rest values |
| M10 | The same on GitHub's Linux and Windows runners | a CI step that runs `device -json` and fails on any zero-that-looks-like-data |
| M11 | The same in a Linux guest under UTM | when the tool can make Linux VMs; this is where Linux battery and lid bugs get found and fixed |

M1, M2 and M8 decide the lid design: if M2 fails there is no lid-closed mode and `-lid` is dropped.

## Verify
- `internal/device`: unit tests per OS from recorded fixtures (this Mac's `ioreg` and `pmset`
  output; a Linux sysfs tree), each with its negative control in the test comment. Live:
  `device -json` here agrees with `pmset -g batt`, `df` and `sysctl hw.memsize`.
- Policy: the table in 4 with a fake reader and a fake `pmset`, with controls.
- Live, lid open: 40 min untouched on AC with `awake` running and `pmset -c sleep 1` restored; no
  `Idle Sleep` in `pmset -g log`. Control: stop `awake`, same 40 min, one `Idle Sleep`.
- Live, lid closed on AC: a `remote-submit` job queued from another machine runs to `exit 0`.
  Control: the same without `-lid` ends `lost`.
- Lid open on battery: a job submitted, then the charger pulled, still ends `exit 0`.
- Worker: tests for newest-wins, a refused report, a duplicate, and each condition in the schema
  plan appearing and clearing, with a control for each. Live: `kill -9` both `awake` and pitchfork
  with sleep disabled; the device shows `unminded` within 15 min.
- `awake-delete` twice in a row: both succeed, `pmset -g` shows `SleepDisabled 0`.

## Decided (1 Oct, delegated by the owner)
1. **Build it properly and cross-platform** (owner, 1 Oct): gopsutil and the battery library
   behind one reader, lid and sleep beside them, every device reporting to the Worker. Bugs in
   the libraries are fixed in the library; Linux is proven under UTM when Linux VMs exist.
2. **Lid-closed mode: yes, under pitchfork with the sudoers rule. No LaunchDaemon.** The gap it
   leaves (sleep disabled, `awake` and pitchfork both dead, laptop put in a bag) cannot be closed
   by the machine itself, so it is made visible: `doctor` locally and the Worker's condition
   remotely (`unminded`), which a dead `awake` cannot suppress because silence is one of its causes.
3. **On battery a running job is held up to its end, lid open only.** Lid closed on battery
   always sleeps.
4. **`sudo pmset -c sleep 1` is restored once the lid-open verify passes**, so `awake` is the one
   mechanism. Until then the owner's manual `sleep 0` stays as the stopgap.

## Not in this plan
Keeping Linux or Windows hosts awake (`systemd-inhibit`, `SetThreadExecutionState`); the web
notifications; making Linux VMs.

## Needs the owner's hands
- `sudo`: M1, M8, `awake-create`, and decision 4's restore.
- Closing the lid for M2, M3, M4 and M6, about 10 min each.
- A yes before the fork `joeblew999/battery` is created and the PR is opened upstream.

Stages 1 to 3 (the reader, the fork's fix locally, the Worker) need none of these.
