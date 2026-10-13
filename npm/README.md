# codex-transfer

Install the native Go CLI for macOS, Linux, or Windows on amd64/arm64:

```sh
npm install --global codex-transfer
codex-transfer --version
```

Requires Node.js 22 or newer and a publicly reachable GitHub Release matching
this npm package version. The package downloads and verifies the release's
SHA-256 checksum during `postinstall`; no local Go compiler is needed.

If lifecycle scripts were disabled, run:

```sh
npm rebuild --global codex-transfer
```

Repeat the global install command to upgrade. Remove with
`npm uninstall --global codex-transfer`; Codex data is not removed.

For a release mirror, set `CODEX_TRANSFER_RELEASE_BASE_URL` to a base directory
containing `vVERSION/checksums.txt` and the matching release archive.
`CODEX_TRANSFER_VERSION` can explicitly override the package version for testing
or a controlled mirror; normally leave it unset.
On Node.js versions supporting `--use-env-proxy`, the installer enables it when
`HTTPS_PROXY` or `HTTP_PROXY` is configured. Windows extraction uses PowerShell;
macOS/Linux extraction uses `tar`.

Usage and migration: [English](https://github.com/Waterkyuu/codex-transfer#readme) · [简体中文](https://github.com/Waterkyuu/codex-transfer/blob/HEAD/README.zh-CN.md).
