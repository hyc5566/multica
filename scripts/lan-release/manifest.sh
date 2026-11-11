#!/usr/bin/env bash
set -euo pipefail
usage='Usage: manifest.sh RELEASE_DIR HTTPS_DOWNLOAD_BASE NOTES_URL [MAC_ARM64_APP_ZIP]'
[[ $# -ge 3 && $# -le 4 ]] || { echo "$usage" >&2; exit 2; }
script_dir=$(cd "$(dirname "$0")" && pwd)
exec python3 "$script_dir/verify_release_binary.py" "$@"
