# vm-ssh: the OpenSSH server in the guest, and one public key allowed in.
# Pushed and run as SYSTEM by `irgo-winvm vm-ssh-create`; -Remove is
# `vm-ssh-delete`. Idempotent: on a VM that has it, every line says "ok" and the
# last says nothing changed.
#
# What it opens, and to whom: TCP 22, from the guest's own subnet only (the UTM
# shared network, where the Mac is), on every profile because Windows files
# that network as Public.
#
# What it does not touch: sshd's own configuration. Only a key is added.
# Whether a password is also accepted is whatever Windows' sshd defaults to.
param([string]$User = 'dev', [string]$KeyFile = '', [switch]$Remove)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$rule = 'irgo-winvm: SSH from the host'
# sshd reads an administrator's keys from here, not from the profile, and
# ignores the file unless only Administrators and SYSTEM can write it.
$keys = "$env:ProgramData\ssh\administrators_authorized_keys"

# The rule the capability installs is Windows' own, for any address. Turned
# off, saying which profiles it was for, so our rule is the only way in and the
# undo leaves 22 closed.
function closeWindowsSSHRule {
  $on = @(Get-NetFirewallRule -Name 'OpenSSH-Server-In-TCP' -ErrorAction SilentlyContinue |
    Where-Object { $_.Enabled -eq 'True' })
  $on | Disable-NetFirewallRule
  if ($on.Count -gt 0) { return "$($on[0].Profile)" }
  return ''
}

function listening { @(Get-NetTCPConnection -LocalPort 22 -State Listen -ErrorAction SilentlyContinue).Count -gt 0 }

if ($Remove) {
  if (Get-Service -Name sshd -ErrorAction SilentlyContinue) {
    Stop-Service -Name sshd -Force
    Set-Service -Name sshd -StartupType Disabled
  }
  # Sessions outlive the service that accepted them.
  Get-Process -Name sshd -ErrorAction SilentlyContinue | Stop-Process -Force
  Get-NetFirewallRule -DisplayName $rule -ErrorAction SilentlyContinue | Remove-NetFirewallRule
  closeWindowsSSHRule | Out-Null
  if (Test-Path -LiteralPath $keys) { Remove-Item -LiteralPath $keys -Force }
  if (listening) { throw 'ssh: something still listens on port 22' }
  "ssh: removed (sshd stopped and disabled, firewall rule, every key in $keys); the OpenSSH Server capability stays installed"
  return
}

$changed = 0

$acct = Get-LocalUser -Name $User -ErrorAction SilentlyContinue
if (-not $acct) { throw "account: there is no local account $User" }
$admins = @(Get-LocalGroupMember -SID 'S-1-5-32-544' | ForEach-Object { $_.SID.Value })
if ($admins -notcontains $acct.SID.Value) {
  throw "account: $User is not an administrator; its keys would go in its own profile, which this script does not write"
}
"account: ok ($User, an administrator)"

if (Get-Service -Name sshd -ErrorAction SilentlyContinue) {
  'openssh server: ok (installed)'
} else {
  Add-WindowsCapability -Online -Name 'OpenSSH.Server~~~~0.0.1.0' | Out-Null
  if (-not (Get-Service -Name sshd -ErrorAction SilentlyContinue)) {
    throw 'openssh server: the capability installed and there is still no sshd service'
  }
  $changed++
  'openssh server: installed now (the Windows capability OpenSSH.Server)'
}

$svc = Get-Service -Name sshd
if ($svc.StartType -eq 'Automatic' -and $svc.Status -eq 'Running') {
  'sshd: ok (automatic, running)'
} else {
  Set-Service -Name sshd -StartupType Automatic
  Start-Service -Name sshd
  $changed++
  "sshd: started now, automatic (it was $($svc.StartType), $($svc.Status))"
}

# LocalSubnet, not Any: the guest's own network is the UTM shared network, and
# the Mac is on it. Profile Any: Windows files that network as Public.
$r = Get-NetFirewallRule -DisplayName $rule -ErrorAction SilentlyContinue
if (-not $r) {
  New-NetFirewallRule -DisplayName $rule -Direction Inbound -Action Allow -Protocol TCP `
    -LocalPort 22 -RemoteAddress LocalSubnet -Profile Any | Out-Null
  $changed++
  'firewall: opened now (TCP 22 from the local subnet, every profile)'
} elseif ($r.Enabled -ne 'True') {
  $r | Enable-NetFirewallRule
  $changed++
  'firewall: rule enabled again (TCP 22 from the local subnet, every profile)'
} else {
  'firewall: ok (TCP 22 from the local subnet, every profile)'
}
$was = closeWindowsSSHRule
if ($was) {
  $changed++
  "firewall: Windows' own rule OpenSSH-Server-In-TCP turned off (any address; it was for profile $was)"
}

# The key, compared by type and key and not by comment. -ceq: base64 is case
# sensitive and PowerShell's -eq is not. .NET reads and writes, so the file is
# UTF-8 with no byte-order mark, which sshd would read as part of the first key.
function keyOf([string]$line) { (($line.Trim() -split '\s+') | Select-Object -First 2) -join ' ' }
$line = @([IO.File]::ReadAllLines($KeyFile) | Where-Object { $_.Trim() })[0].Trim()
$have = @()
if (Test-Path -LiteralPath $keys) { $have = @([IO.File]::ReadAllLines($keys)) }
$present = $false
foreach ($h in $have) { if ((keyOf $h) -ceq (keyOf $line)) { $present = $true } }
if ($present) {
  "key: ok (already in $keys)"
} else {
  New-Item -ItemType Directory -Force -Path (Split-Path $keys) | Out-Null
  [IO.File]::WriteAllLines($keys, [string[]](@($have) + $line), (New-Object System.Text.UTF8Encoding($false)))
  $changed++
  "key: authorized now (in $keys)"
}
# By SID, so it reads the same on a Windows in another language.
icacls $keys /inheritance:r /grant '*S-1-5-32-544:F' /grant '*S-1-5-18:F' | Out-Null
if ($LASTEXITCODE -ne 0) { throw "key: icacls could not restrict $keys to Administrators and SYSTEM" }
Remove-Item -LiteralPath $KeyFile -Force

$deadline = (Get-Date).AddSeconds(20)
while (-not (listening)) {
  if ((Get-Date) -gt $deadline) { throw 'sshd: running, and nothing listens on port 22 after 20 s' }
  Start-Sleep -Milliseconds 500
}

if ($changed -eq 0) { 'ssh: already on, nothing changed' } else { "ssh: on ($changed change(s))" }
