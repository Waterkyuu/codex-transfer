<div align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="./assets/logo-codex-transfer-dark.png" />
    <img src="./assets/logo-codex-transfer.png" width="520" alt="codex-transfer 标志" />
  </picture>
  <h1>codex-transfer</h1>
  <p>直接通过终端，将 Codex 本地数据迁移到另一台电脑。</p>
  <p><a href="./README.md">English</a> | <strong>简体中文</strong></p>
</div>

## 安装

在两台电脑上安装 codex-transfer。提供 **macOS、Linux、Windows** 的 **amd64、arm64** 原生二进制文件。

### npm

```sh
npm install --global codex-transfer
```

需要 Node.js 22 或更新版本，自动下载当前平台的原生程序。

### Homebrew · macOS

```sh
brew tap waterkyuu/codex-transfer https://github.com/Waterkyuu/codex-transfer.git
brew install --cask waterkyuu/codex-transfer/codex-transfer
```

### macOS 和 Linux

```sh
curl -fsSL https://github.com/Waterkyuu/codex-transfer/releases/latest/download/install.sh | sh
```

默认安装到 `~/.local/bin`。如果该目录不在 `PATH` 中，按脚本提示添加。

### Windows · PowerShell

```powershell
irm https://github.com/Waterkyuu/codex-transfer/releases/latest/download/install.ps1 | iex
```

默认安装到 `%LOCALAPPDATA%\Programs\codex-transfer`，并加入用户 `PATH`。安装后打开新终端。

### Windows · Scoop

```powershell
scoop bucket add codex-transfer https://github.com/Waterkyuu/codex-transfer.git
scoop install codex-transfer/codex-transfer
```

