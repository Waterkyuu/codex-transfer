# Local migration protocol and operating contract

## Commands

| Command | Behavior |
| --- | --- |
| `address [--port 7432]` | Show IPv4/IPv6 addresses on active interfaces and send examples; reachability depends on routing and firewalls |
| `paths [--all]` | List existing data roots; `--all` also lists missing standard locations |
| `check` | Check for known local Codex processes and list data roots; does not connect to the peer |
| `receive` | Wait for a sender, approve pairing, allocate staging, receive, verify, and install; exit after one migration |
| `send <ip>` | Create a disk snapshot, wait for receiver startup, approve pairing, and send only after readiness |

`send` accepts literal IPv4/IPv6 addresses only. IPv6 example: `codex-transfer send ::1 --port 7432`. Set `--port` on both computers for a nondefault port. IPv6 reception requires `receive --listen ::`. Sender `--wait 5m` bounds connection retries. Transfers have no overall time limit, but each network operation has a 60-second idle limit. TLS negotiation has a 15-second limit. After sending the offer, waiting for receiver approval and readiness is also subject to the 60-second limit; complete pairing promptly on both terminals.

`--codex-home` overrides `CODEX_HOME`. `--sqlite-home` overrides top-level `sqlite_home` in `config.toml`, which overrides `CODEX_SQLITE_HOME`. Simple top-level `log_dir` strings are recognized. Single quotes, double quotes, and trailing comments are supported; complex or multiline path syntax fails explicitly. Profile settings and other custom external directories require `--extra` and a receiver `--map`. Relative configured paths resolve from the current working directory, consistent with the Codex environment-variable convention. The shell may expand `~` in command arguments; the tool does not expand `~` or environment variables inside TOML strings.

`receive --max-gib 1024` defaults to a 1 TiB archive limit and can be adjusted. Allow additional disk space for the entire archive and extracted data; original destination backups remain. The sender needs enough system temporary-directory space for a complete compressed snapshot (uncompressed with `--compression none`).

## Selective session imports

Supplying a date or project selection flag enables session merging. For example:

```sh
codex-transfer receive --sqlite-home /home/bob/.codex/sqlite
codex-transfer send 192.168.1.20 --sqlite-home /home/alice/.codex/sqlite --since 2026-10-01
```

Each endpoint independently resolves SQLite home using `--sqlite-home`, top-level `sqlite_home` in
`config.toml`, `CODEX_SQLITE_HOME`, then the Codex root. An internal directory is included through the
Codex root, while an external directory requires a separate SQLite root. The resolved location is
retained in both cases. The receiver translates that local location into its private clone before
reading identities or registering new sessions; it never writes to the live database before installation.

