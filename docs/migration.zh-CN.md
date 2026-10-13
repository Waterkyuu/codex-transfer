# 本地迁移协议与操作契约

## 命令

| 命令 | 行为 |
| --- | --- |
| `address [--port 7432]` | 显示已启用接口中的 IPv4、IPv6 地址和发送示例；地址是否可达取决于路由及防火墙 |
| `paths [--all]` | 显示数据根；默认只显示存在的根，`--all` 包括尚不存在的标准位置 |
| `check` | 检查本机是否存在已知 Codex 进程，并列出数据根；不连接对端 |
| `receive` | 等待发送端、核对配对码、分配暂存目录、接收校验和安装；完成一次后退出 |
| `send <ip>` | 创建磁盘快照，等待接收端启动、核对配对码，收到就绪消息后发送 |

`send` 只接受字面 IPv4 / IPv6；IPv6 示例：`codex-transfer send ::1 --port 7432`。两端均使用 `--port` 指定非默认端口。IPv6 接收需 `receive --listen ::`。发送端 `--wait 5m` 限制连接等待，传输本身没有总时长限制，每次网络操作有 60 秒空闲限制。TLS 握手限时 15 秒。开始发送 offer 后，等待接收端批准和就绪消息也受 60 秒限制；两个终端应及时完成配对。

`--codex-home` 优先于 `CODEX_HOME`。`--sqlite-home` 优先于 `config.toml` 的顶层 `sqlite_home`，后者优先于 `CODEX_SQLITE_HOME`。`log_dir` 识别简单顶层字符串；支持单引号、双引号及行尾注释，复杂或多行路径语法会报错。配置 profile 和其他自定义外部目录需要使用 `--extra`，接收端使用 `--map`。相对配置路径按当前工作目录解析，与 Codex 的环境变量约定一致。Shell 可展开命令参数中的 `~`；工具不展开 TOML 字符串中的 `~` 或环境变量。

`receive --max-gib 1024` 默认将归档限制为 1 TiB，可修改。接收前需要为整个归档和解压后的数据留出额外磁盘空间；原目标备份会保留。源端需要能在系统临时目录容纳完整压缩快照（`--compression none` 时为未压缩快照）。

## 默认数据根

| 名称 | 默认位置 |
| --- | --- |
| `codex` | `$CODEX_HOME` 或 `~/.codex`，整个树，包括隐藏文件、会话、配置、技能、插件、工作树、SQLite 及其 WAL/SHM 文件 |
| `agents` | `~/.agents` |
| `workspace` | `~/Documents/Codex` |
| `desktop` | macOS：`~/Library/Application Support/Codex`；Linux：`$XDG_CONFIG_HOME/Codex` 或 `~/.config/Codex`；Windows：`%APPDATA%/Codex` |
| `desktop-support` | macOS：`~/Library/Application Support/com.openai.codex` |
| `preferences` | macOS：`~/Library/Preferences/com.openai.codex.plist` |
| `desktop-cache` | macOS：`~/Library/Caches/com.openai.codex` |
| `desktop-logs` | macOS：`~/Library/Logs/Codex` |
| `saved-state` | macOS：`~/Library/Saved Application State/com.openai.codex.savedState` |
| `sqlite` / `logs` | 配置或环境指定、且位于 `codex` 根外的 SQLite / 日志目录 |

这些桌面目录是本工具的约定，不是 OpenAI 对所有版本存储布局的保证。使用 `paths --all` 核对版本和安装方式；其他位置用 `--extra name=/absolute/source` 与 `--map name=/absolute/destination` 对应。缺少映射时接收端拒绝迁移。

