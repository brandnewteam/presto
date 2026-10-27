# Presto

A Go CLI for developing PrestaShop modules in repositories separate from shop installations.

```text
presto build
presto sync
presto sync local --dry-run
presto sync local
presto deploy production --dry-run
presto deploy production
```

`sync` copies into a **local** shop. `deploy` sends **one ZIP over SSH** to a Go receiver on a remote shop. Both compare SHA-256 content hashes, overwrite local debugging edits, delete obsolete code files, and leave configured runtime paths alone. By default neither command installs/upgrades modules in PrestaShop or changes its database or cache. Optional per-target PrestaShop 1.6 finalization can run native module upgrades after synchronization.

## Install

Install or update directly from GitHub with Go 1.24 or newer:

```sh
go install github.com/brandnewteam/presto@latest
```

Go installs the executable into `GOBIN` when set, otherwise into `$(go env GOPATH)/bin` (usually `~/go/bin`). Add that directory to your PATH, then run `presto --help`. Repeat the install command to update, or replace `@latest` with a published version tag to select a specific release.

On macOS or Linux, you can choose a different installation directory:

```sh
mkdir -p "$HOME/.local/bin"
GOBIN="$HOME/.local/bin" go install github.com/brandnewteam/presto@latest
```

Ensure `~/.local/bin` is on your PATH when using this location.

`presto --version` and receiver metadata use the module version embedded by Go, including release tags and revision-based pseudo-versions, without the leading `v`. The release builder can override this value. Builds without a module version report `dev`.

To build from a local checkout instead:

```sh
go build -o bin/presto .
./bin/presto --help
```

Put the executable in a directory on your PATH. A prebuilt executable requires no Go installation, Bash, Python, rsync, zip, or unzip. Composer/PHP (or Docker with a Composer image) are needed locally only when preparing Composer dependencies. Remote deployment additionally needs local OpenSSH and a matching remote Presto receiver. Targets opting into native module upgrades also need a compatible PHP CLI on the target machine.

The release builder uses Go rather than a shell script:

```sh
go run ./tools/release -version 0.1.1
```

It produces macOS, Linux, and Windows executables for amd64 and arm64 in `dist/`, plus `checksums.sha256`. macOS is the primary development platform. Native Windows build/local sync are included in CI; the POSIX SSH stand-in test is skipped on Windows. Cross-compiling alone does not establish Windows runtime compatibility.

## Configure targets once

```sh
presto init --global
```

Edit the created file. Its default location is:

| Platform | Default global configuration |
| --- | --- |
| macOS | `~/Library/Application Support/presto/config.toml` |
| Linux | `~/.config/presto/config.toml` |
| Windows | `%AppData%\presto\config.toml` |

`XDG_CONFIG_HOME` overrides the base directory on any platform. Set `PRESTO_CONFIG` or pass `--config /path/to/config.toml` to keep configuration in your dotfiles instead. An explicitly selected missing config is an error.

```toml
schema = 1

[targets.local]
type = "local"
root = "~/Sites/prestashop"

[targets.production]
type = "ssh"
host = "shop-server"
root = "/var/www/prestashop"
agent = "~/.local/bin/presto"
```

These are **shop roots**. Presto appends `modules/<module-name>` and checks that `modules/` and `config/config.inc.php` exist. Targets have names rather than positional environment indexes. There is no default production destination, interactive confirmation, or automatic server installation.

SSH uses the system client and its existing aliases, keys, agent, host-key checks, proxy settings, and configuration. `ssh_args = ["-p", "2222"]` can supply additional SSH arguments. `agent` is one remote executable path, not a shell command; use an absolute path or `~/` if it is outside the remote PATH.

For Windows local targets, TOML literal strings make paths convenient:

```toml
[targets.local]
type = "local"
root = 'C:\dev\prestashop'
```

## Sync all local installations

Run `presto sync` without a target to synchronize the current module to every configured **local** target, in alphabetical order. SSH targets are handled through `deploy <target>` and are not selected by this command. Configuration layers and project overrides apply normally.

