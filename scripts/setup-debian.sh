#!/bin/sh
set -eu

apt update
apt install --no-install-recommends nbd-client btrfs-progs
modprobe nbd

echo "WSL Debian setup complete."
