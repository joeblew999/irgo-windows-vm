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
#  - The Start menu, which was open behind that prompt once it was gone.
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
  public delegate bool EnumProc(IntPtr h, IntPtr l);
  [DllImport("user32.dll")] static extern bool EnumWindows(EnumProc f, IntPtr l);
  [DllImport("user32.dll")] static extern bool IsWindowVisible(IntPtr h);
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] static extern int GetClassName(IntPtr h, StringBuilder s, int n);
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] static extern int GetWindowText(IntPtr h, StringBuilder s, int n);
  [DllImport("user32.dll")] static extern uint GetWindowThreadProcessId(IntPtr h, out uint pid);
  [DllImport("user32.dll")] static extern IntPtr GetForegroundWindow();
  [DllImport("user32.dll")] static extern void keybd_event(byte vk, byte scan, uint flags, UIntPtr extra);
  [DllImport("user32.dll")] public static extern bool PostMessage(IntPtr h, uint m, IntPtr w, IntPtr l);
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern IntPtr FindWindow(string c, string t);
  public class Win { public IntPtr Hwnd; public uint Pid; public string Class; public string Title; }
  static Win Describe(IntPtr h) {
    var c = new StringBuilder(256); GetClassName(h, c, 256);
    var t = new StringBuilder(512); GetWindowText(h, t, 512);
    uint pid; GetWindowThreadProcessId(h, out pid);
    return new Win { Hwnd = h, Pid = pid, Class = c.ToString(), Title = t.ToString() };
  }
  public static List<Win> Visible() {
    var r = new List<Win>();
    EnumWindows((h, l) => { if (IsWindowVisible(h)) r.Add(Describe(h)); return true; }, IntPtr.Zero);
    return r;
  }
  public static Win Foreground() { return Describe(GetForegroundWindow()); }
  public static void Escape() { keybd_event(0x1B, 0, 0, UIntPtr.Zero); keybd_event(0x1B, 0, 2, UIntPtr.Zero); }
}
'@

$updateUx = 'MoNotificationUx', 'MusNotificationUx', 'MusNotification'
$startHosts = 'StartMenuExperienceHost', 'SearchHost', 'ShellExperienceHost'

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

function StartIsOpen {
  $fg = [IrgoDesk]::Foreground()
  $fg.Class -eq 'Windows.UI.Core.CoreWindow' -and $startHosts -contains (ProcName $fg.Pid)
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

# Escape closes Start, as it would for a person. Sent only when Start or Search
# has the foreground, so it cannot land in someone's app.
$startWasOpen = StartIsOpen
if ($startWasOpen) {
  [IrgoDesk]::Escape()
  Start-Sleep -Milliseconds 800
}

Say 'explorer windows' ($found | Where-Object Kind -eq 'explorer window')
Say 'explorer error boxes' ($found | Where-Object Kind -eq 'explorer error box')
Say 'windows update prompts' ($found | Where-Object Kind -eq 'windows update prompt')
if ($uxStopped) { "windows update notifier: stopped $(($uxStopped | ForEach-Object Name) -join ', ')" }
else { 'windows update notifier: not running' }
if ($startWasOpen) { 'start menu: closed' } else { 'start menu: not open' }

$bad = $false
foreach ($j in @(Junk)) {
  $bad = $true
  "STILL OPEN: $($j.Kind) '$($j.Title)' ($($j.Proc) pid $($j.Pid))"
}
if (StartIsOpen) {
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
$other = @([IrgoDesk]::Visible() | Where-Object { $shell -notcontains $_.Class })
if ($other) {
  'other windows: ' + (($other | ForEach-Object { "'$($_.Title)' ($(ProcName $_.Pid), $($_.Class))" }) -join ', ')
} else {
  'other windows: none'
}

if ($bad) { exit 1 }
exit 0
