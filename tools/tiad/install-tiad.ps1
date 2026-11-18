<#
.SYNOPSIS
    Install tiad, the nautilus agent for TIA Portal Openness, on a TIA machine.

.DESCRIPTION
    Builds tiad from source with the machine's stock .NET Framework compiler
    (C# 5; no SDK, no NuGet, no network), then: the Openness group, the
    Openness allowlist entry, the token, an interactive scheduled task, and a
    check with health and probe. Idempotent: re-running it rebuilds and
    upgrades.

    Run it in an ELEVATED Windows PowerShell on the TIA machine, from a copy
    of tools/tiad (it compiles core\ and host\ beside it):

        .\install-tiad.ps1 -NoTia          # installed, but TIA can never start
        .\install-tiad.ps1 -Targets 192.0.2.10
        .\install-tiad.ps1 -Uninstall

    The verify step calls health and probe only. Neither ever starts TIA, so
    installing costs no trial time. With -NoTia the agent refuses every call
    that needs TIA (the tia gate fails): use it until TIA may start.

.NOTES
    - tiad.exe never references Siemens.Engineering. The TIA side,
      tiad.Openness.dll, is built separately and loaded on the first call
      that needs TIA. This script does not build it yet, so the agent it
      installs cannot start TIA.
    - TIA's Openness allowlist is checked against the exe's path, last write
      time and SHA-256, so the entry is rewritten after every build. That
      the entry spares the consent prompt is verified at the first TIA
      start.
    - Interactive at logon, not a service: TIA and its licence manager are
      expected to want a desktop session, as logixd's SDK does. Someone (or
      autologon) must be logged in at the console.
    - The token is only ever in a file (TIAD_TOKEN_FILE), never on a command
      line or in the runner.
#>
[CmdletBinding()]
param(
    [int]    $Port       = 8190,
    [string] $Listen     = '127.0.0.1',
    [string] $InstallDir = 'C:\Program Files\tiad',
    [string] $WorkDir    = 'C:\tiad-work',
    # The only CPU addresses tiad may go online or download to (comma separated).
    [string] $Targets    = '',
    # Overridable so a second install does not unregister the first.
    [string] $TaskName   = 'tiad',
    # Fail the tia gate: no call can start TIA (and the trial clock).
    [switch] $NoTia,
    [switch] $Uninstall
)

$ErrorActionPreference = 'Stop'

function Say  ($m) { Write-Host "  $m" }
function Ok   ($m) { Write-Host "  [ok]   $m" -ForegroundColor Green }
function Warn ($m) { Write-Host "  [warn] $m" -ForegroundColor Yellow }
function Die  ($m) { Write-Host "  [fail] $m" -ForegroundColor Red; exit 1 }

$GroupName    = 'Siemens TIA Openness'
$OpennessKey  = 'SOFTWARE\Siemens\Automation\Openness\21.0'
$PublicApiKey = "$OpennessKey\PublicAPI\21.0.0.0\net48"
$AllowListKey = 'SOFTWARE\Siemens\Automation\Openness\AllowList'
$ExeName      = 'tiad.exe'
$CfgDir       = Join-Path $env:ProgramData 'tiad'
$TokenFile    = Join-Path $CfgDir "$TaskName.token"

# HKLM in the 64-bit view, where TIA writes and reads its keys, whichever
# PowerShell runs this.
function Hklm { [Microsoft.Win32.RegistryKey]::OpenBaseKey('LocalMachine', 'Registry64') }

function RegValue ($key, $name) {
    $k = (Hklm).OpenSubKey($key)
    if (-not $k) { return $null }
    try { return $k.GetValue($name) } finally { $k.Close() }
}

function Assert-Admin {
    $id = [Security.Principal.WindowsIdentity]::GetCurrent()
    if (-not (New-Object Security.Principal.WindowsPrincipal $id).IsInRole(
              [Security.Principal.WindowsBuiltInRole]::Administrator)) {
        Die "run this in an ELEVATED PowerShell: it writes HKLM, registers a task and sets ACLs"
    }
}

