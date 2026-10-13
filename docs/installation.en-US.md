# Installation, upgrades, and publishing

## Availability

The npm package is `codex-transfer`; the executable command is `codex-transfer`. Native releases cover macOS, Linux, and Windows on amd64/arm64. Windows archives use ZIP; other platforms use tar.gz.

Online installation requires an anonymously reachable GitHub Release containing the matching archive and `checksums.txt`. npm also requires a published package of the same version. The initial `0.0.0-development` package is not a released version. Anonymous scripts, default Cask/Scoop downloads, and the public npm wrapper cannot download private release assets. Use an authorized GitHub account to download manually or build from source. Workflows do not change repository visibility and skip public npm publishing for private repositories.

## Installation and upgrades

### npm

```sh
npm install --global codex-transfer
codex-transfer --version
```

Node.js 22+ runs `postinstall`, selects the native archive, checks SHA-256, and installs the executable in the npm package's `vendor` directory. npm creates the command entry point. The wrapper forwards arguments, standard streams, and exit status. Extraction uses `tar` on macOS/Linux and PowerShell on Windows.

Repeat the installation command to upgrade. With `--ignore-scripts`, run `npm rebuild --global codex-transfer` to download the binary. Uninstall with `npm uninstall --global codex-transfer`.

### Homebrew / macOS

```sh
brew tap waterkyuu/codex-transfer https://github.com/Waterkyuu/codex-transfer.git
brew install --cask waterkyuu/codex-transfer/codex-transfer
brew upgrade --cask waterkyuu/codex-transfer/codex-transfer
```

Stable releases generate `Casks/codex-transfer.rb` on the repository's default branch with architecture-specific URLs and SHA-256 hashes. The first stable release must exist before the Cask is available. Uninstall with `brew uninstall --cask waterkyuu/codex-transfer/codex-transfer`.

### macOS / Linux script

```sh
curl -fsSL https://github.com/Waterkyuu/codex-transfer/releases/latest/download/install.sh | sh
```

The default destination is `~/.local/bin`. The script prints PATH instructions without editing shell configuration. For a system installation:

```sh
curl -fsSL https://github.com/Waterkyuu/codex-transfer/releases/latest/download/install.sh | sudo sh -s -- --system
```

The default system destination is `/usr/local/bin`. Re-run to upgrade; checksum failure preserves the existing binary. Symlink destinations are rejected, so installations managed by npm or Homebrew must be upgraded through that manager.

After downloading the script, select an existing published version and destination:

```sh
CODEX_TRANSFER_VERSION=v1.2.3 CODEX_TRANSFER_INSTALL_DIR="$HOME/.local/bin" sh scripts/install.sh
```

`v1.2.3` is an example. `CODEX_TRANSFER_SYSTEM_PREFIX` changes the system prefix. `CODEX_TRANSFER_RELEASE_BASE_URL` selects a mirror with `vVERSION/checksums.txt` and matching archives.

### Windows / PowerShell

```powershell
irm https://github.com/Waterkyuu/codex-transfer/releases/latest/download/install.ps1 | iex
```

Installs to `%LOCALAPPDATA%\Programs\codex-transfer`, updates user/current-process PATH, and requires no administrator access. Open a new terminal and run `codex-transfer --version`. PowerShell 5.1+ is supported.

A downloaded script accepts a published version and custom directory:

```powershell
.\scripts\install.ps1 -Version v1.2.3 -InstallDir "$env:LOCALAPPDATA\Programs\codex-transfer"
```

`-NoPath` avoids PATH changes. `-ReleaseBaseUrl` and `CODEX_TRANSFER_RELEASE_BASE_URL` select a mirror. `CODEX_TRANSFER_VERSION` and `CODEX_TRANSFER_INSTALL_DIR` supply environment defaults. Close running executables before upgrading and re-run the script.

Scoop becomes available after the first stable release:

```powershell
scoop bucket add codex-transfer https://github.com/Waterkyuu/codex-transfer.git
scoop install codex-transfer/codex-transfer
scoop update codex-transfer
```

Uninstall with `scoop uninstall codex-transfer`. Installers and package managers do not delete Codex data.

## Manual installation

Download the matching `codex-transfer_VERSION_OS_ARCH` archive and `checksums.txt`, verify SHA-256, and extract `codex-transfer` or `codex-transfer.exe` into a PATH directory. macOS/Linux require executable permissions. For private repositories, download through an authorized signed-in browser or GitHub CLI.

Archives contain a CLI, not a macOS application installer or Windows MSI. The release workflow does not provide code signing or Apple notarization.

## Maintainer releases

`.goreleaser.yml` configures six builds, archives, checksums, Cask, and Scoop manifests. Stable releases update the repository's default branch without assuming `main`. Releases also attach both installer scripts. See the [GoReleaser Cask documentation](https://goreleaser.com/customization/publish/homebrew_casks/).

With Node.js 22+, Go, and GoReleaser 2.18.3 installed:

```sh
task lint
task test
task build
task test-installers
goreleaser check
goreleaser release --snapshot --skip=publish --clean
```

Run the Windows installer fixture with `pwsh -NoProfile -File scripts/install.test.ps1`. Temporary archives, caches, and binaries must remain untracked. `dist/`, `npm/vendor/`, `node_modules/`, and npm tarballs are ignored.

Configure GitHub Actions write permissions and `NPM_TOKEN` with scope publishing access and the account's required 2FA policy. After installer checks pass on macOS/Linux/Windows, pushing a valid version tag triggers publication:

```sh
git tag v1.2.3
git push origin v1.2.3
```

These commands publish externally; the version is an example. npm is published only after Release succeeds. Stable versions use `latest`; prereleases use `next` and do not update Cask/Scoop manifests. If npm publication fails, retry the npm job rather than republishing the existing Release. Verify anonymous access to release downloads before a public launch.
