[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$Disk             = 0
$Partition        = 2
$Distro           = 'Debian'
$Device           = '/dev/nbd0'
$MountPoint       = '/mnt/btrfs'
$Subvolume        = '@home'
$ExplorerUser     = 'weijie'
$Port             = 10809
$BridgeExe        = 'D:\Tools\bin\wsl-partition-bridge.exe'
$FirewallRuleName = 'wsl-partition-bridge-wsl-nbd'

function Test-Administrator {
    $id = [Security.Principal.WindowsIdentity]::GetCurrent()
    $p = [Security.Principal.WindowsPrincipal]::new($id)
    $p.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Test-Mounted {
    & wsl.exe -d $Distro -u root -- findmnt -rn -M $MountPoint -S $Device *> $null
    $LASTEXITCODE -eq 0
}

function Fail-And-Wait([string]$Message) {
    Write-Host ''
    Write-Host $Message -ForegroundColor Red
    Write-Host ''
    Read-Host 'Press Enter to close' | Out-Null
    exit 1
}

function Remove-BridgeFirewallRule {
    Get-NetFirewallRule -Name $FirewallRuleName -ErrorAction SilentlyContinue |
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

if (-not (Test-Path -LiteralPath $BridgeExe)) {
    Fail-And-Wait "Bridge not found: $BridgeExe"
}

$stateDir  = Join-Path $env:LOCALAPPDATA 'wsl-partition-bridge'
$statePath = Join-Path $stateDir 'state.json'
$stdoutLog = Join-Path $stateDir 'bridge.stdout.log'
$stderrLog = Join-Path $stateDir 'bridge.stderr.log'
New-Item -ItemType Directory -Force -Path $stateDir | Out-Null

if (Test-Mounted) {
    $unc = "\\wsl$\$Distro\" + $MountPoint.TrimStart('/').Replace('/', '\') + "\$ExplorerUser"
    Start-Process explorer.exe -ArgumentList $unc
    exit 0
}

if (Test-Path -LiteralPath $statePath) {
    try {
        $old = Get-Content -LiteralPath $statePath -Raw | ConvertFrom-Json
        if (Get-Process -Id ([int]$old.BridgePid) -ErrorAction SilentlyContinue) {
            Fail-And-Wait 'An existing bridge is still running. Run unmount-btrfs first.'
        }
    } catch {}
    Remove-Item -LiteralPath $statePath -Force -ErrorAction SilentlyContinue
}

$bridge = $null
$connected = $false
$mounted = $false
$firewallCreated = $false

try {
    Write-Host '[1/5] Preparing WSL/NBD...'
    & wsl.exe -d $Distro -u root -- modprobe nbd
    if ($LASTEXITCODE -ne 0) { throw 'modprobe nbd failed.' }

    $nbdPid = (& wsl.exe -d $Distro -u root -- sh -lc "test -s /sys/block/nbd0/pid && cat /sys/block/nbd0/pid || true" 2>$null) -join ''
    if ($nbdPid.Trim()) {
        & wsl.exe -d $Distro -u root -- nbd-client -d $Device *> $null
        Start-Sleep -Milliseconds 300
    }

    $route = (& wsl.exe -d $Distro -- ip route show default 2>&1) -join "`n"
    $m = [regex]::Match($route, 'default\s+via\s+([0-9.]+)')
    if (-not $m.Success) { throw "Could not determine Windows host IP from WSL.`n$route" }
    $hostIp = $m.Groups[1].Value

    $routeToHost = (& wsl.exe -d $Distro -- ip -4 route get $hostIp 2>&1) -join "`n"
    $src = [regex]::Match($routeToHost, '\bsrc\s+([0-9.]+)')
    if (-not $src.Success) { throw "Could not determine WSL source IP.`n$routeToHost" }
    $wslIp = $src.Groups[1].Value

    $existingListener = Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue |
        Select-Object -First 1
    if ($existingListener) {
        throw "TCP port $Port is already in use by PID $($existingListener.OwningProcess)."
    }

    Write-Host "[2/5] Allowing only WSL $wslIp -> $hostIp`:$Port..."
    Remove-BridgeFirewallRule
    New-NetFirewallRule `
        -Name $FirewallRuleName `
        -DisplayName 'wsl-partition-bridge temporary WSL NBD' `
        -Direction Inbound `
        -Action Allow `
        -Enabled True `
        -Profile Any `
        -Program $BridgeExe `
        -Protocol TCP `
        -LocalAddress $hostIp `
        -LocalPort $Port `
        -RemoteAddress $wslIp | Out-Null
    $firewallCreated = $true

    Remove-Item -Force -ErrorAction SilentlyContinue $stdoutLog, $stderrLog

    Write-Host "[3/5] Starting read-only bridge at $hostIp`:$Port..."
    $bridge = Start-Process -FilePath $BridgeExe -ArgumentList @(
        '--disk', [string]$Disk,
        '--partition', [string]$Partition,
        '--listen', "$hostIp`:$Port"
    ) -PassThru -WindowStyle Hidden -RedirectStandardOutput $stdoutLog -RedirectStandardError $stderrLog

    $listening = $false
    for ($i = 1; $i -le 100; $i++) {
        $bridge.Refresh()
        if ($bridge.HasExited) {
            $err = if (Test-Path $stderrLog) { Get-Content $stderrLog -Raw } else { '' }
            throw "Bridge exited before listening.`n$err"
        }

        $listener = Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue |
            Where-Object { $_.LocalAddress -eq $hostIp -and $_.OwningProcess -eq $bridge.Id } |
            Select-Object -First 1
        if ($listener) {
            $listening = $true
            break
        }
        Start-Sleep -Milliseconds 100
    }

    if (-not $listening) {
        $err = if (Test-Path $stderrLog) { Get-Content $stderrLog -Raw } else { '' }
        throw "Bridge did not listen on $hostIp`:$Port within 10 seconds.`n$err"
    }

    Write-Host "[4/5] Connecting $Device read-only..."
    & wsl.exe -d $Distro -u root -- nbd-client -readonly -N default $hostIp $Port $Device
    if ($LASTEXITCODE -ne 0) { throw "nbd-client failed with exit code $LASTEXITCODE" }
    $connected = $true

    & wsl.exe -d $Distro -u root -- mkdir -p $MountPoint
    if ($LASTEXITCODE -ne 0) { throw "Could not create $MountPoint" }

    Write-Host "[5/5] Mounting $Subvolume read-only..."
    $lastError = ''
    for ($i = 1; $i -le 20; $i++) {
        $output = (& wsl.exe -d $Distro -u root -- mount -t btrfs -o "ro,subvol=$Subvolume" $Device $MountPoint 2>&1) -join "`n"
        if ($LASTEXITCODE -eq 0 -and (Test-Mounted)) {
            $mounted = $true
            break
        }
        if ($output) { $lastError = $output }
        Start-Sleep -Milliseconds 500
    }

    if (-not $mounted) { throw "Mount failed after 10 seconds.`n$lastError" }

    [ordered]@{
        BridgePid       = $bridge.Id
        Distro          = $Distro
        Device          = $Device
        MountPoint      = $MountPoint
        Subvolume       = $Subvolume
        HostIp          = $hostIp
        WslIp           = $wslIp
        Port            = $Port
        FirewallRuleName = $FirewallRuleName
    } | ConvertTo-Json | Set-Content -LiteralPath $statePath -Encoding UTF8

    $unc = "\\wsl$\$Distro\" + $MountPoint.TrimStart('/').Replace('/', '\') + "\$ExplorerUser"
    Start-Process explorer.exe -ArgumentList $unc
    exit 0
}
catch {
    $message = $_.Exception.Message

    if ($mounted) {
        & wsl.exe -d $Distro -u root -- umount $MountPoint *> $null
    }
    if ($connected) {
        & wsl.exe -d $Distro -u root -- nbd-client -d $Device *> $null
    }
    if ($bridge -and -not $bridge.HasExited) {
        Stop-Process -Id $bridge.Id -Force -ErrorAction SilentlyContinue
    }
    if ($firewallCreated) {
        Remove-BridgeFirewallRule
    }

    Remove-Item -LiteralPath $statePath -Force -ErrorAction SilentlyContinue
    & wsl.exe -d $Distro -u root -- rmdir $MountPoint 2>$null
    Fail-And-Wait $message
}
