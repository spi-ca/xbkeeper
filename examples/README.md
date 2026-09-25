# Packaged default and documentation example

`xbkeeper.toml` is both the generic packaged configuration at
`/etc/xbkeeper/xbkeeper.toml` (root-owned directory 0700, file 0600) and a
readable example under `/usr/share/doc/xbkeeper/examples/`. On upgrades,
pacman's `backup` entry preserves modified operator configuration (a new
version may appear as `.pacnew`); review differences manually. Review local
paths, capacity and retention before running anything. The packaged tmpfiles
rule creates the default backup directory only if absent at boot/package-hook
time; `backup` can safely initialize an absent configured root as well. Existing
roots are never repaired and must be UID-owned mode 0700. `status`/`verify` do
not create directories. The package also installs a private,
comment-only MySQL option template at `/etc/mysql/xbkeeper.cnf` (file 0600, shared `/etc/mysql` directory 0755),
with a readable [example](xbkeeper.cnf). Pacman also preserves operator edits
to this file. No account, password or endpoint is active in the template;
when `user` is unset XtraBackup may use client defaults. Root OS identity
alone does not guarantee MySQL socket authentication: independently confirm
the effective account and privileges, and provision any dedicated backup user
separately before enabling its commented options. Check socket, XtraBackup
version compatibility and an isolated restore before relying on a backup.

Existing JSON config content must be converted manually to TOML; renaming a
JSON file does not convert its format. Installation does not enable the timer
or run a backup. See the [configuration contract](../README.md#configuration)
and [Arch installation boundaries](../packaging/arch/README.md).
