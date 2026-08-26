# Quick start

This is the recommended workflow after the first setup.

## 1. Put the executable in the repository root

Build it yourself:

```powershell
go build -trimpath -ldflags="-s -w" -o wsl-partition-bridge.exe .
```

or place a downloaded release executable next to `README.md`.

## 2. Prepare Debian once

Inside Debian/WSL:

```bash
sudo apt install --no-install-recommends nbd-client btrfs-progs
sudo modprobe nbd
```

## 3. Create your local config once

In PowerShell:

```powershell
Copy-Item .\bridge.config.psd1.example .\bridge.config.psd1
```

Edit `bridge.config.psd1` for your machine. Example:

```powershell
@{
    Disk         = 0
    Partition    = 2
    Distro       = 'Debian'
    FileSystem   = 'btrfs'
    MountPoint   = '/mnt/btrfs'
    Device       = '/dev/nbd0'
    Port         = 10809
    OpenExplorer = $true
}
```

The local config is ignored by Git.

## 4. Mount

Run from an elevated PowerShell:

```powershell
.\scripts\mount.ps1
```

The helper detects the WSL host address, starts the bridge in the background, connects the NBD device read-only, mounts it read-only, and opens the WSL path in Explorer.

## 5. Unmount safely

Run from an elevated PowerShell:

```powershell
.\scripts\unmount.ps1
```

The helper unmounts the filesystem first, disconnects NBD second, and then stops the Windows bridge process.