```sh
presto sync
presto sync --dry-run
presto sync --json
presto sync local
```

All selected shop roots and source/destination overlaps are checked before any synchronization starts. Presto prepares one shared snapshot, including production Composer dependencies when enabled, and then applies it sequentially with each target's own PrestaShop lifecycle settings. Sync does not generate `config.xml`.

An error during an individual target's synchronization is reported while the remaining targets continue; the command exits nonzero if any target fails. Cancellation stops further targets. Invalid configuration, an invalid shop root, or preparation failure stops the command before any target is changed. There is no rollback of targets already synchronized.

With `--json`, the no-target form emits one array in target order. Each entry contains `target` plus the normal result fields; failed entries also contain `error` and any available upgrade details. The no-target form always emits an array, even with only one local target. The array is still emitted when a target fails, with a nonzero exit status. Setup failures occur before result output; after cancellation the array includes only attempted targets. `presto sync <target> --json` keeps its existing single-object output. With no local targets configured, `presto sync` returns an error.

## Dry runs and diffs

`--dry-run` on `sync` and `deploy` lists the planned changes without touching the shop. Each updated file is followed by a unified diff from the shop's copy to the new one, with three lines of context like `diff -u`:

```text
Would synchronize examplemodule → local (/Users/me/Sites/prestashop/modules/examplemodule)
  + views/new.tpl
  ~ examplemodule.php
    --- a/examplemodule.php
    +++ b/examplemodule.php
    @@ -1,3 +1,3 @@
     <?php
    -$this->version = '1.2.2';
    +$this->version = '1.2.3';
     function helper() {
  ~ views/img/logo.png
    binary file differs: 5120 → 4896 bytes
  - obsolete.php
1 created, 2 updated, 1 deleted, 3 unchanged
```

Created and deleted files are listed without their contents. Files with a NUL byte or invalid UTF-8 show `binary file differs` with both sizes, files over 4 MiB show `file too large to diff`, and on macOS/Linux a permission change shows `file mode changed: 0644 → 0755`. Each diff stops after 400 lines (`… N more lines`); after 2000 lines in total, the remaining updated files show `diff omitted: output limit reached`, so a large vendor update does not flood the terminal. Preserved paths are never read or diffed. Control characters in file content are printed as `�`.

`--color=auto|always|never` (default `auto`) colors removed lines red, added lines green, hunk headers cyan and file headers bold. `auto` colors only when stdout is a terminal, `NO_COLOR` is unset or empty and `TERM` is not `dumb`; `always` and `never` override both.

With `--json`, a dry run adds a `diffs` object mapping each updated path to the same text, uncolored and with the same limits; it is omitted when nothing is updated and on real runs.

For `deploy`, the diff is computed by the receiver on the server. A receiver from Presto 0.2.0 or earlier cannot do this: the dry run still works, and Presto prints `receiver 0.2.0 too old to show diffs` on stderr. Update the remote binary to see them.

## Module configuration

From a module repository, optionally run `presto init`. Usually, the module name comes from the directory and the version from a literal `$this->version = '1.2.3';` assignment in `<module>.php`. PHP source is never executed to discover metadata; dynamic metadata can be supplied through TOML. Repositories named differently from their module need an explicit name:

```toml
schema = 1

[module]
name = "examplemodule"
# version = "3.2.1" # Only needed if PHP uses a dynamic version.

[files]
exclude = ["src/debug.php", "assets/source/"]
include = [".env.example"]
preserve = ["views/cache/"]

[composer]
enabled = true
scripts = false
plugins = false
require_lock = false
# command = ["/path/to/php7.1", "/path/to/composer.phar"]
```

Configuration loads in this order: built-in defaults → global config → `presto.toml` → `presto.local.toml`. Scalars override earlier values; file-rule arrays append; a target definition replaces the entire earlier target with the same name. `presto.local.toml` is for private machine settings and should be Git-ignored. Unknown keys, unsupported schemas, and malformed patterns fail before synchronization.

