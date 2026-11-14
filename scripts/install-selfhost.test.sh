#!/usr/bin/env bash
set -euo pipefail
source_script=$(cd "$(dirname "$0")" && pwd)/install-selfhost.sh
temporary=$(mktemp -d)
trap 'rm -rf -- "$temporary"' EXIT
mkdir -p "$temporary/bin" "$temporary/install/bin"
printf '#!/bin/sh\nprintf "%%s\\n" "$*"\n' > "$temporary/bin/uname"
chmod +x "$temporary/bin/uname"
if PATH="$temporary/bin:$PATH" MULTICA_INSTALL_ROOT="$temporary/install" bash "$source_script" > "$temporary/unsupported.log" 2>&1; then
  echo 'Unsupported platform was accepted' >&2; exit 1
fi
grep -q 'Linux x64 only' "$temporary/unsupported.log"
printf 'preserve-existing-binary\n' > "$temporary/install/bin/multica"
if MULTICA_INSTALL_ROOT="$temporary/install" bash "$source_script" > "$temporary/existing.log" 2>&1; then
  echo 'Existing launcher was overwritten' >&2; exit 1
fi
grep -q 'Existing installation found' "$temporary/existing.log"
[[ $(cat "$temporary/install/bin/multica") == preserve-existing-binary ]]
if MULTICA_INSTALL_ROOT="$temporary/new" bash "$source_script" --upgrade > "$temporary/upgrade.log" 2>&1; then
  echo 'Bootstrap accepted an update operation' >&2; exit 1
fi
[[ ! -e $temporary/new ]]
cat > "$temporary/bin/curl" <<'STUB'
#!/usr/bin/env bash
while [[ $# -gt 0 ]]; do
  if [[ $1 == -o ]]; then printf 'corrupt archive' > "$2"; exit 0; fi
  shift
done
exit 2
STUB
chmod +x "$temporary/bin/curl"
# Use real uname for the checksum path.
rm "$temporary/bin/uname"
if PATH="$temporary/bin:$PATH" MULTICA_INSTALL_ROOT="$temporary/corrupt" bash "$source_script" > "$temporary/checksum.log" 2>&1; then
  echo 'Corrupt archive was accepted' >&2; exit 1
fi
grep -q 'CLI checksum mismatch' "$temporary/checksum.log"
[[ ! -e $temporary/corrupt ]]
echo 'PASS: unsupported platform, existing launcher, update refusal and checksum failure preserve installation'
