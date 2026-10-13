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

## Commands

| Command | Purpose |
| --- | --- |
| `address` | Show local IP addresses and connection examples. |
| `paths --all` | Show all conventional data roots, including missing locations. |
| `check` | Check local Codex shutdown and data availability. |
| `receive` | Wait for one approved transfer and install the received data. |
| `send <ip>` | Send selected local data to the receiving computer. |
| `--version` | Show the installed version. |

Run `codex-transfer help` or `codex-transfer <command> --help` for options.

[Installation and upgrades](./docs/installation.en-US.md) · [Migration and recovery](./docs/migration.en-US.md)
