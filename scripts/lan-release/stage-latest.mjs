#!/usr/bin/env node
// Prepare verified copies for the stable download directory. Never publish.
import { createHash } from 'node:crypto';
import { copyFileSync, mkdirSync, readFileSync, writeFileSync, statSync, existsSync, rmSync } from 'node:fs';
import { basename, join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const [releaseArg, outputArg] = process.argv.slice(2);
if (!releaseArg || !outputArg || process.argv.length !== 4) {
  throw new Error('Usage: node stage-latest.mjs RELEASE_DIR NEW_OUTPUT_DIR');
}
const release = resolve(releaseArg), output = resolve(outputArg);
if (existsSync(output)) throw new Error(`Refusing to overwrite ${output}`);
const manifest = JSON.parse(readFileSync(join(release, 'latest.json'), 'utf8'));
if (!/^\d+\.\d+\.\d+-zh-tw\.\d+$/.test(manifest.version)) throw new Error('Not a stable release');
const app = manifest.desktop?.['darwin-arm64'];
const appName = `multica-desktop-${manifest.version}-mac-arm64.zip`;
if (!app || new URL(app.url).pathname.split('/').at(-1) !== appName) throw new Error('App name does not match release');
const sha = (path) => createHash('sha256').update(readFileSync(path)).digest('hex');
const files = new Map();
for (const line of readFileSync(join(release, 'SHA256SUMS'), 'utf8').trim().split('\n')) {
  const match = /^([a-f0-9]{64}) [ *](?:\.\/)?([^/\\]+)$/.exec(line);
  if (!match || ['.', '..', 'SHA256SUMS'].includes(match[2])) throw new Error(`Invalid checksum entry: ${line}`);
  const [, checksum, name] = match;
  if (files.has(name)) throw new Error(`Duplicate checksum entry: ${name}`);
  const path = join(release, name);
  if (!statSync(path).isFile() || sha(path) !== checksum) throw new Error(`Checksum mismatch: ${name}`);
  files.set(name, checksum);
}
if (files.get(appName) !== app.sha256 || statSync(join(release, appName)).size !== app.size) throw new Error('App differs from manifest');
if (files.get('install.sh') !== manifest.cli.install_sha256) throw new Error('Installer differs from manifest');
if (!readFileSync(join(release, 'BUILD.txt'), 'utf8').includes(`version=${manifest.version}\n`)) throw new Error('BUILD.txt version differs');
if (!readFileSync(join(release, 'BUILD.txt'), 'utf8').includes(`commit=${manifest.commit}\n`)) throw new Error('BUILD.txt commit differs');

mkdirSync(output, {recursive: true});
try {
  const sums = [];
  for (const [name, checksum] of files) {
    // Channel URLs stay versioned so in-flight automatic updates remain pinned.
    if (['download.html', 'latest.json'].includes(name)) continue;
    const target = name === appName ? 'multica-desktop-latest-mac-arm64.zip' : basename(name);
    copyFileSync(join(release, name), join(output, target));
    if (sha(join(output, target)) !== checksum) throw new Error(`Copy mismatch: ${target}`);
    sums.push(`${checksum}  ${target}`);
  }
  writeFileSync(join(output, 'SHA256SUMS'), `${sums.join('\n')}\n`);
  copyFileSync(join(release, 'latest.json'), join(output, 'latest.json'));
  const renderer = fileURLToPath(new URL('./render-download-page.sh', import.meta.url));
  const result = spawnSync('bash', [renderer, release, join(output, 'download.html')], {encoding: 'utf8'});
  if (result.status !== 0) throw new Error(result.stderr || 'Rendering failed');
  console.log(`Staged ${manifest.version}: ${output}`);
} catch (error) {
  rmSync(output, {recursive: true, force: true});
  throw error;
}
