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
param([switch]$Clear)
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
if (-not $Clear) { exit 0 }

# The sign-in prompt, Start and Search, notification toasts (an uncloaked
# CoreWindow of ShellExperienceHost), Widgets, OneDrive and Teams: anything
# that covers the desktop or can take the foreground on its own.
$covering = 'WWAHost', 'StartMenuExperienceHost', 'SearchHost', 'ShellExperienceHost', 'Widgets', 'WidgetService', 'OneDrive', 'ms-teams', 'msteams'
$session = (Get-Process -Id $PID).SessionId
$stopped = @(Get-Process -Name $covering -ErrorAction SilentlyContinue | Where-Object SessionId -eq $session)
$stopped | Stop-Process -Force
if ($stopped) { "stopped: $(($stopped | ForEach-Object { "$($_.Name) ($($_.Id))" }) -join ', ')" } else { 'stopped: nothing' }
Start-Sleep -Seconds 3
Show 'shown windows after clearing'

# Checked, not assumed: the sign-in prompt and an open Start or Search pane
# are the ones measured covering the window, so their coming back fails here
# rather than as a dropped event in a test.
$back = [RunnerDesk]::Shown() | Where-Object {
  $n = ProcName $_.Pid
  $n -eq 'WWAHost' -or ($_.Class -eq 'Windows.UI.Core.CoreWindow' -and ('StartMenuExperienceHost', 'SearchHost' -contains $n))
}
if ($back) {
  "still shown after clearing: $(($back | ForEach-Object { "$(ProcName $_.Pid) '$($_.Title)'" }) -join ', ')"
  exit 1
}
