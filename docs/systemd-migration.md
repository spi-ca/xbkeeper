# systemd unit ownership migration

Status: xbkeeper-side unit/package migration implemented in the repository;
host-manifests cleanup and deployment remain separate. The user approved moving
the existing service/timer from kubernetes-manifests into the xbkeeper package.
No host installation, timer activation, backup, restore or DB restart is
authorized by this change.

## Contract

- Canonical units move to `packaging/systemd/xbkeeper.service` and
  `packaging/systemd/xbkeeper.timer`; Arch packaging installs them mode 0644 under
  `/usr/lib/systemd/system`. Package the generic default under `/etc/xbkeeper`
  (directory 0700, config 0600) with pacman backup protection for operator edits.
  The later package update adds a private, comment-only MySQL option template
  with pacman backup protection; no active credentials, install hooks,
  automatic activation. A colon-prefixed tmpfiles rule provisions the default
  backup root only if absent (without repairing existing attributes); `backup`
  may also safely initialize a missing configured root.
- Preserve the existing oneshot CLI, root identity, private umask, six-hour run
  timeout, control-group kill, sandbox and AF_UNIX-only connection policy.
  The first-stage sandbox change denies clearly unrelated capabilities (system
  administration, module/raw I/O, boot/time, network admin/raw, BPF,
  perf and mknod), retains DAC/NICE/resource capabilities and SYS_PTRACE
  (needed for complete `/proc` group inspection on `hidepid` hosts), and adds private
  devices and kernel/clock/control-group protection, namespace/SUID/realtime/
  personality restrictions and native syscall architecture. This is not a
  measured minimum. Do not add a broad syscall filter, ProcSubset, DynamicUser
  or monitoring changes without separate evidence.
- Preserve daily 03:00 host-local scheduling and Persistent=true. Enabling after
  a missed schedule may run immediately; activation remains a separate approval.
- TOML migration updates the originally recorded JSON path; default config path
  is now `/etc/xbkeeper/xbkeeper.toml`. Writable backup root remains
  `/var/backups/xtrabackup`; custom destinations require an explicit
  drop-in resetting ReadWritePaths before adding the new path. Do not weaken
  filesystem protection or auto-create credentials/data directories.
- RequiresMountsFor=/var/backups/xtrabackup orders configured mounts before the
  unit; it cannot detect an absent unconfigured mount. Operators must add an
  explicit mount assertion/drop-in for dedicated disks or custom roots.
- After=mysqld.service is ordering only. Do not add Wants/Requires or restart DB
  services. No ConditionPathExists to turn missing prerequisites into a skip.
- Host-specific TOML values, authentication, capacity, backup/restore validation
  and deployment instructions remain in kubernetes-manifests. Review the generic
  packaged config and any existing operator config before using the service.
  Remove only its two duplicate unit sources after their exact behavior is
  covered in xbkeeper tests.

## Implementation and validation

1. Independently review this plan before editing implementation.
2. Add canonical units and offline Go contract tests for unit contents and package
   installation boundaries. Include units, tests and relevant docs in the source
   distribution; keep CLI code and unrelated existing work unchanged.
3. Update Arch packaging and both repositories' ownership/install documentation.
   The manifests repository is maintained separately: remove its duplicate unit
   sources only after migrating their assertions; keep host configuration contract tests
   and assert no duplicate units remain.
4. This records the prior unit-migration handoff: preserve the completed 0.2.0
   integrity implementation and never rebuild from the stale 0.1 archive. The
   subsequent TOML migration initially bumped the source to 0.3.0; the current
   pinned source version is v20260926-7. Keep its archive, checksum and .SRCINFO
   synchronized whenever the source snapshot changes.
   Verify archive contents include integrity sources/tests, new units, TOML
   examples and module sums. Never bypass checksums or imply a release occurred.
5. Run Go tests/race/vet, source verification and source archive content checks,
   systemd unit syntax verification and calendar parsing. Do not start services.
   Before a separately approved host rollout, check an isolated service sandbox
   and perform a real XtraBackup full backup and prepare with the intended DB,
   then status/verify, cancellation, retention and failure-path checks. A fake
   child or successful syntax check cannot establish XtraBackup compatibility.
   On failure, keep the existing schedule/package and backup data intact; do
   not silently weaken unit restrictions to make an unverified backup pass.
   Verify package payload in an isolated build where available, otherwise state
   that actual package build is unverified. Run host-manifest regression tests
   separately after the manifests-side edits are settled.
6. Independently review the settled implementation and fix concrete findings.

## Existing installations and rollback

Before any separately approved host package install/upgrade, inspect whether the
timer is already enabled/active and whether a backup is currently running. A
binary replacement affects the next scheduled run even without enabling a timer;
explicitly agree how to suspend/resume an existing schedule before changing the
package. Do not interrupt a running backup or silently resume scheduling.

An existing `/etc/systemd/system/xbkeeper.service` or `.timer` shadows the packaged
`/usr/lib` unit. Host operators must compare and safely back up local files and
customizations before a separately approved removal/migration and daemon-reload.
Do not delete unknown local overrides or change enabled state automatically.
Installing the package alone does not migrate shadowing units or enable scheduling.

Retain known-good units/package and config for rollback. Disabling a timer does
not stop an active backup; removal or rollback must never delete backup data or
credentials. No current host unit state is inferred from repository changes.

## Completion boundary

Repository/package ownership, tests and docs must agree. Live activation and
successful real backup/restore remain unverified, separate gates. Kine/MySQL
hardening stays deferred until a usable backup and recovery path are established.
