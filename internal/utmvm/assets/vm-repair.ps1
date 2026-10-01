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

# 6. OneDrive put "Turn On Windows Backup" on the desktop a minute after the
#    reboot that installed an update. Off by policy: it no longer starts.
$od = 'HKLM:\SOFTWARE\Policies\Microsoft\Windows\OneDrive'
New-Item -Path $od -Force | Out-Null
Set-ItemProperty -Path $od -Name DisableFileSyncNGSC -Value 1 -Type DWord
'onedrive: ok (off by policy)'

# 7. Toasts, which is how OneDrive's prompt actually arrived: a per-user policy,
#    so it goes into dev's hive, which is loaded while dev is logged on.
$sid = (New-Object System.Security.Principal.NTAccount($User)).Translate([System.Security.Principal.SecurityIdentifier]).Value
if (Test-Path "Registry::HKEY_USERS\$sid") {
  $pn = "Registry::HKEY_USERS\$sid\Software\Policies\Microsoft\Windows\CurrentVersion\PushNotifications"
  New-Item -Path $pn -Force | Out-Null
  Set-ItemProperty -Path $pn -Name NoToastApplicationNotification -Value 1 -Type DWord
  "notifications: ok (toasts off for $User)"
} else {
  "notifications: NOT SET, $User is not logged on (run vm-repair again once AutoLogon has run)"
}

# 5. UTM forwards the Mac's Command key as the Windows key, so each Cmd-Tab on
#    the Mac opened Start in the guest, over screenshots and -gui windows.
#    Scancode Map maps left and right Windows (E0 5B, E0 5C) to nothing. It is
#    read at boot, so whether it is in effect is a question about when it was
#    written: the marker records that, and is compared with the last boot.
#    Without a marker (set by the answer file at install) that cannot be told.
$kl = 'HKLM:\SYSTEM\CurrentControlSet\Control\Keyboard Layout'
$want = [byte[]](0,0,0,0, 0,0,0,0, 3,0,0,0, 0,0,0x5B,0xE0, 0,0,0x5C,0xE0, 0,0,0,0)
$have = (Get-ItemProperty -Path $kl -Name 'Scancode Map' -ErrorAction SilentlyContinue).'Scancode Map'
$mark = 'HKLM:\SOFTWARE\irgo-winvm'
$boot = (Get-CimInstance Win32_OperatingSystem).LastBootUpTime
if ($have -and (@(Compare-Object $have $want -SyncWindow 0).Count -eq 0)) {
  $setAt = (Get-ItemProperty -Path $mark -Name ScancodeMapSetAt -ErrorAction SilentlyContinue).ScancodeMapSetAt
  if (-not $setAt) {
    'windows key: set (by the answer file); cannot tell whether this boot has it - vm-repair -reboot makes sure'
  } elseif ([datetime]::FromFileTimeUtc([int64]$setAt) -lt $boot.ToUniversalTime()) {
    'windows key: ok (disabled, in effect since the last boot)'
  } else {
    'windows key: set, NEEDS A REBOOT to take effect (vm-repair -reboot)'
  }
} else {
  Set-ItemProperty -Path $kl -Name 'Scancode Map' -Value $want -Type Binary
  New-Item -Path $mark -Force | Out-Null
  Set-ItemProperty -Path $mark -Name ScancodeMapSetAt -Value ([datetime]::UtcNow.ToFileTimeUtc()) -Type QWord
  'windows key: disabled now, NEEDS A REBOOT to take effect (vm-repair -reboot)'
}

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

# 8. Device Encryption. Windows 11 24H2 turns it on by itself on VMs installed
#    before autounattend set PreventDeviceEncryption (irgo-win11: 100 %
#    encrypted, found by vm-check 1 Oct 2026). Prevent it, and start decrypting
#    C: if it is encrypted. Not waited for: decryption runs in the background
#    and vm-check's TestDeviceEncryptionOff/volume_decrypted reports when done.
#    WMI, not manage-bde, whose output is localised.
$bl = 'HKLM:\SYSTEM\CurrentControlSet\Control\BitLocker'
New-Item -Path $bl -Force -ErrorAction SilentlyContinue | Out-Null
Set-ItemProperty -Path $bl -Name PreventDeviceEncryption -Value 1 -Type DWord
$vol = Get-CimInstance -Namespace 'root/CIMV2/Security/MicrosoftVolumeEncryption' `
  -ClassName Win32_EncryptableVolume -Filter "DriveLetter='C:'" -ErrorAction SilentlyContinue
if (-not $vol) {
  'device encryption: ok (prevented; BitLocker not available on C:)'
} else {
  $st = Invoke-CimMethod -InputObject $vol -MethodName GetConversionStatus
  # ConversionStatus: 0 decrypted, 1 encrypted, 2 encrypting, 3 decrypting,
  # 4 encryption paused, 5 decryption paused.
  switch ($st.ConversionStatus) {
    0 { 'device encryption: ok (prevented; C: fully decrypted)' }
    3 { "device encryption: decrypting C: ($($st.EncryptionPercentage) % still encrypted)" }
    default {
      $r = Invoke-CimMethod -InputObject $vol -MethodName Decrypt
      if ($r.ReturnValue -ne 0) { throw "device encryption: Decrypt on C: returned $($r.ReturnValue)" }
      "device encryption: started decrypting C: (was status $($st.ConversionStatus), $($st.EncryptionPercentage) % encrypted)"
    }
  }
}

# 9. Hibernation: hiberfil.sys is gigabytes a VM never uses (3.4 GB on irgo-win11).
powercfg /h off
if ($LASTEXITCODE -ne 0) { throw "powercfg /h off exited $LASTEXITCODE" }
'hibernation: ok (off)'
