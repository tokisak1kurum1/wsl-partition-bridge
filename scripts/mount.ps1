[CmdletBinding()]
param(
    [string]$ConfigPath
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Assert-Administrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw 'Run this script from an elevated PowerShell (Run as administrator).'
    }
}

function Invoke-Wsl {
    param(
        [Parameter(Mandatory)] [string]$Distro,
        [Parameter(Mandatory)] [string[]]$Arguments,
        [switch]$AllowFailure
    )

    & wsl.exe -d $Distro -u root -- @Arguments
    $code = $LASTEXITCODE
    if (-not $AllowFailure -and $code -ne 0) {
        throw "WSL command failed with exit code $code`: $($Arguments -join ' ')"
    }
    return $code
}

function Test-TcpPort {
    param([string]$HostName, [int]$Port, [int]$TimeoutMs = 150)

    $client = [Net.Sockets.TcpClient]::new()
    try {
        $result = $client.BeginConnect($HostName, $Port, $null, $null)
        if (-not $result.AsyncWaitHandle.WaitOne($TimeoutMs)) {
            return $false
        }
        $client.EndConnect($result)
        return $true
    }
    catch {
        return $false
    }
    finally {
        $client.Dispose()
    }
}

Assert-Administrator

$repoRoot = Split-Path -Parent $PSScriptRoot
if (-not $ConfigPath) {
    $ConfigPath = Join-Path $repoRoot 'bridge.config.psd1'
}
if (-not (Test-Path -LiteralPath $ConfigPath)) {
    throw "Config not found: $ConfigPath`nCopy bridge.config.psd1.example to bridge.config.psd1 and edit it once."
}

$config = Import-PowerShellDataFile -Path $ConfigPath
foreach ($name in 'Disk','Partition','Distro','FileSystem','MountPoint','Device','Port') {
    if (-not $config.ContainsKey($name)) {
        throw "Missing '$name' in $ConfigPath"
    }
}

$disk       = [int]$config.Disk
$partition  = [int]$config.Partition
$distro     = [string]$config.Distro
$fileSystem = [string]$config.FileSystem
$mountPoint = [string]$config.MountPoint
$device     = [string]$config.Device
$port       = [int]$config.Port
$openExplorer = if ($config.ContainsKey('OpenExplorer')) { [bool]$config.OpenExplorer } else { $true }

if ($distro -notmatch '^[A-Za-z0-9._-]+$') { throw "Unsafe distro name: $distro" }
if ($fileSystem -notmatch '^[A-Za-z0-9._-]+$') { throw "Unsafe filesystem name: $fileSystem" }
if ($mountPoint -notmatch '^/[A-Za-z0-9._/-]+$') { throw "Unsafe mount point: $mountPoint" }
if ($device -notmatch '^/dev/[A-Za-z0-9._/-]+$') { throw "Unsafe block device: $device" }
if ($port -lt 1 -or $port -gt 65535) { throw "Invalid port: $port" }

$exe = Join-Path $repoRoot 'wsl-partition-bridge.exe'
if (-not (Test-Path -LiteralPath $exe)) {
    throw "Executable not found: $exe`nBuild it with 'go build -o wsl-partition-bridge.exe .' or place the release exe in the repository root."
}

$stateDir = Join-Path $env:LOCALAPPDATA 'wsl-partition-bridge'
$statePath = Join-Path $stateDir 'state.json'
New-Item -ItemType Directory -Force -Path $stateDir | Out-Null

if (Test-Path -LiteralPath $statePath) {
    throw "A bridge state file already exists: $statePath`nRun scripts\unmount.ps1 first, or remove the stale state file after verifying nothing is mounted."
}

Write-Host "[1/5] Checking WSL distribution and NBD tools..."
Invoke-Wsl -Distro $distro -Arguments @('modprobe','nbd') | Out-Null
Invoke-Wsl -Distro $distro -Arguments @('nbd-client','--version') | Out-Null

$routeOutput = (& wsl.exe -d $distro -- ip route show default 2>&1) -join "`n"
if ($LASTEXITCODE -ne 0) {
    throw "Could not query the WSL default route:`n$routeOutput"
}
$match = [regex]::Match($routeOutput, 'default\s+via\s+([0-9.]+)')
if (-not $match.Success) {
    throw "Could not determine the Windows host IP from WSL route:`n$routeOutput"
}
$hostIp = $match.Groups[1].Value
Write-Host "      Windows host visible from WSL: $hostIp"

Write-Host "[2/5] Starting read-only partition bridge..."
$stdoutLog = Join-Path $stateDir 'bridge.stdout.log'
$stderrLog = Join-Path $stateDir 'bridge.stderr.log'
Remove-Item -Force -ErrorAction SilentlyContinue $stdoutLog, $stderrLog

$bridgeArgs = @(
    '--disk', [string]$disk,
    '--partition', [string]$partition,
    '--listen', "$hostIp`:$port"
)
$process = Start-Process -FilePath $exe -ArgumentList $bridgeArgs -PassThru -WindowStyle Hidden `
    -RedirectStandardOutput $stdoutLog -RedirectStandardError $stderrLog

$connected = $false
try {
    $ready = $false
    for ($i = 0; $i -lt 30; $i++) {
        Start-Sleep -Milliseconds 100
        if ($process.HasExited) {
            $stderr = if (Test-Path $stderrLog) { Get-Content $stderrLog -Raw } else { '' }
            throw "Bridge exited early with code $($process.ExitCode).`n$stderr"
        }
        if (Test-TcpPort -HostName $hostIp -Port $port) {
            $ready = $true
            break
        }
    }
    if (-not $ready) {
        throw "Bridge did not begin listening on $hostIp`:$port"
    }

    Write-Host "[3/5] Connecting $device over read-only NBD..."
    Invoke-Wsl -Distro $distro -Arguments @('nbd-client','-readonly','-N','default',$hostIp,[string]$port,$device) | Out-Null
    $connected = $true

    Write-Host "[4/5] Mounting $device read-only at $mountPoint..."
    Invoke-Wsl -Distro $distro -Arguments @('mkdir','-p',$mountPoint) | Out-Null
    Invoke-Wsl -Distro $distro -Arguments @('mount','-t',$fileSystem,'-o','ro',$device,$mountPoint) | Out-Null

    $state = [ordered]@{
        BridgePid  = $process.Id
        Disk       = $disk
        Partition  = $partition
        Distro     = $distro
        FileSystem = $fileSystem
        MountPoint = $mountPoint
        Device     = $device
        HostIp     = $hostIp
        Port       = $port
        StdoutLog  = $stdoutLog
        StderrLog  = $stderrLog
    }
    $state | ConvertTo-Json | Set-Content -LiteralPath $statePath -Encoding UTF8

    Write-Host "[5/5] Mounted successfully." -ForegroundColor Green
    $uncSuffix = $mountPoint.TrimStart('/').Replace('/', '\')
    $uncPath = "\\wsl$\$distro\$uncSuffix"
    Write-Host "      $uncPath"
    Write-Host "      Stop safely with: .\scripts\unmount.ps1"

    if ($openExplorer) {
        Start-Process explorer.exe -ArgumentList $uncPath
    }
}
catch {
    if ($connected) {
        & wsl.exe -d $distro -u root -- nbd-client -d $device | Out-Null
    }
    if (-not $process.HasExited) {
        Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
    }
    throw
}
