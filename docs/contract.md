# Presto behavior contract, version 1

This is the compatibility surface for a future Python implementation. Implementation language is not part of the behavior or wire format. Shared fixtures in `testdata/contracts/` must pass in each implementation.

## Commands and configuration

- `build` packages the prepared working tree under a single module ZIP directory.
- `sync [target]` accepts one named local target, or selects all configured local targets when the argument is omitted. SSH targets are excluded from the no-target form.
- `deploy <target>` accepts exactly one named SSH target.
- Both synchronization commands support `--dry-run`. Explicit targets emit one result object with `--json`; the no-target sync form emits an ordered array as described below.
- `targets`, `doctor [target]`, `init [--global]`, shell completion, and `--version` support setup and inspection.
- Global flags: `--config`, `--project`/`-C`, `--json`, `--no-composer`.
- TOML schema 1 uses `module`, `files`, `composer`, and named `targets` as described in README. Unknown keys fail. Merge rule arrays by concatenation, scalar values by last explicit value, and entire named targets by replacement.
- Source content is never changed by preparation. Local and remote destination changes are restricted to `<shop>/modules/<module>`, plus the sibling `.presto-locks` directory.
- Errors return nonzero status and never emit a successful result. With lifecycle enabled, failures may emit a structured result containing `error` and a failed/not-run upgrade status. A dry run must not create or modify module/lock paths in a shop.

## File semantics

1. Resolve and validate module name (`[a-z][a-z0-9_]*`) and its `<name>.php` entry point.
2. Select the working tree with exclusions, inclusion overrides, and highest-priority preservation rules. Omit Presto configuration/transport metadata unconditionally. Never follow selected symlinks.
3. Prepare production dependencies in an isolated copy when enabled. Snapshot selected regular files with relative `/` paths, SHA-256, size, and normalized mode (0644 or 0755).
4. Inspect destination regular files, skipping preserved subtrees. Reject symlinks outside preservation rules, special files, file/directory conflicts, and destination/source case mismatches before changing module contents.
5. Create missing source files. Update files with different hashes or, on POSIX, different modes. Timestamps are irrelevant. Remove destination files absent from the selected source. Preserve rules protect destination data independently from exclusions.
6. Sort paths lexicographically in results. Empty obsolete directories may be removed after application; they are not counted as deleted files. Empty source directories are not part of the snapshot.
7. Serialize writers per destination module with an exclusive directory lock. Replace individual files via a temporary file in the same directory and rename. This is not a whole-module transaction.

Result JSON has stable fields:

```json
{
  "module": "examplemodule",
  "destination": "/srv/shop/modules/examplemodule",
  "dry_run": true,
  "created": ["new.php"],
  "updated": ["examplemodule.php"],
  "deleted": ["obsolete.php"],
  "unchanged": 2
}
```

The three path arrays are always arrays, including when empty. Destination uses the receiver's native absolute path. Root symlinks are resolved before checking overlap. Source/destination overlap is an error. A shop root must contain a regular `config/config.inc.php` and a real `modules` directory.

## Synchronizing all local targets

For `sync` without a target, resolve all configuration layers, select type=local targets and sort their names lexicographically. No local targets is an error. Validate every selected shop root and source overlap before preparation or module writes. Prepare the module and Composer dependencies once, then synchronize each target sequentially from the same snapshot using its own lifecycle settings. Do not generate config.xml, contact SSH targets or change deploy's required target argument.

Continue with later targets after an individual apply failure and return nonzero if any apply failed. Cancellation stops further targets. Configuration, root validation and shared preparation errors stop the operation before any destination changes. Previously completed targets are not rolled back.

The no-target --json output is one array, even for a single selected target. Entries contain target plus the existing result fields. A failed entry includes error and any available upgrade details, and the process still exits nonzero. Setup errors occur before array output; after cancellation only attempted targets appear. Explicit-target JSON remains one object with its existing shape. No receiver or wire-protocol change is required.

## Deployment protocol 1

Transport uses the system SSH executable with no pseudo-terminal, inheriting SSH configuration. Arguments are quoted as individual POSIX shell literals on the remote side.

First execute `<agent> agent-info --root <shop-root>`. It returns exactly one JSON object with `protocol`, `version`, `os`, and `arch`. Protocol must be 1. It validates shop-root structure without modifying files. Compatibility is based on protocol, not equal release version strings.

Then execute `<agent> receive --root <shop-root> [--dry-run]`. Stdin is one ZIP, stdout is one result JSON object, stderr is diagnostic output. A nonzero receiver/SSH exit is failure. There is no automatic bootstrap or retry after an ambiguous apply.

The deployment ZIP contains:

- Root `.presto-request.json`, a UTF-8 JSON object.
- Exactly one regular-file entry `<module>/<path>` for each declared file. No directory entries, symlinks, extra files, or duplicate ZIP names.

Metadata shape:

```json
{
  "protocol": 1,
  "module": "examplemodule",
  "version": "1.2.3",
  "files": [
    {"path": "examplemodule.php", "sha256": "<64 lowercase hex characters>", "size": 123, "mode": 420}
  ],
  "rules": {"exclude": [], "include": [], "preserve": ["uploads/"]}
}
```

Mode 420 is octal 0644; 493 is octal 0755. Local workspace/source paths are never serialized. Metadata has no destination path: the destination comes from the SSH command, not the ZIP.