# Stop a running agent of this install and wait for its port.
function Stop-Tiad {
    if (Get-ScheduledTask -TaskName $TaskName -EA SilentlyContinue) {
        Stop-ScheduledTask -TaskName $TaskName -EA SilentlyContinue
    }
    $exe = Join-Path $InstallDir $ExeName
    Get-CimInstance Win32_Process -Filter "Name='$ExeName'" -EA SilentlyContinue |
        Where-Object { $_.ExecutablePath -and $_.ExecutablePath -ieq $exe } |
        ForEach-Object { Stop-Process -Id $_.ProcessId -Force -EA SilentlyContinue }
    foreach ($c in @(Get-NetTCPConnection -State Listen -LocalPort $Port -EA SilentlyContinue)) {
        $owner = Get-Process -Id $c.OwningProcess -EA SilentlyContinue
        if ($owner -and $owner.ProcessName -eq 'tiad') {
            Stop-Process -Id $owner.Id -Force -EA SilentlyContinue
        } elseif ($owner) {
            Warn "port $Port is held by $($owner.ProcessName) (pid $($owner.Id)), which is not tiad: not touching it"
        }
    }
    foreach ($i in 1..15) {
        if (-not (Get-NetTCPConnection -State Listen -LocalPort $Port -EA SilentlyContinue)) { return $true }
        Start-Sleep -Milliseconds 400
    }
    return $false
}

# Remove-AllowEntry deletes this install's allowlist entry, and the exe's
# key once no entry is left in it.
function Remove-AllowEntry {
    $exeKey = "$AllowListKey\$ExeName"
    $h = Hklm
    $k = $h.OpenSubKey($exeKey, $true)
    if (-not $k) { return }
    try {
        if ($k.GetSubKeyNames() -contains $TaskName) { $k.DeleteSubKeyTree($TaskName) }
        $left = $k.GetSubKeyNames().Count + $k.GetValueNames().Count
    } finally { $k.Close() }
    if ($left -eq 0) { $h.DeleteSubKey($exeKey) }
}

# ---------------------------------------------------------------- uninstall
if ($Uninstall) {
    Write-Host "`nRemoving tiad`n"
    Assert-Admin
    [void](Stop-Tiad)
    if (Get-ScheduledTask -TaskName $TaskName -EA SilentlyContinue) {
        Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
        Ok "scheduled task removed"
    }
    Remove-AllowEntry
    Ok "Openness allowlist entry removed"
    if (Get-NetFirewallRule -DisplayName "tiad ($Port)" -EA SilentlyContinue) {
        Remove-NetFirewallRule -DisplayName "tiad ($Port)"
        Ok "firewall rule removed"
    }
    if (Test-Path $InstallDir) { Remove-Item $InstallDir -Recurse -Force; Ok "$InstallDir removed" }
    # The token grants downloads to a CPU: never leave it behind.
    if (Test-Path $TokenFile) { Remove-Item $TokenFile -Force; Ok "token removed" }
    Say "left alone: $WorkDir, the logs in $CfgDir, and the Openness group membership"
    exit 0
}

Write-Host "`nInstalling tiad`n"
Assert-Admin

# ------------------------------------------------------------ 1. preflight
Write-Host "1. Preflight"
$v = RegValue $OpennessKey 'PortalVersion'
if ($v -ne 'V21') { Die "TIA Portal V21 with Openness is not registered (HKLM\$OpennessKey PortalVersion: '$v')" }
$base = RegValue $PublicApiKey 'Siemens.Engineering.Base'
if (-not $base -or -not (Test-Path $base)) { Die "the Openness V21 net48 public API is not where HKLM\$PublicApiKey says ('$base')" }
Ok "TIA Portal V21, Openness public API at $(Split-Path $base)"

$csc = Join-Path $env:WINDIR 'Microsoft.NET\Framework64\v4.0.30319\csc.exe'
if (-not (Test-Path $csc)) { Die "the .NET Framework 4 compiler is missing: $csc" }
Ok ".NET Framework compiler at $csc"

