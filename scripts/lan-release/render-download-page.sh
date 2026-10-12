#!/usr/bin/env bash
set -euo pipefail

# Render the standalone /download page from a release's latest.json, so the
# page always names exactly the App package the update channel offers.
usage='Usage: render-download-page.sh RELEASE_DIR OUTPUT_HTML'
release_dir=${1:?$usage}
output=${2:?$usage}
repo=$(cd "$(dirname "$0")/../.." && pwd)
[[ ! -e "$output" ]] || { echo "Refusing to overwrite $output." >&2; exit 2; }
node - "$release_dir/latest.json" "$repo/docs/download.zh-tw.html.in" "$output" <<'NODE'
const fs = require('node:fs');
const [manifestPath, templatePath, outputPath] = process.argv.slice(2);
const manifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8'));
const app = manifest.desktop?.['darwin-arm64'];
if (!app) throw new Error('latest.json has no darwin-arm64 App package');
const base = manifest.cli.install_url.replace(/\/install\.sh$/, '');
const escape = (value) => String(value).replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);
const values = {VERSION: manifest.version, APP_URL: app.url, APP_SHA256: app.sha256, NOTES_URL: manifest.notes_url, BASE_URL: base};
let html = fs.readFileSync(templatePath, 'utf8');
for (const [key, value] of Object.entries(values)) {
  if (!value) throw new Error(`missing ${key}`);
  html = html.replaceAll(`@${key}@`, escape(value));
}
if (/@[A-Z_]+@/.test(html)) throw new Error('unrendered placeholder');
fs.writeFileSync(outputPath, html, {flag: 'wx'});
NODE
echo "Wrote $output"
