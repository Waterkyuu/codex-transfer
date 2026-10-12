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

`receive --max-gib 1024` defaults to a 1 TiB archive limit and can be adjusted. Allow additional disk space for the entire archive and extracted data; original destination backups remain. The sender needs enough system temporary-directory space for a complete uncompressed archive.

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

Desktop paths are tool conventions, not an OpenAI guarantee covering every release. Inspect `paths --all` for your version and installation method. Pair additional locations with `--extra name=/absolute/source` and `--map name=/absolute/destination`. Missing mappings reject the migration.

OpenAI documents `CODEX_HOME` as the root for configuration, authentication, logs, sessions, and skills, with a separate SQLite location available. See [environment variables](https://learn.chatgpt.com/docs/config-file/environment-variables#core-locations). Authentication can be file-backed or stored in the OS keychain; this tool migrates filesystem data only. See [config and state locations](https://learn.chatgpt.com/docs/config-file/config-advanced#config-and-state-locations).

## Transfer sequence

1. Both computers check that Codex is stopped. The sender builds an uncompressed tar snapshot with bounded memory, then checks file identity, size, mode, modification times, and directory entries. Observed source changes reject the snapshot.
2. Establish TLS 1.3 and derive a session-specific 12-character code with a TLS exporter. Both terminals must enter the matching code from the other screen. Copying the local code alone does not authenticate the peer. Optionally pin the receiver certificate with `send --fingerprint <SHA-256>`.
3. The sender offers protocol version, OS, root names, length, and SHA-256. The receiver validates same-platform compatibility, size, mappings, and disjoint paths, obtains exclusive `.transfer-lock` files, allocates private staging on each destination filesystem, and announces readiness.
4. Stream the archive with per-second throughput; the final progress line shows average throughput. The receiver checks the entire archive's SHA-256 before replacement. Checksum failure preserves the destination.
5. Extraction rejects traversal, duplicate paths, non-directory ancestors, and unsupported entries. Symlinks are created last and never followed during extraction. Remote absolute source paths never select local destinations.
6. Same-filesystem renames retain the original target as a backup and install the new root. Ordinary failures roll back completed replacements. The sender reports success only after the receiver acknowledges completion.

Only offered roots are replaced; other conventional receiver roots remain. Replacement does not merge SQLite content or rewrite absolute project paths, Git worktree metadata, session databases, or configuration. Use compatible Codex versions and matching directory layouts where possible. Cross-OS raw-state migration is rejected; different CPU architectures may require reinstalling plugin runtimes.

## Data fidelity and recovery

Ordinary files, directories, and symlinks within selected roots are included. Symlink targets are preserved literally, and their external contents are not followed. Runtime sockets are skipped and counted; other special files fail explicitly. Files are created for the receiver user with owner-only access. Source UIDs/GIDs, ACLs, extended attributes, directory timestamps, and hardlink relationships are not replayed. The OS keychain and arbitrary external project repositories are outside scope. Windows Store isolated package paths and nonstandard installations require explicit roots. Authentication may require signing in again.

Ctrl+C cancels connection waiting and streaming and cleans up staging and migration locks. Source data is retained. Individual root renames are atomic, but multiple roots are not a power-loss transaction. Resume of interrupted transfers is not supported.

Ordinary failures preserve original targets and attempt rollback. Rollback errors name recoverable backups. If the sender loses the final acknowledgement, the receiver may already have installed the data; inspect the receiver output and backups before retrying.

A forced kill or power failure may leave `.codex-transfer-*` staging directories, `<target>.transfer-lock`, or `.backup-*`. Quit Codex and inspect targets and backups. Remove locks and temporary data manually only after verifying no migration process remains. Inspect system temporary storage for `codex-transfer-snapshot-*`, which can contain a full sensitive snapshot. Keep any required backups.

To restore an old batch, quit Codex, rename new targets aside, then rename the reported backups to their original target paths. Restore related roots as the same batch.

`--skip-process-check` is for isolated synthetic fixtures only. Process detection is best effort: it cannot stop users reopening Codex or identify every custom daemon name. Keep Codex closed throughout migration on both computers. This is a single approved transfer, not a continuous synchronization daemon. The computers require a reachable route and an open TCP port; a public IP alone cannot traverse NAT automatically.