Local target paths are relative to the configuration file defining them. A leading `~/` expands to the local home directory. Remote roots are interpreted on the receiver; prefer absolute paths or `~/`. Arbitrary environment-variable interpolation and shell expansion are not supported. File patterns always use `/`, including on Windows.

Commands discover `presto.toml` above the current directory, or a parent containing a PHP entry point matching its directory name. Use `--project /path/to/module` (`-C`) to work from elsewhere.

## Generated module metadata

`build` and `deploy` generate a fresh English `config.xml` in the prepared temporary copy. The module checkout is never modified. `sync` does not generate it and keeps its existing file rules. A deploy dry run still prepares the XML but does not modify the destination.

The XML uses PrestaShop 1.6's metadata layout: module name, version, display name, description, author, optional author URI and uninstall confirmation, tab, configurability, instance requirement and country restriction. The output is byte-identical to PrestaShop 1.6.1's `Module::_generateConfigXml()`, including `htmlentities(ENT_QUOTES)` escaping (`&#039;`, `&quot;`, `&egrave;`) and no trailing newline. A literal `]]>` in `confirm_uninstall`, which the native generator would emit as invalid XML, is split across CDATA sections. A single limited country is serialized; multiple restrictions produce an empty `limited_countries`, matching the native 1.6 generator.

Presto reads literal class defaults and constructor assignments, including `$this->l('literal text')`, without executing module PHP. Comments, other classes and quoted code are ignored. Absent fields use the base Module defaults (`need_instance = true`, empty text/country restriction); `getContent()` implies configurability. An assignment is static when it always executes: at the top level of the constructor, not inside a block, a braceless `if`/`else`/loop body, alternative control syntax (`if (...):`), a closure, or after an early `return`/`exit`/`throw`. Control blocks before it do not matter, and a later unconditional assignment replaces an earlier one, as in PHP. Compound and element assignments (`.=`, `[] =`) make a field dynamic. Dynamic expressions, conditional assignments, or inherited metadata outside the entry-point class need explicit overrides:

```toml
[module]
version = "1.2.3" # Required here only when PHP computes the version dynamically.

[module.config_xml]
display_name = "Example module"
description = "An example module description."
author = "Example author"
# author_uri = "https://example.com"
# tab = "front_office_features"
# confirm_uninstall = "Remove this module?"
# is_configurable = true
# need_instance = false
# limited_countries = ["FR"]
```

Each override is optional and merges across configuration layers by the last explicit value. A module with inherited metadata should declare those values here. A dynamic module version must be supplied with `module.version` or `build --module-version`; `--no-version` changes only the ZIP filename and does not remove the XML's version requirement.

The generated root `config.xml` replaces any selected source copy and is explicitly included in build/deploy manifests despite the default exclusion. Preservation rules still win: preserving `config.xml` is an error for build/deploy. Existing localized `config_<language>.xml` files retain the normal selection rules; Presto does not generate translations.

## File rules

`exclude` omits files from the package/source set. `include` can override an exclusion. **`preserve` wins over both**: matching files are neither shipped nor overwritten nor deleted at the destination.

For example, excluding `tests/` means an old destination copy of those tests is removed. Preserving `uploads/` means existing uploads survive and repository uploads are not copied. Files missing from the selected source set are deleted unless preserved. Direct edits to non-preserved destination files are overwritten on the next sync/deploy, even if timestamps and sizes match.

Patterns use `*`, `?`, character classes, and `**`. A pattern without an internal `/` matches a name anywhere in the tree; a pattern containing `/` is relative to the module root. A leading `/` explicitly anchors a basename at the root. Matching a directory also matches its descendants. A trailing `/` (or `/**`) matches directories only: `dist/` excludes directories named `dist` and their contents, but not a regular file named `dist`. Symlinks match like directories. `cache/` matches directories named `cache` anywhere; `/cache/` matches the root cache; `views/cache/` matches that exact subtree. Trailing `/**` also selects the whole subtree. There are no ordered `!` negation rules: use `include` instead.

