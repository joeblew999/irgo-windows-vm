# What is on a hosted Windows runner's desktop, and (with -Clear) clearing
# what covers it or takes the foreground, before the conformance suite drives
# a window there. Used by conformance.yml only. The VM has its own reset,
# internal/utmvm/assets/desktop-reset.ps1, for dev's session; the ideas are
# the same (walk every z-order band, a CoreWindow is shown only when DWM has
# not cloaked it), the targets are the runner's.
#
# What the windows-11-arm image comes up with (measured 1 Oct 2026): a
# full-screen "Microsoft account" sign-in prompt (WWAHost.exe) over every
# window, with the Start menu open under it. Chromium drops a wheel event
# whose point is over another process's window, so the prompt made
# TestDriveScroll see nothing. Stopping a shell host is safe on a disposable
# runner: StartMenuExperienceHost and SearchHost start again on demand.
param([switch]$Clear, [int]$Watch = 0)
$ErrorActionPreference = 'Stop'

Add-Type @'
using System;
using System.Collections.Generic;
using System.Runtime.InteropServices;
using System.Text;
public static class RunnerDesk {
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] static extern IntPtr FindWindowEx(IntPtr p, IntPtr after, string c, string t);
  [DllImport("user32.dll")] static extern bool IsWindowVisible(IntPtr h);
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] static extern int GetClassName(IntPtr h, StringBuilder s, int n);
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] static extern int GetWindowText(IntPtr h, StringBuilder s, int n);
  [DllImport("user32.dll")] static extern uint GetWindowThreadProcessId(IntPtr h, out uint pid);
  [DllImport("user32.dll")] static extern IntPtr GetForegroundWindow();
  [DllImport("user32.dll")] static extern bool GetWindowRect(IntPtr h, out RECT r);
  [DllImport("dwmapi.dll")] static extern int DwmGetWindowAttribute(IntPtr h, int attr, out int v, int size);
  [StructLayout(LayoutKind.Sequential)] public struct RECT { public int L, T, R, B; }
  public class Win { public IntPtr Hwnd; public uint Pid; public string Class; public string Title; public string Rect; }
  static Win Describe(IntPtr h) {
    var c = new StringBuilder(256); GetClassName(h, c, 256);
    var t = new StringBuilder(512); GetWindowText(h, t, 512);
    uint pid; GetWindowThreadProcessId(h, out pid);
    RECT r; GetWindowRect(h, out r);
    return new Win { Hwnd = h, Pid = pid, Class = c.ToString(), Title = t.ToString(), Rect = r.L + "," + r.T + " " + (r.R - r.L) + "x" + (r.B - r.T) };
  }
  public static Win Foreground() { var h = GetForegroundWindow(); return h == IntPtr.Zero ? null : Describe(h); }
  // FindWindowEx walks every z-order band; EnumWindows misses Start, Search
  // and the taskbar on Windows 11. Shown: visible and not cloaked.
  public static List<Win> Shown() {
    var r = new List<Win>();
    IntPtr h = IntPtr.Zero;
    while ((h = FindWindowEx(IntPtr.Zero, h, null, null)) != IntPtr.Zero) {
      if (!IsWindowVisible(h)) continue;
      int cloaked;
      if (DwmGetWindowAttribute(h, 14, out cloaked, 4) == 0 && cloaked != 0) continue;
      r.Add(Describe(h));
    }
    return r;
  }
}
'@

function ProcName([uint32]$id) {
  $p = Get-Process -Id $id -ErrorAction SilentlyContinue
  if ($p) { $p.Name } else { '?' }
}

function Show($label) {
  "== $label"
  $fg = [RunnerDesk]::Foreground()
  if ($fg) { "foreground: $(ProcName $fg.Pid).exe '$($fg.Title)' (pid $($fg.Pid), $($fg.Class), $($fg.Rect))" } else { 'foreground: none' }
  [RunnerDesk]::Shown() | ForEach-Object {
    [pscustomobject]@{ Pid = $_.Pid; Process = (ProcName $_.Pid); Class = $_.Class; Title = $_.Title; Rect = $_.Rect }
  } | Format-Table -AutoSize | Out-String -Width 220
}

Show 'shown windows'
# Who started the console programs and terminals on the desktop: the wsl.exe
# that turns into a Windows Terminal window was started by something at logon.
Get-CimInstance Win32_Process -Filter "Name='wsl.exe' OR Name='wslhost.exe' OR Name='WindowsTerminal.exe' OR Name='OpenConsole.exe' OR Name='conhost.exe'" |
  ForEach-Object {
    $parent = Get-CimInstance Win32_Process -Filter "ProcessId=$($_.ParentProcessId)" -ErrorAction SilentlyContinue
    [pscustomobject]@{ Pid = $_.ProcessId; Name = $_.Name; Started = $_.CreationDate; Parent = "$($parent.Name) ($($_.ParentProcessId)) $($parent.CommandLine)"; CommandLine = $_.CommandLine }
  } | Format-List | Out-String -Width 300
