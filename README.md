<div align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="./assets/logo-codex-transfer-dark.png" />
    <img src="./assets/logo-codex-transfer.png" width="520" alt="codex-transfer logo" />
  </picture>
  <h1>codex-transfer</h1>
  <p>Move your local Codex data between computers, directly from the terminal.</p>
  <p><strong>English</strong> | <a href="./README.zh-CN.md">简体中文</a></p>
</div>

## Install

Install codex-transfer on both computers. Native binaries support **macOS, Linux, and Windows** on **amd64 and arm64**.

### npm

```sh
npm install --global codex-transfer
```

Requires Node.js 22 or newer. Downloads the native executable for your platform.

### Homebrew · macOS

```sh
brew tap waterkyuu/codex-transfer https://github.com/Waterkyuu/codex-transfer.git
brew install --cask waterkyuu/codex-transfer/codex-transfer
```

### macOS and Linux

```sh
curl -fsSL https://github.com/Waterkyuu/codex-transfer/releases/latest/download/install.sh | sh
```

Installs to `~/.local/bin`. Add this directory to your `PATH` if needed.

### Windows · PowerShell

```powershell
irm https://github.com/Waterkyuu/codex-transfer/releases/latest/download/install.ps1 | iex
```

Installs to `%LOCALAPPDATA%\Programs\codex-transfer` and adds it to your user `PATH`. Open a new terminal after installation.

### Windows · Scoop

```powershell
scoop bucket add codex-transfer https://github.com/Waterkyuu/codex-transfer.git
scoop install codex-transfer/codex-transfer
```