$src = $PSScriptRoot
foreach ($d in 'core', 'host') {
    if (-not (Test-Path (Join-Path $src "$d\*.cs"))) { Die "no $d\*.cs beside this script: run it from a copy of tools/tiad" }
}

$user = [Security.Principal.WindowsIdentity]::GetCurrent().Name
if (-not (Get-LocalGroup -Name $GroupName -EA SilentlyContinue)) {
    Die "there is no '$GroupName' group: install TIA Openness"
}
$member = Get-LocalGroupMember -Group $GroupName -EA SilentlyContinue | Where-Object { $_.Name -ieq $user }
if ($member) {
    Ok "$user is in '$GroupName'"
} else {
    Add-LocalGroupMember -Group $GroupName -Member $user
    Warn "added $user to '$GroupName': log off and on before the openness-group gate passes"
}

# ---------------------------------------------------------------- 2. build
Write-Host "`n2. Build"
if (-not (Stop-Tiad)) { Warn "port $Port is still held after stopping the old agent; continuing" }
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
$common = @('/nologo', '/langversion:5', '/warnaserror+', '/r:System.Web.Extensions.dll', '/r:System.Xml.dll')
$core = Join-Path $InstallDir 'tiad-core.dll'
$exe  = Join-Path $InstallDir $ExeName
& $csc @common /target:library "/out:$core" (Join-Path $src 'core\*.cs')
if ($LASTEXITCODE -ne 0) { Die "building tiad-core.dll failed" }
& $csc @common /target:exe "/out:$exe" "/r:$core" (Join-Path $src 'host\*.cs')
if ($LASTEXITCODE -ne 0) { Die "building $ExeName failed" }
Ok "built $exe (C# 5, stock csc)"

# ------------------------------------------------------ 3. Openness allowlist
Write-Host "`n3. Openness allowlist"
# The Openness allowlist entry TIA requires: the full path, the last write
# time in UTC to the millisecond, and the SHA-256 hash, in base64.
$fi = Get-Item $exe
$stamp = $fi.LastWriteTimeUtc.ToString('yyyy/MM/dd HH:mm:ss.fff', [Globalization.CultureInfo]::InvariantCulture)
$sha = [Security.Cryptography.SHA256]::Create()
$fs = [IO.File]::OpenRead($exe)
try { $hash = [Convert]::ToBase64String($sha.ComputeHash($fs)) } finally { $fs.Close() }
Remove-AllowEntry
$k = (Hklm).CreateSubKey("$AllowListKey\$ExeName\$TaskName")
try {
    $k.SetValue('Path', $fi.FullName, 'String')
    $k.SetValue('DateModified', $stamp, 'String')
    $k.SetValue('FileHash', $hash, 'String')
} finally { $k.Close() }
Ok "HKLM\$AllowListKey\$ExeName\$TaskName names this build"