if (-not $Clear) { exit 0 }
'== where a program started at logon could come from'
foreach ($k in 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run', 'HKCU:\Software\Microsoft\Windows\CurrentVersion\RunOnce',
  'HKLM:\Software\Microsoft\Windows\CurrentVersion\Run', 'HKLM:\Software\Microsoft\Windows\CurrentVersion\RunOnce') {
  $v = Get-ItemProperty -Path $k -ErrorAction SilentlyContinue
  if ($v) { $v.PSObject.Properties | Where-Object Name -notlike 'PS*' | ForEach-Object { "${k}: $($_.Name) = $($_.Value)" } }
}
Get-ChildItem "$env:APPDATA\Microsoft\Windows\Start Menu\Programs\Startup", "$env:ProgramData\Microsoft\Windows\Start Menu\Programs\StartUp" -ErrorAction SilentlyContinue | ForEach-Object { "startup folder: $($_.FullName)" }
Get-ScheduledTask -ErrorAction SilentlyContinue | Where-Object { ($_.Actions | Out-String) -match 'wsl|bash' } | ForEach-Object { "scheduled task: $($_.TaskPath)$($_.TaskName) [$($_.State)]: $(($_.Actions | ForEach-Object { "$($_.Execute) $($_.Arguments)" }) -join '; ')" }

# Anything that covers the desktop or can take the foreground on its own: the
# sign-in prompt, notification toasts (an uncloaked CoreWindow of
# ShellExperienceHost), Widgets, OneDrive and Teams, and what the image's
# provisioning leaves open — a "System Properties" (performance options)
# dialog, and on some runners a wsl.exe console that Windows later hands to a
# new Windows Terminal window, which takes the foreground mid-run (both
# measured 1 Oct 2026). Nothing in the job uses WSL. The runner's own
# hosted-compute-agent console stays.
$covering = 'WWAHost', 'ShellExperienceHost', 'Widgets', 'WidgetService', 'OneDrive', 'ms-teams', 'msteams',
  'SystemPropertiesPerformance', 'wsl', 'wslhost', 'WindowsTerminal', 'OpenConsole'
$session = (Get-Process -Id $PID).SessionId
function StopNamed($names) {
  $p = @(Get-Process -Name $names -ErrorAction SilentlyContinue | Where-Object SessionId -eq $session)
  $p | Stop-Process -Force -ErrorAction SilentlyContinue
  if ($p) { "stopped: $(($p | ForEach-Object { "$($_.Name) ($($_.Id))" }) -join ', ')" } else { "stopped: none of $($names -join ', ')" }
}
StopNamed $covering

# The wsl.exe is the image's own WSL updater, `wsl.exe --update --confirm
# --prompt-before-exit`, which Windows relaunches (30 s after it was stopped,
# measured; every ~90 s for the whole job, actions/runner-images#14264) until
# WSL is up to date, each time in a new Windows Terminal window that takes the
# foreground. Stopping it is not enough. So run the update to the end, here,
# in this step's own console, and say how it went.
'== updating WSL, so the image stops relaunching its updater'
$t = [Diagnostics.Stopwatch]::StartNew()
$u = Start-Process wsl.exe -ArgumentList '--update', '--web-download' -NoNewWindow -PassThru
if (-not $u.WaitForExit(300000)) { $u | Stop-Process -Force; "wsl --update did not finish in 300 s; stopped" }
else { "wsl --update exited $($u.ExitCode) after $([int]$t.Elapsed.TotalSeconds) s" }
& wsl.exe --version 2>&1 | ForEach-Object { "wsl --version: $_" }
StopNamed $covering
Start-Sleep -Seconds 2

# Start and Search: the sign-in prompt sits over an open Start menu, and with
# the prompt gone Start (or Search) can open again by itself: measured on one
# runner, both were back 3 s after their hosts were stopped. So close them
# until they stay closed: Escape, which goes to the pane when it has the
# foreground (global input, fine on a disposable runner), and stopping the
# hosts, which start again on demand. Every round is printed.
function StartPanes {
  [RunnerDesk]::Shown() | Where-Object {
    $_.Class -eq 'Windows.UI.Core.CoreWindow' -and ('StartMenuExperienceHost', 'SearchHost' -contains (ProcName $_.Pid))
  }
}
$shell = New-Object -ComObject WScript.Shell
for ($round = 1; $round -le 5; $round++) {
  $open = @(StartPanes)
  if (-not $open) { break }
  "round ${round}: open: $(($open | ForEach-Object { "'$($_.Title)'" }) -join ', '); Escape, then stopping their hosts"
  $shell.SendKeys('{ESC}')
  Start-Sleep -Milliseconds 500
  StopNamed 'StartMenuExperienceHost', 'SearchHost'
  Start-Sleep -Seconds 3
}
Show 'shown windows after clearing'

# Checked, not assumed: the sign-in prompt and an open Start or Search pane
# are the ones measured covering the window, so their coming back fails here
# rather than as a dropped event in a test.
$back = @(StartPanes) + @([RunnerDesk]::Shown() | Where-Object { (ProcName $_.Pid) -eq 'WWAHost' })
if ($back) {
  "still shown after clearing: $(($back | ForEach-Object { "$(ProcName $_.Pid) '$($_.Title)'" }) -join ', ')"
  exit 1
}

# -Watch N: for N seconds, print every wsl.exe or Windows Terminal that starts
# (to see whether the updater still comes back).
$seen = @{}
for ($i = 0; $i -lt $Watch; $i += 5) {
  Get-CimInstance Win32_Process -Filter "Name='wsl.exe' OR Name='WindowsTerminal.exe'" | Where-Object { -not $seen[$_.ProcessId] } | ForEach-Object {
    $seen[$_.ProcessId] = $true
    "+${i}s: $($_.Name) ($($_.ProcessId)) started $($_.CreationDate): $($_.CommandLine)"
  }
  Start-Sleep -Seconds 5
}