You can also download a native archive from [Releases](https://github.com/Waterkyuu/codex-transfer/releases). See the [installation guide](./docs/installation.en-US.md) for upgrades and custom installation locations.

## Features

| Feature | What you can do |
| --- | --- |
| **Direct IP connection** | Transfer between two computers over IPv4 or IPv6, without setting up SSH or a cloud service. |
| **Cross-system migration** | Move common Codex state between macOS, Windows, and Linux; translate known config, session, and database paths. |
| **Parallel processing** | Use multiple CPU workers for compression, decompression, and metadata; choose compression effort to match your network. |
| **Address discovery** | List local interface addresses and copy a ready-to-use send command. |
| **Both-terminal approval** | Compare and enter matching pairing codes on both computers before data can be sent. |
| **Startup checks** | Check that Codex is stopped; the sender waits for the receiving terminal to start. |
| **Live transfer progress** | View percentage, transferred bytes, total size, throughput in MiB/s, and elapsed time. |
| **Encrypted transfer** | Protect the connection with TLS 1.3 and verify the archive with SHA-256 before installation. |
| **Backups and recovery** | Keep source data and back up replaced destination roots; roll back ordinary installation failures. |
| **Custom data locations** | Add external data directories and choose their destinations on the receiving computer. |
| **Session selection** | Match session creation dates and project-name prefixes or substrings, then merge into existing receiving data. |

## Quick start

Quit Codex Desktop, CLI, IDE extensions, and its background daemon on **both computers**. Keep them closed until the transfer finishes.

**1. On the destination computer**, find a reachable IP and start receiving:

```sh
codex-transfer address
codex-transfer receive
```

**2. On the source computer**, connect to that IP:

```sh
codex-transfer send 192.168.1.20
```

Replace `192.168.1.20` with the destination's IP. The default port is TCP `7432`; both computers must be able to reach this port. Either terminal may start first. The sender waits up to five minutes for the receiver by default.

**3. Verify the connection on both screens.** Each terminal displays a 12-character pairing code. Enter the code shown on the **other computer** at each prompt. The codes must match. Transfer begins after both approvals and the receiver's readiness check.

Example progress:

```text
42.0% | 84.00 / 200.00 MiB | 18.60 MiB/s | 5.0s
```

Wait for **`Migration complete`**. A 100% progress line means the network transfer has finished; the receiver still verifies and installs the data before confirming completion.

## What gets transferred

Existing files in these data roots are included:

| Data | Location |
| --- | --- |
| Conversations, history, configuration, file-based credentials, plugins, and worktrees | `CODEX_HOME`, usually `~/.codex` |
| User skills and agent configuration | `~/.agents` |
| Projectless chat workspaces | `~/Documents/Codex` |
| Desktop application data | Conventional platform-specific Codex locations, including macOS preferences and cache |
| External SQLite state and logs | Recognized top-level configuration paths or `CODEX_SQLITE_HOME` |

Inspect the exact locations on your computer:

```sh
codex-transfer paths --all
codex-transfer check
```

Destination roots are **replaced as a batch, rather than merged**. Existing data is kept in adjacent `.backup-*` files or directories; the source remains intact. Symbolic links retain their targets; cross-system imports into Windows materialize links whose targets are inside transferred roots. External link targets must be included explicitly. Runtime sockets are skipped and reported.

**macOS, Windows, and Linux can transfer to one another.** Common data roots are installed in the destination's local locations. Known paths in configuration, rollout metadata, and Codex SQLite tables are translated; conversation text and credentials are not rewritten. Existing Zstandard rollouts keep their original files and gain adapted plain siblings.

For a different source OS, the complete unchanged snapshot and its manifest are retained under `~/.codex-transfer/imports/<source-os>`. Foreign desktop settings and other roots without an explicit portable destination remain in this archive. Use `--map` when you deliberately want to install a named root elsewhere. OS keychain credentials, OS-specific plugin executables, and repositories outside selected roots require signing in, reinstalling, or copying separately.

## Cross-system paths and transfer speed

The basic `send` and `receive` commands work across supported operating systems. Home and data-root paths are mapped automatically. Map an external project prefix when the destination directory layout differs:

```powershell
codex-transfer receive --path-map "/Users/alice/dev=D:/dev"
```

By default, parallel gzip compression uses the runtime's CPU budget. Tune the sender's compression independently of worker count:

```sh
codex-transfer send 192.168.1.20 --workers 0 --compression fast
```

`fast` favors throughput; `balanced` and `best` spend more CPU to reduce network bytes. `none` avoids compression overhead for already-compressed files or a very fast local connection. Set `--workers 4` on either terminal to limit parallel processing. Queues are bounded; memory grows with worker count. Progress reports network bytes and speed; the final overall rate also includes preparation and installation. Actual throughput depends on the data, disks, CPU, and network.

## Choose your connection and data locations

### Transfer matching sessions

Start `codex-transfer receive` on the destination as usual, then select sessions on the source:

```sh
codex-transfer send 192.168.1.20 --since 2026-10-01 --until 2026-10-13 --project-prefix app-
codex-transfer send 192.168.1.20 --project-contains api --project-contains backend
```

Supplying any selection flag enables **session merging**. Both computers need a version that supports it;
older receivers reject the offer before receiving archive data. Without selection flags, full migration
continues to replace offered roots and retain backups.

- `--since` and `--until` use inclusive `YYYY-MM-DD` dates in UTC, matched against the creation timestamp
  in `session_meta`, independently of file modification dates. Either boundary may be omitted.
- `--project-prefix` and `--project-contains` match the final directory name of the session's initial
  working directory. Matching is case-sensitive and supports both Unix and Windows path separators.
- Repeat a project flag to accept any of its values. Different flag categories must all match; for example,
  a date range plus a prefix plus a substring requires all three conditions.

Only matching active and archived rollouts are sent, including Zstandard rollouts. Source credentials,
configuration, databases, workspaces, and other roots are excluded; `--extra` cannot be combined with
session selection. Database-only source metadata, such as custom titles and pinning, is not imported.
Database-backed paginated histories require full migration and are rejected by session selection.
Invalid metadata or an empty selection stops the sender before connecting.
Both endpoints resolve SQLite home using `--sqlite-home`, top-level `sqlite_home` in `config.toml`,
`CODEX_SQLITE_HOME`, then the Codex root, in that order. Internal directories such as `.codex/sqlite`
retain their selection without adding an overlapping transfer root. The source's current rollout pointers
are read from this resolved directory. When a session has multiple
rollouts and its current file cannot be determined, selection stops instead of guessing from filenames.

Receiving conversations and settings are retained. Duplicate **session IDs** keep the receiving version,
even if filenames or archived status differ. A path collision between different IDs aborts the merge
without changing live data. The merge uses private staging and backs up the receiving Codex root and any
separate SQLite root before installation. Allow disk space for the received archive, local staging copies, and backups.
Session selection and merging support Codex versions that use `state_5.sqlite`. Only that exact filename
in the resolved SQLite home is used; older state files alongside it, worktree fixtures, and backup
subdirectories are excluded. A higher numbered state file or only older state files stops the operation;
the tool does not infer the active database from numbering. Initialize a compatible Codex version or use
full migration for an unsupported layout. New sessions are registered
with insert-only statements in the staged database; existing thread rows and discovery markers remain unchanged.
If the receiving database has an unknown required thread column or unfinished discovery, the merge stops before
installation. Complete initialization in Codex before retrying, or use full migration for an unsupported schema.
If no receiving state database exists, Codex initializes its index on startup. Restart Codex after completion.
Unrelated symlinks inside receiving session trees are retained and skipped without reading their targets.
Links named `*.jsonl` or `*.jsonl.zst`, symbolic session tree roots, and symbolic SQLite directories or sidecars
are rejected. Incoming session archives cannot contain symlinks. Use `receive --sqlite-home /real/path`
to include the actual SQLite directory as a separate root.

### Use a different port or wait longer

On the destination:

```sh
codex-transfer receive --port 8000
```

On the source:

```sh
codex-transfer send 192.168.1.20 --port 8000 --wait 10m
```

### Include an external directory

On the destination, choose where the named root should be installed:

```sh
codex-transfer receive --map project-data=/absolute/destination
```

On the source, add that root to the transfer:

```sh
codex-transfer send 192.168.1.20 --extra project-data=/absolute/source
```

Repeat `--extra` and `--map` for multiple roots. Names must match between computers and paths must not overlap. Use `--codex-home` or `--sqlite-home` to select custom Codex state locations.

## Quit Codex before migration or clearing

Run this from a separate terminal on each computer before transferring or clearing local conversations:

```sh
codex-transfer quit-codex
codex-transfer quit-codex --timeout 1m
```

The command requests normal exit for detected Codex Desktop, CLI, and app-server processes,
then verifies they have stopped. It succeeds when Codex is already stopped and waits up to
20 seconds by default. On macOS, Desktop receives a native application quit event; standalone
terminal processes receive `SIGTERM`. Linux uses `SIGTERM`. Windows uses `taskkill` without
`/F`; Windows may refuse normal closing for console processes, which must then be closed manually.
The command reports permission failures and timeouts and never escalates to forced termination.
Migration and clearing still check shutdown themselves. Run the quit command outside Codex,
because closing the host application can interrupt a command launched from its own terminal.

## Inspect and clear conversation storage

List conversation storage grouped by full workspace path:

```sh
codex-transfer workspaces
codex-transfer workspaces --details
```

The table shows conversation counts, rollout file counts, exact bytes, and MiB, plus a grand total.
It includes active and archived `.jsonl` and `.jsonl.zst` files; compressed files contribute their
compressed size. Every physical rollout counts toward storage, including older copies of the same
conversation. Workspace grouping uses the Codex state database's current `cwd`, falling back to
the initial rollout metadata for unindexed conversations. Database-only conversations are included
with zero rollout bytes. The totals exclude database storage, credentials, caches, and project files.
Listing is read-only and does not require quitting Codex. Workspaces without stored conversations
are not listed. `--details` exposes the session IDs used for individual selection.

Quit Codex Desktop, CLI, IDE extensions, and daemons before clearing conversations. Preview a
selection with `--dry-run`, then run the same command without that flag to delete it:

```sh
# Preview all conversations in one project.
codex-transfer clear-sessions --workspace /absolute/project/path --dry-run

# Select one or more projects using the full paths from the workspace table.
codex-transfer clear-sessions --workspace /absolute/project/path --workspace /absolute/other/project

# Select individual conversations using IDs from workspaces --details.
codex-transfer clear-sessions --session SESSION_ID --session OTHER_SESSION_ID

# Clear all conversations, including archived conversations and database-only entries.
codex-transfer clear-sessions --all
```

Repeated `--workspace` and `--session` selections form a union. Unknown selections are rejected;
`--all` cannot be combined with either selector. Every deletion shows its scope and requires
two explicit responses: `yes`, then `DELETE`. There is no flag to bypass either confirmation.
Declining, missing input, or cancelling before installation leaves live data unchanged.

Clearing removes selected rollout files, matching `threads` records and foreign-key cascades,
and matching records in `history.jsonl` and `session_index.jsonl`. Project files, workspace
registrations, credentials, configuration, and unrelated conversations are retained. Restart
Codex after clearing to refresh its project and recent conversation lists. This clears local
stored history only; it does not delete cloud conversations or separate migration backups.

The operation prepares private copies of the Codex root and any separate SQLite root, then
installs them together with rollback on failure. Allow enough free space for those copies.
Temporary backups are removed after a successful installation, so deletion is permanent.
If backup cleanup fails, the error identifies the retained backup. Unsupported database versions,
invalid metadata, symbolic session paths, and unsupported foreign-key dependencies stop deletion
before installation. These commands support the same `state_5.sqlite` contract as session merging.
Use `--codex-home` and `--sqlite-home` with either command for custom data locations.

## Commands

| Command | Purpose |
| --- | --- |
| `address` | Show local IP addresses and connection examples. |
| `paths --all` | Show all conventional data roots, including missing locations. |
| `check` | Check local Codex shutdown and data availability. |
| `quit-codex` | Request normal Codex exit and verify shutdown. |
| `workspaces [--details]` | Show conversation file sizes per workspace and optional session IDs. |
| `clear-sessions` | Clear selected or all conversations after two confirmations. |
| `receive` | Wait for one approved transfer and install the received data. |
| `send <ip>` | Send selected local data to the receiving computer. |
| `--version` | Show the installed version. |

Run `codex-transfer help` or `codex-transfer <command> --help` for options.

[Installation and upgrades](./docs/installation.en-US.md) · [Migration and recovery](./docs/migration.en-US.md)
