# Arch package build

This is a local source-archive package, not an AUR publication or remote release.
The package installs only `/usr/bin/xbkeeper`, its BSD-3-Clause license and README.
It does not create a DB account, install credentials/configuration/systemd units,
enable a timer or delete backups on removal. Host configuration belongs to
`~/Codebase/kubernetes-manifests/03-arch-systemd/xbkeeper/`.

## Existing host build sandbox

Use an ordinary non-root Arch package-build account with `base-devel` and Go
1.24+ available. The compatible XtraBackup package is a runtime dependency;
`xtrabackup-ari` satisfies it through its `provides=xtrabackup` declaration.

```sh
# From the xbkeeper repository root:
make test
make dist
cd packaging/arch
makepkg --verifysource
makepkg -srf
```

`-s` may install missing build/runtime dependencies **inside that build host or
sandbox**; `-r` removes dependencies installed by makepkg after a successful
build, and `-f` permits replacing an existing package artifact. Review the
transaction; these are not commands to run on a production DB host casually.
Tests invoke a fake XtraBackup, never a database. No automatic package install
(`-i`) is used.

`make dist` uses deterministic archive timestamps, owner IDs and gzip headers,
and copies the archive next to `PKGBUILD` for makepkg's local-source lookup.
The checked-in SHA-256 covers this exact source snapshot. After intentionally
changing source files, bump the version/release as appropriate, regenerate the
archive, review `makepkg -g`, update `sha256sums` in `PKGBUILD`, then regenerate
`.SRCINFO` with `makepkg --printsrcinfo > .SRCINFO`. Do not bypass a mismatch with
`--skipchecksums`. Keep the Makefile version and `pkgver` synchronized.

## Isolated offline verification

A pre-existing Arch `base-devel` container image can verify packaging without
installing packages on the host. Run it with networking disabled, a read-only
source mount, an isolated writable working copy, and a trusted Go toolchain.
`makepkg --nodeps` is appropriate **only for this controlled verification** when
Go is supplied outside pacman and the tests do not require installed XtraBackup.
It does not prove that dependency resolution works on a fresh Arch system.
Normal package builds should check dependencies as above.

After building, inspect the package file list and `.PKGINFO`; check the extracted
binary's `version` command without running `backup`. Keep package archives out
of Git. Package creation does not establish real backup or restore correctness.
