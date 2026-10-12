#!/bin/sh
set -eu

case "${1:-}" in
  '') install_dir=${CODEX_TRANSFER_INSTALL_DIR:-"$HOME/.local/bin"} ;;
  --system) install_dir="${CODEX_TRANSFER_SYSTEM_PREFIX:-/usr/local}/bin" ;;
  *) echo 'Usage: install.sh [--system]' >&2; exit 1 ;;
esac
if [ "$#" -gt 1 ]; then echo 'Usage: install.sh [--system]' >&2; exit 1; fi
case "$(uname -s)" in
  Darwin) platform=darwin ;;
  Linux) platform=linux ;;
  *) echo 'Supported systems: macOS and Linux.' >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) architecture=amd64 ;;
  arm64|aarch64) architecture=arm64 ;;
  *) echo 'Supported architectures: amd64 and arm64.' >&2; exit 1 ;;
esac
version=${CODEX_TRANSFER_VERSION:-}
if [ -z "$version" ]; then
  latest_url=$(curl -fsSL --connect-timeout 15 --max-time 60 -o /dev/null -w '%{url_effective}' \
    https://github.com/Waterkyuu/codex-transfer/releases/latest)
  version=${latest_url##*/}
fi
if ! printf '%s\n' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
  echo "No valid published release found: $version" >&2; exit 1
fi
asset="codex-transfer_${version#v}_${platform}_${architecture}.tar.gz"
base_url=${CODEX_TRANSFER_RELEASE_BASE_URL:-https://github.com/Waterkyuu/codex-transfer/releases/download}
release_url="${base_url%/}/$version"
temporary_dir=$(mktemp -d)
staged=
trap 'rm -rf "$temporary_dir"; if [ -n "$staged" ]; then rm -f "$staged"; fi' EXIT
trap 'exit 1' HUP INT TERM
curl -fsSL --connect-timeout 15 --max-time 120 "$release_url/checksums.txt" -o "$temporary_dir/checksums.txt"
curl -fsSL --connect-timeout 15 --max-time 120 "$release_url/$asset" -o "$temporary_dir/$asset"
expected=$(awk -v name="$asset" '$2 == name || $2 == "*" name { print $1 }' "$temporary_dir/checksums.txt")
if ! printf '%s\n' "$expected" | grep -Eq '^[[:xdigit:]]{64}$' || [ "$(printf '%s\n' "$expected" | wc -l | tr -d ' ')" != 1 ]; then
  echo "No unique valid SHA-256 checksum for $asset." >&2; exit 1
fi
expected=$(printf '%s' "$expected" | tr 'A-F' 'a-f')
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$temporary_dir/$asset" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$temporary_dir/$asset" | awk '{ print $1 }')
else
  echo 'A SHA-256 tool is required (sha256sum or shasum).' >&2; exit 1
fi
if [ "$actual" != "$expected" ]; then echo "SHA-256 mismatch for $asset." >&2; exit 1; fi
mkdir "$temporary_dir/extracted"
tar -xzf "$temporary_dir/$asset" -C "$temporary_dir/extracted" codex-transfer
binary="$temporary_dir/extracted/codex-transfer"
if [ ! -f "$binary" ] || [ -L "$binary" ]; then echo 'Archive has no regular codex-transfer binary.' >&2; exit 1; fi
mkdir -p "$install_dir"
if [ -L "$install_dir/codex-transfer" ]; then
  echo 'Existing codex-transfer is a symlink; update it through its original package manager.' >&2; exit 1
fi
if [ -e "$install_dir/codex-transfer" ] && [ ! -f "$install_dir/codex-transfer" ]; then
  echo 'Installation destination is not a regular file.' >&2; exit 1
fi
staged=$(mktemp "$install_dir/.codex-transfer-XXXXXX")
install -m 755 "$binary" "$staged"
mv -f "$staged" "$install_dir/codex-transfer"
staged=
echo "Installed codex-transfer $version to $install_dir."
case ":$PATH:" in *":$install_dir:"*) ;; *) echo "Add $install_dir to PATH to run codex-transfer." ;; esac
