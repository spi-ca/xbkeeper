# Development and release provenance

[Operator entry](../README.md) · [Arch packaging](../packaging/arch/README.md)

## Module and package roles

The existing module path `amuz.es/src/spi-ca/xbkeeper` and Go 1.24 baseline are
unchanged. There is one module, no `go.work`, and no new dependency.

- `cmd/xbkeeper`: composition root only; signal context, streams, exit status and
  the `main.Version` linker variable. Build with `go build ./cmd/xbkeeper`, not `.`.
- `internal/cli`: argument parsing, INFO/DEBUG logger selection, human/JSON
  presentation and the already-presented error marker. `Run` accepts a version
  and an `Opener`; its typed `Operations` interface contains only backup, status
  and verify operations. No filesystem mutation or concrete engine dependency.
- `internal/backup`: `Open` validates private configuration and returns a `Keeper`
  with `Backup`, `Status`, and `Verify`. Configuration, path validation, metadata,
  retention, storage/locking/fsync, integrity and Linux child lifecycle helpers
  stay private and together: their safety invariants and fault-injection tests
  are coupled. No artificial package per source file.
- `internal/backup/report`: small shared domain result types (`Backup`, `Status`,
  `Verify`, `Verification`), including their unchanged JSON tags. No I/O or
  implementation dependencies; not a public library API.
- `tests/packaging`: offline source, version, package payload, units and built-CLI
  contracts, run against the checkout or extracted source root.

Dependency direction is `cmd -> cli, backup`; both CLI and engine use `report`,
but neither depends on the other's implementation. The runtime implementation
packages are internal to this module. The engine intentionally remains
single-purpose: `Backup` sets process-global `umask(0077)`. Do not embed it into
concurrent unrelated filesystem work or interpret this split as a reusable service
framework. See [process containment](operations.md#process-lifecycle-and-containment).

## Build and test

```sh
go build -o xbkeeper ./cmd/xbkeeper
make test                 # go test ./..., race detector, go vet
make build                # static binary in build/, pinned version
make source-list          # source archive allowlist; no artifact created
```

`version` is `dev` for an ordinary Go build; the historical linker override still
works: `go build -ldflags '-X main.Version=YOUR_VERSION' ./cmd/xbkeeper`.

Tests use private temporary directories, Unix sockets and a fake XtraBackup;
they need neither MySQL nor root. The engine tests keep fault-injection helpers
private and wire the real CLI to `Open` through test-local adapters, preserving
end-to-end output assertions without exporting test-only APIs. CLI-local tests
cover parsing/version without loading config. Packaging tests also build the
actual entrypoint and check exit diagnostics and the linker version override.
These tests do **not** establish real XtraBackup compatibility or restore correctness.

## Source and documentation distribution

The Makefile enumerates Go sources across `cmd`, `internal`, and `tests`, plus
all canonical docs, examples, units and license notices. GNU make/tar and gzip
are required for deterministic source archives. Source archives intentionally
exclude checksum-bearing `PKGBUILD`, `.SRCINFO`, and GitHub workflows, avoiding
a self-referential checksum. Recipe/workflow tests skip only those unavailable
inputs in an extracted archive; runtime tests still run there. Source-list
inventory tests additionally require GNU make; without it they skip explicitly.
The documentation link and isolated package-payload tests do not require make.

Arch documentation preserves the README/docs/examples/packaging documentation
hierarchy under `/usr/share/doc/xbkeeper`. Standalone Linux binary archives carry
the same linked documentation hierarchy and both license notices. Relative links
are intended to resolve without a separate source checkout; source-code paths in
this document are descriptive, not installed file links.

## Release boundary (not an instruction to publish)

The prepared release version is `v20261027-1` (`pkgver=20261027`, `pkgrel=1`).
Its source archive, Arch checksum and generated `.SRCINFO` describe the finalized
package-separated source snapshot, not the previous `v20260926-7` tree. Preparing
these inputs does not establish that a tag, remote CI run or publication succeeded.

For each separately authorized release, select a new matching version, finalize
distributed inputs, regenerate the deterministic source archive and checksum,
then regenerate/review `.SRCINFO` and package payloads. A same-day release uses a
positive incremented revision. Source/archive directory, binary linker version,
Makefile, Arch recipe and tag must agree. `make dist` creates an archive and copies
it beside `PKGBUILD`; it does not synchronize the checksum or authorize publication.
Any subsequent distributed source or documentation edit requires regenerating the
archive and checksum again. Never bypass checksum mismatches.

PR/main CI runs Go checks and nonpublishing release/package builds. A matching
reviewed tag at the current main tip triggers release verification:
source checksum and extracted-source tests/build, static Linux amd64/arm64
binaries, isolated Arch main/debug packages and local dependency transactions.
Only successful gates allow the workflow to publish the complete set and
`SHA256SUMS`. Arch builds use `--nodeps` with fixtures; they do not prove live
backup/restore compatibility or authorize host installation/activation.
