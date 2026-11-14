#!/usr/bin/env bash
set -euo pipefail

# Bootstrap a NEW CLI installation only. Managed updates use guideline.md.
version='0.9.0-zh-tw.1+up.013be77'
archive_url='https://github.com/hyc5566/multica/releases/download/zh-tw-v0.9.0-zh-tw.1%2Bup.013be77/multica-cli-0.9.0-zh-tw.1%2Bup.013be77-linux-amd64.tar.gz'
archive_sha256='260da62ecc572d994d6a793647a709623eefab2559d2e65242d90da0872c0d1e'
server_url='https://10.1.24.90:45671'
ca_sha256='e1ffc707220f8dfa00ffa4b7c9699d9b93fd1d407d065bf8801c3cea67037e6d'
[[ $# == 0 ]] || { echo 'Usage: bash install.sh (new Linux x64 installations only)' >&2; exit 2; }
[[ $(uname -s)-$(uname -m) == Linux-x86_64 ]] || {
  echo 'This release provides a standalone CLI for Linux x64 only. On Apple Silicon use the App; see guideline.md.' >&2; exit 2;
}
root=${MULTICA_INSTALL_ROOT:-"$HOME/.local"}
[[ $root == /* ]] || { echo 'MULTICA_INSTALL_ROOT must be absolute.' >&2; exit 2; }
if [[ -z ${MULTICA_INSTALL_ROOT:-} ]] &&
  { command -v multica >/dev/null || [[ -e $HOME/.multica/config.json ]]; }; then
  echo 'Existing CLI or profile found. Follow guideline.md; this bootstrap is for new installations.' >&2; exit 2;
fi
destination="$root/share/multica-selfhost/$version"
launcher="$root/bin/multica"
[[ ! -e $launcher && ! -L $launcher && ! -e $destination && ! -L $destination ]] || {
  echo 'Existing installation found. Preserve it and follow guideline.md for a controlled update.' >&2; exit 2;
}
for required in curl tar sha256sum git; do command -v "$required" >/dev/null; done
git_version=$(git --version)
if [[ ! $git_version =~ ^git\ version\ ([0-9]+)\.([0-9]+)(\.|$) ]] ||
  (( 10#${BASH_REMATCH[1]} < 2 || (10#${BASH_REMATCH[1]} == 2 && 10#${BASH_REMATCH[2]} < 40) )); then
  echo 'Git 2.40 or later is required. Update Git separately before retrying.' >&2; exit 2;
fi
temporary=$(mktemp -d)
trap 'rm -rf -- "$temporary"' EXIT
public_ca=/etc/ssl/certs/ca-certificates.crt
[[ -s $public_ca ]] || { echo 'Install the OS ca-certificates package first.' >&2; exit 2; }
cat "$public_ca" > "$temporary/ca-bundle.crt"
for candidate in "${MULTICA_INSTALL_CA_FILE:-}" "${CURL_CA_BUNDLE:-}" "${SSL_CERT_FILE:-}"; do
  [[ -n $candidate ]] || continue
  [[ -f $candidate && -r $candidate && -s $candidate ]] || { echo 'CA file must be readable and non-empty.' >&2; exit 2; }
  if grep -Eq -- '-----BEGIN .*PRIVATE KEY-----' "$candidate"; then
    echo 'Use public CA certificates only.' >&2; exit 2;
  fi
  printf '\n' >> "$temporary/ca-bundle.crt"
  cat "$candidate" >> "$temporary/ca-bundle.crt"
done
download() {
  curl -q --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
    --cacert "$temporary/ca-bundle.crt" --proxy-cacert "$temporary/ca-bundle.crt" \
    --connect-timeout 10 --max-time 120 "$1" -o "$2"
}
download "$archive_url" "$temporary/cli.tar.gz"
printf '%s  %s\n' "$archive_sha256" "$temporary/cli.tar.gz" | sha256sum --check --status || {
  echo 'CLI checksum mismatch. Installation unchanged.' >&2; exit 1;
}
# The trusted script carries only the public server CA, never a private key.
cat > "$temporary/s90-ca.crt" <<'S90_PUBLIC_CA'
-----BEGIN CERTIFICATE-----
MIIBpjCCAUugAwIBAgIQF+6Xd72ZRJbG+uLU/dfJ4TAKBggqhkjOPQQDAjAxMS8w
LQYDVQQDEyZNdWx0aWNhIHM5MCAyMDI2LTA5LTE3IC0gMjAyNiBFQ0MgUm9vdDAe
Fw0yNjA5MTcwOTMzMjRaFw0zNjA3MjYwOTMzMjRaMDExLzAtBgNVBAMTJk11bHRp
Y2EgczkwIDIwMjYtMDktMTcgLSAyMDI2IEVDQyBSb290MFkwEwYHKoZIzj0CAQYI
KoZIzj0DAQcDQgAEqF+SrGZZbLCLMc+BajtWHnG0dgTuicYZlGjzcM9NO2dFCUbW
SO670r7W3bwXZ0p4s7SezU56np8qaClI5kNpB6NFMEMwDgYDVR0PAQH/BAQDAgEG
MBIGA1UdEwEB/wQIMAYBAf8CAQEwHQYDVR0OBBYEFJ5AqZnqPhCWZdET79Ql9ZGs
c1BoMAoGCCqGSM49BAMCA0kAMEYCIQCfloRil4/az2YFsOcYpWpZxQ7LHeGPHMWd
36ffT7gNlwIhALkew3XEthaEaI8Liy5uRRyoVIAGK5XXgu+h0m+W9vyb
-----END CERTIFICATE-----
S90_PUBLIC_CA
printf '%s  %s\n' "$ca_sha256" "$temporary/s90-ca.crt" | sha256sum --check --status || {
  echo 'Server CA checksum mismatch. Installation unchanged.' >&2; exit 1;
}
printf '\n' >> "$temporary/ca-bundle.crt"
cat "$temporary/s90-ca.crt" >> "$temporary/ca-bundle.crt"
tar -xzf "$temporary/cli.tar.gz" -C "$temporary" multica LICENSE NOTICE
[[ -f $temporary/multica && ! -L $temporary/multica ]] || exit 1
mkdir -p "$root/bin" "$(dirname "$destination")"
mkdir "$destination"
install -m 755 "$temporary/multica" "$destination/multica"
install -m 644 "$temporary/LICENSE" "$temporary/NOTICE" "$temporary/ca-bundle.crt" "$destination/"
{
  printf '#!/usr/bin/env bash\n'
  printf 'export MULTICA_SERVER_URL=%q MULTICA_APP_URL=%q\n' "$server_url" "$server_url"
  printf 'export MULTICA_CA_CERT_FILE=%q\n' "$destination/ca-bundle.crt"
  printf 'export MULTICA_DAEMON_AUTO_UPDATE=false MULTICA_DAEMON_AUTO_RELOAD=false\n'
  printf 'exec %q "$@"\n' "$destination/multica"
} > "$temporary/launcher"
install -m 755 "$temporary/launcher" "$destination/launcher"
# Link creation refuses an existing destination instead of replacing its binary.
ln "$destination/launcher" "$launcher"
printf 'Installed %s at %s\nServer: %s\n' "$version" "$launcher" "$server_url"
printf 'Add %s/bin to PATH, then run multica login. Start a daemon only after reviewing guideline.md.\n' "$root"