也可以从 [Releases](https://github.com/Waterkyuu/codex-transfer/releases) 下载原生归档。升级和自定义安装位置见[安装指南](./docs/installation.zh-CN.md)。

## 功能

| 功能 | 可以完成的操作 |
| --- | --- |
| **跨系统迁移** | 在 macOS、Windows、Linux 之间迁移通用 Codex 数据，转换已知的配置、会话和数据库路径。 |
| **多核并行处理** | 并行压缩、解压和处理元数据，按网络条件选择压缩强度。 |
| **IP 直连** | 通过 IPv4 或 IPv6 在两台电脑间传输，无需配置 SSH 或云服务。 |
| **地址查看** | 列出本机网络接口地址，并提供可直接使用的发送命令。 |
| **两端确认** | 在两台电脑上核对并输入相同的配对码，再开始发送数据。 |
| **启动检查** | 检查 Codex 是否退出；发送端会等待接收端终端启动。 |
| **实时传输进度** | 显示百分比、已传输量、总量、MiB/s 速率和耗时。 |
| **加密与校验** | 使用 TLS 1.3 加密连接，安装前校验整个归档的 SHA-256。 |
| **备份与恢复** | 保留源数据，备份被替换的目标目录，安装出现普通错误时回滚。 |
| **自定义数据位置** | 添加外部数据目录，并在接收电脑上指定对应的安装位置。 |

## 快速开始

先退出**两台电脑**上的 Codex 桌面端、CLI、IDE 扩展及后台 daemon，并在整个迁移期间保持关闭。

**1. 在接收电脑上**，查看可达 IP 并启动接收：

```sh
codex-transfer address
codex-transfer receive
```

**2. 在发送电脑上**，连接这个 IP：

```sh
codex-transfer send 192.168.1.20
```

将 `192.168.1.20` 替换为接收电脑的 IP。默认使用 TCP `7432`，两台电脑需要能访问这个端口。两端启动顺序不限；发送端默认等待接收端最多五分钟。

**3. 在两台电脑的屏幕上核对连接。** 每个终端都会显示 12 位配对码，分别输入**另一台电脑**显示的码。配对码必须一致；两端都确认且接收端通过就绪检查后，才会开始传输。

进度示例：

```text
42.0% | 84.00 / 200.00 MiB | 18.60 MiB/s | 5.0s
```

等待最终的 **`Migration complete`** 提示。进度达到 100% 表示网络传输结束，接收端还会校验、安装数据，再确认完成。

## 迁移哪些数据

会包含以下数据根中已存在的文件：

| 数据 | 位置 |
| --- | --- |
| 对话、历史、配置、文件中的凭据、插件和工作树 | `CODEX_HOME`，通常为 `~/.codex` |
| 用户技能和 Agent 配置 | `~/.agents` |
| 无项目聊天的工作目录 | `~/Documents/Codex` |
| 桌面应用数据 | 当前平台约定的 Codex 数据位置，包括 macOS 偏好设置和缓存 |
| 外部 SQLite 状态和日志 | 已识别的顶层配置路径或 `CODEX_SQLITE_HOME` |

查看本机的实际数据位置：

```sh
codex-transfer paths --all
codex-transfer check
```

接收端会**按批次替换所传的数据根，不合并内容**。原数据保存在相邻的 `.backup-*` 文件或目录中，发送端数据保留。符号链接保留目标；跨系统导入 Windows 时，位于已传输根内的链接会转换为实际文件或目录，外部链接目标需要明确加入传输。运行时套接字会跳过并报告。

**macOS、Windows、Linux 之间可以互相迁移。** 通用数据根安装到接收电脑的本地位置，配置、会话元数据和 Codex SQLite 表中的已知路径会转换；对话内容和凭据不会改写。已有 Zstandard 压缩会话保留原文件，并生成适配后的普通会话文件。

跨系统迁移会把完整未修改的源快照和清单保留在 `~/.codex-transfer/imports/<源系统>`。源系统专有的桌面设置，以及未明确指定可移植目标的其他根，保留在归档中；需要安装到自选位置时使用 `--map`。系统钥匙串凭据、依赖源系统的插件可执行文件，以及所选根以外的仓库，需要重新登录、安装或单独复制。

## 跨系统路径与传输速度

支持的操作系统之间可直接使用 `send` 和 `receive`，主目录和数据根路径会自动映射。外部项目在目标电脑上的目录结构不同时，可以指定路径前缀：

```powershell
codex-transfer receive --path-map "/Users/alice/dev=D:/dev"
```

默认并行 gzip 压缩使用 Go 运行时的 CPU 配额，可独立调整并行度和压缩强度：

```sh
codex-transfer send 192.168.1.20 --workers 0 --compression fast
```

`fast` 优先吞吐，`balanced` 和 `best` 用更多 CPU 减少网络字节；`none` 适用于已经压缩的数据或非常快的本地网络。任一端可用 `--workers 4` 限制并行处理。处理队列有界，内存占用随并行度增长。进度显示网络字节和速率，最终总体速率还包含准备和安装耗时。实际速度取决于数据、磁盘、CPU 和网络。

## 自定义连接和数据位置

### 修改端口或延长等待

接收电脑：

```sh
codex-transfer receive --port 8000
```

发送电脑：

```sh
codex-transfer send 192.168.1.20 --port 8000 --wait 10m
```

### 添加外部目录

在接收电脑上，为指定名称的数据根选择目标位置：

```sh
codex-transfer receive --map project-data=/absolute/destination
```

在发送电脑上，将同名数据根加入迁移：

```sh
codex-transfer send 192.168.1.20 --extra project-data=/absolute/source
```

可以重复使用 `--extra` 和 `--map` 添加多个根。两端名称必须一致，路径不能重叠。自定义 Codex 状态位置可使用 `--codex-home` 或 `--sqlite-home`。

## 命令

| 命令 | 用途 |
| --- | --- |
| `address` | 查看本机 IP 和连接示例。 |
| `paths --all` | 查看所有标准数据根，包括尚不存在的位置。 |
| `check` | 检查本机 Codex 是否退出及数据是否存在。 |
| `receive` | 等待一次已确认的传输，并安装接收到的数据。 |
| `send <ip>` | 将所选本地数据发送到接收电脑。 |
| `--version` | 查看安装版本。 |

使用 `codex-transfer help` 或 `codex-transfer <命令> --help` 查看选项。

[安装和升级](./docs/installation.zh-CN.md) · [迁移和恢复](./docs/migration.zh-CN.md)
