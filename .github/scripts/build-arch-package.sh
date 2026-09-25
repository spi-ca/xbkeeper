#!/usr/bin/env bash
set -euo pipefail

version="${VERSION:?set VERSION to the validated release version}"
[[ "$version" =~ ^v[0-9]{8}-[1-9][0-9]*$ ]]
test -f "dist/xbkeeper-$version.tar.gz"
mkdir -p dist

# Only the source tree is mounted, read-only. Packaging runs as a non-root
# account in an ephemeral Arch container; no host package transaction occurs.
docker run --rm -v "$PWD:/src:ro" archlinux:base-devel bash -euo pipefail -c '
  exec 3>&1
  exec 1>&2
  pacman -Syu --noconfirm --needed go
  useradd -m builder
  install -d -o builder -g builder /home/builder/work
  cp /src/packaging/arch/PKGBUILD "/src/dist/xbkeeper-$1.tar.gz" /home/builder/work/
  chown builder:builder /home/builder/work/*
  runuser -u builder -- bash -euo pipefail -c '\''
    cd /home/builder/work
    makepkg --verifysource
    makepkg --nodeps --noconfirm
  '\''
  cd /home/builder/work
  pkg="xbkeeper-${1#v}-x86_64.pkg.tar.zst"
  test -f "$pkg"
  bsdtar -xOf "$pkg" .PKGINFO | grep -Fx "pkgname = xbkeeper"
  bsdtar -xOf "$pkg" .PKGINFO | grep -Fx "pkgver = ${1#v}"
  bsdtar -xOf "$pkg" .PKGINFO | grep -Fx "depend = xtrabackup"
  bsdtar -xOf "$pkg" .PKGINFO | grep -Fx "backup = etc/xbkeeper/xbkeeper.toml"
  bsdtar -xOf "$pkg" .PKGINFO | grep -Fx "backup = etc/mysql/xbkeeper.cnf"
  bsdtar -tf "$pkg" | grep -Fx usr/bin/xbkeeper
  bsdtar -tf "$pkg" | grep -Fx usr/lib/systemd/system/xbkeeper.service
  bsdtar -tf "$pkg" | grep -Fx usr/lib/systemd/system/xbkeeper.timer
  bsdtar -tf "$pkg" | grep -Fx usr/lib/tmpfiles.d/xbkeeper.conf
  tar -cf - "$pkg" >&3
' bash "$version" | tar -C dist -xf -