Defaults exclude `.git/`, `.gitignore`, `.svn/`, `.hg/`, `.github/`, `.vscode/`, `.idea/`, `node_modules/`, `dist/`, `deps/`, `tests/`, `.DS_Store`, `.env`, `.env.*`, `.rsyncrc`, `.prestarc*`, `.tags*`, `.editorconfig`, `.tm_properties`, `.eslintrc*`, `.jshintrc`, `.jscsrc`, `build.sh`, `stage.sh`, `todo.md`, `TODO.md`, and `config.xml`. Defaults preserve **`uploads/`**. Appending an empty array does not remove defaults. Presto config and transport metadata are always omitted.

Git ignore rules are not consulted: ignored production `vendor/` files may be needed. Old `.prestarc`/`.rsyncrc` files are not executed or imported automatically; translate any project-specific rules into TOML.

Selected symlinks and special files are rejected. Preserved destination symlinks are left alone. Symlink module roots/modules directories, file/directory conflicts, and destination/source case mismatches require manual resolution. Runtime directories should exist or be created by the application; source-only empty directories are not packaged. Portable file paths and case collisions are checked before packaging; executable files use mode 0755 and other files 0644.

## Composer production preparation

All three operations prepare dependencies by default when `composer.json` exists:

1. Copy selected module files and Composer inputs into a fresh private temporary directory.
2. Run `composer install --no-dev --prefer-dist --optimize-autoloader --no-interaction --no-progress --no-scripts --no-plugins` there.
3. Package/sync that prepared result, including production dependencies and autoload files.

The checkout's lock file and vendor tree remain untouched. Existing vendor/bin directories are not copied into preparation; relative custom Composer vendor/bin directories are supported. Composer file/vendor/bin environment variables are scoped to the temporary copy. The configured command is an argument array, never a shell expression. The standard Windows `composer.bat` installation is handled by invoking its adjacent `composer.phar` with PHP directly; custom launchers can use `command = ['php', 'C:/path/to/composer.phar']`. Package requirements are respected; for PHP 7 modules, configure a compatible PHP/Composer executable or Docker image rather than silently ignoring platform requirements.

With Composer 2.2, the last release that runs on PHP 7.1, every fresh install picks a random autoloader class suffix. Unless `composer.json` sets `config.autoloader-suffix`, Presto pins one derived from the module name and lock file in the temporary copy, so repeated syncs do not rewrite `vendor/autoload.php` and builds stay reproducible. The packaged `composer.json` is the original.

### Composer in Docker

To avoid installing old PHP versions locally, run Composer in an image that has the module's PHP version, Composer and the zip extension or `unzip`:

```sh
docker build -t presto-composer:php7.1 -f examples/composer-php71.Dockerfile examples
```

```toml
[composer]
docker_image = "presto-composer:php7.1"
# docker_args = ["--platform", "linux/amd64"] # For example, for an amd64-only image.
```

This can live in the global configuration and be disabled per module with `docker_image = ""`. `command` (default `["composer"]`) then runs inside the container with the same install flags. Presto runs `docker run --rm --init`, mounting only the private temporary copy at `/presto/work` and a persistent Composer cache from the user cache directory (for example `~/Library/Caches/presto/docker`) at `/presto/cache`. The temporary copy is created below that cache directory so Docker VMs that share only the home directory (such as Colima) can mount it. On macOS/Linux the container runs with your user and group IDs. `COMPOSER_AUTH` is forwarded when set; host `auth.json` files and SSH keys are not available inside the container. `docker_args` are passed before the image name, for example `--platform` or `--network`. Canceling removes the container. `presto doctor` reports the Docker setup.

Set `scripts = true` and/or `plugins = true` only when the module needs them; these enable Composer code execution as usual. A missing lock can be resolved in the temporary directory; set `require_lock = true` for reproducible dependency selection. Relative path repositories must remain resolvable from the temporary layout; external build systems/frontend compilation are not run automatically.

Use `--no-composer`, or `[composer] enabled = false`, to package the current vendor contents as-is. That mode does not verify that development dependencies were removed. A Composer failure stops the operation before the shop is changed. Dry runs still prepare Composer and may download dependencies; they do not change destination module files.

## Optional PrestaShop 1.6 finalization