Selective imports support Codex versions that use the exact filename `state_5.sqlite`, consistent with
[Codex's state database filename contract](https://github.com/openai/codex/blob/main/codex-rs/state/src/sqlite.rs).
Discovery only examines direct children of the resolved SQLite home. Older state files alongside
`state_5.sqlite`, worktree fixtures, and backup subdirectories are excluded. A higher numbered state
file, a noncanonical version-5 filename, or only older state files causes an explicit failure. Database
numbering and schema compatibility do not identify which Codex version is running. Both endpoints must
use compatible Codex versions; initialize a version using `state_5.sqlite` or use full migration for an
unsupported layout. Full migration does not use this selective-import database rule.

With no state database, the sender accepts only one rollout per session ID and rejects ambiguous
histories. The receiver leaves index initialization to Codex on startup. With a supported database,
the sender uses its current rollout pointers and the receiver inserts only new thread IDs, preserving
existing rows and completed discovery markers. Unknown required columns or unfinished discovery stop
the merge before installation.

Local receiving symlinks beneath `sessions/` and `archived_sessions/` are retained and skipped when their
names do not end in `.jsonl` or `.jsonl.zst`. This includes dangling links and links to directories; their
targets are never traversed. Rollout-named links, symbolic session tree roots, symbolic SQLite directories
and sidecars, and symbolic ancestors of incoming rollout destinations remain rejected. Source session
selection and incoming session archives continue to reject symlinks.

Regression tests cover both CLI endpoints, configuration precedence, internal SQLite directories with
decoy databases, unsupported database layouts, and retained receiving links. These tests use synthetic
fixtures and do not establish discovery or resume behavior after a real Codex restart. Restart a compatible
Codex version after migration and verify the imported conversations.

## Default data roots

| Name | Default location |
| --- | --- |
| `codex` | `$CODEX_HOME` or `~/.codex`, the full tree including hidden files, sessions, configuration, skills, plugins, worktrees, SQLite and WAL/SHM files |
| `agents` | `~/.agents` |
| `workspace` | `~/Documents/Codex` |
| `desktop` | macOS: `~/Library/Application Support/Codex`; Linux: `$XDG_CONFIG_HOME/Codex` or `~/.config/Codex`; Windows: `%APPDATA%/Codex` |
| `desktop-support` | macOS: `~/Library/Application Support/com.openai.codex` |
| `preferences` | macOS: `~/Library/Preferences/com.openai.codex.plist` |
| `desktop-cache` | macOS: `~/Library/Caches/com.openai.codex` |
| `desktop-logs` | macOS: `~/Library/Logs/Codex` |
| `saved-state` | macOS: `~/Library/Saved Application State/com.openai.codex.savedState` |
| `sqlite` / `logs` | Configured SQLite/log directories outside the `codex` root |

Desktop paths are tool conventions, not an OpenAI guarantee covering every release. Inspect `paths --all` for your version and installation method. Pair additional locations with `--extra name=/absolute/source` and `--map name=/absolute/destination`. Missing active mappings reject the migration; other foreign-system roots are preserved in the original archive by default.

OpenAI documents `CODEX_HOME` as the root for configuration, authentication, logs, sessions, and skills, with a separate SQLite location available. See [environment variables](https://learn.chatgpt.com/docs/config-file/environment-variables#core-locations). Authentication can be file-backed or stored in the OS keychain; this tool migrates filesystem data only. See [config and state locations](https://learn.chatgpt.com/docs/config-file/config-advanced#config-and-state-locations).

## Transfer sequence

1. Both computers check that Codex is stopped. The sender builds a disk-backed tar snapshot using bounded parallel gzip members by default. Compression, output, and SHA-256 overlap; a second metadata scan checks file identity, size, mode, modification times, and directory entries. Observed source changes reject the snapshot.
2. Establish TLS 1.3 and derive a session-specific 12-character code with a TLS exporter. Both terminals must enter the matching code from the other screen. Copying the local code alone does not authenticate the peer. Optionally pin the receiver certificate with `send --fingerprint <SHA-256>`.
3. The sender offers protocol version 2, source OS/home/root prefixes, compression, raw and transmitted lengths, and SHA-256. The receiver validates supported OSes, both size limits, mappings, and disjoint paths, obtains exclusive `.transfer-lock` files, allocates private staging on each destination filesystem, and announces readiness.
4. Stream the archive with per-second throughput; the final progress line shows average throughput. The receiver checks the entire archive's SHA-256 before replacement. Checksum failure preserves the destination.
5. Extraction rejects traversal, duplicate paths, non-directory ancestors, and unsupported entries. Symlinks are handled last and never followed into live or external data. Cross-system imports into Windows materialize links within transferred roots; unsupported external links fail before replacement. Source paths translate metadata only and never select installation destinations. Known config, session, and SQLite path fields are adapted in staging.
6. Same-filesystem renames retain the original target as a backup and install the new root. Ordinary failures roll back completed replacements. The sender reports success only after the receiver acknowledges completion.

Only selected active roots are replaced; other receiver roots remain. State is replaced rather than merged. Home/data prefixes and known structural paths are translated in `config.toml`, session/turn metadata, global workspace state, and allowlisted Codex SQLite columns. Prompts, titles, credentials, and arbitrary project files are not rewritten. External projects can use `receive --path-map SOURCE=/local/path`.

Across macOS, Linux, and Windows, `codex`, `agents`, `workspace`, `sqlite`, and `logs` are active portable roots. A separately offered SQLite/log root without a target defaults to a sibling `<codex-home>.sqlite` or `<codex-home>.logs`, and config is updated. Other foreign roots are retained in the complete original source archive under `~/.codex-transfer/imports/<source-os>`; explicit `--map` overrides this classification. `--archive-dir` changes its receiver-selected base. The preservation directory participates in backup and rollback like every other root.

Zstandard `.jsonl.zst` rollouts retain their original compressed file and gain an adapted `.jsonl` sibling; supported SQLite rollout references point to that sibling. Conversion limits each record to 16 MiB, each expanded compressed rollout to 1 GiB, and the Zstandard decoding window to 64 MiB. Unsupported formats fail before replacing live data. Windows-incompatible filenames and case collisions also fail explicitly instead of dropping or renaming files.

Different OSes/CPU architectures can require reinstalling plugin executables and recreating Git worktrees. Worktree Git internals and arbitrary embedded paths are preserved; no generic binary/text substitution is performed. Use compatible Codex versions.

## Data fidelity and recovery

Ordinary files, directories, and symlinks within selected roots are included. Mapped absolute symlink targets follow selected destination paths; external contents are never followed. Runtime sockets are skipped and counted; other special files fail explicitly. Files are created for the receiver user with owner-only access. Source UIDs/GIDs, ACLs, extended attributes, directory timestamps, and hardlink relationships are not replayed. The OS keychain and arbitrary external project repositories are outside scope. Windows Store isolated package paths and nonstandard installations require explicit roots. Authentication may require signing in again.

Ctrl+C cancels connection waiting and streaming and cleans up staging and migration locks. Source data is retained. Individual root renames are atomic, but multiple roots are not a power-loss transaction. Resume of interrupted transfers is not supported.

Ordinary failures preserve original targets and attempt rollback. Rollback errors name recoverable backups. If the sender loses the final acknowledgement, the receiver may already have installed the data; inspect the receiver output and backups before retrying.

A forced kill or power failure may leave `.codex-transfer-*` staging directories, `<target>.transfer-lock`, or `.backup-*`. Quit Codex and inspect targets and backups. Remove locks and temporary data manually only after verifying no migration process remains. Inspect system temporary storage for `codex-transfer-snapshot-*`, which can contain a full sensitive snapshot. Keep any required backups.

To restore an old batch, quit Codex, rename new targets aside, then rename the reported backups to their original target paths. Restore related roots as the same batch.

`--skip-process-check` is for isolated synthetic fixtures only. Process detection is best effort: it cannot stop users reopening Codex or identify every custom daemon name. Keep Codex closed throughout migration on both computers. This is a single approved transfer, not a continuous synchronization daemon. The computers require a reachable route and an open TCP port; a public IP alone cannot traverse NAT automatically.

## CPU and throughput

`--workers 0` uses the Go runtime CPU budget; `--workers 1` through `256` selects explicit processing fan-out. The sender defaults to `--compression fast`; `balanced` and `best` spend more CPU on network reduction, while `none` skips compression. Independent 4 MiB gzip members are compressed and decompressed in parallel and written in order. The archive remains readable by ordinary gzip/tar tools. Queue sizes and block allocation are bounded, and compressor state is reused.

The progress rate measures actual network bytes. The sender also prints overall source-data throughput including snapshot preparation and receiver installation. This includes tar framing, and is not a promise of a particular physical-network speed.

To inspect a preserved source snapshot, read its `manifest.json` for original root locations and use ordinary tar tools on `source.tar` or `source.tar.gz`. Extract into a new directory; the original archive is a recovery copy, not the adapted live state.
