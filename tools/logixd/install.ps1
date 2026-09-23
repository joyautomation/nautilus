<#
.SYNOPSIS
    Install logixd -- the nautilus agent for the Studio 5000 Logix Designer SDK.

.DESCRIPTION
    Does the whole setup: preflight, build, token, scheduled task, firewall
    rule, and a probe to prove it works. Idempotent -- safe to re-run.

    Run it in an elevated PowerShell ON the licensed Windows machine.

        .\install.ps1
        .\install.ps1 -Port 8188 -Listen 0.0.0.0
        .\install.ps1 -Uninstall

.NOTES
    Two constraints are not negotiable, both learned expensively:

    1. DOTNET_ROOT must NOT point at an x64 .NET install. FactoryTalk
       authentication runs through FtspAdapterLDSDK.exe, which is a 32-BIT
       apphost; with DOTNET_ROOT set to x64 it cannot resolve a runtime, dies
       before connecting its pipe, and every SDK call fails with a bare
       TimeoutException that names nothing. Use DOTNET_ROOT_X64 instead.
       This script refuses to install into that trap.

    2. The agent needs an INTERACTIVE console session. FactoryTalk auth does
       not work from session 0, so the task is registered Interactive and
       starts at logon -- not as a service. After a reboot, someone must log
       in at the console before logixd can run. That is a property of the
       SDK, not of this script.
#>
[CmdletBinding()]
param(
    [int]    $Port       = 8188,
    [string] $Listen     = '127.0.0.1',
    [string] $InstallDir = 'C:\Program Files\logixd',
    [string] $WorkDir    = 'C:\logixd-work',
    [string] $DotnetRoot = '',
    [string] $Token      = '',
    [int]    $IdleMinutes = 30,
    # Overridable so a second install (a test, or a second SDK revision on
    # one box) does not unregister the one already running.
    [string] $TaskName    = 'logixd',
    [switch] $Uninstall
)

$ErrorActionPreference = 'Stop'

function Say  ($m) { Write-Host "  $m" }
function Ok   ($m) { Write-Host "  [ok]   $m"   -ForegroundColor Green }
function Warn ($m) { Write-Host "  [warn] $m"   -ForegroundColor Yellow }
function Die  ($m) { Write-Host "  [fail] $m"   -ForegroundColor Red; exit 1 }

# Stop a running agent and wait for its port. Re-running the installer is an
# UPGRADE, and without this the old process keeps the port, the new build
# never starts, and the verify step blames the console session.
function Stop-Logixd ($task, $dir, $port) {
    if (Get-ScheduledTask -TaskName $task -EA SilentlyContinue) {
        Stop-ScheduledTask -TaskName $task -EA SilentlyContinue
    }
    Get-CimInstance Win32_Process -Filter "Name='dotnet.exe'" -EA SilentlyContinue |
        Where-Object { $_.CommandLine -and $_.CommandLine -like "*$dir*" } |
        ForEach-Object { Stop-Process -Id $_.ProcessId -Force -EA SilentlyContinue }
    foreach ($i in 1..15) {
        if (-not (Get-NetTCPConnection -State Listen -LocalPort $port -EA SilentlyContinue)) { return $true }
        Start-Sleep -Milliseconds 400
    }
    return $false
}

function Assert-Admin {
    $id = [Security.Principal.WindowsIdentity]::GetCurrent()
    if (-not (New-Object Security.Principal.WindowsPrincipal $id).IsInRole(
              [Security.Principal.WindowsBuiltInRole]::Administrator)) {
        Die "Run this in an ELEVATED PowerShell -- it registers a scheduled task and a firewall rule."
    }
}

# ---------------------------------------------------------------- uninstall
if ($Uninstall) {
    Write-Host "`nRemoving logixd`n"
    Assert-Admin
    if (Get-ScheduledTask -TaskName $TaskName -EA SilentlyContinue) {
        Stop-ScheduledTask     -TaskName $TaskName -EA SilentlyContinue
        Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
        Ok "scheduled task removed"
    } else { Say "no scheduled task" }
    [void](Stop-Logixd $TaskName $InstallDir $Port)
    if (Get-NetFirewallRule -DisplayName "logixd ($Port)" -EA SilentlyContinue) {
        Remove-NetFirewallRule -DisplayName "logixd ($Port)"
        Ok "firewall rule removed"
    }
    if (Test-Path $InstallDir) { Remove-Item $InstallDir -Recurse -Force; Ok "$InstallDir removed" }
    # The token is per-install and grants code download to a controller.
    # Leaving it behind after an uninstall is a live credential for a
    # service that is gone.
    $tok = Join-Path (Join-Path $env:ProgramData 'logixd') "$TaskName.token"
    if (Test-Path $tok) { Remove-Item $tok -Force; Ok "token removed" }
    Say "left alone: $WorkDir (projects) and the Rockwell SDK"
    Write-Host ""
    exit 0
}

Write-Host "`nInstalling logixd`n"
Assert-Admin

# ------------------------------------------------------------ 1. preflight
Write-Host "1. Preflight"

