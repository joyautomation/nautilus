<#
.SYNOPSIS
    Check install-tiad.ps1's security properties on a TIA machine.

.DESCRIPTION
    The agent runs elevated, so anything a local user can change under it is
    a privilege escalation. This script installs tiad several times, always
    with -NoTia (TIA never starts), and checks:

      1. a token is reused only as the installer leaves it: locked, owned
         by Administrators, one name (no hard link), 64 hex characters;
      2. the token, its directory, the work dir and the install dir are
         owned by Administrators, with Administrators and SYSTEM the only
         entries and inheritance off;
      3. a junction planted in the work dir makes the install refuse;
      4. -Listen 0.0.0.0, and an address of no interface here, are refused;
      5. an install on this machine's own address verifies on that address
         and opens a firewall rule for it;
      6. a reinstall on loopback, or an uninstall given another port,
         leaves no tiad firewall rule.

    Then it reinstalls on loopback with -NoTia. Run it in an elevated Windows
    PowerShell, from a copy of tools/tiad, only where tiad may be reinstalled.
    It prints no address and no token. The exit code is the number of failed
    checks.

        .\check-install.ps1
        .\check-install.ps1 -Installer C:\elsewhere\install-tiad.ps1
#>
[CmdletBinding()]
param(
    # Default: install-tiad.ps1 beside this script.
    [string] $Installer  = '',
    [string] $TaskName   = 'tiad',
    [int]    $Port       = 8190,
    [string] $InstallDir = 'C:\Program Files\tiad',
    [string] $WorkDir    = 'C:\tiad-work'
)

$ErrorActionPreference = 'Stop'
# $PSScriptRoot is empty in a param default under Windows PowerShell 5.1.
if (-not $Installer) { $Installer = Join-Path $PSScriptRoot 'install-tiad.ps1' }
$script:failed = 0
$SidAdmins = New-Object Security.Principal.SecurityIdentifier 'S-1-5-32-544'
$SidSystem = New-Object Security.Principal.SecurityIdentifier 'S-1-5-18'
$SidUser   = [Security.Principal.WindowsIdentity]::GetCurrent().User
$CfgDir    = Join-Path $env:ProgramData 'tiad'
$TokenFile = Join-Path $CfgDir "$TaskName.token"

function Check ($name, $ok, $why) {
    if ($ok) { Write-Host "  [pass] $name" -ForegroundColor Green }
    else { Write-Host "  [FAIL] ${name}: $why" -ForegroundColor Red; $script:failed++ }
}

# Install runs the installer and returns its exit code; its output is not
# shown, since it names this machine's address.
function Install {
    param([string] $Listen = '127.0.0.1', [int] $P = $Port, [switch] $Uninstall)
    $a = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $Installer, '-TaskName', $TaskName, '-Port', $P,
           '-InstallDir', $InstallDir, '-WorkDir', $WorkDir, '-Listen', $Listen)
    if ($Uninstall) { $a += '-Uninstall' } else { $a += '-NoTia' }
    & powershell.exe @a *> $null
    return $LASTEXITCODE
}

function TiadRules {
    @(Get-NetFirewallRule -EA SilentlyContinue | Where-Object { $_.DisplayName -like 'tiad*' })
}

# LockedAcl is what the installer gives a locked file.
function LockedAcl {
    $acl = New-Object Security.AccessControl.FileSecurity
    $acl.SetOwner($SidAdmins)
    $acl.SetAccessRuleProtection($true, $false)
    foreach ($sid in $SidAdmins, $SidSystem) {
        $acl.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule($sid, 'FullControl', 'Allow')))
    }
    return $acl
}

# Locked: owner Administrators, protected, only Administrators and SYSTEM.
function Locked ($path) {
    if (-not (Test-Path -LiteralPath $path)) { return "missing" }
    $acl = Get-Acl -LiteralPath $path
    if ($acl.GetOwner([Security.Principal.SecurityIdentifier]) -ne $SidAdmins) { return "owner is not Administrators" }
    if (-not $acl.AreAccessRulesProtected) { return "inherits from its parent" }
    foreach ($r in $acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier])) {
        if ($r.IdentityReference -ne $SidAdmins -and $r.IdentityReference -ne $SidSystem) { return "grants $($r.IdentityReference)" }
    }
    return $null
}

if (Get-Process Siemens.Automation.Portal -EA SilentlyContinue) { throw "TIA Portal is running: not touching this machine" }
Write-Host "`nChecking $Installer`n"

# A first install, so the directories exist.
Check "a loopback install succeeds" ((Install) -eq 0) "exit code"

# 1. Tokens. Each case breaks exactly one property of a token the installer
#    wrote (locked, Administrators-owned, one name, 64 hex characters) and
#    keeps the rest, so only that check can replace it. Each is written
#    while the agent is stopped.
function Token { [IO.File]::ReadAllText($TokenFile) }
function Known { -join (1..64 | ForEach-Object { '0123456789abcdef'[(Get-Random -Maximum 16)] }) }
function Plant ($value) {
    Stop-ScheduledTask -TaskName $TaskName -EA SilentlyContinue
    if (Test-Path -LiteralPath $TokenFile) { Remove-Item -LiteralPath $TokenFile -Force }
    [IO.File]::WriteAllText($TokenFile, $value)
    Set-Acl -LiteralPath $TokenFile -AclObject (LockedAcl)
}

