#!/usr/bin/env bash
set -euo pipefail

# Publish an already verified stage; preview unless explicitly applied.
usage='Usage: publish-latest.sh STAGED_DIR RELEASES_ROOT [--apply]'
stage=${1:?$usage}
root=${2:?$usage}
mode=${3:-}
[[ $# -le 3 && ( -z "$mode" || "$mode" == --apply ) ]] || { echo "$usage" >&2; exit 2; }
[[ -d "$stage" && -d "$root" && "$stage" != "$root" ]] || exit 2
stage=$(cd "$stage" && pwd)
root=$(cd "$root" && pwd)
[[ -f "$stage/multica-desktop-latest-mac-arm64.zip" && -f "$stage/download.html" && -f "$stage/latest.json" ]] || exit 2
(cd "$stage" && sha256sum -c SHA256SUMS)
version=$(sed -n 's/^version=//p' "$stage/BUILD.txt")
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+-zh-tw\.[0-9]+$ && -d "$root/$version" ]] || { echo 'Publish the immutable version first.' >&2; exit 2; }
echo "Publish $version copies to $root/latest; then switch download.html and latest.json."
[[ "$mode" == --apply ]] || exit 0

# Retain complete previous copies for an explicit rollback, without pruning.
backup="$root/.before-latest-$(date -u +%Y%m%dT%H%M%SZ)-$$"
mkdir "$backup"
for name in latest download.html latest.json; do
  [[ ! -e "$root/$name" ]] || cp -a "$root/$name" "$backup/"
done
echo "Rollback backup: $backup"
mkdir -p "$root/latest"
for file in "$stage"/*; do
  name=${file##*/}
  [[ "$name" != download.html && "$name" != latest.json && "$name" != SHA256SUMS ]] || continue
  cp "$file" "$root/latest/.$name.new-$$"
  mv "$root/latest/.$name.new-$$" "$root/latest/$name"
done
cp "$stage/SHA256SUMS" "$root/latest/.SHA256SUMS.new-$$"
mv "$root/latest/.SHA256SUMS.new-$$" "$root/latest/SHA256SUMS"
(cd "$root/latest" && sha256sum -c SHA256SUMS)
# Automatic update URLs remain immutable in this channel document.
for name in download.html latest.json; do
  cp "$stage/$name" "$root/.$name.new-$$"
  mv "$root/.$name.new-$$" "$root/$name"
done
echo "Published $version. Verify public HTTPS downloads before accepting."
