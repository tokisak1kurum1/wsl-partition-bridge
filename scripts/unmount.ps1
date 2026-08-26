[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Assert-Administrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw 'Run this script from an elevated PowerShell (Run as administrator).'
    }
}

Assert-Administrator

$stateDir = Join-Path $env:LOCALAPPDATA 'wsl-partition-bridge'
$statePath = Join-Path $stateDir 'state.json'
if (-not (Test-Path -LiteralPath $statePath)) {
    throw "No active bridge state found at $statePath"
}

$state = Get-Content -LiteralPath $statePath -Raw | ConvertFrom-Json
$distro = [string]$state.Distro
$mountPoint = [string]$state.MountPoint
$device = [string]$state.Device
$bridgePid = [int]$state.BridgePid

Write-Host "[1/3] Unmounting $mountPoint..."
& wsl.exe -d $distro -u root -- findmnt -rn -M $mountPoint *> $null
if ($LASTEXITCODE -eq 0) {
    & wsl.exe -d $distro -u root -- umount $mountPoint
    if ($LASTEXITCODE -ne 0) {
        throw "Unmount failed. The NBD device and bridge were left running. Close files using $mountPoint and try again."
    }
}
else {
    Write-Host '      Mount point is already unmounted.'
}

Write-Host "[2/3] Disconnecting $device..."
& wsl.exe -d $distro -u root -- nbd-client -d $device
if ($LASTEXITCODE -ne 0) {
    Write-Warning "nbd-client could not disconnect $device; continuing because the filesystem is already unmounted."
}

Write-Host '[3/3] Stopping Windows bridge...'
$process = Get-Process -Id $bridgePid -ErrorAction SilentlyContinue
if ($process) {
    Stop-Process -Id $bridgePid -Force
}
else {
    Write-Host '      Bridge process is already stopped.'
}

Remove-Item -LiteralPath $statePath -Force
Write-Host 'Disconnected safely.' -ForegroundColor Green