$before = Token
$code = Install
Check "a token the installer wrote is reused" ($code -eq 0 -and (Token) -eq $before) "exit $code, or the token changed"

$k = Known; Plant $k
$acl = Get-Acl -LiteralPath $TokenFile; $acl.SetOwner($SidUser); Set-Acl -LiteralPath $TokenFile -AclObject $acl
$code = Install
Check "a token Administrators do not own is replaced" ($code -eq 0 -and (Token) -ne $k) "exit $code, or it was reused"

$k = Known; Plant $k
$acl = Get-Acl -LiteralPath $TokenFile
$acl.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule((New-Object Security.Principal.SecurityIdentifier 'S-1-5-32-545'), 'Read', 'Allow')))
Set-Acl -LiteralPath $TokenFile -AclObject $acl
$code = Install
Check "a token others could read (as 0.1.0 left one) is replaced" ($code -eq 0 -and (Token) -ne $k) "exit $code, or it was reused"

$k = 'planted-' + [guid]::NewGuid().ToString('N'); Plant $k
$code = Install
Check "a token that is not 64 hex characters is replaced" ($code -eq 0 -and (Token) -ne $k) "exit $code, or it was reused"

# A hard link to another locked, Administrators-owned file of 64 hex
# characters: everything but its second name is as the installer leaves it.
$k = Known
$other = Join-Path $env:TEMP ('tiad-other-' + [guid]::NewGuid().ToString('N'))
[IO.File]::WriteAllText($other, $k)
Set-Acl -LiteralPath $other -AclObject (LockedAcl)
Stop-ScheduledTask -TaskName $TaskName -EA SilentlyContinue
Remove-Item -LiteralPath $TokenFile -Force
& cmd.exe /c mklink /H "$TokenFile" "$other" *> $null
$linked = (Test-Path -LiteralPath $TokenFile) -and (Token) -eq $k
$code = Install
Check "a hard-linked token is replaced, and the other file left alone" ($linked -and $code -eq 0 -and (Token) -ne $k -and [IO.File]::ReadAllText($other) -eq $k) "linked=$linked, exit $code, or it was reused"
Remove-Item -LiteralPath $other -Force

# 2. Ownership and ACLs.
foreach ($p in $CfgDir, $TokenFile, $WorkDir, $InstallDir) {
    $why = Locked $p
    Check "$p is locked" ($null -eq $why) $why
}

# 3. A junction in the work dir, which the elevated agent would follow.
$target = Join-Path $env:TEMP ('tiad-junction-' + [guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($target)
$junction = Join-Path $WorkDir 'planted'
& cmd.exe /c mklink /J "$junction" "$target" *> $null
$code = Install
& cmd.exe /c rmdir "$junction" *> $null
Remove-Item -LiteralPath $target -Force
Check "a junction in the work dir is refused" ($code -ne 0) "the install succeeded"

# 4. Listen addresses that are not one address of this machine.
Check "-Listen 0.0.0.0 is refused" ((Install -Listen '0.0.0.0') -ne 0) "the install succeeded"
Check "-Listen of no interface here is refused" ((Install -Listen '192.0.2.1') -ne 0) "the install succeeded"

# 5. This machine's own address: verify must call that address.
$own = (Get-NetIPAddress -AddressFamily IPv4 -EA SilentlyContinue |
        Where-Object { $_.IPAddress -notlike '127.*' -and $_.IPAddress -notlike '169.254.*' } |
        Select-Object -First 1).IPAddress
if ($own) {
    Check "an install on this machine's address verifies on it" ((Install -Listen $own) -eq 0) "exit code"
    Check "it opens one firewall rule" (@(TiadRules).Count -eq 1) "$(@(TiadRules).Count) rules"

    # 6. Back to loopback: nothing stays open.
    Check "a loopback reinstall succeeds" ((Install) -eq 0) "exit code"
    Check "a loopback reinstall leaves no firewall rule" (@(TiadRules).Count -eq 0) "$(@(TiadRules).Count) rules left"

    [void](Install -Listen $own)
    [void](Install -Uninstall -P ($Port + 1))
    Check "an uninstall given another port leaves no firewall rule" (@(TiadRules).Count -eq 0) "$(@(TiadRules).Count) rules left"
} else {
    Check "this machine has an address to test -Listen with" $false "none found"
}

# Leave the standard install, and no rule behind.
foreach ($r in TiadRules) { Remove-NetFirewallRule -Name $r.Name }
Check "the final loopback install succeeds" ((Install) -eq 0) "exit code"
if (Get-Process Siemens.Automation.Portal -EA SilentlyContinue) { Check "TIA never started" $false "TIA Portal is running" }

Write-Host ""
if ($script:failed -eq 0) { Write-Host "all checks passed" -ForegroundColor Green } else { Write-Host "$($script:failed) check(s) failed" -ForegroundColor Red }
exit $script:failed
