# xbkeeper

A small Linux-only full-backup wrapper for Percona XtraBackup. It runs one backup and prepares it, keeps a configured number of completed backups, and exits. No daemon, restore, incremental, or remote storage support. **Not a substitute for restore drills or an independently stored backup.** Licensed under BSD-3-Clause (see `LICENSE`). The linked TOML parser uses MIT (see `LICENSES/go-toml-MIT.txt`).

## Build and use

The pinned source, binary and release-tag version is `v20260926-7` (date and positive same-day revision); it does not change automatically on each build. A further release on the same date increments the revision. Arch maps this to `pkgver=20260926`, `pkgrel=7`. Keep the source archive and binary version identical to the full tag; a release tag must match the pinned source and Arch recipe. Regenerate the source archive, pinned checksum, and `.SRCINFO` after changing distributed files; older-version artifacts do not represent these sources. Do not package or publish until those pins are synchronized. The v20260926-7 source pins are unreleased until the archive/checksum and
`.SRCINFO` are regenerated and independently reviewed; no tag or release is
created by building locally.

PR and main CI test release builds without publishing. After review and green
main CI, a matching tag at the current main tip triggers source, static Linux
amd64/arm64 and isolated Arch x86_64 builds. The tag workflow publishes a
GitHub release with the source archive, two static Linux archives, paired Arch
main/debug packages and `SHA256SUMS` only after tests, source/checksum validation,
package inspection and isolated local dependency transactions succeed. The Arch
build uses `--nodeps`; fixture transactions do not establish live backup/restore
compatibility.

Requires Go 1.24+ and an installed compatible XtraBackup. Build with `go build -o xbkeeper .`; check with `go test ./...`, `go test -race ./...`, and `go vet ./...`. Building from source does not install or activate anything. The Arch package
includes the binary, a generic private `/etc/xbkeeper/xbkeeper.toml`, a
comment-only MySQL option template at `/etc/mysql/xbkeeper.cnf`, documentation
the [service/timer](packaging/systemd/) and a [tmpfiles rule](packaging/tmpfiles/xbkeeper.conf) for the default backup root; it does not enable or start the units.
Review and adapt the default before use; pacman's `backup` entry preserves
operator edits across upgrades. The separate debug package requires the exact
matching main package version; it is not needed for `--verbose` logging. See
[Arch packaging](packaging/arch/README.md)
and the [unit migration plan](docs/systemd-migration.md). Host-specific TOML values,
credentials, prerequisites and deployment policy remain outside this repository in
`kubernetes-manifests/03-arch-systemd/xbkeeper/`.

```sh
xbkeeper version
xbkeeper backup [--config /absolute/path/to/config.toml] [--json] [--verbose]
xbkeeper status [--config /absolute/path/to/config.toml] [--json] [--verbose]
xbkeeper verify [--config /absolute/path/to/config.toml] [--backup backup-UTC-RANDOM] [--json] [--verbose]
```

Omitting `--config` uses exactly `/etc/xbkeeper/xbkeeper.toml` for all three
commands; an explicit path overrides it. The default must exist and pass the
same private-file and configuration checks as any override; no config file is
created automatically. Only `backup` may initialize a missing backup directory;
`status` and `verify` fail read-only with an uninitialized-directory error.
Before backup starts, initialization syncs every parent directory entry, including
existing entries that may remain after an interrupted initialization. A sync
failure stops before taking the backup lock or starting backup/retention;
created empty directories may remain for a later checked retry. An explicit empty path, missing flag value, unknown
flag or positional argument is an error. `verify --backup NAME` also uses the
default config when no override is given.

`version` prints exactly one line (the build version, `dev` unless built with
`-ldflags '-X main.Version=YOUR_VERSION'`). **Breaking output migration in
v20260926-5:** all three operational commands now print human-readable
`Command: NAME` and `Result: ok` or `Result: failed: REASON` lines by default,
followed by command-specific details when available. `backup` prints
`Backup: backup-UTC-RANDOM` on success; `status` lists completed backups,
incomplete stages, pending deletions and last success; `verify` lists each checked backup with outcome, number
of successfully hashed files, and fixed reason on failure. Automation that
previously consumed a bare backup name, raw status JSON, or default verify JSON
must opt into `--json` and read the new `data` fields. No logs are written to
stdout.

