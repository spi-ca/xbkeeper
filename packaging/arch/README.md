# Arch package build

This is a local source-archive package, not an AUR publication or remote release.
The package installs `/usr/bin/xbkeeper`, its BSD-3-Clause license, the linked
TOML parser's MIT license, inactive examples under `/usr/share/doc/xbkeeper/examples/`, relevant
documentation and the canonical `xbkeeper.service`/`xbkeeper.timer` (mode 0644)
in `/usr/lib/systemd/system`. It does not create a DB account, install
credentials/configuration, provision directories, enable/start a timer, run an
install hook or delete backups on removal. Host TOML config, prerequisites and deployment
instructions remain in `~/Codebase/kubernetes-manifests/03-arch-systemd/xbkeeper/`.
The [unit migration plan](../../docs/systemd-migration.md) records the handoff.

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
Tests invoke a fake XtraBackup, never a database. Normal builds resolve the
pinned `github.com/pelletier/go-toml/v2` module (see `go.mod`/`go.sum`);
prefetch and verify it separately for offline builds. No automatic package
install (`-i`) is used.

`make dist` uses deterministic archive timestamps, owner IDs, regular-file modes
(0644) and gzip headers, and copies the archive next to `PKGBUILD` for makepkg's
local-source lookup. Source file permissions/umask do not change its checksum.
The canonical pinned version is `v20260926-1`: the release tag, Makefile
`VERSION`, source archive directory/name and binary `version` output use the
full `vYYYYMMDD-N` string. Arch splits it into `pkgver=20260926` and
`pkgrel=1`. A second release on the same day uses `v20260926-2`,
`pkgver=20260926`, `pkgrel=2` and a **new matching source archive** named
`xbkeeper-v20260926-2.tar.gz`; never bump only `pkgrel` while reusing the
previous source version/archive. Versions are pinned, not generated from the
build date. After intentionally changing source files, update all version
fields, regenerate the archive, review `makepkg -g`, update `sha256sums` in
`PKGBUILD`, then regenerate `.SRCINFO` with
`makepkg --printsrcinfo > .SRCINFO`. Do not bypass a mismatch with
`--skipchecksums`. The pinned checksum and `.SRCINFO` must match the finalized
source snapshot before package verification or building.

## Isolated offline verification

A pre-existing Arch `base-devel` container image can verify packaging without
installing packages on the host. Run it with networking disabled, a read-only
source mount, an isolated writable working copy, a trusted Go toolchain and a
read-only, checksum-verified Go module download cache (including go-toml/v2
v2.3.0). Copy the cached module downloads into an isolated writable `GOMODCACHE`
so Go can unpack them without changing the host cache. Set `GOPROXY=off`; without
the module the offline build fails rather than disabling sum checks.
`makepkg --nodeps` is appropriate **only for this controlled verification** when
Go is supplied outside pacman and the tests do not require installed XtraBackup.
It does not prove that dependency resolution works on a fresh Arch system.
Normal package builds should check dependencies as above.

After building, inspect the package file list and `.PKGINFO`, including the
`/usr/lib/systemd/system` units; check the extracted binary's `version` command
without running `backup`. Keep package archives out of Git. Package creation
does not establish real backup or restore correctness.

## Separate host migration and activation

The service runs as root with a private umask, reads
`/etc/xbkeeper/xbkeeper.toml`, and can write only to `/var/backups/xtrabackup`
under its filesystem sandbox. A custom backup root needs an explicit, reviewed
systemd drop-in that resets `ReadWritePaths=` before adding the new directory.
`After=mysqld.service` orders the service; it neither starts nor restarts MySQL.
The timer schedules 03:00 host local time with `Persistent=true`: enabling it
after a missed run can trigger a backup immediately. Neither package build nor
installation authorizes enabling or running it.

Before any separately approved host install/upgrade, inspect whether the timer
is active/enabled and whether a backup is running; agree how an existing
schedule is suspended/resumed without interrupting a backup. Inspect and safely
back up any existing `/etc/systemd/system/xbkeeper.service` or `.timer`:
local units shadow the new packaged `/usr/lib` units until a separately approved
migration and daemon-reload. Preserve unknown drop-ins/customizations, config,
credentials and backup data. Retain known-good package/units for rollback;
disabling a timer does not stop an active backup. See the host deployment guide
for prerequisites and isolated restore validation. No live activation, backup
or restore is established by this package change.
