# vm-repair: fix what silently breaks -gui runs on a VM that has lived a while.
# Pushed and run as SYSTEM by `irgo-winvm vm-repair`. Idempotent: on a healthy
# VM every check prints "ok" and changes nothing. One line per check.
#
# Both failures were found the hard way on 2026-09-30
# (.plans/2026-09-30_1215_gui-probes-blocked-by-password-expiry.md):
#  1. Local passwords expire after 42 days; AutoLogon then stops at "Your
#     password has expired", there is no desktop session, and -gui hangs.
#  2. A WebView2 self-update interrupted by a shutdown leaves EBWebView naming a
#     deleted version folder; glaze then reports the runtime "not found"
#     (crgimenes/glaze#34) while a working runtime sits beside it.
#  3. Windows Update scheduled a forced restart nine minutes out in the middle
#     of a test run. No auto-restart with a user logged on; notify, never
#     install on its own. autounattend.xml sets the same on new VMs.
param([string]$User = 'dev')
$ErrorActionPreference = 'Stop'

net accounts /maxpwage:unlimited | Out-Null
Set-LocalUser -Name $User -PasswordNeverExpires $true
"password: ok ($User never expires)"

$au = 'HKLM:\SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate\AU'
New-Item -Path $au -Force | Out-Null
Set-ItemProperty -Path $au -Name NoAutoRebootWithLoggedOnUsers -Value 1 -Type DWord
Set-ItemProperty -Path $au -Name AUOptions -Value 2 -Type DWord
'windows update: ok (no auto-restart, notify only)'

# 4. ...and its restart prompt, "We've got an update for you", stayed on the
#    desktop through every run and screenshot. No update notifications at all,
#    restart warnings included (UpdateNotificationLevel 2, which needs
#    SetUpdateNotificationLevel 1), and no auto-restart notifications. The
#    desktop reset irgo-winvm runs afterwards closes a prompt already up.
$wu = 'HKLM:\SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate'
Set-ItemProperty -Path $wu -Name SetUpdateNotificationLevel -Value 1 -Type DWord
Set-ItemProperty -Path $wu -Name UpdateNotificationLevel -Value 2 -Type DWord
Set-ItemProperty -Path $wu -Name SetAutoRestartNotificationDisable -Value 1 -Type DWord
'windows update notifications: ok (off, restart warnings included)'

$key = 'HKLM:\SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\ClientState\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}'
$reg = (Get-ItemProperty -Path $key -Name EBWebView -ErrorAction SilentlyContinue).EBWebView
if (-not $reg) {
  'webview2: not registered (nothing to repair)'
} elseif (Test-Path (Join-Path $reg 'EBWebView')) {
  "webview2: ok ($(Split-Path $reg -Leaf))"
} else {
  $newest = Get-ChildItem (Split-Path $reg) -Directory |
    Where-Object { $_.Name -match '^\d+(\.\d+){3}$' -and (Test-Path (Join-Path $_.FullName 'Installer\setup.exe')) } |
    Sort-Object { [version]$_.Name } | Select-Object -Last 1
  if (-not $newest) {
    throw "webview2: registered folder $reg is missing and no installed version is there to re-register"
  }
  & (Join-Path $newest.FullName 'Installer\setup.exe') --msedgewebview --system-level | Out-Null
  "webview2: re-registered $($newest.Name) (registration named $(Split-Path $reg -Leaf), which is gone)"
}
