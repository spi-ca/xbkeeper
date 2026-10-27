# xbkeeper

A small Linux-only full-backup wrapper for Percona XtraBackup. It runs one full
backup, prepares it, keeps a configured number of completed backups, and exits.
No daemon, restore, incremental, or remote storage support. **Not a substitute
for restore drills or an independently stored backup.**

## Start here

Requires Linux, a compatible installed XtraBackup, sufficient OS/database
privileges, and space for multiple backups. To build from source with Go 1.24+:

```sh
go build -o xbkeeper ./cmd/xbkeeper
./xbkeeper version
```

Building does not install or activate anything. Review the
[configuration example](examples/xbkeeper.toml),
[MySQL option template](examples/xbkeeper.cnf), and
[installation prerequisites](examples/README.md) before use. The Arch package
supplies private generic defaults, units and a tmpfiles rule, but no active
credentials, DB account, or automatic timer activation.

```sh
xbkeeper backup --config /absolute/path/to/xbkeeper.toml
xbkeeper status --config /absolute/path/to/xbkeeper.toml
xbkeeper verify --config /absolute/path/to/xbkeeper.toml
```

Omitting `--config` uses `/etc/xbkeeper/xbkeeper.toml`; the config must already
exist and be private. Only `backup` may initialize a missing backup root.
`status` and `verify` work offline and never initialize it. Commands print human
results by default; automation must use `--json` and check both exit status and
`ok`. Detailed command, JSON, error and logging contracts are in
[Commands and output](docs/cli.md).

## Operate safely

- Review [configuration, process containment, promotion and retention](docs/operations.md).
- Use the canonical [integrity contract](docs/integrity.md) to understand format-1
  compatibility, format-2 manifests and the limits of verification.
- Protect logs: `--verbose` streams bounded DEBUG child output that may include
  secrets; the private failure tail may also contain sensitive content.
- Investigate incomplete stages and failed jobs before retrying. A nonzero result
  after promotion can leave a new backup present; inspect status first.
- Host configuration, credentials, mount assertions, restore validation and
  deployment approval are operator responsibilities, not supplied by this project.

## Develop and package

See [package roles, tests and release provenance](docs/development.md) and
[Arch packaging](packaging/arch/README.md). Historical migration records:
[systemd ownership](docs/systemd-migration.md) and
[TOML configuration](docs/toml-migration.md).

Licensed under BSD-3-Clause (`LICENSE`); the linked TOML parser uses MIT
(`LICENSES/go-toml-MIT.txt`).