Both `sync` and `deploy` can run PrestaShop's native module upgrade process after copying files. This is off by default and configured independently for each target. `build` never runs shop lifecycle operations.

```toml
[targets.production.prestashop]
upgrade = true
php = ["/usr/bin/php7.1"]
shop_context = "all"
```

The same nested table works under a local target such as `targets.local`. `php` is an executable/argument array on the **target machine**, separate from the local Composer command. It defaults to `["php"]`; configure the PHP version compatible with the shop and module. Custom PHP command prefixes must see the shop, prepared snapshot, and private adapter temporary files at the same absolute paths; Docker wrappers therefore need matching mounts. Wrappers must propagate exit status and termination to their PHP worker; detached worker processes are not supported. No HTTP endpoint, back-office credentials, or permanent PHP helper is installed.

The context is required when `upgrade = true`; it is never inferred:

| `shop_context` | Required identifier | Meaning |
| --- | --- | --- |
| `"all"` | None; IDs must be omitted | Native all-shops context |
| `"shop"` | `shop_id = 2` | One explicitly selected existing shop |
| `"group"` | `shop_group_id = 1` | One explicitly selected nonempty shop group |

Use your actual IDs. Invalid/mixed/missing context settings fail before synchronization. `all`/`group` resolve existing shops, including inactive shops, for failure deactivation. Migration helpers retain PrestaShop 1.6's own active-shop behavior. The context's default shop is used as its representative when available, otherwise the first selected shop.

**PrestaShop 1.6 stores one module version across all shops.** The configured context affects native configuration/hooks and deactivation scope, not a separate per-shop module version. Run a migration once under the intended scope; do not expect the same version's migrations to rerun automatically for another shop. Module upgrade scripts themselves can explicitly access global data or change context, as they can from the back office.

With finalization enabled, the module lock covers the whole sequence:

1. Validate the archive/file plan and lint **the module's selected PHP files using the target PHP runtime** before replacing module files. Files in the Composer vendor directory (`config.vendor-dir` of the prepared `composer.json`, otherwise `vendor/`) are skipped: Composer already resolved them for the platform, and packages such as Symfony polyfills ship `bootstrap80.php` files that only newer PHP versions load or can parse. Several lint processes run in parallel; the first failure in path order is reported.
2. Bootstrap PrestaShop and inspect the installed module record and selected shop context, without constructing the old module class. Reject unsupported PrestaShop versions, invalid shop IDs, and known downgrades before copying.
3. Copy/update/delete module files using the normal preservation rules.
4. Instantiate only the deployed module and call native `Module::initUpgradeModule()` / `runUpgradeModule()`. Run scripts in native version order. If no upgrade scripts exist, use the native version-registration path. After successful scripts, finish registering the package version without rerunning applied scripts.
5. Report versions, applied script count, selected shop IDs, and enabled shop IDs. An uninstalled module remains uninstalled; no automatic installation or re-enabling is performed. Pending upgrades are checked even when the file plan has no changes.

If the upgrade throws, exits early, reports failure, or is canceled, Presto starts a **separate recovery PHP process** that uses the core base `Module` class to disable the module without instantiating its broken class again. Deactivation is limited to the shop IDs captured before copying, includes inactive shops in `all`/`group`, and is verified by reading the activation associations. Recovery gets up to 30 seconds even after cancellation. The POSIX receiver handles SIGINT, SIGTERM, and SSH hangup (SIGHUP); hard kills and machine loss cannot run recovery. If the framework/database cannot be loaded or deactivation cannot be verified, the operation fails and explicitly reports that recovery also failed; it does not claim that the module was disabled.

Preflight errors leave module files and activation untouched. Filesystem errors before finalization report `not_run`; the normal partial-file-update limitation still applies. Failed upgrades leave copied files and any completed database changes in place. Native 1.6 exceptions can leave partial migration changes even without advancing the registered version. There is no automatic retry or file/database rollback: inspect the failure before trying again. Arbitrary module code can perform side effects just as it can in the back office.

