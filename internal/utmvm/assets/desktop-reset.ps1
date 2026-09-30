# desktop-reset: put the VM's desktop back to nothing but the shell.
# Pushed by `irgo-winvm` and run as the logged-in user IN THEIR SESSION, through
# the same /it scheduled task app-create -gui uses. Not as SYSTEM: SYSTEM is in
# session 0 and cannot see, let alone close, a window on dev's desktop.
#
# What it closes, found on the VM on 2026-09-30:
#  - Explorer folder windows (CabinetWClass). Every openurl.Open / Reveal left
#    one; six explorer.exe processes had accumulated.
#  - Explorer's own error boxes (#32770 owned by explorer.exe): "Location is
#    not available", from a probe deleting its temp dir while Explorer was
#    still navigating to it.
#  - Windows Update's restart prompt, "We've got an update for you ... restart
#    at 7:04". The window is a Shell_SystemDialogProxy owned by PickerHost.exe,
#    NOT by MoNotificationUx.exe, which only asks for it (/NotificationType
#    Reboot_Engaged). That is why killing MoNotificationUx left it on screen:
#    the pixels were not stale, the window belonged to another process.
#  - The Start menu, found open after that prompt was gone and again after a
#    glaze-check run.
#
# explorer.exe is never killed: that takes the taskbar with it, and Windows did
# not restart the shell when it was tried. Explorer's windows are asked to close
# with WM_CLOSE, what their close button sends.
#
# Checked, not assumed: it looks again afterwards and exits 1 if any of those is
# still there, or if the taskbar is gone. One line per kind.
$ErrorActionPreference = 'Stop'

Add-Type @'
using System;
using System.Collections.Generic;
using System.Runtime.InteropServices;
using System.Text;
public static class IrgoDesk {
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] static extern IntPtr FindWindowEx(IntPtr p, IntPtr after, string c, string t);
  [DllImport("user32.dll")] static extern bool IsWindowVisible(IntPtr h);
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] static extern int GetClassName(IntPtr h, StringBuilder s, int n);
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] static extern int GetWindowText(IntPtr h, StringBuilder s, int n);
  [DllImport("user32.dll")] static extern uint GetWindowThreadProcessId(IntPtr h, out uint pid);
  [DllImport("dwmapi.dll")] static extern int DwmGetWindowAttribute(IntPtr h, int attr, out int v, int size);
  [DllImport("user32.dll")] public static extern bool PostMessage(IntPtr h, uint m, IntPtr w, IntPtr l);
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern IntPtr FindWindow(string c, string t);
  public class Win { public IntPtr Hwnd; public uint Pid; public string Class; public string Title; }
  static Win Describe(IntPtr h) {
    var c = new StringBuilder(256); GetClassName(h, c, 256);
    var t = new StringBuilder(512); GetWindowText(h, t, 512);
    uint pid; GetWindowThreadProcessId(h, out pid);
    return new Win { Hwnd = h, Pid = pid, Class = c.ToString(), Title = t.ToString() };
  }
  // FindWindowEx, not EnumWindows. EnumWindows lists only the desktop's own
  // z-order band, and Windows 11 puts the taskbar, Start, Search and the
  // update prompt in others: measured 30 Sep 2026, EnumWindows (and UI
  // Automation) showed neither the taskbar nor the prompt that was on the
  // screen, while FindWindowEx walked every band.
  public static List<Win> Visible() {
    var r = new List<Win>();
    IntPtr h = IntPtr.Zero;
    while ((h = FindWindowEx(IntPtr.Zero, h, null, null)) != IntPtr.Zero) {
      if (IsWindowVisible(h)) r.Add(Describe(h));
    }
    return r;
  }
  // Shown: visible AND not cloaked. The Start and Search panes are CoreWindows
  // that stay "visible" all the time and are cloaked by DWM while closed.
  public static List<Win> Shown() {
    var r = new List<Win>();
    foreach (var w in Visible()) {
      int cloaked;
      if (DwmGetWindowAttribute(w.Hwnd, 14, out cloaked, 4) == 0 && cloaked != 0) continue;
      r.Add(w);
    }
    return r;
  }
}
'@

$updateUx = 'MoNotificationUx', 'MusNotificationUx', 'MusNotification'
$startHosts = 'StartMenuExperienceHost', 'SearchHost'
# The shell's always-present immersive windows, left out of "other windows".
# Explorer is among them: whatever of its windows is not an Explorer window or
# error box (both handled above) is the shell itself.
$immersive = 'explorer', 'ShellExperienceHost', 'ShellHost', 'TextInputHost'

function ProcName([uint32]$id) {
  $p = Get-Process -Id $id -ErrorAction SilentlyContinue
  if ($p) { $p.Name } else { '' }
}

# The windows this script is responsible for, labelled by kind.
function Junk {
  foreach ($w in [IrgoDesk]::Visible()) {
    $name = ProcName $w.Pid
    $kind = $null
    if ($w.Class -eq 'CabinetWClass') { $kind = 'explorer window' }
    elseif ($w.Class -eq '#32770' -and $name -eq 'explorer') { $kind = 'explorer error box' }
    elseif ($w.Class -eq 'Shell_SystemDialogProxy') { $kind = 'windows update prompt' }
    elseif ($updateUx -contains $name) { $kind = 'windows update prompt' }
    if ($kind) {
      [pscustomobject]@{ Kind = $kind; Hwnd = $w.Hwnd; Pid = $w.Pid; Proc = $name; Title = $w.Title }
    }
  }
}

