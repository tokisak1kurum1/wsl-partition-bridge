# wsl-partition-bridge

A tiny, read-only bridge that exposes **one partition of a Windows physical disk** to WSL as an NBD block device — without taking the whole disk offline.

This is useful when Windows is actively using another partition on the same physical disk. `wsl --mount` attaches the entire physical disk, so Windows normally has to release every partition on that disk first. `wsl-partition-bridge` instead opens the physical disk read-only, restricts access to the selected partition byte range, and serves that range over NBD to WSL.

The bridge does **not** understand Btrfs, ext4, XFS, or any other filesystem. Filesystem parsing stays inside the Linux kernel in WSL.

## Example use case

```text
PhysicalDrive0
├─ Partition 1  NTFS   D:      <- Windows keeps using this
└─ Partition 2  Btrfs  300 GiB <- exposed read-only to WSL
                                      │
                                      ▼
                                 /dev/nbd0
                                      │
                                      ▼
                               Linux Btrfs driver
                                      │
                                      ▼
                                /mnt/btrfs
```

## Safety model

The project is intentionally read-only at several independent layers:

- Opens `\\.\PhysicalDriveN` with `GENERIC_READ` only.
- Exposes only the selected partition's byte range.
- Advertises the NBD export with `NBD_FLAG_READ_ONLY`.
- Rejects `WRITE`, `FLUSH`, `TRIM`, and `WRITE_ZEROES` requests.
- The recommended Linux mount is read-only again (`mount -o ro`).

The Windows process therefore has no write handle to the physical disk.

> [!WARNING]
> This is an experimental low-level storage utility. Back up important data. NBD is unencrypted and unauthenticated, so bind the server only to the WSL-accessible Windows address, not to `0.0.0.0` or a public interface.

## Requirements

### Windows

- Windows 11 with WSL 2
- Administrator PowerShell to open `\\.\PhysicalDriveN`

### WSL / Linux

- Kernel with NBD support (`modprobe nbd`)
- `nbd-client`
- Filesystem userspace tools as needed, e.g. `btrfs-progs`

Tested with:

- WSL 2.7.12.0
- `6.18.33.2-microsoft-standard-WSL2`
- Debian 13
- Btrfs

## Quick start

### 1. Identify the Windows disk and partition

In PowerShell:

```powershell
Get-Disk
Get-Partition | Sort-Object DiskNumber,PartitionNumber
```

### 2. Probe the partition without starting NBD

Run from an elevated PowerShell:

```powershell
.\wsl-partition-bridge.exe --disk 0 --partition 2 --probe
```

For Btrfs, a successful probe looks like:

```text
wsl-partition-bridge (READ ONLY)
Backend   : \\.\PhysicalDrive0 [offset=... size=...]
Offset    : ... bytes
Size      : ... bytes (... GiB)
Probe     : Btrfs magic [_BHRfS_M]
```

A missing Btrfs magic does **not** prevent the bridge from serving another filesystem; the probe is informational only.

### 3. Find the Windows host address visible from WSL

Inside WSL:

```bash
ip route show default
```

For example:

```text
default via 172.24.240.1 dev eth0 proto kernel
```

Here the Windows host address is `172.24.240.1`.

### 4. Start the bridge on Windows

In elevated PowerShell:

```powershell
.\wsl-partition-bridge.exe --disk 0 --partition 2 --listen 172.24.240.1:10809
```

Keep this window open while the block device is in use.

### 5. Connect from WSL

On Debian/Ubuntu:

```bash
sudo apt install --no-install-recommends nbd-client
sudo modprobe nbd
sudo nbd-client -readonly -N default 172.24.240.1 10809 /dev/nbd0
```

Verify the filesystem before mounting:

```bash
lsblk -f /dev/nbd0
sudo blkid /dev/nbd0
```

For Btrfs:

```bash
sudo mkdir -p /mnt/btrfs
sudo mount -t btrfs -o ro /dev/nbd0 /mnt/btrfs
findmnt /mnt/btrfs
```

From Windows Explorer, the mounted files are then available at:

```text
\\wsl$\Debian\mnt\btrfs
```

## Disconnect safely

Unmount the filesystem first, then disconnect NBD:

```bash
sudo umount /mnt/btrfs
sudo nbd-client -d /dev/nbd0
```

After that, stop `wsl-partition-bridge.exe` with `Ctrl+C` in PowerShell.

Do not terminate the bridge while `/dev/nbd0` is still mounted.

## Command-line options

```text
--disk N          Windows disk number, e.g. 0
--partition N     Windows partition number, e.g. 2
--offset BYTES    Override partition byte offset
--size BYTES      Override partition byte size
--listen ADDR     NBD listen address (default 127.0.0.1:10809)
--probe           Probe the selected range and exit; do not start NBD
```

Normally use `--disk` and `--partition`. The bridge asks Windows `Get-Partition` for the exact offset and size automatically.

## Build

The program uses only the Go standard library.

```powershell
go test ./...
$env:GOOS = "windows"
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "0"
go build -trimpath -ldflags="-s -w" -o wsl-partition-bridge.exe .
```

Cross-compiling from Linux:

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -ldflags='-s -w' -o wsl-partition-bridge.exe .
```

## Why not `wsl --mount`?

`wsl --mount` attaches a **whole physical disk** to WSL. If Windows is actively using an NTFS partition on that same disk, the disk cannot simply be handed over to WSL without taking the Windows volume offline.

This bridge works at a different layer: Windows keeps ownership of the disk, while the program only reads the selected partition range and presents those bytes to Linux over NBD.

## Scope

The bridge deliberately stays small. It currently implements the NBD functionality needed for a read-only block export:

- fixed-newstyle negotiation
- `NBD_OPT_EXPORT_NAME`
- `NBD_OPT_INFO`
- `NBD_OPT_GO`
- `NBD_OPT_LIST`
- `READ`
- `DISC`
- explicit rejection of write-like commands

It is not a general-purpose NBD server and does not provide encryption, authentication, caching, snapshots, or write support.
