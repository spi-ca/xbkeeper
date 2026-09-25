# Arch package build

This is a local source-archive package, not an AUR publication. On a reviewed
matching tag at the current main tip, the release workflow builds it in an
isolated Arch container and publishes both `xbkeeper` and `xbkeeper-debug`
with the source archive, Linux amd64/arm64 static binaries and SHA256SUMS as
a GitHub release. PR/main CI builds the same artifacts without publishing.
The container runs `makepkg --nodeps`, then exercises isolated local pacman
transactions with a minimal `xtrabackup` metadata stub (no real DB): a debug-only
install fails with an older main, while the matching main/debug pair succeeds.
This does not verify live backup/restore compatibility.
The package installs `/usr/bin/xbkeeper`, its BSD-3-Clause license, the linked
TOML parser's MIT license, the generic default config at
`/etc/xbkeeper/xbkeeper.toml` (root-owned file mode 0600 in a mode 0700
`/etc/xbkeeper` directory), a comment-only MySQL option template at
`/etc/mysql/xbkeeper.cnf` (file 0600; shared parent directory 0755), copies
under `/usr/share/doc/xbkeeper/examples/` (mode 0644),
relevant documentation and the canonical `xbkeeper.service`/`xbkeeper.timer`
(mode 0644) in `/usr/lib/systemd/system`, plus a mode 0644 tmpfiles rule under
`/usr/lib/tmpfiles.d` for the default backup root. Pacman's `backup` entry preserves
modified TOML and MySQL option files during upgrades (new defaults may arrive
as `.pacnew`); compare these manually. It does not create a DB account, install
active credentials, enable/start a timer, run an install hook or delete backups
on removal. System tmpfiles boot/package hooks may create the default root if
absent; the package payload itself contains no backup directory. Host-specific config values, prerequisites and deployment instructions
remain in `~/Codebase/kubernetes-manifests/03-arch-systemd/xbkeeper/`.
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
install (`-i`) is used. The split recipe explicitly detaches Go DWARF symbols,
strips the main binary's debug sections, adds a GNU debuglink and disables
makepkg's automatic debug package. `xbkeeper-debug` contains the separate
symbols and licenses and depends on `xbkeeper=20260926-6`; it is **optional**
for running xbkeeper, including `--verbose` (which controls runtime logs, not
debug symbols). Install both matching files in one reviewed transaction when
symbols are desired:

```sh
sudo pacman -U ./xbkeeper-20260926-6-x86_64.pkg.tar.zst \
  ./xbkeeper-debug-20260926-6-x86_64.pkg.tar.zst
```

This is an example for a separately approved host deployment, not an instruction
to run it during build/review; normal main-only installation is also supported.

`make dist` uses deterministic archive timestamps, owner IDs, regular-file modes
(0644) and gzip headers, and copies the archive next to `PKGBUILD` for makepkg's
local-source lookup. Source file permissions/umask do not change its checksum.
The canonical pinned version is `v20260926-6`: the release tag, Makefile
`VERSION`, source archive directory/name and binary `version` output use the
full `vYYYYMMDD-N` string. Arch splits it into `pkgver=20260926` and
`pkgrel=6`. A further release on the same day requires another revision and a
**new matching source archive**; never bump only `pkgrel` while reusing the
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
The isolated transaction fixture tests the exact main/debug dependency and
uses a minimal local `xtrabackup` stub instead of installing a real backup tool;
it does not prove runtime dependency resolution on a fresh Arch system.
Normal package builds should check dependencies as above.

After building, inspect the package file list and `.PKGINFO`, including the
`/usr/lib/systemd/system` units, the tmpfiles rule, the private `/etc/xbkeeper` config permissions
and both `backup` entries and the `/etc/mysql` template/file modes; check the
extracted binary's `version` command without running `backup`. Verify that
`xbkeeper-debug` has exactly `depend = xbkeeper=20260926-6` in `.PKGINFO`,
contains `/usr/lib/debug/usr/bin/xbkeeper.debug`, and the main binary has a
matching GNU debuglink. Keep package archives out of Git. Package creation
does not establish real backup or restore correctness.

## Separate host migration and activation

The service runs as root with a private umask, reads
`/etc/xbkeeper/xbkeeper.toml`, and can write only to `/var/backups/xtrabackup`
under its filesystem sandbox. A custom backup root needs an explicit, reviewed
systemd drop-in that resets `ReadWritePaths=` before adding the new directory.
`RequiresMountsFor=/var/backups/xtrabackup` orders configured mounts, but cannot
detect a missing unconfigured dedicated disk: add an explicit mount assertion
and update the mount requirement in a reviewed drop-in for that case/custom roots.
`After=mysqld.service` orders the service; it neither starts nor restarts MySQL.
The timer schedules 03:00 host local time with `Persistent=true`: enabling it
after a missed run can trigger a backup immediately. Neither package build nor
installation authorizes enabling or running it.

Before any separately approved host install/upgrade, inspect any existing
`/etc/xbkeeper/xbkeeper.toml` and preserve operator changes. Review the packaged
default's generic MySQL paths against the host; the TOML config is not a
credential file. Review any existing `/etc/mysql/xbkeeper.cnf` and preserve
operator changes before upgrading. The packaged private option template has no
active user/password: an unset user may use MySQL client defaults, but root OS
identity does not prove socket authentication. Independently confirm effective
DB authentication and privileges, or provision a dedicated backup user and
configure its credentials separately. Review capacity, destination and retention before initializing backups. The
packaged colon-prefixed tmpfiles rule creates the default backup root if absent
at boot/package-hook time, without changing existing directory attributes.
`backup` alone can create missing root components under trusted ancestors (UID
owner, mode 0700); existing unsafe directories fail validation, never repaired.
`status` and `verify` never initialize the root. Inspect whether the timer
is active/enabled and whether a backup is running; agree how an existing
schedule is suspended/resumed without interrupting a backup. Inspect and safely
back up any existing `/etc/systemd/system/xbkeeper.service` or `.timer`:
local units shadow the new packaged `/usr/lib` units until a separately approved
migration and daemon-reload. Preserve unknown drop-ins/customizations, config,
credentials and backup data. Retain known-good package/units for rollback;
disabling a timer does not stop an active backup. See the host deployment guide
for prerequisites and isolated restore validation. No live activation, backup
or restore is established by this package change.