OpenAI 文档说明认证信息可能保存在文件或系统钥匙串中；本工具只覆盖文件系统数据，不迁移钥匙串。参见 [配置和状态位置](https://learn.chatgpt.com/docs/config-file/config-advanced#config-and-state-locations)。

## 传输顺序

1. 两端检查 Codex 是否退出。源端默认以有界并行 gzip 生成磁盘 tar 快照，压缩、写盘和 SHA-256 同时进行；再检查文件身份、大小、模式、修改时间及目录项变化，源变化则拒绝发送。
2. 建立 TLS 1.3 连接，利用 TLS exporter 派生本次连接的 12 位配对码。两端必须输入对方屏幕上相同的码；不能仅复制本机显示的码。可额外使用 `send --fingerprint <SHA-256>` 固定接收端证书。
3. 发送端传递协议版本 2、源操作系统、主目录与根路径前缀、压缩方式、原始与网络长度和 SHA-256。接收端检查支持的平台、两种大小限制、目标映射和路径不重叠，为每个根取得独占 `.transfer-lock`，在目标所在文件系统分配私有暂存目录，然后宣布就绪。
4. 传输开始，每秒显示归档字节的实时速率，最后一次显示平均速率。接收端保存归档并校验完整 SHA-256；校验失败不替换目标。
5. 解压拒绝路径穿越、重复路径、非目录祖先和不受支持的条目。符号链接最后处理，不跟随链接进入活动目录或外部数据。跨系统导入 Windows 时，传输根内的链接转换为实际文件或目录，外部链接在替换前报错。源路径仅用于元数据转换，不决定本地安装目标。已知配置、会话和 SQLite 路径在暂存目录转换。
6. 使用同文件系统重命名保留原目标备份，再安装新根。普通错误会回滚已经替换的根；接收端发送完成确认，发送端收到确认后才报告成功。

只替换选中的活动数据根，其他接收端数据保留；状态整体替换，不合并。主目录和数据前缀，以及 `config.toml`、会话元数据、全局工作区状态和白名单 Codex SQLite 列中的已知路径会转换。提示词、标题、凭据和任意项目文件不改写。外部项目可使用 `receive --path-map 源前缀=/本地路径`。

macOS、Linux、Windows 之间可以互相迁移。`codex`、`agents`、`workspace`、`sqlite`、`logs` 是活动可移植根。源端单独提供 SQLite 或日志根、接收端没有目标时，默认使用 `<codex-home>.sqlite` 或 `<codex-home>.logs` 相邻目录，并更新配置。其他外来根保留在 `~/.codex-transfer/imports/<源系统>` 下的完整原始快照；明确的 `--map` 可覆盖此分类，`--archive-dir` 修改接收端归档基目录。归档目录也参加备份和回滚。

已有 `.jsonl.zst` 会话保留原压缩文件，并生成适配后的 `.jsonl` 文件；支持的 SQLite 会话路径指向新文件。转换限制为单条记录 16 MiB、单个展开的压缩会话 1 GiB、Zstandard 解码窗口 64 MiB。不支持的格式会在活动数据替换前报错。Windows 不支持的文件名和大小写冲突也明确报错，不丢弃或擅自重命名文件。

不同系统或 CPU 架构可能需要重新安装插件可执行文件、重新创建 Git 工作树。工作树 Git 内部数据和任意嵌入路径保留，不做通用二进制或文本替换。两端应使用兼容的 Codex 版本。

## 中断与恢复

Ctrl+C 会取消连接等待和网络传输，清理暂存归档和迁移锁。源数据不删除。安装中的每次根重命名是原子的，多根替换不具备断电事务保证；工具也不提供断点续传。

正常失败会保留原目标，并尽力回滚已替换的根。回滚异常会输出备份路径。如果发送端提示完成确认丢失，接收端可能已安装成功；先检查接收端输出和备份，不要立即重试。

被强制杀死或断电后可能留下 `.codex-transfer-*` 暂存目录、`<target>.transfer-lock` 或 `.backup-*`。退出 Codex，核对目标和备份；确认没有迁移进程后才手动清除锁及暂存数据。系统临时目录中的 `codex-transfer-snapshot-*` 可能包含完整敏感数据，也应检查清理。不要删除所需备份。

恢复旧数据时，先退出 Codex，将新目标改名保留，再把输出中的备份改回原目标路径。多个根应作为同一批数据一起恢复。

`--skip-process-check` 只用于隔离的合成测试数据。进程检查是尽力检测，无法阻止用户在迁移期间重新打开 Codex，也无法保证识别自定义命名的 daemon；两端必须在整个迁移过程中保持 Codex 关闭。工具只接受一个已批准迁移，不是长期运行的同步服务。两台电脑需要有可达路由并放行所选 TCP 端口；仅提供公网 IP 无法自动穿透 NAT。

## CPU 与吞吐

`--workers 0` 使用 Go 运行时 CPU 配额，`1` 至 `256` 指定并行处理数量。发送端默认 `--compression fast`；`balanced`、`best` 用更多 CPU 减少网络字节，`none` 跳过压缩。独立的 4 MiB gzip 数据块并行压缩和解压，并按原顺序输出。归档仍可用普通 gzip/tar 工具读取。队列和数据块有界，压缩器状态复用。

进度速率表示实际网络字节。发送端还显示包含快照准备和接收端安装的总体源数据速率，其中包含 tar 结构开销，不代表物理网络一定达到该速度。

查看保留的源快照时，可从 `manifest.json` 获取原始根位置，再用普通 tar 工具读取 `source.tar` 或 `source.tar.gz`。解压到新目录；原始归档用于恢复，不是已适配的活动状态。
