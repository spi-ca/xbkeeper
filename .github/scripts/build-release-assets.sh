#!/usr/bin/env bash
set -euo pipefail

version="${VERSION:?set VERSION to the validated release version}"
[[ "$version" =~ ^v[0-9]{8}-[1-9][0-9]*$ ]]
test "$(sed -n 's/^VERSION := //p' Makefile)" = "$version"

make dist
source_archive="dist/xbkeeper-$version.tar.gz"
test -f "$source_archive"
expected="$(sed -n "s/^sha256sums=('\([0-9a-f]\{64\}\)')$/\1/p" packaging/arch/PKGBUILD)"
test "$expected" = "$(sha256sum "$source_archive" | cut -d ' ' -f1)"
for arch in amd64 arm64; do
  stage="$(mktemp -d)"
  trap 'rm -rf "$stage"' EXIT
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -buildvcs=false \
    -ldflags "-X main.Version=$version" -o "$stage/xbkeeper" .
  if [ "$arch" = amd64 ]; then
    test "$("$stage/xbkeeper" version)" = "$version"
  else
    readelf -h "$stage/xbkeeper" | grep -q 'Machine:.*AArch64'
  fi
  cp LICENSE README.md "$stage/"
  mkdir "$stage/LICENSES"
  cp LICENSES/go-toml-MIT.txt "$stage/LICENSES/"
  tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner \
    -czf "dist/xbkeeper-$version-linux-$arch.tar.gz" -C "$stage" \
    xbkeeper LICENSE LICENSES/go-toml-MIT.txt README.md
  rm -rf "$stage"
  trap - EXIT
done
