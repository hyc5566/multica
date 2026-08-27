#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "Usage: $0 [repo-dir] [desktop-version]" >&2
  echo "Example: $0 . 0.4.36-zh-tw.1" >&2
}

repo_dir=${1:-$(git rev-parse --show-toplevel 2>/dev/null || true)}
requested_version=${2:-}

if [[ -z "$repo_dir" || ! -d "$repo_dir/.git" ]]; then
  usage
  echo "error: repo-dir must be a Multica Git checkout" >&2
  exit 2
fi

repo_dir=$(cd "$repo_dir" && pwd -P)
desktop_dir="$repo_dir/apps/desktop"
builder_config="$desktop_dir/electron-builder.zh-tw.yml"

if [[ $(uname -s) != "Darwin" || $(uname -m) != "arm64" ]]; then
  echo "error: this build script currently supports Apple Silicon macOS only" >&2
  exit 2
fi

for required_command in git node pnpm xcode-select codesign ditto shasum ssh scp; do
  if ! command -v "$required_command" >/dev/null 2>&1; then
    echo "error: missing required command: $required_command" >&2
    exit 2
  fi
done

if ! xcode-select -p >/dev/null 2>&1; then
  echo "error: Xcode Command Line Tools are not configured" >&2
  exit 2
fi

node_major=$(node -p 'Number(process.versions.node.split(".")[0])')
if ((node_major < 22 || node_major > 24)); then
  echo "error: Node.js 22-24 is required (found $(node --version))" >&2
  exit 2
fi
if ((node_major != 22)); then
  echo "warning: official CI uses Node.js 22; this local build uses $(node --version)" >&2
fi

if [[ ! -f "$builder_config" ]]; then
  echo "error: missing Taiwan Desktop config: $builder_config" >&2
  echo "Apply the maintained zh-TW patch before building." >&2
  exit 2
fi

cd "$repo_dir"

if [[ -z "$requested_version" ]]; then
  base_tag=$(git describe --tags --match 'v[0-9]*' --abbrev=0 2>/dev/null || true)
  if [[ -z "$base_tag" ]]; then
    echo "error: no upstream vX.Y.Z tag found; pass desktop-version explicitly" >&2
    exit 2
  fi
  requested_version="${base_tag#v}-zh-tw.1"
fi

if [[ ! "$requested_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+-zh-tw\.[0-9]+$ ]]; then
  echo "error: desktop-version must look like 0.4.36-zh-tw.1" >&2
  exit 2
fi

echo "Building Multica Desktop zh-TW $requested_version from $repo_dir"
echo "pnpm=$(pnpm --version) node=$(node --version)"

pnpm install --frozen-lockfile
pnpm --filter @multica/desktop typecheck
pnpm --filter @multica/desktop test

# bundle-cli.mjs builds and embeds the matching CLI when Go is available.
# Without Go it intentionally leaves the app to use Multica's verified
# runtime bootstrap path.
pnpm -C apps/desktop run bundle-cli
pnpm -C apps/desktop exec electron-vite build

CSC_IDENTITY_AUTO_DISCOVERY=false \
  pnpm -C apps/desktop exec electron-builder \
  --config electron-builder.zh-tw.yml \
  --mac dir \
  --arm64 \
  --publish never \
  "-c.extraMetadata.version=$requested_version"

built_app="$desktop_dir/dist-zh-tw/mac-arm64/Multica 繁中版.app"
if [[ ! -d "$built_app" ]]; then
  echo "error: expected build output not found: $built_app" >&2
  exit 1
fi

codesign --verify --deep --strict "$built_app"
built_id=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$built_app/Contents/Info.plist")
built_version=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$built_app/Contents/Info.plist")

if [[ "$built_id" != "ai.multica.desktop.zh-tw.local" ]]; then
  echo "error: unexpected bundle ID: $built_id" >&2
  exit 1
fi
if [[ "$built_version" != "$requested_version" ]]; then
  echo "error: unexpected version: $built_version" >&2
  exit 1
fi

artifact_dir="$repo_dir/artifacts"
zip_path="$artifact_dir/multica-desktop-$requested_version-mac-arm64.zip"
mkdir -p "$artifact_dir"
if [[ -e "$zip_path" ]]; then
  echo "error: refusing to overwrite existing artifact: $zip_path" >&2
  exit 1
fi

ditto -c -k --sequesterRsrc --keepParent "$built_app" "$zip_path"

artifact_sha=$(shasum -a 256 "$zip_path" | awk '{print $1}')
artifact_name=$(basename "$zip_path")

if [[ ${MULTICA_ZH_TW_SKIP_S90_SYNC:-0} == "1" ]]; then
  echo "warning: s90 artifact sync explicitly skipped (MULTICA_ZH_TW_SKIP_S90_SYNC=1)" >&2
else
  artifact_host=${MULTICA_ZH_TW_ARTIFACT_HOST:-s90}
  artifact_remote_dir=${MULTICA_ZH_TW_ARTIFACT_DIR:-/home/hungyu/artifacts}
  if [[ ! "$artifact_host" =~ ^[A-Za-z0-9._@-]+$ ]]; then
    echo "error: invalid MULTICA_ZH_TW_ARTIFACT_HOST: $artifact_host" >&2
    exit 2
  fi
  if [[ ! "$artifact_remote_dir" =~ ^/[A-Za-z0-9._/-]+$ ]]; then
    echo "error: MULTICA_ZH_TW_ARTIFACT_DIR must be a simple absolute path" >&2
    exit 2
  fi

  artifact_remote_final="$artifact_remote_dir/$artifact_name"
  artifact_remote_tmp="$artifact_remote_dir/.$artifact_name.partial-$$"
  artifact_remote_checksum="$artifact_remote_final.sha256"

  echo "Syncing verified artifact to $artifact_host:$artifact_remote_dir"
  ssh -o BatchMode=yes -o ConnectTimeout=8 "$artifact_host" \
    "mkdir -p '$artifact_remote_dir' && test ! -e '$artifact_remote_final' && test ! -e '$artifact_remote_tmp'"
  scp -o BatchMode=yes -o ConnectTimeout=8 "$zip_path" "$artifact_host:$artifact_remote_tmp"

  remote_sha=$(ssh -o BatchMode=yes -o ConnectTimeout=8 "$artifact_host" \
    "sha256sum '$artifact_remote_tmp' | awk '{print \$1}'")
  if [[ "$remote_sha" != "$artifact_sha" ]]; then
    ssh -o BatchMode=yes -o ConnectTimeout=8 "$artifact_host" "rm -f '$artifact_remote_tmp'"
    echo "error: s90 artifact checksum mismatch (local $artifact_sha, remote $remote_sha)" >&2
    exit 1
  fi

  ssh -o BatchMode=yes -o ConnectTimeout=8 "$artifact_host" \
    "mv '$artifact_remote_tmp' '$artifact_remote_final' && printf '%s  %s\\n' '$artifact_sha' '$artifact_name' > '$artifact_remote_checksum' && chmod 0644 '$artifact_remote_final' '$artifact_remote_checksum' && cd '$artifact_remote_dir' && sha256sum -c '$artifact_name.sha256'"
fi

echo "Build complete:"
echo "  App: $built_app"
echo "  ZIP: $zip_path"
echo "  SHA-256: $artifact_sha"
if [[ ${MULTICA_ZH_TW_SKIP_S90_SYNC:-0} != "1" ]]; then
  echo "  s90: $artifact_host:$artifact_remote_final"
fi
echo "Signature: ad-hoc local build (not notarized)"
