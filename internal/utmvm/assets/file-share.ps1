# file-share: the SMB share the host pushes binaries through (utmvm.pushShared).
# utmctl file push moves ~0.4 MB/s; this moves them at network speed. The Mac
# connects OUT to the guest, so nothing on the Mac changes: its firewall (in
# stealth mode) drops connections to it, not from it.
#
# Run as SYSTEM by `irgo-winvm vm-repair` (through vm-repair.ps1), and at first
# logon from the unattend CD. Idempotent: on a VM that has it, every line says
# "ok" and nothing changes. -Remove takes all of it away again.
#
# What it opens, and to whom: \\<guest>\irgo-drop, a folder of its own, to the
# dev account only, on TCP 445 from the guest's own subnet only — the UTM shared
# network, whose only other member is the Mac.
#
# No LocalAccountTokenFilterPolicy: that is for admin shares (C$). dev reaches
# this share with its filtered network token, through its own grants below.
param([string]$User = 'dev', [switch]$Remove)
$ErrorActionPreference = 'Stop'

$name = 'irgo-drop'
$path = 'C:\irgo-drop'
$rule = 'irgo-winvm: SMB from the host'

if ($Remove) {
  if (Get-SmbShare -Name $name -ErrorAction SilentlyContinue) { Remove-SmbShare -Name $name -Force }
  Get-NetFirewallRule -DisplayName $rule -ErrorAction SilentlyContinue | Remove-NetFirewallRule
  if (Test-Path $path) { Remove-Item -LiteralPath $path -Recurse -Force }
  "file share: removed (\\$env:COMPUTERNAME\$name, $path, firewall rule)"
  return
}

# The Server service is what answers on 445. It is on by default; a VM where
# someone turned it off would otherwise fail with a timeout that names nothing.
Set-Service -Name LanmanServer -StartupType Automatic
Start-Service -Name LanmanServer

New-Item -ItemType Directory -Force -Path $path | Out-Null
# Explicit, because a network logon is not INTERACTIVE, and inherited grants on
# a folder vary with where it is. Modify: create, write, rename, delete.
icacls $path /grant "${User}:(OI)(CI)M" | Out-Null
if ($LASTEXITCODE -ne 0) { throw "icacls could not grant $User on $path" }

$s = Get-SmbShare -Name $name -ErrorAction SilentlyContinue
if ($s -and $s.Path -ne $path) { Remove-SmbShare -Name $name -Force; $s = $null }
if (-not $s) {
  New-SmbShare -Name $name -Path $path -FullAccess $User -Description 'irgo-winvm push' | Out-Null
} else {
  Grant-SmbShareAccess -Name $name -AccountName $User -AccessRight Full -Force | Out-Null
}

# LocalSubnet, not Any: the guest's own network is the UTM shared network, and
# the Mac is on it. Profile Any: Windows files that network as Public.
if (-not (Get-NetFirewallRule -DisplayName $rule -ErrorAction SilentlyContinue)) {
  New-NetFirewallRule -DisplayName $rule -Direction Inbound -Action Allow -Protocol TCP `
    -LocalPort 445 -RemoteAddress LocalSubnet -Profile Any | Out-Null
}

"file share: ok (\\$env:COMPUTERNAME\$name is $path, for $User, TCP 445 from the local subnet)"
