# Operations and safety

[Operator entry](../README.md) · [Commands and output](cli.md) · [Integrity](integrity.md)

## Configuration

Private, UID-owned TOML file (mode `0600` recommended; no group/other permissions):

```toml
backup_dir = "/var/backups/xtrabackup"
datadir = "/var/lib/mysql"
socket = "/run/mysqld/mysqld.sock"
defaults_file = "/etc/mysql/xbkeeper.cnf"
keep = 3
min_free_bytes = 10_737_418_240
xtrabackup = "/usr/bin/xtrabackup"
```

The [generic packaged default and documentation example](../examples/xbkeeper.toml)
contain no credentials; the separate [MySQL option template](../examples/xbkeeper.cnf)
has only commented account/password placeholders. Review local paths, reserve and DB
authentication before use. See [installation notes](../examples/README.md).

TOML is the only accepted operator config; migrate existing JSON content manually
(renaming a file is insufficient). Backup metadata (`xbkeeper.json`) remains unchanged
JSON; command `--json` output uses the [CLI envelope](cli.md). Supply all host-specific
paths and `keep` (at least 1). `xtrabackup` defaults to `/usr/bin/xtrabackup`; omitting
`min_free_bytes` means zero reserve, so set it explicitly as in the example. Unknown,
incorrectly cased, and duplicate TOML fields are errors.

`backup_dir` may be absent before `backup`, which creates missing components under
trusted existing ancestors (new directories UID-owned mode `0700`). Existing directories
are never chmod/chown repaired; the backup root must be executing-UID-owned mode `0700`,
and must not overlap `datadir`. `status` and `verify` require the root to exist and
never create it.

The MySQL option file must be a private regular file owned by the executing UID; its
content is never read or printed by xbkeeper. The packaged comment-only file supplies no
active account or password; when `user` is unset XtraBackup may use client defaults, so
independently confirm effective DB authentication and privileges. Running the service as
root does not itself establish MySQL socket authentication. Configure a separate backup
user only after provisioning and reviewing its grants; no DB account is created by the
package.

For `backup`, the socket must exist and be a Unix socket. `status` only needs the config
and backup directory: it checks live database, credentials, executable, and socket paths
lexically, so it works while the database is offline.

All paths must be clean and absolute; existing paths used by `backup` and the
config/backup root used by `status` must have no symlink components. Ancestors of
config/credential/backup root must be root- or UID-owned, not writable by others (a
root-owned sticky `/tmp` is allowed for local testing). The XtraBackup path must be a
root- or UID-owned executable regular file with no group/other write permissions and
controlled ancestors.

Run with sufficient OS/database privileges, typically as root; tests use temporary
directories and a fake executable without a live database.

## Process lifecycle and containment

The wrapper invokes `xtrabackup --no-defaults --version`, then `xtrabackup
--defaults-file=PATH --backup --target-dir=DIR --datadir=PATH --socket=PATH`, then
`xtrabackup --no-defaults --prepare --target-dir=DIR`. The defaults-file option is first
for backup; prepare never loads credentials. Child output is not sent to stderr by
default; `backup --verbose` opts into bounded DEBUG line-wise streaming.

The child runs with a private file creation mask. An exclusive nonblocking flock
prevents concurrent backups. During normal execution only the direct child is polled via
`/proc` (every 100 ms); after its exit or on cancellation the process group is
inspected.

On SIGINT/SIGTERM or inspection failure the group receives TERM followed by KILL after
two seconds. If containment cannot be verified within four seconds, the command returns
a containment-uncertain error and **leaves its staging directory quarantined**; a later
backup refuses to start while any managed `inprogress-*` or legacy `.inprogress-*`
directory exists. A stuck kernel task can survive KILL; this policy prevents a
subsequent backup from proceeding after lock release but cannot guarantee process
termination.

Operators must investigate and explicitly resolve quarantined stages before retrying.
This requires Linux `/proc`; descendants that daemonize into another process
group/session are **not supported and cannot be contained**. Do not run unrelated
processes inside the backup command's process group. The process-wide `umask(0077)` is
safe for this single-purpose CLI; embedded callers must not run backups concurrently
with other filesystem work in the same process.

## Promotion and retention

Backups first land in a unique visible `inprogress-UTC-random` directory (XtraBackup
prepare skips dot-prefixed target basenames). After a successful prepare, xbkeeper
checks `xtrabackup_checkpoints` for exactly one `backup_type = full-prepared`, writes
`xbkeeper.json` (format 2, creation/preparation timestamps, XtraBackup version), hashes
all files into an exclusive root `SHA256SUMS` (including metadata and control files,
excluding the manifest), syncs every safe regular file and directory in the staged tree
(including metadata), atomically renames to `backup-UTC-random`, syncs the backup parent
directory, and only then prunes older **validated, tool-named** backups down to `keep`.
Invalid/tampered completed backups stop backup and status rather than being pruned.

