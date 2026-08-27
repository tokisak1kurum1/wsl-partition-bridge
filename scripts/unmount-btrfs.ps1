[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$Distro             = 'Debian'
$Device             = '/dev/nbd0'
$MountPoint         = '/mnt/btrfs'
$FirewallRuleName   = 'wsl-partition-bridge-wsl-nbd'

function Test-Administrator {
    $id = [Security.Principal.WindowsIdentity]::GetCurrent()
    $p = [Security.Principal.WindowsPrincipal]::new($id)
    $p.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Fail-And-Wait([string]$Message) {
    Write-Host ''
    Write-Host $Message -ForegroundColor Red
    Write-Host ''
    Read-Host 'Press Enter to close' | Out-Null
    exit 1
}

function Remove-BridgeFirewallRule([string]$Name) {
    Get-NetFirewallRule -Name $Name -ErrorAction SilentlyContinue |
        Remove-NetFirewallRule -ErrorAction SilentlyContinue
}

if (-not (Test-Administrator)) {
    $pwsh = Get-Command pwsh.exe -ErrorAction SilentlyContinue
    $hostExe = if ($pwsh) { $pwsh.Source } else { 'powershell.exe' }
    Start-Process -FilePath $hostExe -Verb RunAs -ArgumentList @(
        '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', "`"$PSCommandPath`""
    )
    exit
}

$statePath = Join-Path $env:LOCALAPPDATA 'wsl-partition-bridge\state.json'
if (-not (Test-Path -LiteralPath $statePath)) {
    Remove-BridgeFirewallRule $FirewallRuleName
    Write-Host 'Nothing is mounted.' -ForegroundColor Yellow
    exit 0
}

try {
    $state = Get-Content -LiteralPath $statePath -Raw | ConvertFrom-Json
    if ($state.Distro)     { $Distro = [string]$state.Distro }
    if ($state.Device)     { $Device = [string]$state.Device }
    if ($state.MountPoint) { $MountPoint = [string]$state.MountPoint }
    if ($state.FirewallRuleName) { $FirewallRuleName = [string]$state.FirewallRuleName }
    $bridgePid = [int]$state.BridgePid

    Write-Host '[1/4] Unmounting Btrfs...'
    & wsl.exe -d $Distro -u root -- findmnt -rn -M $MountPoint *> $null
    if ($LASTEXITCODE -eq 0) {
        & wsl.exe -d $Distro -u root -- umount $MountPoint
        if ($LASTEXITCODE -ne 0) {
            throw "umount failed. Close files using $MountPoint and run unmount-btrfs again."
        }
    }

    Write-Host '[2/4] Disconnecting NBD...'
    $disconnected = $false
    for ($i = 1; $i -le 3; $i++) {
        & wsl.exe -d $Distro -u root -- nbd-client -d $Device *> $null
        Start-Sleep -Milliseconds 200
        $pidText = (& wsl.exe -d $Distro -u root -- sh -lc "test -s /sys/block/nbd0/pid && cat /sys/block/nbd0/pid || true" 2>$null) -join ''
        if (-not $pidText.Trim()) {
            $disconnected = $true
            break
        }
    }
    if (-not $disconnected) {
        throw "$Device is still connected; bridge and firewall rule were left in place."
    }

    Write-Host '[3/4] Stopping bridge...'
    $process = Get-Process -Id $bridgePid -ErrorAction SilentlyContinue
    if ($process) {
        Stop-Process -Id $bridgePid -Force -ErrorAction Stop
    }

    Write-Host '[4/4] Removing temporary firewall rule...'
    Remove-BridgeFirewallRule $FirewallRuleName

    Remove-Item -LiteralPath $statePath -Force -ErrorAction SilentlyContinue
    & wsl.exe -d $Distro -u root -- rmdir $MountPoint 2>$null
    exit 0
}
catch {
    Fail-And-Wait $_.Exception.Message
}