`--json` on **backup, status, and verify** emits exactly one JSON object per
parsed invocation: `{"command":"backup|status|verify","ok":true|false,
"data":...,"error":null|"sanitized phase/reason"}`. On success `error` is
`null`; on failure it is a nonempty sanitized message, `ok` is `false`, and the
exit status is nonzero. `data` is `null` when no result is available (including
config, missing-root, lock, and early validation failures). Successful backup
`data` is `{"name":"backup-..."}`; on backup failure it is `null` even when a
post-promotion durability/retention failure leaves a backup present: inspect
status before retrying. Status `data` has `backups` (descending recorded preparation time),
`incomplete` (staging directories), `pending_deletions` (interrupted
retention) and optional `last_success` (the first completed backup in that
ordering). Empty status has empty arrays and omits `last_success`. Cancellation
of status reports a nonzero failure, including when canceled during inspection;
status still does not modify the backup root. Verify `data` retains its original
`{"backups":[{"name":...,"ok":...,"files":...,"reason":...}]}` results,
including failed/unverifiable backups and any completed results before
cancellation; `reason` is omitted for successful entries. Verify without any
completed backups fails. A selected `--backup` must be an exact completed
basename and is checked independently of unrelated broken backups. An invalid
command, flag, missing flag value, or unexpected positional argument may only
produce a sanitized stderr diagnostic with no stdout: format selection is not
reliably known before parsing succeeds. Operational errors already presented on stdout are not duplicated on stderr;
parse and output-writer errors still receive stderr diagnostics. Programs must check exit status as
well as `ok`, and must not mistake `data` for a guarantee that a failed backup
was rolled back.

Format-1 backups remain visible to status and retention but always report
`legacy format 1 is unverifiable` in verify, even if hashes were added later.
Verification works offline without a database, credential file or XtraBackup
executable; it cannot run when the managed `.lock` is absent, unsafe or held
exclusively. Concurrent verifies may share the lock. Status does not hash file
contents and is read-only without acquiring the backup lock. Wall-clock rollback
can make status order differ from actual completion order. See the
[integrity contract](docs/integrity.md).

Stderr receives structured INFO phase/start/end, duration, result, and
backup-ID events with or without `--verbose`. `--verbose` before or after any operational
command enables DEBUG logging; during backup, child stdout/stderr lines are
DEBUG phase/stream-labeled events, **not INFO** (at most 4096 bytes per line,
at most 1 MiB of emitted child content; excess is drained). `--verbose` does
**not** automatically redact sensitive child content and should be enabled
only when its stderr destination is protected. Without it, child output is not
streamed. Up to 1 MiB of raw child-output tail is retained on failure in the
private `.last-failure.log` in the backup root: this too can contain
credentials or other sensitive content. Command arguments/credential-file
contents are never intentionally logged by xbkeeper. The child output tail is
written to a unique private temporary file outside the XtraBackup target and
removed after the run.

## Source layout and checks

The executable stays in one Go package; files separate responsibilities without
introducing a service framework or public library API:

- `main.go`: CLI and signal context.
- `output.go`: bounded child output streaming and failure-log tail.
- `config.go`: strict configuration and path/runtime validation.
- `workflow.go`: backup/prepare, promotion and retention orchestration.
- `state.go`: managed backup metadata, checkpoints and status ordering.
- `integrity.go` / `verify.go`: bounded SHA-256 manifests and read-only audits.
- `process.go`: bounded private output and Linux process-group lifecycle.
- `storage.go` / `rootops.go`: locking, space checks, confined deletion and sync.

Tests use temporary directories, Unix-socket fixtures and a fake XtraBackup.
`main_test.go` covers CLI/configuration and fake-process flows;
`workflow_test.go` covers retention, clock rollback and command contracts;
`safety_test.go` covers inspection failures, quarantine, durability failures and
safe diagnostics; `systemd_test.go` checks the offline package/unit contract.
They require neither MySQL nor root. They do **not** establish
real XtraBackup compatibility or restore correctness. Run `make test` before
building a source archive; GNU make/tar and gzip are needed for `make dist`.

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