Ordinary failed backup/prepare staging is removed only when its tree is safe; hashing
failures/cancellation after metadata, tampered staging, uncertain containment, and
pre-promotion sync failures leave the stage for manual investigation. Legacy
`.inprogress-*` stages are always treated as incomplete and never automatically removed
or pruned; investigate manually. Any existing managed staging directory blocks new
backups, while `status` reports it under `incomplete`.

A post-promotion parent sync failure leaves the renamed backup in place but returns
nonzero; old backups are not pruned. Existing successful backups are not pruned before
the new backup passes the sync boundary. Unmanaged names are not deleted. Manual changes
inside a managed backup can cause validation to fail. Regular files with multiple hard
links, symlinks, and special files are rejected. Wall timestamps are recorded as
observed (including clock rollback); the just-completed backup is always protected
during retention.

During retention, each validated old completed backup is atomically renamed from
`backup-UTC-random` to the matching `.deleting-UTC-random` name with no-replace
collision handling, and the backup parent is synced **before** any unlink. A failed
rename leaves the old completed backup intact; a failed post-rename sync leaves its
still-complete tree in the deletion namespace. Interrupted unlinks remain there,
separately reported as `pending_deletions`; they do not count toward `keep` or
invalidate the completed inventory.

Under the exclusive backup lock, after a new backup is durably promoted, xbkeeper checks
and retries deletion of exact-name pending trees only if every remaining entry is
private, UID-owned, regular or a directory, with no hardlinks. Unsafe pending entries
are never deleted and make retention fail nonzero. Failed cleanup (including parent
sync) leaves the new backup but reports a nonzero result; examine `status` before
retrying.

Pending trees may still occupy space: free-space preflight can fail before any retry,
requiring operator review and manual recovery. Invalid preexisting `backup-*` trees
still fail closed; legacy `.inprogress-*` stages are never pruned automatically.

## Capacity and deployment boundaries

Free-space preflight requires `min_free_bytes` plus the sum of allocated datadir file
blocks to fit in currently available backup-volume bytes. This is a rough heuristic,
**not a guarantee**: XtraBackup can use different space, the database can grow, and
concurrent writes can consume free space. Filesystem sync errors abort retention, but
successful `fsync` and rename do not prove hardware integrity, power-loss survival on
every filesystem, database consistency, restore correctness, filesystem snapshot, or
off-host durability. Monitor jobs, protect the credential and backup volume, test
restores separately, and ensure enough space for multiple retained backups.

The Arch package supplies an unactivated oneshot service and daily persistent timer. It
supplies a generic private default configuration and a comment-only MySQL option
template, but not host-specific settings, actual credentials or production activation
approval.

The default unit adds a conservative first-stage capability denylist and
kernel/device/namespace/process hardening. This is **not** a proven minimal capability
set; DAC/NICE/resource capabilities remain available pending real backup evidence.
SYS_PTRACE is also retained so process-group inspection can read other users'
`/proc/PID/stat` on hosts using `hidepid`.

Do not deploy based solely on fake tests: first verify unit syntax and an isolated
service sandbox, then under a separately approved real XtraBackup/DB setup verify full
backup, prepare, status/verify, cancellation and retention/failure handling (including
sufficient space and restore testing) before any host timer rollout. Failed backup,
prepare, cancellation or cleanup must be visible as a nonzero job result; stop rollout
and preserve existing known-good backups/configuration rather than loosening the sandbox
blindly. The default unit writes only to `/var/backups/xtrabackup` and invokes
`/etc/xbkeeper/xbkeeper.toml`; a different backup root requires a reviewed
`ReadWritePaths=` reset/drop-in. The packaged tmpfiles rule creates the default root at
boot/package-hook time only if absent, preserving existing attributes; unsafe existing
roots fail validation. `RequiresMountsFor=/var/backups/xtrabackup` orders known
configured mounts before the unit, but cannot detect an absent unconfigured dedicated
disk: operators need an explicit mount assertion/drop-in for that case and for custom
roots.

Before an approved upgrade, check the current timer/backup state and any
`/etc/systemd/system/xbkeeper.*` units shadowing the packaged `/usr/lib/systemd/system`
units. A newly enabled persistent timer may run immediately after a missed schedule. See
[Arch packaging](../packaging/arch/README.md) for migration and rollback boundaries.