Before application, reject unsupported protocol versions, invalid module names, missing entry points, invalid checksums, path traversal/absolute paths/backslashes/control characters, duplicate or case-colliding paths, files forbidden by the declared rules, and archive/manifest disagreements. Limits: 512 MiB wire ZIP, 2 GiB expanded bytes, 100,001 total entries including metadata, 16 MiB metadata. All archive validation/extraction occurs before module writes. A corrupt ZIP must leave the shop untouched.

The Go receiver can accept a future Python sender and vice versa when protocol 1 is preserved. Distribution ZIPs omit `.presto-request.json`; they are not deployment envelopes.


## Optional PrestaShop 1.6 lifecycle capability

Protocol 1 remains the file transport. An updated receiver advertises `capabilities: ["prestashop16-upgrade-v1"]` in `agent-info`. A target requesting lifecycle processing requires this capability before transfer. Ordinary deployments do not require the extension.

Targets may contain a `prestashop` table with `upgrade` (default false), `php` (default `["php"]`), `shop_context` (mandatory when enabled: `all`, `shop`, or `group`), and the corresponding positive `shop_id` or `shop_group_id` only. Named-target replacement includes the entire lifecycle table. The lifecycle setting is target configuration, not module/package metadata. A receiver must not execute a PHP command supplied inside an untrusted ZIP manifest.

For the extension, invoke `receive --root <root> --prestashop-json <JSON settings> [--dry-run]`. The JSON settings use the same field names as TOML. Quote the settings as one remote argument. The receiver validates unknown keys and scope before applying anything. The ZIP format is unchanged.

Local and remote apply share this sequence under the module lock: target-runtime lint of selected PHP files outside the Composer vendor directory (`config.vendor-dir` of the shipped composer.json, default `vendor`) → inspect native 1.6 state/context → synchronize files → run native upgrade APIs → verify/report state. Uninstalled modules are not installed; no automatic enable occurs. A known downgrade or invalid shop/context/runtime fails before module writes. The registered version is global across shops; migrations run once under the explicit native context. Successful upgrades advance to the package version, including the native no-script path after the last migration.

If finalization fails, attempt independent deactivation using the base core Module class and the captured shop IDs, including inactive shops. Verify no matching activation rows remain. Do not instantiate the broken deployed module to disable it, widen the selected scope, retry migrations automatically, or imply that partial database changes were rolled back. An unavailable database/core can prevent recovery; report that explicitly and exit nonzero. POSIX receivers cancel into recovery on SIGINT/SIGTERM/SIGHUP, allowing up to 30 seconds for independent deactivation; hard kills or server loss cannot run recovery.

Results add optional `upgrade` and `error`. `upgrade` includes `status`, `shop_context`, optional identifiers, resolved `shop_ids`, `module_id`, `installed`, `enabled_shop_ids` (null if unknown), `from_version`, `to_version`, `applied`, `disabled`, `messages`, optional `error`, and optional `recovery_error`. Failure JSON is emitted on stdout even when the receiver exits nonzero; callers must preserve it and still propagate failure. PHP/module output goes to diagnostic stderr, never result stdout. File arrays are plans and may only be partly applied on filesystem failure.

Dry runs show `planned` without invoking PHP or the database. The feature is disabled unless the target explicitly enables it; build archives and ordinary file-only sync/deploy behavior remain unchanged.

The offline test harness in `internal/testshop` contains upgrade methods from PrestaShop 1.6.1.22 under their retained OSL notice and `LICENSES-prestashop.txt`, with JSON-backed replacements for database/HTTP services. The adapter runs against these native methods on host PHP and PHP 7.1. No live shop is required.


## Generated PrestaShop module XML

Before archiving, `build` and `deploy` generate the root `config.xml` in the private prepared directory. `sync` does not generate XML. The original module checkout is never changed. A source copy of the XML is replaced in the prepared result. Add/update its SHA-256, size and 0644 mode in the snapshot, sort the file list and include `/config.xml` in the snapshot rules so protocol 1 receivers validate it normally. Preservation cannot be overridden; generating a preserved config.xml is an error. No transport extension is required.

Use the prepared module name/version and the native PrestaShop 1.6 field order and meanings. Output is byte-identical to PrestaShop 1.6.1's `Module::_generateConfigXml()`, with no trailing newline. Text fields use CDATA; displayName/description/author use PHP `htmlentities(ENT_QUOTES, 'UTF-8')` (HTML 4.01 table) with `&amp;` restored to `&`, author_uri/tab use the same entities without restoring, and confirmUninstall remains raw text, except that `]]>` is split across CDATA sections. XML must be UTF-8 without forbidden control characters. Missing fields use base Module defaults (empty text, need_instance=1, no country restriction); a getContent method sets configurability. Only a single country restriction is serialized, otherwise limited_countries is empty. Optional empty author_uri is omitted.

Metadata comes from static PHP properties/constructor assignments and literal `$this->l()` calls, with per-field overrides under `[module.config_xml]`. Never execute PHP to infer metadata. Unsupported expressions and conditional assignments require an override. An assignment is unconditional only at constructor top level, outside blocks, braceless control bodies, alternative control syntax and closures, and before any early return/exit/throw; the last unconditional assignment wins. Compound or element assignments make a field dynamic. Metadata in parent classes is not inferred; modules inheriting it must configure overrides. A version is always required for XML, including when build uses --no-version. Module version discovery uses the same data-only token reader, ignoring comments and quoted code. Localized XML files retain normal file-selection behavior.

XML generation is deterministic and also occurs during deploy dry runs without connecting to a shop database. Archive format and receiver lifecycle settings remain unchanged.
