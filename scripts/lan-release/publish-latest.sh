#!/bin/sh
set -eu

# Publish an already verified stage; preview unless explicitly applied.
usage='Usage: publish-latest.sh STAGED_DIR RELEASES_ROOT [--apply]'
stage=${1:?$usage}
root=${2:?$usage}
mode=${3:-}
[ "$#" -le 3 ] || { echo "$usage" >&2; exit 2; }
case "$mode" in ''|--apply) ;; *) echo "$usage" >&2; exit 2;; esac
[ -d "$stage" ] && [ -d "$root" ] && [ "$stage" != "$root" ] || exit 2
stage=$(cd "$stage" && pwd)
root=$(cd "$root" && pwd)
[ -f "$stage/multica-desktop-latest-mac-arm64.zip" ] && [ -f "$stage/download.html" ] && [ -f "$stage/latest.json" ] || exit 2
[ -f "$stage/agent-update-guide.md" ] && [ -f "$stage/multica-update.sh" ] && [ -f "$stage/UPDATE-SHA256SUMS" ] || exit 2
verify_files() {
  directory=$1
  [ -f "$directory/SHA256SUMS" ] && [ ! -L "$directory/SHA256SUMS" ] || return 1
  awk 'NF != 2 || length($1) != 64 || $1 !~ /^[a-f0-9]+$/ || $2 !~ /^[A-Za-z0-9._+-]+$/ || $2 == "." || $2 == ".." || $2 == "SHA256SUMS" || seen[$2]++ { bad=1 } END { exit bad || NR == 0 }' "$directory/SHA256SUMS" || return 1
  while read -r checksum name; do
    [ -f "$directory/$name" ] && [ ! -L "$directory/$name" ] || return 1
  done < "$directory/SHA256SUMS"
  (cd "$directory" && sha256sum -c SHA256SUMS)
}
verify_files "$stage"
version=$(sed -n 's/^version=//p' "$stage/BUILD.txt")
printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+-zh-tw\.[0-9]+(\+up\.[0-9a-f]{7})?$' || exit 2
[ -d "$root/$version" ] || { echo 'Publish the immutable version first.' >&2; exit 2; }
immutable="$root/$version"
[ ! -L "$immutable" ] && [ ! -L "$root/latest" ] || { echo 'Release directories must not be symlinks.' >&2; exit 2; }
verify_files "$immutable"
cmp "$stage/BUILD.txt" "$immutable/BUILD.txt" >/dev/null || exit 2
cmp "$stage/latest.json" "$immutable/latest.json" >/dev/null || exit 2
# The Caddy image has only POSIX tools. Build metadata and JSON contracts were
# already verified during staging; compare every immutable byte here without
# installing Python or Go into production.
for name in BUILD.txt RELEASE-NOTES.md s90-ca.crt multica-update.sh update-package.json UPDATE-SHA256SUMS update-manifest-linux-amd64.json update-manifest-linux-arm64.json latest.json \
  "multica-$version-linux-amd64" "multica-$version-linux-arm64" "multica-$version-darwin-amd64" "multica-$version-darwin-arm64" "multica-desktop-$version-mac-arm64.zip"; do
  [ "$(awk -v name="$name" '$2 == name { print $1 }' "$immutable/SHA256SUMS")" != "" ] || exit 2
done
while read -r checksum name; do
  case "$name" in install.sh|INSTALL.md) echo 'Legacy installer cannot be published.' >&2; exit 2;; esac
  case "$name" in
    "multica-desktop-$version-mac-arm64.zip") staged_name=multica-desktop-latest-mac-arm64.zip;;
    agent-update-guide.md|download.html) continue;;
    *) staged_name=$name;;
  esac
  [ -f "$stage/$staged_name" ] && [ ! -L "$stage/$staged_name" ] || exit 2
  staged_hash=$(sha256sum "$stage/$staged_name")
  [ "${staged_hash%% *}" = "$checksum" ] || { echo "Immutable asset differs: $name" >&2; exit 2; }
done < "$immutable/SHA256SUMS"
echo "Publish $version copies to $root/latest; then switch download.html and latest.json."
[ "$mode" = --apply ] || exit 0

# Retain complete previous copies for an explicit rollback, without pruning.
backup="$root/.before-latest-$(date -u +%Y%m%dT%H%M%SZ)-$$"
mkdir "$backup"
for name in latest download.html latest.json agent-update-guide.md; do
  [ ! -e "$root/$name" ] || cp -a "$root/$name" "$backup/"
done
echo "Rollback backup: $backup"
mkdir -p "$root/latest"
for file in "$stage"/*; do
  name=${file##*/}
  case "$name" in SHA256SUMS) continue;; esac
  cp "$file" "$root/latest/.$name.new-$$"
  mv "$root/latest/.$name.new-$$" "$root/latest/$name"
done
cp "$stage/SHA256SUMS" "$root/latest/.SHA256SUMS.new-$$"
mv "$root/latest/.SHA256SUMS.new-$$" "$root/latest/SHA256SUMS"
(cd "$root/latest" && sha256sum -c SHA256SUMS)
# Publish the operation guide before the page and open the channel last.
# Automatic update URLs remain immutable in this channel document.
for name in agent-update-guide.md download.html latest.json; do
  cp "$stage/$name" "$root/.$name.new-$$"
  mv "$root/.$name.new-$$" "$root/$name"
done
echo "Published $version. Verify public HTTPS downloads before accepting."
