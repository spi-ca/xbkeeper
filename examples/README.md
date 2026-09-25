# Example configuration (inactive)

`xbkeeper.toml` is a commented example, not an installed configuration. After
reviewing local paths and capacity, manually copy it to
`/etc/xbkeeper/xbkeeper.toml` in a root-owned directory (0700) as a root-owned
private file (0600). Create the backup directory in advance, root-owned mode
0700. Supply `/etc/mysql/xbkeeper.cnf` separately, root-owned mode 0600, with
reviewed XtraBackup connection settings. Do not place credentials in this
example or the checkout. Check socket, MySQL authentication, XtraBackup version
compatibility and an isolated restore before relying on a backup.

Existing JSON config content must be converted manually to TOML; renaming a
JSON file does not convert its format. The package never copies or activates
this example under `/etc` and never enables the timer. See the [configuration
contract](../README.md#configuration) and [Arch installation boundaries](../packaging/arch/README.md).