# The DOTNET_ROOT trap. Machine and user scope both matter: the task inherits
# them, and a plain DOTNET_ROOT is what breaks FactoryTalk auth.
foreach ($scope in 'Machine','User') {
    $v = [Environment]::GetEnvironmentVariable('DOTNET_ROOT', $scope)
    if ($v) {
        Write-Host ""
        Write-Host "  DOTNET_ROOT is set at $scope scope:" -ForegroundColor Red
        Write-Host "      $v" -ForegroundColor Red
        Write-Host ""
        Write-Host "  If that is an x64 .NET, FactoryTalk authentication will fail with a"
        Write-Host "  TimeoutException that explains nothing. FtspAdapterLDSDK.exe is a"
        Write-Host "  32-bit apphost and cannot use an x64 runtime."
        Write-Host ""
        Write-Host "  Fix it, then re-run:" -ForegroundColor Yellow
        Write-Host "      [Environment]::SetEnvironmentVariable('DOTNET_ROOT', `$null, '$scope')"
        Write-Host "      [Environment]::SetEnvironmentVariable('DOTNET_ROOT_X64', '$v', '$scope')"
        Die "refusing to install into a configuration that is known to fail"
    }
}
Ok "DOTNET_ROOT is not set"

# .NET 10 runtime, x64.
if (-not $DotnetRoot) {
    $DotnetRoot = [Environment]::GetEnvironmentVariable('DOTNET_ROOT_X64','Machine')
    if (-not $DotnetRoot) {
        $c = @('C:\dotnet10', "$env:ProgramFiles\dotnet") |
             Where-Object { Test-Path (Join-Path $_ 'dotnet.exe') } | Select-Object -First 1
        if (-not $c) { Die "no .NET found. Install the .NET 10 x64 SDK, or pass -DotnetRoot." }
        $DotnetRoot = $c
    }
}
$dotnet = Join-Path $DotnetRoot 'dotnet.exe'
if (-not (Test-Path $dotnet)) { Die "no dotnet.exe under $DotnetRoot" }
$sdks = & $dotnet --list-sdks 2>$null
if (-not ($sdks -match '^10\.')) { Die "the .NET 10 SDK is required to build logixd (found: $($sdks -join ', '))" }
Ok ".NET 10 SDK at $DotnetRoot"

# The Logix Designer SDK's NuGet package. Not on nuget.org -- the installer
# drops it beside its examples. We never redistribute it.
$pkgDir = $env:LOGIXD_SDK_PACKAGE_DIR
if (-not $pkgDir) { $pkgDir = 'C:\Users\Public\Documents\Studio 5000\Logix Designer SDK\dotnet' }
if (-not (Test-Path $pkgDir)) {
    Die @"
the Logix Designer SDK client package was not found at
           $pkgDir
         Install the Studio 5000 Logix Designer SDK first, or set
         LOGIXD_SDK_PACKAGE_DIR to the directory holding its .nupkg.
"@
}
Ok "SDK client package source at $pkgDir"

if (-not (Get-Service 'LdSdkService' -EA SilentlyContinue)) {
    Warn "the LdSdkService service was not found -- the SDK may not be installed"
} else { Ok "LdSdkService present" }

# ---------------------------------------------------------------- 2. build
Write-Host "`n2. Build"
$src = $PSScriptRoot
if (-not (Test-Path (Join-Path $src 'logixd.csproj'))) { Die "run this from tools/logixd (no logixd.csproj beside it)" }

if (-not (Stop-Logixd $TaskName $InstallDir $Port)) {
    Warn "port $Port is still held after stopping the old agent; continuing anyway"
}
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
# Framework-dependent ON PURPOSE. A self-contained publish fills the output
# directory with an x64 runtime, and the 32-bit FactoryTalk adapter resolves
# hostfxr from that directory -- so self-contained ships the very trap the
# preflight above refuses. See README, "Packaging: what does NOT work".
$env:DOTNET_ROOT_X64 = $DotnetRoot
& $dotnet publish $src -c Release -o $InstallDir --nologo -v quiet
if ($LASTEXITCODE -ne 0) { Die "build failed" }
Ok "published (framework-dependent) to $InstallDir"