# ---------------------------------------------------------------- 4. token
Write-Host "`n4. Token"
New-Item -ItemType Directory -Force -Path $CfgDir | Out-Null
if (Test-Path $TokenFile) {
    Ok "reusing the existing token"
} else {
    $b = New-Object byte[] 32
    [Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($b)
    Set-Content -Path $TokenFile -Value (($b | ForEach-Object { $_.ToString('x2') }) -join '') -NoNewline -Encoding ascii
    Ok "generated a new token"
}
# Administrators, and the account the task runs as: a bearer token for an
# agent that can download to a CPU.
$acl = Get-Acl $TokenFile
$acl.SetAccessRuleProtection($true, $false)
$acl.SetAccessRule((New-Object Security.AccessControl.FileSystemAccessRule('BUILTIN\Administrators', 'FullControl', 'Allow')))
$acl.SetAccessRule((New-Object Security.AccessControl.FileSystemAccessRule($user, 'Read', 'Allow')))
Set-Acl $TokenFile $acl
Ok "token at $TokenFile (administrators and $user)"

# --------------------------------------------------------------- 5. runner
Write-Host "`n5. Scheduled task"
New-Item -ItemType Directory -Force -Path $WorkDir | Out-Null
$bat = Join-Path $InstallDir 'run-tiad.bat'
$noTiaLine = if ($NoTia) { 'set TIAD_NO_TIA=1' } else { 'set TIAD_NO_TIA=' }
@"
@echo off
rem Written by install-tiad.ps1. The token is in TIAD_TOKEN_FILE, never here.
set TIAD_ADDR=http://${Listen}:${Port}
set TIAD_TOKEN=
set TIAD_TOKEN_FILE=$TokenFile
set TIAD_WORKDIR=$WorkDir
set TIAD_TARGETS=$Targets
$noTiaLine
set TIAD_EXIT_ON_STDIN=
cd /d "$InstallDir"
"$exe" >> "$CfgDir\$TaskName.out.log" 2>> "$CfgDir\$TaskName.err.log"
"@ | Set-Content -Path $bat -Encoding ascii

if (Get-ScheduledTask -TaskName $TaskName -EA SilentlyContinue) {
    Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
}
# Highest: HttpListener binds without a urlacl for an administrator.
$action    = New-ScheduledTaskAction  -Execute 'cmd.exe' -Argument "/c `"$bat`""
$trigger   = New-ScheduledTaskTrigger -AtLogOn -User $user
$principal = New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel Highest
$settings  = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -ExecutionTimeLimit ([TimeSpan]::Zero)
Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger -Principal $principal -Settings $settings `
    -Description 'nautilus tiad: TIA Portal Openness agent' | Out-Null
$mode = if ($NoTia) { 'TIA starts disabled (-NoTia)' } else { 'TIA may start' }
Ok "task '$TaskName' registered (interactive, at logon, as $user; $mode)"

# ------------------------------------------------------------- 6. firewall
Write-Host "`n6. Firewall"
if ($Listen -eq '127.0.0.1') {
    Say "loopback only: no firewall rule"
} else {
    if (Get-NetFirewallRule -DisplayName "tiad ($Port)" -EA SilentlyContinue) { Remove-NetFirewallRule -DisplayName "tiad ($Port)" }
    New-NetFirewallRule -DisplayName "tiad ($Port)" -Direction Inbound -Action Allow -Protocol TCP -LocalPort $Port -Profile Private,Domain | Out-Null
    Warn "opened TCP $Port (Private, Domain). This agent can DOWNLOAD to a CPU: keep it off untrusted networks"
}

# --------------------------------------------------------------- 7. verify
Write-Host "`n7. Verify (health and probe only: neither starts TIA)"
Start-ScheduledTask -TaskName $TaskName
$hdr = @{ Authorization = "Bearer $((Get-Content $TokenFile -Raw).Trim())" }
$up = $false
foreach ($i in 1..20) {
    Start-Sleep -Seconds 1
    try { $h = Invoke-RestMethod "http://127.0.0.1:$Port/v1/health" -Headers $hdr -TimeoutSec 5; $up = $true; break } catch { }
}
if (-not $up) {
    Warn "tiad did not answer on port $Port. Is someone logged in at the console? An interactive task starts only then."
    Say  "logs: $CfgDir\$TaskName.out.log and $CfgDir\$TaskName.err.log"
    exit 1
}
Ok "tiad $($h.data.version) answering on port $Port (TIA $($h.data.tia), Openness $($h.data.openness))"
$probe = Invoke-RestMethod "http://127.0.0.1:$Port/v1/probe" -Headers $hdr -TimeoutSec 30
foreach ($g in $probe.data.gates) {
    $line = "$($g.name) [$($g.state)]: $($g.detail)"
    if ($g.state -eq 'ok') { Ok $line } else { Warn $line; if ($g.remedy) { Say "         $($g.remedy)" } }
}
if ($probe.data.usable) { Ok "usable" } else { Warn "not usable yet: see the gates above" }

Write-Host ""
Write-Host "Done. Point naut at it:" -ForegroundColor Green
Write-Host "    NAUTILUS_TIAD_URL=http://<this machine>:$Port"
Write-Host "    NAUTILUS_TIAD_TOKEN_FILE=<a copy of $TokenFile>"
Write-Host ""