Dry runs **do not execute PHP, bootstrap the shop, inspect its database, or perform upgrades/deactivation**. They return `upgrade.status = "planned"` with the configured context; runtime shop IDs/version/state remain unknown until an actual apply. Composer preparation retains its usual dry-run behavior.

SSH receivers must advertise `prestashop16-upgrade-v1`. Update the remote Presto binary before using this setting; an older receiver is rejected before sending the ZIP. File-only protocol 1 deployments remain compatible.

`--json` adds an `upgrade` object with status (`planned`, `preflight_failed`, `not_run`, `upgraded`, `up_to_date`, `not_installed`, or `failed`), context/shop IDs, module ID, installed state, from/to registered versions, applied count, messages, and recovery details. `enabled_shop_ids` is null when unknown, an array when inspected/verified. `disabled = true` confirms failure recovery in the selected context; it is not an indication that a successful deployment enabled a module. An error result still exits nonzero. The file arrays describe the computed plan; after filesystem failure they do not imply that every change was applied.

## Build

```sh
presto build
presto build --no-version
presto build --module-version 3.2.1 --output /tmp/module.zip
```

Default output is `dist/v<version>-<module>.zip`, or `dist/latest-<module>.zip` with `--no-version` (`-n`). ZIP entries live under `<module>/`, include the generated `config.xml`, use stable ordering/timestamps, and contain no deployment transport metadata. Identical prepared inputs produce identical ZIP bytes. Existing output is replaced only after the new ZIP has been written successfully.

## Remote receiver setup

The server needs the matching Presto executable and permission to write the target module directory. For file-only deployment it does **not** need Go, Composer, rsync, PHP CLI, or unzip. Opting into PrestaShop lifecycle finalization adds the PHP CLI requirement on the server. Prepare Composer dependencies on the development machine.

Build release binaries, check the server architecture, then install the correct Linux binary once. For example, for an amd64 server using the `shop-server` alias:

```sh
ssh shop-server uname -m
ssh shop-server 'mkdir -p ~/.local/bin'
scp dist/presto-linux-amd64 shop-server:.local/bin/presto
ssh shop-server 'chmod 755 ~/.local/bin/presto'
presto doctor production
```

Use `presto-linux-arm64` for an aarch64 server. These setup commands are examples; Presto never runs them implicitly. Receiver and client must support the same transport protocol; `doctor` and `deploy` check it before sending the ZIP. Deployment is supported to POSIX SSH hosts; native Windows remote receivers are not a v1 target.

Deploy streams a ZIP on SSH stdin, validates paths, sizes, checksums, and metadata into private temporary storage, then runs the same sync engine as local copying. The receiver creates no permanent uploaded archive or application-side helper script. The limits are 512 MiB compressed, 2 GiB expanded, and 100,000 module files.

Writes replace individual files atomically; the entire module update is **not** one atomic transaction. Obsolete files are deleted after writes. An interruption or filesystem failure can leave a partially updated module; inspect and retry. Cooperative locks in `<shop>/modules/.presto-locks/<module>` prevent concurrent Presto writers. A killed receiver may leave a lock: verify no operation is running before manually removing that module's empty lock directory. Presto does not silently break locks or change directory ownership.

## Inspection, completion, and development

```sh
presto targets
presto doctor
presto doctor local
presto completion zsh
presto completion powershell
presto sync local --dry-run --json
go test ./...
go test -race ./...
go vet ./...
```

`targets` does not connect to servers. `doctor <ssh-target>` connects for receiver/root checks without changing module files. JSON goes to stdout; Composer logs and errors go to stderr. Failures exit with status 1. Without `--json`, a failed target also prints its planned file changes and any PrestaShop upgrade and recovery state (disabled shops, recovery errors) on stderr, because nothing is rolled back. Lifecycle failures additionally return a JSON result with an `error` and failed upgrade status when `--json` is used; always check the exit status.

There is no legacy `validate` command in v1. This does not assume that the old Validator endpoint still works.

The [behavior and transport contract](docs/contract.md) and [shared fixtures](testdata/contracts/sync.json) are intentionally language-neutral so a future Python implementation can be compared against the same expectations.
