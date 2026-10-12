# 安装、升级与发布

## 可用条件

npm 包名是 `@waterkyuu/codex-transfer`，程序命令是 `codex-transfer`。原生二进制覆盖 macOS、Linux、Windows 的 amd64/arm64；Windows 使用 ZIP，其余使用 tar.gz。

在线安装需要公开可达、含对应归档和 `checksums.txt` 的 GitHub Release。npm 还需要同版本的 npm 包。初始开发版本 `0.0.0-development` 不是已发布版本。仓库私有时，匿名下载脚本、默认 Cask/Scoop 下载和公开 npm 包无法取得二进制；可通过有权限的 GitHub 登录手动下载，或从源码构建。工作流不会将仓库改为公开，也不会为私有仓库发布公开 npm 包。

## 安装与升级

### npm

```sh
npm install --global @waterkyuu/codex-transfer
codex-transfer --version
```

Node.js 22 或更新版本执行 `postinstall`：按平台选择归档、验证 SHA-256、安装到 npm 包的 `vendor` 中。npm 提供命令入口；包装器转发参数、标准输入输出和退出码。macOS/Linux 解压需要 `tar`，Windows 需要 PowerShell。

再次运行安装命令即可升级。启用 `--ignore-scripts` 时不会下载二进制，可用 `npm rebuild --global @waterkyuu/codex-transfer` 补齐。卸载：`npm uninstall --global @waterkyuu/codex-transfer`。

### Homebrew / macOS

```sh
brew tap waterkyuu/codex-transfer https://github.com/Waterkyuu/codex-transfer.git
brew install --cask waterkyuu/codex-transfer/codex-transfer
brew upgrade --cask waterkyuu/codex-transfer/codex-transfer
```

稳定版发布自动在仓库默认分支生成 `Casks/codex-transfer.rb`，包含各架构 URL 与 SHA-256。首个稳定版发布前没有可用 Cask。卸载：`brew uninstall --cask waterkyuu/codex-transfer/codex-transfer`。

### macOS / Linux 脚本

```sh
curl -fsSL https://github.com/Waterkyuu/codex-transfer/releases/latest/download/install.sh | sh
```

默认安装到 `~/.local/bin`，仅提示 PATH 操作，不修改 shell 配置。需要系统级位置时：

```sh
curl -fsSL https://github.com/Waterkyuu/codex-transfer/releases/latest/download/install.sh | sudo sh -s -- --system
```

系统级默认位置为 `/usr/local/bin`。重跑脚本即可升级，校验和失败会保留旧程序。脚本拒绝覆盖符号链接；如果程序由 npm 或 Homebrew 管理，应通过原管理器升级。

下载到本地后可以固定版本和安装位置：

```sh
CODEX_TRANSFER_VERSION=v1.2.3 CODEX_TRANSFER_INSTALL_DIR="$HOME/.local/bin" sh scripts/install.sh
```

`v1.2.3` 为示例，替换为真实发布的版本。`CODEX_TRANSFER_SYSTEM_PREFIX` 可修改系统级前缀；`CODEX_TRANSFER_RELEASE_BASE_URL` 可指定镜像根目录，镜像需要提供 `vVERSION/checksums.txt` 和对应归档。

### Windows / PowerShell

```powershell
irm https://github.com/Waterkyuu/codex-transfer/releases/latest/download/install.ps1 | iex
```

默认安装到 `%LOCALAPPDATA%\Programs\codex-transfer`，更新用户和当前进程 PATH，无需管理员权限。打开新终端后执行 `codex-transfer --version`。PowerShell 5.1 或更新版本可用。

下载脚本后也可以指定真实已发布的版本及位置：

```powershell
.\scripts\install.ps1 -Version v1.2.3 -InstallDir "$env:LOCALAPPDATA\Programs\codex-transfer"
```

`-NoPath` 不修改 PATH，适合测试或自定义管理。`-ReleaseBaseUrl`、`CODEX_TRANSFER_RELEASE_BASE_URL` 可配置镜像；`CODEX_TRANSFER_VERSION`、`CODEX_TRANSFER_INSTALL_DIR` 提供环境默认值。升级时先关闭正在运行的程序，重跑脚本。

Scoop 在首个稳定版后可用：

```powershell
scoop bucket add codex-transfer https://github.com/Waterkyuu/codex-transfer.git
scoop install codex-transfer/codex-transfer
scoop update codex-transfer
```

卸载：`scoop uninstall codex-transfer`。安装器和包管理器不会删除 Codex 数据。

## 手动安装

从 GitHub Release 下载当前系统和架构的 `codex-transfer_VERSION_OS_ARCH` 归档与 `checksums.txt`，确认 SHA-256 后解压，将 `codex-transfer` 或 `codex-transfer.exe` 放入 PATH。macOS/Linux 需要执行权限。私有仓库使用已登录且有仓库权限的浏览器或 GitHub CLI 下载。

归档中的二进制是 CLI，不是 macOS 应用安装器或 Windows MSI。发布流程不提供代码签名或 Apple notarization。

## 维护者发布

`.goreleaser.yml` 定义六个平台构建、归档、SHA-256、Cask 和 Scoop 清单；稳定版更新仓库默认分支，不假定默认分支名为 `main`。GitHub Release 同时附带两个安装脚本。配置参考 [GoReleaser Cask 文档](https://goreleaser.com/customization/publish/homebrew_casks/)。

准备 Node.js 22+、Go 和 GoReleaser 2.18.3，运行：

```sh
task lint
task test
task build
task test-installers
goreleaser check
goreleaser release --snapshot --skip=publish --clean
```

Windows 原生安装器测试：`pwsh -NoProfile -File scripts/install.test.ps1`。临时下载、归档、npm 缓存和二进制不加入 Git。`dist/`、`npm/vendor/`、`node_modules/` 与 npm tarball 已忽略。

配置 GitHub Actions 的写入权限，以及 npm 发布所需的 `NPM_TOKEN`（有该 scope 发布权限并符合账号 2FA 策略）。安装器 CI 在 macOS/Linux/Windows 检查通过后，推送合法 `vX.Y.Z` 标签会触发发布工作流：

```sh
git tag v1.2.3
git push origin v1.2.3
```

这些命令会触发实际发布，版本号仅为示例。Release 成功后才发布对应版本的 npm 包；稳定版使用 `latest`，预发布版使用 `next`，Cask/Scoop 不更新到预发布版。npm 发布失败可单独重跑 npm job，避免重复发布已存在的 Release。正式发布前，应确认所有下载方式能够从目标用户环境访问 Release。
