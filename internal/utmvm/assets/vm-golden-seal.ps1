# vm-golden-seal: make an installed Windows fit to be copied.
# Pushed and run as SYSTEM by `irgo-winvm vm-golden-create`, one -Step at a
# time, so the host can measure the disk between steps and say which one is
# running. Idempotent: every step on a VM already sealed prints what it found
# and changes nothing. One line per fact, "name: value", which the host reads.
#
# Why each step, from .plans/2026-09-30_1700_vm-golden-image.md (measured on
# irgo-win11, 30 Sep 2026):
#  decrypt    Windows 11 24H2 turned on Device Encryption by itself: "Used
#             Space Only Encrypted, 100 %". Ciphertext does not compress, and a
#             TPM protector added later would lock a copy out. Through WMI
#             rather than manage-bde, whose output is localised.
#  hibernate  hiberfil.sys was 3.43 GB of nothing a copy needs.
#  cleanup    WinSxS held 6.50 GB of "Backups and Disabled Features".
#             /ResetBase means updates already installed can no longer be
#             uninstalled, which a disposable VM never needs.
#  trim       UTM passes discard=unmap to QEMU, so a TRIM may punch holes in
#             the raw disk.img on the host. Whether it does is what the host
#             measures around this step.
param(
  [Parameter(Mandatory = $true)]
  [ValidateSet('facts', 'decrypt', 'hibernate', 'cleanup', 'trim')]
  [string]$Step
)
$ErrorActionPreference = 'Stop'

function Get-SystemVolume {
  Get-CimInstance -Namespace 'root/CIMV2/Security/MicrosoftVolumeEncryption' `
    -ClassName Win32_EncryptableVolume -Filter "DriveLetter='C:'" -ErrorAction SilentlyContinue
}

# ConversionStatus: 0 fully decrypted, 1 fully encrypted, 2 encrypting,
# 3 decrypting, 4 encryption paused, 5 decryption paused.
function Get-Conversion($v) {
  Invoke-CimMethod -InputObject $v -MethodName GetConversionStatus
}

$began = Get-Date
function Elapsed { [int]((Get-Date) - $began).TotalSeconds }

switch ($Step) {
  'facts' {
    $cv = Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion'
    "windows: $($cv.CurrentBuild).$($cv.UBR)"
    $wv = (Get-ItemProperty 'HKLM:\SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}' `
      -Name pv -ErrorAction SilentlyContinue).pv
    if (-not $wv) { $wv = 'none' }
    "webview2: $wv"
    $v = Get-SystemVolume
    if ($v) {
      $s = Get-Conversion $v
      "bitlocker: status $($s.ConversionStatus), $($s.EncryptionPercentage)% encrypted"
    } else {
      'bitlocker: not available'
    }
    "hiberfil: $(Test-Path 'C:\hiberfil.sys')"
    $c = Get-Volume -DriveLetter C
    "c-used: $($c.Size - $c.SizeRemaining)"
  }

  'decrypt' {
    # The same value autounattend.xml sets in specialize, for a VM installed
    # before it did: without it Windows may encrypt again on the next boot.
    $key = 'HKLM:\SYSTEM\CurrentControlSet\Control\BitLocker'
    New-Item -Path $key -Force | Out-Null
    Set-ItemProperty -Path $key -Name PreventDeviceEncryption -Value 1 -Type DWord
    $v = Get-SystemVolume
    if (-not $v) { 'bitlocker: not available, nothing to decrypt'; break }
    $s = Get-Conversion $v
    if ($s.ConversionStatus -eq 0) { 'bitlocker: already fully decrypted'; break }
    if ($s.ConversionStatus -ne 3) {
      $r = Invoke-CimMethod -InputObject $v -MethodName Decrypt
      if ($r.ReturnValue -ne 0) { throw "bitlocker: Decrypt returned $($r.ReturnValue)" }
    }
    # Polled, not waited on: Decrypt returns at once and the conversion runs in
    # the background.
    while ($true) {
      $s = Get-Conversion $v
      if ($s.ConversionStatus -eq 0) { break }
      if ((Elapsed) -gt 7200) { throw "bitlocker: still $($s.EncryptionPercentage)% encrypted after $(Elapsed) s" }
      Start-Sleep -Seconds 10
    }
    "bitlocker: fully decrypted in $(Elapsed) s"
  }

  'hibernate' {
    powercfg /h off
    if ($LASTEXITCODE -ne 0) { throw "powercfg /h off exited $LASTEXITCODE" }
    if (Test-Path 'C:\hiberfil.sys') { throw 'hiberfil: powercfg said yes and C:\hiberfil.sys is still there' }
    'hiberfil: off, C:\hiberfil.sys gone'
  }

  'cleanup' {
    Dism.exe /Online /Cleanup-Image /StartComponentCleanup /ResetBase /Quiet | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "dism /StartComponentCleanup /ResetBase exited $LASTEXITCODE" }
    "winsxs: cleaned up in $(Elapsed) s"
  }

  'trim' {
    Optimize-Volume -DriveLetter C -ReTrim
    "trim: C: retrimmed in $(Elapsed) s"
  }
}
