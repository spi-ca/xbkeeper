# systemd unit ownership migration

Status: xbkeeper-side unit/package migration implemented in the repository;
host-manifests cleanup and deployment remain separate. The user approved moving
the existing service/timer from kubernetes-manifests into the xbkeeper package.
No host installation, timer activation, backup, restore or DB restart is
authorized by this change.

## Contract

- Canonical units move to `packaging/systemd/xbkeeper.service` and
  `packaging/systemd/xbkeeper.timer`; Arch packaging installs them mode 0644 under
  `/usr/lib/systemd/system`. No install hooks, credentials, config, directory
  provisioning, enabling or starting units.
- Preserve the existing oneshot CLI, root identity, private umask, six-hour run
  timeout, control-group kill, sandbox and AF_UNIX-only connection policy.
- Preserve daily 03:00 host-local scheduling and Persistent=true. Enabling after
  a missed schedule may run immediately; activation remains a separate approval.
- Default config path remains `/etc/xbkeeper/xbkeeper.json`. Writable backup root
  remains `/var/backups/xtrabackup`; custom destinations require an explicit
  drop-in resetting ReadWritePaths before adding the new path. Do not weaken
  filesystem protection or auto-create credentials/data directories.
- After=mysqld.service is ordering only. Do not add Wants/Requires or restart DB
  services. No ConditionPathExists to turn missing prerequisites into a skip.
- Host JSON, authentication, capacity, backup/restore validation and deployment
  instructions remain in kubernetes-manifests. Remove only its two duplicate unit
  sources after their exact behavior is covered in xbkeeper tests.

## Implementation and validation

1. Independently review this plan before editing implementation.
2. Add canonical units and offline Go contract tests for unit contents and package
   installation boundaries. Include units, tests and relevant docs in the source
   distribution; keep CLI code and unrelated existing work unchanged.
3. Update Arch packaging and both repositories' ownership/install documentation.
   The manifests repository is maintained separately: remove its duplicate unit
   sources only after migrating their assertions; keep host JSON contract tests
   and assert no duplicate units remain.
4. Preserve the completed 0.2.0 integrity implementation; never rebuild from the
   stale 0.1 archive or revert existing code. Increment the package release for
   the added units, regenerate the 0.2.0 source archive and verify that it contains
   integrity sources/tests plus the new units before pinning its checksum.
   Keep package version, source filename, checksum and .SRCINFO consistent; do
   not publish or imply a release occurred. Never bypass checksums.
5. Run Go tests/race/vet, source verification and source archive content checks,
   systemd unit syntax verification and calendar parsing. Do not start services.
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
