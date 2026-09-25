# Packaged default and documentation example

`xbkeeper.toml` is both the generic packaged configuration at
`/etc/xbkeeper/xbkeeper.toml` (root-owned directory 0700, file 0600) and a
readable example under `/usr/share/doc/xbkeeper/examples/`. On upgrades,
pacman's `backup` entry preserves modified operator configuration (a new
version may appear as `.pacnew`); review differences manually. Review local
paths, capacity and retention before running anything. Create the backup
directory separately, root-owned mode 0700. Supply `/etc/mysql/xbkeeper.cnf`
separately, root-owned mode 0600, with reviewed XtraBackup connection settings.
The package contains no credentials or private option file. Check socket,
MySQL authentication, XtraBackup version compatibility and an isolated restore
before relying on a backup.

Existing JSON config content must be converted manually to TOML; renaming a
JSON file does not convert its format. Installation does not enable the timer
or run a backup. See the [configuration contract](../README.md#configuration)
and [Arch installation boundaries](../packaging/arch/README.md).
