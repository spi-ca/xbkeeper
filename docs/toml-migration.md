# TOML configuration and packaged examples

Status: configuration migration originally implemented at version 0.3.0;
the current pinned source version is v20260926-6. Independent
design and implementation reviews completed; review findings required exact-case
key checks and complete-fixture negative/escape tests. Packaging validation
requirements are recorded below.
No host installation, timer change, database access, publication or commit is included.
Review resolution: go-toml/v2 v2.3.0 `DisallowUnknownFields` matches struct
fields case-insensitively, so parsed top-level keys are checked for exact names
before strict typed decoding. Existing JSON files must be converted manually;
renaming JSON content to `.toml` is not migration.

## Scope and compatibility

- Replace only the operator configuration parser with TOML. Use pinned
  `github.com/pelletier/go-toml/v2` (also used by sibling Go projects), with
  `go.mod`/`go.sum`. Preserve its MIT notice in `LICENSES/go-toml-MIT.txt`
  and in the binary package. Do not implement a partial TOML parser.
- Flat, exact lowercase keys retain the current seven fields and semantics:
  `backup_dir`, `datadir`, `socket`, `defaults_file`, `keep`, `min_free_bytes`,
  optional `xtrabackup` (defaults to `/usr/bin/xtrabackup`). Reject duplicate,
  unknown, incorrectly cased, nested and wrongly typed fields. Keep permission,
  path, size and range checks. Only backup may initialize missing backup-root
  components beneath trusted ancestors; status/verify remain read-only. Do not leak parser input/credential content in
  diagnostics. TOML comments and integer separators should work normally.
- TOML is the sole runtime config format; do not silently fall back to JSON.
  Changing a suffix is not conversion: existing JSON content needs explicit
  operator migration. The original plan called for tool version 0.3.0 and Arch
  release 1; the current version policy supersedes that numbering with
  `v20260926-6` (`pkgver=20260926`, `pkgrel=6`).
- `xbkeeper.json` INSIDE a backup, metadata versions 1/2, `SHA256SUMS`, and JSON
  the command output now uses a common `{command,ok,data,error}` envelope with `--json`; status and verify payload fields remain inside `data`. All commands default to human-readable results. Keep strict JSON checks for metadata.
  Never bulk-replace every `.json` reference or alter existing backup artifacts.

## Ownership and paths

- Package source example: `examples/xbkeeper.toml` with concise field comments;
  accompanying `examples/README.md` describes installation and prerequisites.
- Install the generic default at `/etc/xbkeeper/xbkeeper.toml` (root-owned mode
  0600, parent directory 0700), with a pacman `backup` entry preserving modified
  operator configs across upgrades. Keep the documentation copy mode 0644 under
  `/usr/share/doc/xbkeeper/examples/`. The later MySQL template adds its own
  pacman `backup` entry and mode 0644 documentation copy without active secrets,
  a config-generation hook or automatic activation.
- Operator config: review and adapt the installed default at
  `/etc/xbkeeper/xbkeeper.toml` before use; it is not a host-specific config.
  The package now installs a comment-only option template at
  `/etc/mysql/xbkeeper.cnf` (root-owned 0600); authentication and any actual
  credentials remain separately managed. An unset user may use client defaults.
- Preserve the current package-owned systemd units under `/usr/lib/systemd/system`;
  change only ExecStart's config path to `.toml`. Existing `/etc/systemd` full-unit
  overrides can shadow that change and must be reviewed separately.
- Convert only `kubernetes-manifests/03-arch-systemd/xbkeeper/xbkeeper.json` to the
  corresponding `.toml`; update its runbook and offline contract tests. Keep the
  existing host values and the separation from the control-plane renderer.
- Preserve the user's staged systemd migration and untracked CI work. Do not
  stage/unstage/commit files, change the unit schedule/sandbox or edit live `/etc`.

## Implementation and verification order

1. Independently review this plan and the strict-decoding/dependency choice (done).
2. Implement the TOML config parser and commented example. Update Go fixture
   config generation without changing JSON metadata fixtures. Run the actual
   built CLI with a fake XtraBackup through TOML backup/status/verify first.
3. Add/adjust tests for comments, separators, escaping, duplicate/unknown/case
   keys, nested tables, scalar types and bounds, invalid legacy JSON and private
   permissions. Retain all SHA256/legacy-backup/lock/cancellation tests.
4. Update source/host docs and systemd/package contracts. Test that the default
   matches accepted configuration keys, that the package installs it privately
   with pacman backup protection and retains the documentation copy, and that
   no secrets are shipped.
5. Run Go tests/race/vet and host contract tests, validate unit syntax offline;
   include go.sum/examples/new docs in deterministic source archives and package
   docs. Keep the minimum Go version compatible with the pinned parser.
6. Verify downloaded module sums; build the Arch package in the pre-existing
   network-disabled container with a read-only, checksum-verified module cache
   and Go toolchain. Dependency acquisition is separate from that offline build.
   Normal package builds use the pinned Go module dependency, not an invented
   remote source URL or disabled checksum validation.
7. After source finalization, regenerate the matching versioned archive, package
   SHA-256 and `.SRCINFO`; inspect payload and the packaged
   binary's version. Release/publication remains out of scope; do not claim a
   remote CI run or real database backup from local fake-process checks.
