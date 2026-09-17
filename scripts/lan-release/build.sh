#!/usr/bin/env bash
set -euo pipefail

# Build a local release without publishing, installing, or starting services.
repo=$(git rev-parse --show-toplevel)
version=${1:?Usage: build.sh VERSION OUTPUT_DIR HTTPS_DOWNLOAD_BASE VERIFIED_CA_FILE}
out=${2:?Missing output directory}
base_url=${3:?Missing immutable HTTPS download base}
ca_file=${4:?Missing verified s90 public CA certificate path}
openssl x509 -in "$ca_file" -noout -checkend 0 >/dev/null
if grep -q 'PRIVATE KEY' "$ca_file"; then echo 'CA input must not contain a private key.' >&2; exit 2; fi
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+-zh-tw\.[0-9]+(-rc\.[0-9]+)?$ ]] || exit 2
[[ "$base_url" =~ ^https://[a-zA-Z0-9.:/-]+$ && "$base_url" != */ ]] || exit 2
[[ "$out" == /* && ! -e "$out" ]] || { echo 'Use a new absolute output directory.' >&2; exit 2; }
[[ -z $(git status --porcelain) ]] || { echo 'Commit source changes before building.' >&2; exit 2; }
commit=$(git rev-parse HEAD)
build_date=$(date -u +%Y-%m-%dT%H:%M:%SZ)
mkdir -p "$out"
while IFS= read -r line; do
  if [[ "$line" == '@DOWNLOAD_CA_PEM@' ]]; then
    cat "$ca_file"
    printf '\n'
  else
    printf '%s\n' "$line"
  fi
done < "$repo/scripts/lan-release/install.sh.in" > "$out/install.sh"
sed -i "s|@VERSION@|$version|g; s|@BASE_URL@|$base_url|g" "$out/install.sh"
export CGO_ENABLED=0
export GOMAXPROCS=${GOMAXPROCS:-4}
export GOFLAGS="${GOFLAGS:-} -p=4"
cd "$repo/server"
for platform in linux-amd64 linux-arm64 darwin-amd64 darwin-arm64; do
  mkdir "$out/$platform"
  GOOS=${platform%-*} GOARCH=${platform#*-} go build -trimpath \
    -ldflags "-s -w -X main.version=$version -X main.commit=$commit -X main.date=$build_date" \
    -o "$out/$platform/multica" ./cmd/multica
  cp "$repo/LICENSE" "$repo/NOTICE" "$out/$platform/"
  cp "$ca_file" "$out/$platform/s90-ca.crt"
  tar -czf "$out/multica-$platform.tar.gz" -C "$out/$platform" multica LICENSE NOTICE s90-ca.crt
  checksum=$(sha256sum "$out/multica-$platform.tar.gz")
  placeholder=$(printf '%s' "$platform" | tr 'a-z-' 'A-Z_')
  sed -i "s|@$placeholder@|${checksum%% *}|g" "$out/install.sh"
done
mkdir "$out/server-linux-amd64"
for binary in server migrate; do
  GOOS=linux GOARCH=amd64 go build -trimpath \
    -ldflags "-s -w -X main.version=$version -X main.commit=$commit" \
    -o "$out/server-linux-amd64/$binary" "./cmd/$binary"
done
cp -R migrations "$out/server-linux-amd64/"
cp "$repo/LICENSE" "$repo/NOTICE" "$out/server-linux-amd64/"
tar -czf "$out/server-linux-amd64.tar.gz" -C "$out/server-linux-amd64" .
git -C "$repo" archive --format=tar.gz --output="$out/source.tar.gz" HEAD
cp "$repo/docs/lan-installation.zh-tw.md" "$out/INSTALL.md"
printf 'version=%s\ncommit=%s\nbuild_date=%s\ngo=%s\n' \
  "$version" "$commit" "$build_date" "$(go version)" > "$out/BUILD.txt"
cd "$out"
sha256sum ./*.tar.gz install.sh INSTALL.md BUILD.txt > SHA256SUMS
echo "Release candidate: $out"
