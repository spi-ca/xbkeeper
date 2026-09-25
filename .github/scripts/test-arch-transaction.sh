#!/usr/bin/env bash
set -euo pipefail

# Run only inside a disposable Arch container; all pacman databases/roots are
# temporary and no remote repositories are configured. This is metadata and
# transaction validation, not a DB or host-install test.
main="$1"
debug="$2"
version="$3"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
printf '[options]\nSigLevel = Never\nLocalFileSigLevel = Never\n' > "$work/pacman.conf"

stub() {
  local name="$1" ver="$2" path="$3"
  mkdir -p "$work/stub"
  printf 'pkgname = %s\npkgver = %s\npkgdesc = isolated transaction fixture\nsize = 0\narch = x86_64\n' "$name" "$ver" > "$work/stub/.PKGINFO"
  bsdtar --zstd -cf "$path" -C "$work/stub" .PKGINFO
}

transaction() {
  local root="$1"
  shift
  mkdir -p "$root/var/lib/pacman" "$root/hooks"
  pacman --root "$root" --dbpath "$root/var/lib/pacman" --hookdir "$root/hooks" \
    --config "$work/pacman.conf" --noconfirm --noscriptlet -U "$@"
}

stub xtrabackup 1-1 "$work/xtrabackup.pkg.tar.zst"
stub xbkeeper "${version%-*}-$(( ${version##*-} - 1 ))" "$work/old-main.pkg.tar.zst"
transaction "$work/wrong" "$work/xtrabackup.pkg.tar.zst" "$work/old-main.pkg.tar.zst"
if transaction "$work/wrong" "$debug" > "$work/reject.log" 2>&1; then
  echo 'debug-only unexpectedly accepted mismatched main' >&2
  exit 1
fi
grep -F "xbkeeper=$version" "$work/reject.log"
transaction "$work/correct" "$work/xtrabackup.pkg.tar.zst"
transaction "$work/correct" "$main" "$debug"
pacman --root "$work/correct" --dbpath "$work/correct/var/lib/pacman" \
  --config "$work/pacman.conf" -Q xbkeeper xbkeeper-debug
