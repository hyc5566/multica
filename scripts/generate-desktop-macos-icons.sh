#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "Usage: $0 <1024x1024-source.png> [downward-shift-pixels]" >&2
  echo "Example: $0 ./multica-icon-source.png 6" >&2
}

source_png=${1:-}
downward_shift=${2:-0}

if [[ -z "$source_png" || ! -f "$source_png" ]]; then
  usage
  exit 2
fi
if [[ ! "$downward_shift" =~ ^[0-9]+$ ]] || ((downward_shift > 128)); then
  echo "error: downward-shift-pixels must be an integer from 0 to 128" >&2
  exit 2
fi
if [[ $(uname -s) != "Darwin" ]]; then
  echo "error: iconutil requires macOS" >&2
  exit 2
fi

for required_command in git magick oxipng iconutil; do
  if ! command -v "$required_command" >/dev/null 2>&1; then
    echo "error: missing required command: $required_command" >&2
    exit 2
  fi
done

repo_dir=$(git rev-parse --show-toplevel 2>/dev/null || true)
if [[ -z "$repo_dir" ]]; then
  echo "error: run this script from a Multica Git checkout" >&2
  exit 2
fi

source_png=$(cd "$(dirname "$source_png")" && pwd -P)/$(basename "$source_png")
dimensions=$(magick identify -format '%wx%h' "$source_png")
if [[ "$dimensions" != "1024x1024" ]]; then
  echo "error: source must be 1024x1024 (found $dimensions)" >&2
  exit 2
fi

desktop_dir="$repo_dir/apps/desktop"
work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT

master_png="$work_dir/icon.png"
magick "$source_png" \
  -background none \
  -gravity north \
  -splice "0x${downward_shift}" \
  -crop "1024x1024+0+0" \
  +repage \
  -strip \
  "PNG32:$master_png"
oxipng -o max --strip safe "$master_png"

install -m 0644 "$master_png" "$desktop_dir/build/icon.png"
install -m 0644 "$master_png" "$desktop_dir/resources/icon.png"

for icon_size in 16 24 32 48 64 128 256 512; do
  output_png="$desktop_dir/build/icons/${icon_size}x${icon_size}.png"
  magick "$master_png" -filter Lanczos -resize "${icon_size}x${icon_size}" -strip "PNG32:$output_png"
  oxipng -o max --strip safe "$output_png"
done

iconset_dir="$work_dir/icon.iconset"
mkdir "$iconset_dir"
while read -r logical_size scale output_name; do
  pixel_size=$((logical_size * scale))
  magick "$master_png" -filter Lanczos -resize "${pixel_size}x${pixel_size}" -strip \
    "PNG32:$iconset_dir/$output_name"
  oxipng -o max --strip safe "$iconset_dir/$output_name"
done <<'SIZES'
16 1 icon_16x16.png
16 2 icon_16x16@2x.png
32 1 icon_32x32.png
32 2 icon_32x32@2x.png
128 1 icon_128x128.png
128 2 icon_128x128@2x.png
256 1 icon_256x256.png
256 2 icon_256x256@2x.png
512 1 icon_512x512.png
512 2 icon_512x512@2x.png
SIZES

iconutil --convert icns --output "$desktop_dir/build/icon.icns" "$iconset_dir"

echo "Regenerated aligned Desktop PNG and macOS ICNS assets."
