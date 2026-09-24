#!/usr/bin/env bash
set -euo pipefail

# Delegate to the immutable release installer, whose artifact hashes are generated
# after this source commit is built. Both repository entry points target s90.
version='0.4.43-zh-tw.6'
installer_url="https://github.com/hyc5566/multica/releases/download/zh-tw-v$version/install.sh"
[[ ${1:-} == "" || ${1:-} == --login || ${1:-} == --service ]] || { echo 'Usage: install.sh [--login|--service]' >&2; exit 2; }
[[ $# -le 1 ]] || { echo 'Usage: install.sh [--login|--service]' >&2; exit 2; }
command -v curl >/dev/null
tmp=$(mktemp -d)
trap 'rm -rf -- "$tmp"' EXIT
# Establish download trust before contacting the artifact server. A private-only inherited
# CA bundle must not hide the OS public roots. Keep explicitly provided roots.
public_ca=''
for candidate in /etc/ssl/certs/ca-certificates.crt /etc/pki/tls/certs/ca-bundle.crt /etc/ssl/cert.pem; do
  if [[ -f "$candidate" && -r "$candidate" && -s "$candidate" ]]; then public_ca="$candidate"; break; fi
done
[[ -n "$public_ca" ]] || { echo 'System CA bundle not found; install the OS ca-certificates package first.' >&2; exit 1; }
if [[ -n ${MULTICA_INSTALL_CA_FILE:-} && ( ! -f "$MULTICA_INSTALL_CA_FILE" || ! -r "$MULTICA_INSTALL_CA_FILE" || ! -s "$MULTICA_INSTALL_CA_FILE" ) ]]; then
  echo 'MULTICA_INSTALL_CA_FILE must name a readable, non-empty public CA certificate file.' >&2
  exit 1
fi
if grep -Eq -- '-----BEGIN .*PRIVATE KEY-----' "$public_ca"; then
  echo 'CA bundle contains a private key; use public CA certificates only.' >&2; exit 1
fi
cat "$public_ca" > "$tmp/download-ca.crt"
for candidate in "${CURL_CA_BUNDLE:-}" "${SSL_CERT_FILE:-}" "${MULTICA_INSTALL_CA_FILE:-}"; do
  if [[ -n "$candidate" && "$candidate" != "$public_ca" && -f "$candidate" && -r "$candidate" && -s "$candidate" ]]; then
    if grep -Eq -- '-----BEGIN .*PRIVATE KEY-----' "$candidate"; then
      echo 'CA bundle contains a private key; use public CA certificates only.' >&2; exit 1
    fi
    printf '\n' >> "$tmp/download-ca.crt"
    cat "$candidate" >> "$tmp/download-ca.crt"
  fi
done
# Never execute a partial download or relax TLS verification for executable code.
curl -q --cacert "$tmp/download-ca.crt" --proxy-cacert "$tmp/download-ca.crt" \
  --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  --connect-timeout 10 --max-time 120 "$installer_url" -o "$tmp/install.sh"
bash "$tmp/install.sh" "$@"