# ---------------------------------------------------------------- 3. token
Write-Host "`n3. Token"
$cfgDir   = Join-Path $env:ProgramData 'logixd'
# Per-install, not shared. Two agents on one box (a second SDK revision,
# or a test alongside the real one) must not share a bearer token that can
# download code to a controller.
$tokenOut = Join-Path $cfgDir "$TaskName.token"
New-Item -ItemType Directory -Force -Path $cfgDir | Out-Null
if (-not $Token) {
    if (Test-Path $tokenOut) {
        $Token = (Get-Content $tokenOut -Raw).Trim()
        Ok "reusing the existing token"
    } else {
        $b = New-Object byte[] 24
        [Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($b)
        $Token = ($b | ForEach-Object { $_.ToString('x2') }) -join ''
        Ok "generated a new token"
    }
}
Set-Content -Path $tokenOut -Value $Token -NoNewline -Encoding ascii
# Readable by administrators only -- it is a bearer token for a service that
# can download code to a controller.
$acl = Get-Acl $tokenOut
$acl.SetAccessRuleProtection($true, $false)
$acl.SetAccessRule((New-Object Security.AccessControl.FileSystemAccessRule(
    'BUILTIN\Administrators','FullControl','Allow')))
Set-Acl $tokenOut $acl
Ok "token at $tokenOut (administrators only)"

# --------------------------------------------------------------- 4. runner
Write-Host "`n4. Scheduled task"
$bat = Join-Path $InstallDir 'run-logixd.bat'
@"
@echo off
rem Written by install.ps1. DOTNET_ROOT is cleared deliberately: a plain
rem DOTNET_ROOT pointing at x64 breaks FactoryTalk auth through the 32-bit
rem FtspAdapterLDSDK.exe. DOTNET_ROOT_X64 is the supported spelling.
set DOTNET_ROOT=
set DOTNET_ROOT_X64=$DotnetRoot
set LOGIXD_ADDR=http://${Listen}:${Port}
set LOGIXD_TOKEN=$Token
set LOGIXD_WORKDIR=$WorkDir
set LOGIXD_IDLE_MINUTES=$IdleMinutes
cd /d "$InstallDir"
"$dotnet" "$InstallDir\logixd.dll" >> "$InstallDir\out.log" 2>> "$InstallDir\err.log"
"@ | Set-Content -Path $bat -Encoding ascii
New-Item -ItemType Directory -Force -Path $WorkDir | Out-Null

if (Get-ScheduledTask -TaskName $TaskName -EA SilentlyContinue) {
    Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
}
# Interactive, NOT a service: FactoryTalk authentication does not work from
# session 0. The cost is that someone must be logged in at the console.
$action   = New-ScheduledTaskAction  -Execute 'cmd.exe' -Argument "/c `"$bat`""
$trigger  = New-ScheduledTaskTrigger -AtLogOn
# NOT "$env:USERDOMAIN\$env:USERNAME": on a workgroup machine
# USERDOMAIN is "WORKGROUP", which does not resolve to a SID. The
# identity object gives the machine-qualified name that does.
$user     = [Security.Principal.WindowsIdentity]::GetCurrent().Name
$principal= New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel Highest
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries `
              -DontStopIfGoingOnBatteries -ExecutionTimeLimit ([TimeSpan]::Zero)
Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger `
    -Principal $principal -Settings $settings `
    -Description 'nautilus logixd -- Studio 5000 SDK agent' | Out-Null
Ok "task '$TaskName' registered (interactive, at logon, as $user)"

# ------------------------------------------------------------- 5. firewall
Write-Host "`n5. Firewall"
if ($Listen -eq '127.0.0.1') {
    Say "listening on loopback only -- no firewall rule needed"
} else {
    if (Get-NetFirewallRule -DisplayName "logixd ($Port)" -EA SilentlyContinue) {
        Remove-NetFirewallRule -DisplayName "logixd ($Port)"
    }
    New-NetFirewallRule -DisplayName "logixd ($Port)" -Direction Inbound `
        -Action Allow -Protocol TCP -LocalPort $Port -Profile Private,Domain | Out-Null
    Warn "opened TCP $Port on Private and Domain profiles -- this agent can DOWNLOAD CODE"
    Warn "to a controller. Keep it off untrusted networks."
}

# ---------------------------------------------------------------- 6. verify
Write-Host "`n6. Verify"
Start-ScheduledTask -TaskName $TaskName
$health = "http://127.0.0.1:$Port/v1/health"
$up = $false
foreach ($i in 1..20) {
    Start-Sleep -Seconds 2
    try { Invoke-RestMethod $health -Headers @{ Authorization = "Bearer $Token" } -TimeoutSec 5 | Out-Null
          $up = $true; break } catch { }
}
if (-not $up) {
    Write-Host ""
    Warn "logixd did not answer on $health"
    Warn "Logged on at the CONSOLE? An interactive task will not start otherwise."
    Say  "logs: $InstallDir\out.log and $InstallDir\err.log"
    exit 1
}
Ok "logixd is answering on port $Port"

try {
    $probe = Invoke-RestMethod "http://127.0.0.1:$Port/v1/probe" `
                -Headers @{ Authorization = "Bearer $Token" } -TimeoutSec 120
    foreach ($g in $probe.data.gates) {
        if ($g.ok) { Ok "$($g.name) -- $($g.detail)" } else { Warn "$($g.name) -- $($g.detail)" }
    }
    if ($probe.data.usable) { Ok "the SDK is usable" }
    else { Warn "the SDK is NOT usable yet -- see the failing gate above"; Say $probe.data.hint }
} catch { Warn "probe failed: $_" }

Write-Host ""
Write-Host "Done. Point nautilus at it:" -ForegroundColor Green
Write-Host "    NAUTILUS_LOGIXD_URL=http://${Listen}:${Port}"
Write-Host "    NAUTILUS_LOGIXD_TOKEN=<contents of $tokenOut>"
Write-Host ""
Write-Host "    nautilus logix probe"
Write-Host ""
