#!/usr/bin/env bash
set -euo pipefail

# Render the standalone /download page from a release's latest.json, so the
# page shows the current version and stable download links.
usage='Usage: render-download-page.sh RELEASE_DIR OUTPUT_HTML'
release_dir=${1:?$usage}
output=${2:?$usage}
repo=$(cd "$(dirname "$0")/../.." && pwd)
[[ ! -e "$output" ]] || { echo "Refusing to overwrite $output." >&2; exit 2; }
node - "$release_dir/latest.json" "$repo/docs/download.zh-tw.html.in" "$output" <<'NODE'
const fs = require('node:fs');
const path = require('node:path');
const [manifestPath, templatePath, outputPath] = process.argv.slice(2);
const manifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8'));
const app = manifest.desktop?.['darwin-arm64'];
if (!app) throw new Error('latest.json has no darwin-arm64 App package');
const base = new URL('../latest/', manifest.cli.install_url).href.replace(/\/$/, '');
const appUrl = `${base}/multica-desktop-latest-mac-arm64.zip`;
const notesUrl = new URL('../latest', manifest.notes_url).href;
const escape = (value) => String(value).replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);
const published = new Date(manifest.published_at);
if (Number.isNaN(published.getTime())) throw new Error('latest.json has no valid published_at');
const publishedDate = new Intl.DateTimeFormat('sv-SE', {timeZone: 'Asia/Taipei', year: 'numeric', month: '2-digit', day: '2-digit'}).format(published);
// Release notes must provide an opening prose paragraph, optionally after a heading.
const notes = fs.readFileSync(path.join(path.dirname(manifestPath), 'RELEASE-NOTES.md'), 'utf8');
const summary = notes.split(/\r?\n\s*\r?\n/)
  .find((paragraph) => paragraph.trim() && !/^(?:#|[-*] |\d+\. |```)/.test(paragraph.trim()))
  ?.trim().replace(/\r?\n/g, ' ');
if (!summary) throw new Error('RELEASE-NOTES.md has no release summary paragraph');
const values = {VERSION: manifest.version, APP_URL: appUrl, APP_SHA256: app.sha256, NOTES_URL: notesUrl, BASE_URL: base,
  PUBLISHED_AT: published.toISOString(), PUBLISHED_DATE: publishedDate, RELEASE_SUMMARY: summary};
let html = fs.readFileSync(templatePath, 'utf8');
for (const [key, value] of Object.entries(values)) {
  if (!value) throw new Error(`missing ${key}`);
  html = html.replaceAll(`@${key}@`, escape(value));
}
if (/@[A-Z_]+@/.test(html)) throw new Error('unrendered placeholder');
fs.writeFileSync(outputPath, html, {flag: 'wx'});
NODE
echo "Wrote $output"
