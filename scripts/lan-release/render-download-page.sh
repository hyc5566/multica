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
const {createHash} = require('node:crypto');
const [manifestPath, templatePath, outputPath] = process.argv.slice(2);
const manifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8'));
const release = path.dirname(manifestPath);
const updatePackage = JSON.parse(fs.readFileSync(path.join(release, 'update-package.json'), 'utf8'));
if (updatePackage.source !== 'hyc5566/multica' || updatePackage.version !== manifest.version || updatePackage.commit !== manifest.commit) {
  throw new Error('Update package does not match this release');
}
const digest = (bytes) => createHash('sha256').update(bytes).digest('hex');
const verify = (asset) => {
  const url = new URL(asset.url);
  if (url.protocol !== 'https:' || url.origin !== new URL(manifest.cli.install_url).origin ||
      !url.pathname.includes(`/${manifest.version}/`) || url.search || url.hash) throw new Error('Update asset must use the immutable release origin');
  const bytes = fs.readFileSync(path.join(release, path.basename(url.pathname)));
  if (bytes.length !== asset.size || digest(bytes) !== asset.sha256) throw new Error('Update package asset differs from the release');
};
verify(updatePackage.script);
verify(updatePackage.manifests['linux-amd64']);
verify(updatePackage.manifests['linux-arm64']);
const app = manifest.coordinated_desktop?.['darwin-arm64'];
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
  PUBLISHED_AT: published.toISOString(), PUBLISHED_DATE: publishedDate, RELEASE_SUMMARY: summary,
  UPDATE_SCRIPT_URL: updatePackage.script.url, UPDATE_SCRIPT_SHA256: updatePackage.script.sha256,
  UPDATE_CHECKSUM_URL: updatePackage.checksums_url,
  AGENT_GUIDE_URL: new URL('/download/agent-update-guide.md', updatePackage.script.url).href};
let html = fs.readFileSync(templatePath, 'utf8');
for (const [key, value] of Object.entries(values)) {
  if (!value) throw new Error(`missing ${key}`);
  html = html.replaceAll(`@${key}@`, escape(value));
}
if (/@[A-Z_]+@/.test(html)) throw new Error('unrendered placeholder');
fs.writeFileSync(outputPath, html, {flag: 'wx'});
const guideValues = {VERSION: manifest.version, COMMIT: manifest.commit,
  APP_MANIFEST_URL: new URL('latest.json', updatePackage.script.url).href,
  APP_MANIFEST_SHA256: digest(fs.readFileSync(manifestPath)),
  SCRIPT_URL: updatePackage.script.url, SCRIPT_SHA256: updatePackage.script.sha256,
  MANIFEST_AMD64_URL: updatePackage.manifests['linux-amd64'].url,
  MANIFEST_AMD64_SHA256: updatePackage.manifests['linux-amd64'].sha256,
  MANIFEST_ARM64_URL: updatePackage.manifests['linux-arm64'].url,
  MANIFEST_ARM64_SHA256: updatePackage.manifests['linux-arm64'].sha256};
let guide = fs.readFileSync(path.join(path.dirname(templatePath), 'agent-update-guide.md.in'), 'utf8');
for (const [key, value] of Object.entries(guideValues)) {
  if (!value) throw new Error(`missing guide value ${key}`);
  guide = guide.replaceAll(`@${key}@`, String(value));
}
if (/@[A-Z_]+@/.test(guide)) throw new Error('unrendered guide placeholder');
fs.writeFileSync(path.join(path.dirname(outputPath), 'agent-update-guide.md'), guide, {flag: 'wx'});
NODE
echo "Wrote $output"
