# codex-transfer

用 Go 编写的 Codex 本地数据迁移 CLI。两台电脑通过 IP 直连，无需 SSH、云服务或预先配置证书。

支持本机 IP 查看、数据路径查看、Codex 退出检查、两端启动握手、TLS 1.3 加密、实时传输速率、SHA-256 校验，以及接收端备份和失败回滚。迁移采用复制方式，保留发送端数据。

## 构建

需要 Go 1.22 或更新版本。在两台电脑上准备对应平台的可执行文件：

```sh
mkdir -p bin
go build -trimpath -o bin/codex-transfer ./cmd/codex-transfer
./bin/codex-transfer help
```

可以将可执行文件放入 PATH，后续直接使用 `codex-transfer`。项目无第三方 Go 依赖。

## 使用

先退出两台电脑上的 Codex 桌面端、CLI、IDE 扩展及后台 daemon。

接收电脑：

```sh
codex-transfer address
codex-transfer paths --all
codex-transfer check
codex-transfer receive
```

发送电脑：

```sh
codex-transfer check
codex-transfer send 192.168.1.20
```

将示例 IP 替换为接收电脑的可达 IP。默认端口是 TCP `7432`。发送端可以先启动，会等待接收端，默认最多五分钟。

连接后，两端显示相同的 12 位配对码。**分别输入另一台电脑屏幕显示的配对码**；核对不一致时立即停止。两个终端均批准、接收端完成目录和暂存检查后，才开始传送数据。

进度包含已传输量、总量、百分比、MiB/s 和耗时。100% 表示网络传输结束；最终 `Migration complete` 表示接收端校验和安装均完成。

## 数据范围

默认包括存在的 `CODEX_HOME`（通常为 `~/.codex`）、`~/.agents`、`~/Documents/Codex`，以及当前平台约定的桌面数据位置。macOS 还覆盖 Codex 应用数据、偏好设置、缓存、日志和保存状态。通过简单顶层 `config.toml` 路径或 `CODEX_SQLITE_HOME` 指定的外部 SQLite 目录也会被识别。实际列表以 `paths --all` 为准。

OpenAI 官方将 `CODEX_HOME` 定义为配置、认证、日志、会话和技能等状态的根目录，并允许单独指定 SQLite 目录。参见 [环境变量](https://learn.chatgpt.com/docs/config-file/environment-variables#core-locations)。

“完整”指所选数据根中的普通文件、目录和符号链接。工具不迁移系统钥匙串、不跟随符号链接复制外部内容、不自动收集任意项目仓库；运行时套接字会跳过并报告。Windows Store 的包隔离位置、非标准安装目录和其他自定义位置需要 `--extra` / `--map` 明确指定。

接收端是**替换所传根**，不是合并会话。原数据保存为相邻 `.backup-*` 文件或目录。文件按接收用户创建，权限收紧至用户自身可访问；不复制源 UID/GID、ACL、扩展属性或硬链接关系。绝对项目路径、数据库中的工作目录、外部符号链接及配置中的绝对路径保持原文，用户名或目录结构不同时需要自行迁移对应项目和调整配置。认证可能需要重新登录。

## 自定义目录与端口

```sh
# 接收端；自定义 SQLite 目标和端口
codex-transfer receive --port 8000 --map sqlite=/Users/newuser/codex-sqlite

# 发送端；自定义数据根和相同端口
codex-transfer send 192.168.1.20 --port 8000 \
  --codex-home /Users/olduser/.codex \
  --sqlite-home /Users/olduser/codex-sqlite

# 添加非标准数据位置，两端使用相同的根名称
codex-transfer receive --map extra-state=/absolute/destination
codex-transfer send 192.168.1.20 --extra extra-state=/absolute/source
```

同一个示例中的两端命令在各自电脑运行。`--extra` 和 `--map` 可重复，但名称不能重复、路径不能重叠。接收端的路径始终由接收端选择。

## 开发和说明

```sh
task fmt
task lint
task test
task build
```

测试包括单元测试、回环网络 TLS 集成测试和两个真实 CLI 进程的端到端测试，使用隔离的临时数据。二进制、测试产物和 `work/` 已加入 `.gitignore`。

详细说明：[中文](docs/migration.zh-CN.md) · [English](docs/migration.en-US.md)。