The [generic packaged default and documentation example](examples/xbkeeper.toml) contain no credentials; the separate [MySQL option template](examples/xbkeeper.cnf) has only commented account/password placeholders. Review local paths, reserve and DB authentication before use. See [installation notes](examples/README.md). TOML is the only accepted operator config; migrate existing JSON content manually (renaming a file is insufficient). Backup metadata (`xbkeeper.json`) remains unchanged JSON; command `--json` output uses the envelope described above. Supply all host-specific paths and `keep` (at least 1). `xtrabackup` defaults to `/usr/bin/xtrabackup`; omitting `min_free_bytes` means zero reserve, so set it explicitly as in the example. Unknown, incorrectly cased, and duplicate TOML fields are errors. `backup_dir` may be absent before `backup`, which creates missing components under trusted existing ancestors (new directories UID-owned mode `0700`). Existing directories are never chmod/chown repaired; the backup root must be executing-UID-owned mode `0700`, and must not overlap `datadir`. `status` and `verify` require the root to exist and never create it. The MySQL option file must be a private regular file owned by the executing UID; its content is never read or printed by xbkeeper. The packaged comment-only file supplies no active account or password; when `user` is unset XtraBackup may use client defaults, so independently confirm effective DB authentication and privileges. Running the service as root does not itself establish MySQL socket authentication. Configure a separate backup user only after provisioning and reviewing its grants; no DB account is created by the package. For `backup`, the socket must exist and be a Unix socket. `status` only needs the config and backup directory: it checks live database, credentials, executable, and socket paths lexically, so it works while the database is offline. All paths must be clean and absolute; existing paths used by `backup` and the config/backup root used by `status` must have no symlink components. Ancestors of config/credential/backup root must be root- or UID-owned, not writable by others (a root-owned sticky `/tmp` is allowed for local testing). The XtraBackup path must be a root- or UID-owned executable regular file with no group/other write permissions and controlled ancestors. Run with sufficient OS/database privileges, typically as root; tests use temporary directories and a fake executable without a live database.

The wrapper invokes `xtrabackup --no-defaults --version`, then `xtrabackup --defaults-file=PATH --backup --target-dir=DIR --datadir=PATH --socket=PATH`, then `xtrabackup --no-defaults --prepare --target-dir=DIR`. The defaults-file option is first for backup; prepare never loads credentials. Child output is not sent to stderr by default; `backup --verbose` opts into bounded DEBUG line-wise streaming. The child runs with a private file creation mask. An exclusive nonblocking flock prevents concurrent backups. During normal execution only the direct child is polled via `/proc` (every 100 ms); after its exit or on cancellation the process group is inspected. On SIGINT/SIGTERM or inspection failure the group receives TERM followed by KILL after two seconds. If containment cannot be verified within four seconds, the command returns a containment-uncertain error and **leaves its staging directory quarantined**; a later backup refuses to start while any managed `inprogress-*` or legacy `.inprogress-*` directory exists. A stuck kernel task can survive KILL; this policy prevents a subsequent backup from proceeding after lock release but cannot guarantee process termination. Operators must investigate and explicitly resolve quarantined stages before retrying. This requires Linux `/proc`; descendants that daemonize into another process group/session are **not supported and cannot be contained**. Do not run unrelated processes inside the backup command's process group. The process-wide `umask(0077)` is safe for this single-purpose CLI; embedded callers must not run backups concurrently with other filesystem work in the same process.