# The Start or Search pane, if one is showing: an uncloaked CoreWindow of one
# of their hosts. Not the foreground window: this script runs in a console of
# its own, which has the foreground, while Start stays open behind it —
# measured 30 Sep 2026, a check by foreground said "not open" over an open Start.
function StartPanes {
  [IrgoDesk]::Shown() | Where-Object {
    $_.Class -eq 'Windows.UI.Core.CoreWindow' -and $startHosts -contains (ProcName $_.Pid)
  }
}

function Say($label, $items) {
  if (-not $items) { "${label}: none"; return }
  $titles = ($items | ForEach-Object { if ($_.Title) { "'$($_.Title)'" } else { "($($_.Proc))" } }) -join ', '
  "${label}: closed $(@($items).Count) - $titles"
}

$found = @(Junk)

# The update prompt's host is stopped, not asked. WM_CLOSE to the proxy window
# made the window go while its picture stayed on the screen — measured 30 Sep
# 2026: gone from EnumWindows and from UI Automation, still in a guest-side
# screen capture, and PickerHost.exe still running. Stopping PickerHost took the
# picture with it. COM starts it again on demand for whatever next needs it.
$promptHosts = @($found | Where-Object { $_.Kind -eq 'windows update prompt' -and $_.Proc -ne 'explorer' } |
  Select-Object -ExpandProperty Pid -Unique)
foreach ($id in $promptHosts) { Stop-Process -Id $id -Force }
foreach ($j in ($found | Where-Object Kind -ne 'windows update prompt')) {
  [void][IrgoDesk]::PostMessage($j.Hwnd, 0x0010, [IntPtr]::Zero, [IntPtr]::Zero)
}

# Up to 10 s for them to go; Explorer answers at once.
$deadline = (Get-Date).AddSeconds(10)
do {
  Start-Sleep -Milliseconds 500
  $left = @(Junk)
} while ($left.Count -gt 0 -and (Get-Date) -lt $deadline)

# The requester too, whether or not its prompt was up: left running, it asks
# again.
$session = (Get-Process -Id $PID).SessionId
$uxStopped = @(Get-Process -Name $updateUx -ErrorAction SilentlyContinue | Where-Object SessionId -eq $session)
$uxStopped | Stop-Process -Force

# Its host is stopped: Escape needs the foreground, which this console has, and
# a keystroke that misses lands in whatever does. StartMenuExperienceHost and
# SearchHost are started again on demand, the next time Start is opened;
# explorer.exe is not among them.
$startPanes = @(StartPanes)
$startNames = ($startPanes | ForEach-Object { ProcName $_.Pid } | Select-Object -Unique) -join ', '
foreach ($id in ($startPanes | Select-Object -ExpandProperty Pid -Unique)) { Stop-Process -Id $id -Force }
if ($startPanes) { Start-Sleep -Seconds 1 }

Say 'explorer windows' ($found | Where-Object Kind -eq 'explorer window')
Say 'explorer error boxes' ($found | Where-Object Kind -eq 'explorer error box')
Say 'windows update prompts' ($found | Where-Object Kind -eq 'windows update prompt')
if ($uxStopped) { "windows update notifier: stopped $(($uxStopped | ForEach-Object Name) -join ', ')" }
else { 'windows update notifier: not running' }
if ($startPanes) { "start menu: closed ($startNames)" } else { 'start menu: not open' }

$bad = $false
foreach ($j in @(Junk)) {
  $bad = $true
  "STILL OPEN: $($j.Kind) '$($j.Title)' ($($j.Proc) pid $($j.Pid))"
}
if (StartPanes) {
  $bad = $true
  'STILL OPEN: the Start menu'
}
# FindWindow, not the visible list: while the update prompt was up the taskbar
# was drawn on the screen and reported not visible.
if ([IrgoDesk]::FindWindow('Shell_TrayWnd', $null) -eq [IntPtr]::Zero) {
  $bad = $true
  'TASKBAR MISSING: no Shell_TrayWnd on this desktop (explorer.exe is not running as the shell)'
}

# Whatever else is on the screen, so the caller can see it. Not closed: an app
# window may be the thing someone is looking at (glaze:hands leaves one up on
# purpose). The shell's own windows and this script's console are left out.
$shell = 'Shell_TrayWnd', 'Shell_SecondaryTrayWnd', 'Progman', 'WorkerW', 'DummyDWMListenerWindow',
  'EdgeUiInputTopWndClass', 'CASCADIA_HOSTING_WINDOW_CLASS', 'PseudoConsoleWindow', 'ConsoleWindowClass'
$other = @([IrgoDesk]::Shown() | Where-Object { $shell -notcontains $_.Class -and $immersive -notcontains (ProcName $_.Pid) })
if ($other) {
  'other windows: ' + (($other | ForEach-Object { "'$($_.Title)' ($(ProcName $_.Pid), $($_.Class))" }) -join ', ')
} else {
  'other windows: none'
}

if ($bad) { exit 1 }
exit 0
