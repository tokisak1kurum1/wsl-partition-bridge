# wsl-partition-bridge

A tiny, read-only bridge that exposes one partition of a Windows physical disk to WSL as an NBD block device without taking the whole disk offline.

This is useful when Windows is actively using another partition on the same physical disk. `wsl --mount` attaches the whole physical disk, while `wsl-partition-bridge` opens the disk read-only, restricts access to one partition byte range, and serves that range over NBD to WSL. Filesystem parsing remains inside the Linux kernel.

## Safety model

The project is intentionally read-only at several independent layers:

- Windows opens `\\.\PhysicalDriveN` with `GENERIC_READ` only.
- Reads are restricted to the selected partition byte range.
- The NBD export advertises `NBD_FLAG_READ_ONLY`.
- `WRITE`, `FLUSH`, `TRIM`, and `WRITE_ZEROES` are rejected.
- The helper connects NBD with `-readonly`.
- Btrfs is mounted read-only.

The Windows bridge process therefore has no write handle to the physical disk.

> [!WARNING]
> This is a low-level storage utility. Keep important data backed up. NBD is unencrypted and unauthenticated. The helper binds only to the Windows address visible from WSL and creates a temporary inbound firewall rule restricted to the current WSL source IP, bridge executable, and NBD port. The rule is removed during unmount. Never bind the bridge to `0.0.0.0` or a public interface.

## Requirements

### Windows

- Windows 11 with WSL 2
- Administrator rights to open `\\.\PhysicalDriveN` and manage the temporary firewall rule

### WSL / Linux

- NBD kernel support (`modprobe nbd`)
- `nbd-client`
- filesystem userspace tools as needed, e.g. `btrfs-progs`

Tested with:

- WSL 2.7.12.0
- `6.18.33.2-microsoft-standard-WSL2`
- Debian 13
- Btrfs

## Recommended helper scripts

The repository has one helper pair:

```text
scripts/mount-btrfs.ps1
scripts/unmount-btrfs.ps1
```

The current preset is intentionally self-contained and uses:

```text
Windows disk     : 0
Partition        : 2
WSL distro       : Debian
NBD device       : /dev/nbd0
Btrfs subvolume  : @home
Mount point      : /mnt/btrfs
Bridge executable: D:\Tools\bin\wsl-partition-bridge.exe
Explorer target  : \\wsl$\Debian\mnt\btrfs\weijie
```

For another machine, edit the fixed configuration block at the top of `mount-btrfs.ps1`.

### One-time WSL setup

Inside Debian/WSL:

```bash
sudo apt install --no-install-recommends nbd-client btrfs-progs
sudo modprobe nbd
```

### Windows tool layout

A simple local layout is:

```text
D:\Tools\bin\wsl-partition-bridge.exe
D:\Tools\bin\mount-btrfs.ps1
D:\Tools\bin\unmount-btrfs.ps1
```

If the PowerShell files were downloaded from the internet, remove the Mark-of-the-Web once:

```powershell
Unblock-File D:\Tools\bin\mount-btrfs.ps1
Unblock-File D:\Tools\bin\unmount-btrfs.ps1
```

Add `D:\Tools\bin` to the user `PATH` if you want to invoke the helpers from any directory.

### Mount

```powershell
mount-btrfs
```

The script:

1. elevates through UAC when needed;
2. loads the NBD module;
3. detects both the Windows WSL gateway address and the current WSL source address;
4. creates a temporary Windows Firewall rule restricted to that WSL source IP, the bridge executable, and TCP port 10809;
5. starts the bridge hidden and waits until Windows confirms that the expected process is actually listening;
6. connects `/dev/nbd0` read-only;
7. mounts the Btrfs `@home` subvolume at `/mnt/btrfs` with `ro,subvol=@home`;
8. verifies the mount with `findmnt`;
9. opens `\\wsl$\Debian\mnt\btrfs\weijie` only after a real mount is confirmed.

The raw-disk handle and NBD export are hard read-only, so the helper does not request any Btrfs write path.

The elevated PowerShell window closes automatically on success and stays open on failure so the real error remains visible.

### Unmount

```powershell
unmount-btrfs
```

The helper unmounts the filesystem first, disconnects NBD second, verifies that the NBD attachment is gone, stops the Windows bridge, removes the temporary firewall rule, and removes its state file. The elevated window closes automatically on success.

## Manual usage

The bridge can also be used without the helper scripts. When starting it manually on the WSL-visible Windows address, ensure Windows Firewall permits the WSL client to reach the selected NBD port.

### Probe a partition

From elevated PowerShell:

```powershell
.\wsl-partition-bridge.exe --disk 0 --partition 2 --probe
```

For Btrfs, a successful probe includes:

```text
Probe     : Btrfs magic [_BHRfS_M]
```

### Start the bridge

Find the Windows host address from WSL:

```bash
ip route show default
```

Then start the bridge from elevated PowerShell, for example:

```powershell
.\wsl-partition-bridge.exe --disk 0 --partition 2 --listen 172.24.240.1:10809
```

### Connect and mount in WSL

```bash
sudo modprobe nbd
sudo nbd-client -readonly -N default 172.24.240.1 10809 /dev/nbd0
sudo mkdir -p /mnt/btrfs
sudo mount -t btrfs -o ro /dev/nbd0 /mnt/btrfs
```

Disconnect in the safe order:

```bash
sudo umount /mnt/btrfs
sudo nbd-client -d /dev/nbd0
```

Only then stop the Windows bridge process.

## Command-line options

```text
--disk N          Windows disk number, e.g. 0
--partition N     Windows partition number, e.g. 2
--offset BYTES    Override partition byte offset
--size BYTES      Override partition byte size
--listen ADDR     NBD listen address (default 127.0.0.1:10809)
--probe           Probe the selected range and exit
```

Normally use `--disk` and `--partition`; the bridge queries Windows for the exact partition offset and size.

## Build

The program uses only the Go standard library.

```powershell
go test ./...
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'
go build -trimpath -ldflags='-s -w' -o wsl-partition-bridge.exe .
```

## Scope

The bridge deliberately stays small. It implements the read-only NBD functionality needed here:

- fixed-newstyle negotiation
- `NBD_OPT_EXPORT_NAME`
- `NBD_OPT_INFO`
- `NBD_OPT_GO`
- `NBD_OPT_LIST`
- `READ`
- `DISC`
- explicit rejection of write-like commands

It is not a general-purpose NBD server and does not provide encryption, authentication, caching, snapshots, or write support.

## License

MIT.