Backups first land in a unique visible `inprogress-UTC-random` directory (XtraBackup prepare skips dot-prefixed target basenames). After a successful prepare, xbkeeper checks `xtrabackup_checkpoints` for exactly one `backup_type = full-prepared`, writes `xbkeeper.json` (format 2, creation/preparation timestamps, XtraBackup version), hashes all files into an exclusive root `SHA256SUMS` (including metadata and control files, excluding the manifest), syncs every safe regular file and directory in the staged tree (including metadata), atomically renames to `backup-UTC-random`, syncs the backup parent directory, and only then prunes older **validated, tool-named** backups down to `keep`. Invalid/tampered completed backups stop backup and status rather than being pruned. Ordinary failed backup/prepare staging is removed only when its tree is safe; hashing failures/cancellation after metadata, tampered staging, uncertain containment, and pre-promotion sync failures leave the stage for manual investigation. Legacy `.inprogress-*` stages are always treated as incomplete and never automatically removed or pruned; investigate manually. Any existing managed staging directory blocks new backups, while `status` reports it under `incomplete`. A post-promotion parent sync failure leaves the renamed backup in place but returns nonzero; old backups are not pruned. Existing successful backups are not pruned before the new backup passes the sync boundary. Unmanaged names are not deleted. Manual changes inside a managed backup can cause validation to fail. Regular files with multiple hard links, symlinks, and special files are rejected. Wall timestamps are recorded as observed (including clock rollback); the just-completed backup is always protected during retention. During retention, each validated old completed backup is atomically renamed
from `backup-UTC-random` to the matching `.deleting-UTC-random` name with
no-replace collision handling, and the backup parent is synced **before** any
unlink. A failed rename leaves the old completed backup intact; a failed
post-rename sync leaves its still-complete tree in the deletion namespace.
Interrupted unlinks remain there, separately reported as `pending_deletions`;
they do not count toward `keep` or invalidate the completed inventory. Under
the exclusive backup lock, after a new backup is durably promoted, xbkeeper
checks and retries deletion of exact-name pending trees only if every remaining
entry is private, UID-owned, regular or a directory, with no hardlinks. Unsafe
pending entries are never deleted and make retention fail nonzero. Failed
cleanup (including parent sync) leaves the new backup but reports a nonzero
result; examine `status` before retrying. Pending trees may still occupy space:
free-space preflight can fail before any retry, requiring operator review and
manual recovery. Invalid preexisting `backup-*` trees still fail closed; legacy
`.inprogress-*` stages are never pruned automatically.

Free-space preflight requires `min_free_bytes` plus the sum of allocated datadir file blocks to fit in currently available backup-volume bytes. This is a rough heuristic, **not a guarantee**: XtraBackup can use different space, the database can grow, and concurrent writes can consume free space. Filesystem sync errors abort retention, but successful `fsync` and rename do not prove hardware integrity, power-loss survival on every filesystem, database consistency, restore correctness, filesystem snapshot, or off-host durability. Monitor jobs, protect the credential and backup volume, test restores separately, and ensure enough space for multiple retained backups. The Arch package supplies an unactivated oneshot service and daily persistent
timer. It supplies a generic private default configuration and a comment-only
MySQL option template, but not host-specific settings, actual credentials or
production activation approval. The default unit adds a conservative first-stage capability denylist and
kernel/device/namespace/process hardening. This is **not** a proven minimal
capability set; DAC/NICE/resource capabilities remain available pending real
backup evidence. SYS_PTRACE is also retained so process-group inspection can
read other users' `/proc/PID/stat` on hosts using `hidepid`. Do not deploy based solely on fake tests: first verify unit
syntax and an isolated service sandbox, then under a separately approved
real XtraBackup/DB setup verify full backup, prepare, status/verify, cancellation
and retention/failure handling (including sufficient space and restore testing)
before any host timer rollout. Failed backup, prepare, cancellation or cleanup
must be visible as a nonzero job result; stop rollout and preserve existing
known-good backups/configuration rather than loosening the sandbox blindly.
The default unit
writes only to `/var/backups/xtrabackup` and invokes
`/etc/xbkeeper/xbkeeper.toml`; a different backup root requires a reviewed
`ReadWritePaths=` reset/drop-in. The packaged tmpfiles rule creates the default root at boot/package-hook time only if absent, preserving existing attributes; unsafe existing roots fail validation. `RequiresMountsFor=/var/backups/xtrabackup` orders known configured mounts before the unit, but cannot detect an absent unconfigured dedicated disk: operators need an explicit mount assertion/drop-in for that case and for custom roots. Before an approved upgrade, check the current
timer/backup state and any `/etc/systemd/system/xbkeeper.*` units shadowing the
packaged `/usr/lib/systemd/system` units. A newly enabled persistent timer may
run immediately after a missed schedule. See [Arch packaging](packaging/arch/README.md)
for migration and rollback boundaries.
