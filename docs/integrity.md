# SHA-256 integrity extension

Status: implemented in xbkeeper 0.2.0. The Arch recipe is maintained in
`packaging/arch/` alongside the source. No real database backup or restore
was performed. Scope: file corruption detection, not signatures, database
consistency validation or automated repair.

## Artifact and creation boundary

- New backups use metadata format 2 and a root-level `SHA256SUMS` manifest.
- Flow: backup -> prepare -> verify full-prepared checkpoint -> write metadata
  -> hash all regular files except the root manifest itself -> write manifest
  exclusively -> validate -> sync the entire tree -> promote and sync parent
  -> retention. Any hash failure must not promote or prune; keep the prepared
  incomplete directory for explicit investigation. Set the preservation boundary
  before hashing; cancellation, read/write errors and a partial manifest all
  leave staging intact and block subsequent backups pending manual investigation.
  Cancellation is checked again after syncing and before promotion/retention.
  A cancellation observed after promotion keeps the completed backup and skips
  subsequent deletions; deletions already completed before cancellation cannot
  be undone. A filesystem sync already in progress is not interruptible here.
- Use streaming SHA-256, not whole-file reads, and stable lexicographic relative
  path ordering. Include metadata and XtraBackup control files in the manifest.
- Format is lowercase 64-digit SHA-256, two spaces, canonical relative filename,
  newline. It is compatible with ordinary GNU `sha256sum -c` for supported paths.
  Reject CR, LF, backslash and invalid UTF-8 in filenames instead of inventing a
  second escape grammar. For example, a line for `data file-世界` has the form
  `<64 lowercase hex digits>  data file-世界` followed by LF. Spaces and Unicode are allowed. Reject empty/absolute paths, `.`/`..`
  components, duplicate paths, a manifest self-entry and malformed digest lines.
- Manifest parsing is bounded to 16 MiB and 100,000 entries; the tree walk
  permits at most 100,001 entries including the manifest. A manifest
  records regular files, not empty directories. Structural traversal still rejects
  symlinks, hardlinks, devices and FIFOs, even if absent from the manifest.
- Traverse/open under `os.Root`, use non-following/nonblocking opens and validate
  regular file identity, owner, permissions and link count. Check file metadata
  before/after hashing to detect ordinary concurrent changes. This is not a
  guarantee against a malicious same-UID writer controlling data and hashes.
  Verification is not a filesystem snapshot: keep completed backup contents
  immutable during the audit. The lock coordinates xbkeeper processes, not
  arbitrary external writers.

## CLI contract

```sh
xbkeeper verify --config /etc/xbkeeper/xbkeeper.json
xbkeeper verify --config /etc/xbkeeper/xbkeeper.json --backup backup-UTC-RANDOM
```

Without `--backup`, check every managed completed backup. With it, accept only an
exact completed basename within `backup_dir`, never an arbitrary filesystem path
or an in-progress name. No completed backups is an error, not a successful audit.
Enumerate names under the shared lock without the fail-fast `inspect` helper:
a selected backup is independent of unrelated broken entries; an all-backups
audit reports each completed backup's result even if some fail.

- No live DB, credential-file read or XtraBackup executable is needed.
- Read-only: do not create hashes, fix files, promote, delete, or update metadata.
- Acquire a nonblocking shared flock on the existing managed `.lock` opened
  read-only/no-follow; refuse if absent/unsafe or an exclusive backup is active.
  Multiple verifies can coexist. No fallback that checks without a lock.
- Match the exact regular-file set as well as each digest: missing/extra files
  and bad content fail. Do not dump file contents, credential values or arbitrary
  manifest input to diagnostics. Report safe backup name, outcome and counts.
- Emit JSON on stdout, safe phase/timing logs on stderr; return nonzero on any
  failed or unverifiable selected backup. Example:
  `{"backups":[{"name":"backup-20260101T000000Z-0123456789abcdef","ok":true,"files":3}]}`.
  `files` counts successfully hashed files during this audit (not expected
  manifest entries; zero on structural failure). Failure reasons are fixed:
  `invalid or unsafe backup`, `checksum or file set mismatch`, or
  `legacy format 1 is unverifiable`. Missing exact selections are invalid
  results. With no completed names or no usable lock, exit nonzero without JSON.
- Check cancellation while walking/hashing large files. Hashing may add a full
  sequential read of the backup; no automatic daily rehash of old backups.

## Backward compatibility

Validate formats 1 and 2 with explicit branches (not a single equality against
the latest format constant). Retain status/retention support for format-1 backups.
They are always reported as unverifiable, even if a manifest is later added. Never auto-generate
hashes for old backups: doing so would bless their current contents. New format-2
backups must have a well-formed manifest. Cheap status/retention structural checks
must not perform a full-content hash audit; only explicit verify does so.

## Verification and packaging

1. Build the actual CLI; run a temporary fake-XtraBackup functional smoke through
   backup -> status -> verify, then corrupt a file and require verification to fail.
   No installed XtraBackup or database is invoked.
2. Test file addition/removal/corruption; metadata/control-file hashing; unsupported
   names; malformed/duplicate/traversal/self-reference manifests; symlink/hardlink/
   special-file safety; offline/no-side-effect behavior; lock conflicts; legacy
   version-1 behavior; cancellation and hash failure without old-backup pruning.
3. Compare the supported manifest format with GNU `sha256sum -c` in a fixture.
4. Run all Go tests, race detector, vet and cross-build; update source/host docs.
5. Keep the Makefile and Arch versions synchronized, regenerate the deterministic
   source archive, pin its checksum and `.SRCINFO`, then build the Arch package
   in the offline container. This document is included in the archive. These
   checks do not install a package or activate a timer.

A successful SHA-256 check proves agreement with the stored manifest, not trusted
provenance, disk redundancy or restore correctness. Keep independent recovery
tests and external storage as separate operational requirements.
