# Quick start

This repository builds the read-only Windows-to-WSL partition bridge. The recommended local layout is:

```text
D:\Tools\bin\wsl-partition-bridge.exe
D:\Tools\bin\mount-btrfs.ps1
D:\Tools\bin\unmount-btrfs.ps1
```

Add `D:\Tools\bin` to your user `PATH`.

## Prepare Debian once

```bash
sudo apt install --no-install-recommends nbd-client btrfs-progs
sudo modprobe nbd
```

## Mount

```powershell
mount-btrfs
```

The helper auto-elevates, detects the current Windows/WSL NAT addresses, creates a temporary inbound Windows Firewall rule restricted to the current WSL source IP plus the bridge executable and NBD port, starts the bridge, waits until Windows confirms the listener is active, connects `/dev/nbd0` read-only, mounts Btrfs `@home` with `ro,subvol=@home`, and opens:

```text
\\wsl$\Debian\mnt\btrfs\weijie
```

The elevated window closes automatically after success and stays open on failure.

## Unmount safely

```powershell
unmount-btrfs
```

The helper unmounts Btrfs, disconnects NBD, verifies the NBD attachment is gone, stops the Windows bridge, and removes the temporary firewall rule. The elevated window closes automatically after success.
