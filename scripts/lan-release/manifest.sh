#!/usr/bin/env bash
set -euo pipefail

# Write the release channel document (latest.json) for a release built by
# build.sh. Publishing copies it to <base>/../latest.json after every asset it
# names is reachable; App and daemon update checks read only that file.
usage='Usage: manifest.sh RELEASE_DIR HTTPS_DOWNLOAD_BASE NOTES_URL [MAC_ARM64_APP_ZIP]'
release_dir=${1:?$usage}
base_url=${2:?$usage}
notes_url=${3:?$usage}
app_zip=${4:-}
[[ "$base_url" =~ ^https://[a-zA-Z0-9.:/-]+$ && "$base_url" != */ ]] || { echo 'Base must be an https URL without trailing slash.' >&2; exit 2; }
[[ "$notes_url" =~ ^https://[^[:space:]\"\\]+$ ]] || { echo 'Notes URL must be https.' >&2; exit 2; }
version=$(sed -n 's/^version=//p' "$release_dir/BUILD.txt")
commit=$(sed -n 's/^commit=//p' "$release_dir/BUILD.txt")
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+-zh-tw\.[0-9]+$ ]] || { echo "Not a channel release version: $version" >&2; exit 2; }
[[ "$commit" =~ ^[0-9a-f]{40}$ ]] || { echo 'BUILD.txt lacks a commit.' >&2; exit 2; }
[[ "${base_url##*/}" == "$version" ]] || { echo "Base must end with /$version." >&2; exit 2; }
grep -q "^version='$version'$" "$release_dir/install.sh" || { echo 'install.sh does not belong to this release.' >&2; exit 2; }

sha256_file() {
  if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi
}
file_size() { wc -c < "$1" | tr -d ' '; }
install_sum=$(sha256_file "$release_dir/install.sh")
desktop='{}'
if [[ -n "$app_zip" ]]; then
  zip_name="multica-desktop-$version-mac-arm64.zip"
  [[ "$(basename "$app_zip")" == "$zip_name" ]] || { echo "App ZIP must be named $zip_name." >&2; exit 2; }
  zip_sum=$(sha256_file "$app_zip")
  desktop=$(printf '{"darwin-arm64":{"url":"%s/%s","sha256":"%s","size":%s}}' \
    "$base_url" "$zip_name" "${zip_sum%% *}" "$(file_size "$app_zip")")
fi
printf '{"version":"%s","commit":"%s","published_at":"%s","notes_url":"%s","cli":{"install_url":"%s/install.sh","install_sha256":"%s"},"desktop":%s}\n' \
  "$version" "$commit" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$notes_url" "$base_url" "${install_sum%% *}" "$desktop" \
  > "$release_dir/latest.json"
echo "Wrote $release_dir/latest.json"
