#!/bin/sh

set -eu

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
temporary_dir=$(mktemp -d)
trap 'rm -rf "$temporary_dir"' EXIT HUP INT TERM

case "$(uname -s)" in
  Darwin) platform=darwin ;;
  Linux) platform=linux ;;
esac
case "$(uname -m)" in
  x86_64|amd64) architecture=amd64 ;;
  arm64|aarch64) architecture=arm64 ;;
esac

version=v1.2.3
asset="codex-transfer_1.2.3_${platform}_${architecture}.tar.gz"
release_dir="$temporary_dir/releases/download/$version"
mkdir -p "$release_dir" "$temporary_dir/package" "$temporary_dir/home"
printf '#!/bin/sh\necho installed\n' > "$temporary_dir/package/codex-transfer"
chmod 755 "$temporary_dir/package/codex-transfer"
tar -czf "$release_dir/$asset" -C "$temporary_dir/package" codex-transfer

if command -v sha256sum >/dev/null 2>&1; then
  checksum=$(sha256sum "$release_dir/$asset" | awk '{ print $1 }')
else
  checksum=$(shasum -a 256 "$release_dir/$asset" | awk '{ print $1 }')
fi
printf '%s  %s\n' "$checksum" "$asset" > "$release_dir/checksums.txt"

CODEX_TRANSFER_INSTALL_DIR="$temporary_dir/home/bin" CODEX_TRANSFER_VERSION="$version" \
  CODEX_TRANSFER_RELEASE_BASE_URL="file://$temporary_dir/releases/download" \
  sh "$repository_root/scripts/install.sh"

test "$("$temporary_dir/home/bin/codex-transfer")" = installed

CODEX_TRANSFER_SYSTEM_PREFIX="$temporary_dir/system" CODEX_TRANSFER_VERSION="$version" \
  CODEX_TRANSFER_RELEASE_BASE_URL="file://$temporary_dir/releases/download" \
  sh "$repository_root/scripts/install.sh" --system

test "$("$temporary_dir/system/bin/codex-transfer")" = installed

printf 'tampered' >> "$release_dir/$asset"
if CODEX_TRANSFER_INSTALL_DIR="$temporary_dir/home/bin" CODEX_TRANSFER_VERSION="$version" \
   CODEX_TRANSFER_RELEASE_BASE_URL="file://$temporary_dir/releases/download" \
   sh "$repository_root/scripts/install.sh" > "$temporary_dir/error.log" 2>&1; then
  echo 'Installer accepted an archive with an invalid checksum.' >&2
  exit 1
fi
grep -q 'SHA-256 mismatch' "$temporary_dir/error.log"
test "$("$temporary_dir/home/bin/codex-transfer")" = installed
