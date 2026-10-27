# Commands and output

[Operator entry](../README.md) · [Operations](operations.md) · [Integrity](integrity.md)

## Commands and flags

```sh
xbkeeper version
xbkeeper backup [--config /absolute/path/to/config.toml] [--json] [--verbose]
xbkeeper status [--config /absolute/path/to/config.toml] [--json] [--verbose]
xbkeeper verify [--config /absolute/path/to/config.toml] [--backup backup-UTC-RANDOM] [--json] [--verbose]
```

Omitting `--config` uses exactly `/etc/xbkeeper/xbkeeper.toml` for all three commands;
an explicit path overrides it. The default must exist and pass the same private-file and
configuration checks as any override; no config file is created automatically. Only
`backup` may initialize a missing backup directory; `status` and `verify` fail read-only
with an uninitialized-directory error. Before backup starts, initialization syncs every
parent directory entry, including existing entries that may remain after an interrupted
initialization. A sync failure stops before taking the backup lock or starting
backup/retention; created empty directories may remain for a later checked retry. An
explicit empty path, missing flag value, unknown flag or positional argument is an
error. `verify --backup NAME` also uses the default config when no override is given.

## Results and compatibility

`version` prints exactly one line (the build version, `dev` unless built with `-ldflags
'-X main.Version=YOUR_VERSION'`). **Breaking output migration in v20260926-5:** all
three operational commands now print human-readable `Command: NAME` and `Result: ok` or
`Result: failed: REASON` lines by default, followed by command-specific details when
available. `backup` prints `Backup: backup-UTC-RANDOM` on success; `status` lists
completed backups, incomplete stages, pending deletions and last success; `verify` lists
each checked backup with outcome, number of successfully hashed files, and fixed reason
on failure. Automation that previously consumed a bare backup name, raw status JSON, or
default verify JSON must opt into `--json` and read the new `data` fields. No logs are
written to stdout.

`--json` on **backup, status, and verify** emits exactly one JSON object per parsed
invocation: `{"command":"backup|status|verify","ok":true|false,
"data":...,"error":null|"sanitized phase/reason"}`. On success `error` is `null`; on
failure it is a nonempty sanitized message, `ok` is `false`, and the exit status is
nonzero. `data` is `null` when no result is available (including config, missing-root,
lock, and early validation failures). Successful backup `data` is
`{"name":"backup-..."}`; on backup failure it is `null` even when a post-promotion
durability/retention failure leaves a backup present: inspect status before retrying.
Status `data` has `backups` (descending recorded preparation time), `incomplete`
(staging directories), `pending_deletions` (interrupted retention) and optional
`last_success` (the first completed backup in that ordering). Empty status has empty
arrays and omits `last_success`. Cancellation of status reports a nonzero failure,
including when canceled during inspection; status still does not modify the backup root.
Verify `data` retains its original
`{"backups":[{"name":...,"ok":...,"files":...,"reason":...}]}` results, including
failed/unverifiable backups and any completed results before cancellation; `reason` is
omitted for successful entries. Verify without any completed backups fails. A selected
`--backup` must be an exact completed basename and is checked independently of unrelated
broken backups. An invalid command, flag, missing flag value, or unexpected positional
argument may only produce a sanitized stderr diagnostic with no stdout: format selection
is not reliably known before parsing succeeds. Operational errors already presented on
stdout are not duplicated on stderr; parse and output-writer errors still receive stderr
diagnostics. Programs must check exit status as well as `ok`, and must not mistake
`data` for a guarantee that a failed backup was rolled back.

## Offline status and verification

Format-1 backups remain visible to status and retention but always report `legacy format
1 is unverifiable` in verify, even if hashes were added later. Verification works
offline without a database, credential file or XtraBackup executable; it cannot run when
the managed `.lock` is absent, unsafe or held exclusively. Concurrent verifies may share
the lock. Status does not hash file contents and is read-only without acquiring the
backup lock. Wall-clock rollback can make status order differ from actual completion
order. See the [integrity contract](integrity.md).

## Logging

Stderr receives structured INFO phase/start/end, duration, result, and backup-ID events
with or without `--verbose`. `--verbose` before or after any operational command enables
DEBUG logging; during backup, child stdout/stderr lines are DEBUG phase/stream-labeled
events, **not INFO** (at most 4096 bytes per line, at most 1 MiB of emitted child
content; excess is drained). `--verbose` does **not** automatically redact sensitive
child content and should be enabled only when its stderr destination is protected.
Without it, child output is not streamed. Up to 1 MiB of raw child-output tail is
retained on failure in the private `.last-failure.log` in the backup root: this too can
contain credentials or other sensitive content. Command arguments/credential-file
contents are never intentionally logged by xbkeeper. The child output tail is written to
a unique private temporary file outside the XtraBackup target and removed after the run.
