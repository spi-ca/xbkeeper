#!/usr/bin/env bash
set -euo pipefail

version="${VERSION:?set VERSION to the validated release version}"
[[ "$version" =~ ^v[0-9]{8}-[1-9][0-9]*$ ]]
test -f "dist/xbkeeper-$version.tar.gz"
mkdir -p dist

# Only the source tree is mounted, read-only. Packaging and pacman transaction
# fixtures run in an ephemeral Arch container, never against the host database.
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
  main="xbkeeper-${1#v}-x86_64.pkg.tar.zst"
  debug="xbkeeper-debug-${1#v}-x86_64.pkg.tar.zst"
  test -s "$main" && test -s "$debug"
  test "$(find . -maxdepth 1 -name "*.pkg.tar.zst" | wc -l)" -eq 2
  main_info="$(bsdtar -xOf "$main" .PKGINFO)"
  debug_info="$(bsdtar -xOf "$debug" .PKGINFO)"
  grep -Fx "pkgname = xbkeeper" <<< "$main_info"
  grep -Fx "pkgver = ${1#v}" <<< "$main_info"
  grep -Fx "depend = xtrabackup" <<< "$main_info"
  grep -Fx "backup = etc/xbkeeper/xbkeeper.toml" <<< "$main_info"
  grep -Fx "backup = etc/mysql/xbkeeper.cnf" <<< "$main_info"
  if grep -F "depend = xbkeeper=" <<< "$main_info"; then exit 1; fi
  grep -Fx "pkgname = xbkeeper-debug" <<< "$debug_info"
  grep -Fx "pkgver = ${1#v}" <<< "$debug_info"
  test "$(grep -c "^depend = " <<< "$debug_info")" -eq 1
  grep -Fx "depend = xbkeeper=${1#v}" <<< "$debug_info"
  if grep -F "depend = xtrabackup" <<< "$debug_info"; then exit 1; fi
  for path in usr/bin/xbkeeper usr/lib/systemd/system/xbkeeper.service usr/lib/systemd/system/xbkeeper.timer usr/lib/tmpfiles.d/xbkeeper.conf; do
    bsdtar -tf "$main" | grep -Fx "$path"
  done
  bsdtar -tf "$debug" | grep -Fx usr/lib/debug/usr/bin/xbkeeper.debug
  bsdtar -tf "$main" | grep -Fx usr/share/licenses/xbkeeper/LICENSE
  bsdtar -tf "$main" | grep -Fx usr/share/licenses/xbkeeper/go-toml-MIT.txt
  bsdtar -tf "$debug" | grep -Fx usr/share/licenses/xbkeeper-debug/LICENSE
  bsdtar -tf "$debug" | grep -Fx usr/share/licenses/xbkeeper-debug/go-toml-MIT.txt
  bsdtar -xOf "$main" usr/bin/xbkeeper > /tmp/xbkeeper-binary
  bsdtar -xOf "$debug" usr/lib/debug/usr/bin/xbkeeper.debug > /tmp/xbkeeper-symbols
  readelf -S /tmp/xbkeeper-binary | grep -F .gnu_debuglink
  readelf -S /tmp/xbkeeper-symbols | grep -F .debug_info
  objcopy --dump-section .gnu_debuglink=/tmp/xbkeeper-debuglink /tmp/xbkeeper-binary
  grep -aqF xbkeeper.debug /tmp/xbkeeper-debuglink
  # Both x86_64 ELF debuglinks and gzip trailers store IEEE CRC32 little-endian.
  # Compare the actual packaged symbols, not only the debuglink filename.
  gzip -n -c /tmp/xbkeeper-symbols > /tmp/xbkeeper-symbols.gz
  tail -c 4 /tmp/xbkeeper-debuglink > /tmp/expected-crc
  tail -c 8 /tmp/xbkeeper-symbols.gz | head -c 4 > /tmp/actual-crc
  cmp /tmp/expected-crc /tmp/actual-crc
  chmod +x /tmp/xbkeeper-binary
  test "$(/tmp/xbkeeper-binary version)" = "$1"
  bash /src/.github/scripts/test-arch-transaction.sh "$PWD/$main" "$PWD/$debug" "${1#v}"
  tar -cf - "$main" "$debug" >&3
' bash "$version" | tar -C dist -xf -
