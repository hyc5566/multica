#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "Usage: $0 <1024x1024-source.png> [--hig-standard | downward-shift-pixels]" >&2
  echo "Example: $0 ./multica-icon-source.png --hig-standard" >&2
}

source_png=${1:-}
mode=${2:---hig-standard}

if [[ -z "$source_png" || ! -f "$source_png" ]]; then
  usage
  exit 2
fi

if [[ $(uname -s) != "Darwin" ]]; then
  echo "error: iconutil requires macOS" >&2
  exit 2
fi

if ! command -v iconutil >/dev/null 2>&1; then
  echo "error: missing iconutil command" >&2
  exit 2
fi

repo_dir=$(git rev-parse --show-toplevel 2>/dev/null || true)
if [[ -z "$repo_dir" ]]; then
  echo "error: run this script from a Multica Git checkout" >&2
  exit 2
fi

desktop_dir="$repo_dir/apps/desktop"
work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT

master_png="$work_dir/icon.png"

python3 -c '
import sys
from PIL import Image

src_path = sys.argv[1]
dst_path = sys.argv[2]
mode = sys.argv[3]

orig = Image.open(src_path)
if mode == "--hig-standard" or mode == "hig":
    # Apple HIG standard: 824x824 squircle centered in 1024x1024 canvas (margin 100px)
    scale = 824.0 / 910.0
    new_w = int(round(1024 * scale))
    new_h = int(round(1024 * scale))
    scaled = orig.resize((new_w, new_h), Image.Resampling.LANCZOS)
    canvas = Image.new("RGBA", (1024, 1024), (0, 0, 0, 0))
    offset_x = (1024 - new_w) // 2
    offset_y = 100
    canvas.paste(scaled, (offset_x, offset_y))
    canvas.save(dst_path, optimize=True)
else:
    shift = int(mode)
    canvas = Image.new("RGBA", (1024, 1024), (0, 0, 0, 0))
    canvas.paste(orig, (0, shift))
    canvas.save(dst_path, optimize=True)
' "$source_png" "$master_png" "$mode"

if command -v oxipng >/dev/null 2>&1; then
  oxipng -o max --strip safe "$master_png" || true
fi

install -m 0644 "$master_png" "$desktop_dir/build/icon.png"
install -m 0644 "$master_png" "$desktop_dir/resources/icon.png"

for icon_size in 16 24 32 48 64 128 256 512; do
  output_png="$desktop_dir/build/icons/${icon_size}x${icon_size}.png"
  python3 -c '
import sys
from PIL import Image
m = Image.open(sys.argv[1])
sz = int(sys.argv[2])
r = m.resize((sz, sz), Image.Resampling.LANCZOS)
r.save(sys.argv[3], optimize=True)
' "$master_png" "$icon_size" "$output_png"
  if command -v oxipng >/dev/null 2>&1; then
    oxipng -o max --strip safe "$output_png" || true
  fi
done

iconset_dir="$work_dir/icon.iconset"
mkdir -p "$iconset_dir"

python3 -c '
import os, sys
from PIL import Image
m = Image.open(sys.argv[1])
out_dir = sys.argv[2]
sizes = [
    (16, 1, "icon_16x16.png"),
    (16, 2, "icon_16x16@2x.png"),
    (32, 1, "icon_32x32.png"),
    (32, 2, "icon_32x32@2x.png"),
    (128, 1, "icon_128x128.png"),
    (128, 2, "icon_128x128@2x.png"),
    (256, 1, "icon_256x256.png"),
    (256, 2, "icon_256x256@2x.png"),
    (512, 1, "icon_512x512.png"),
    (512, 2, "icon_512x512@2x.png"),
]
for base_sz, factor, fname in sizes:
    px = base_sz * factor
    r = m.resize((px, px), Image.Resampling.LANCZOS)
    r.save(os.path.join(out_dir, fname), optimize=True)
' "$master_png" "$iconset_dir"

iconutil --convert icns --output "$desktop_dir/build/icon.icns" "$iconset_dir"

echo "Regenerated Apple HIG aligned Desktop PNG and macOS ICNS assets."
