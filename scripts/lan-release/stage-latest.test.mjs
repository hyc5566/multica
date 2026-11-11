import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, existsSync, rmSync, readdirSync, symlinkSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const staging = fileURLToPath(new URL('./stage-latest.mjs', import.meta.url));
const publisher = fileURLToPath(new URL('./publish-latest.sh', import.meta.url));
const hash = (value) => createHash('sha256').update(value).digest('hex');
const run = (command, args) => spawnSync(command, args, {encoding: 'utf8'});

test('successive verified releases overwrite stable downloads; corrupt releases cannot publish', () => {
  const root = mkdtempSync(join(tmpdir(), 'multica-latest-'));
  try {
    const published = join(root, 'published');
    mkdirSync(published);
    writeFileSync(join(published, 'download.html'), 'previous-page');
    writeFileSync(join(published, 'latest.json'), 'previous-channel');
    const minimalBin = join(root, 'publisher-bin');
    mkdirSync(minimalBin);
    for (const command of ['awk', 'cmp', 'cp', 'date', 'grep', 'mkdir', 'mv', 'sed', 'sha256sum']) {
      const located = run('sh', ['-c', `command -v ${command}`]);
      assert.equal(located.status, 0);
      symlinkSync(located.stdout.trim(), join(minimalBin, command));
    }
    for (const version of ['0.6.1-zh-tw.1', '0.6.2-zh-tw.1']) {
      const release = join(published, version), stage = join(root, `stage-${version}`);
      mkdirSync(release);
      const appName = `multica-desktop-${version}-mac-arm64.zip`;
      const releaseLabel = version === '0.6.1-zh-tw.1' ? 'A' : 'B';
      const summary = `Release ${releaseLabel}: 修復登入回跳 & <安全文字>`;
      const base = `https://10.1.24.90:45671/downloads/${version}`;
      const script = `#!/bin/sh\necho '${version}'\n`;
      const manifestBytes = Object.fromEntries(['amd64', 'arm64'].map(arch => [arch, JSON.stringify({source: 'hyc5566/multica', version, commit: 'a'.repeat(40), platform: `linux-${arch}`, data_compatibility: 'multica-profile-v1', assets: [{role: 'binary', url: `${base}/multica-${version}-linux-${arch}`, size: Buffer.byteLength(`binary-linux-${arch}`), sha256: hash(`binary-linux-${arch}`)}]})]));
      const metadata = {source: 'hyc5566/multica', version, commit: 'a'.repeat(40),
        script: {url: `${base}/multica-update.sh`, sha256: hash(script), size: Buffer.byteLength(script)},
        manifests: Object.fromEntries(['amd64', 'arm64'].map(arch => [`linux-${arch}`,
          {url: `${base}/update-manifest-linux-${arch}.json`, sha256: hash(manifestBytes[arch]), size: Buffer.byteLength(manifestBytes[arch])}])),
        checksums_url: `${base}/UPDATE-SHA256SUMS`};
      const files = {[appName]: `App-${version}`, 's90-ca.crt': 'fake-ca',
        ...Object.fromEntries(['linux-amd64', 'linux-arm64', 'darwin-amd64', 'darwin-arm64'].map(platform => [`multica-${version}-${platform}`, `binary-${platform}`])),
        'agent-update-guide.md': 'stale guide must be regenerated',
        'multica-update.sh': script,
        'update-manifest-linux-amd64.json': manifestBytes.amd64,
        'update-manifest-linux-arm64.json': manifestBytes.arm64,
        'UPDATE-SHA256SUMS': `${hash(script)}  multica-update.sh\n`,
        'update-package.json': JSON.stringify(metadata),
        'RELEASE-NOTES.md': `# Release notes\n\n${summary}\n\n- Detailed changes\n`,
        'BUILD.txt': `version=${version}\ncommit=${'a'.repeat(40)}\n`};
      for (const [name, bytes] of Object.entries(files)) writeFileSync(join(release, name), bytes);
      const manifest = {source_id: 'hyc5566/multica', coordinated_update_protocol: 1,
        version, commit: 'a'.repeat(40), published_at: '2026-10-12T18:30:00Z', notes_url: `https://github.com/hyc5566/multica/releases/tag/zh-tw-v${version}`,
        cli: {install_url: `${base}/multica-update.sh`, install_sha256: hash(files['multica-update.sh'])},
        desktop: {}, coordinated_desktop: {'darwin-arm64': {url: `${base}/${appName}`, sha256: hash(files[appName]), size: files[appName].length}}};
      files['latest.json'] = JSON.stringify(manifest);
      writeFileSync(join(release, 'latest.json'), files['latest.json']);
      writeFileSync(join(release, 'SHA256SUMS'), Object.entries(files).map(([name, bytes]) => `${hash(bytes)}  ${name}\n`).join(''));
      const result = run(process.execPath, [staging, release, stage]);
      assert.equal(result.status, 0, result.stderr);
      const emptyRoot = join(root, `empty-${version}`);
      mkdirSync(join(emptyRoot, version), {recursive: true});
      assert.notEqual(run('sh', [publisher, stage, emptyRoot]).status, 0, 'empty immutable directory must be rejected');
      const oldPage = readFileSync(join(published, 'download.html'), 'utf8');
      const page = readFileSync(join(stage, 'download.html'), 'utf8');
      writeFileSync(join(stage, 'download.html'), 'unreviewed page');
      assert.notEqual(run('sh', [publisher, stage, published]).status, 0, 'page checksum is part of the publish gate');
      writeFileSync(join(stage, 'download.html'), page);
      const posixPreview = spawnSync('/bin/sh', [publisher, stage, published], {
        encoding: 'utf8', env: {...process.env, PATH: minimalBin},
      });
      assert.equal(posixPreview.status, 0, posixPreview.stderr);
      assert.equal(run('sh', [publisher, stage, published]).status, 0);
      assert.equal(readFileSync(join(published, 'download.html'), 'utf8'), oldPage, 'preview never publishes');
      const applied = run('sh', [publisher, stage, published, '--apply']);
      assert.equal(applied.status, 0, applied.stderr);
      const stable = join(published, 'latest');
      assert.equal(readFileSync(join(stable, 'multica-desktop-latest-mac-arm64.zip'), 'utf8'), files[appName]);
      assert.ok(!existsSync(join(stable, appName)), 'stable downloads omit versioned App filenames');
      const verified = spawnSync('sha256sum', ['-c', 'SHA256SUMS'], {cwd: stable, encoding: 'utf8'});
      assert.equal(verified.status, 0, verified.stderr);
      const html = readFileSync(join(published, 'download.html'), 'utf8');
      assert.ok(html.includes(`<strong class="nowrap">${version}</strong>`));
      assert.ok(html.includes(`${base}/multica-update.sh`), 'script URL stays pinned');
      assert.ok(html.includes(hash(script)), 'page displays the actual script checksum');
      const guide = readFileSync(join(published, 'agent-update-guide.md'), 'utf8');
      assert.ok(guide.includes(version));
      assert.ok(!guide.includes('stale guide must be regenerated'));
      assert.ok(guide.includes(hash(script)));
      assert.ok(guide.includes(`${base}/multica-update.sh`));
      assert.ok(!guide.includes('@VERSION@'));
      assert.ok(html.includes('2026-10-13</time>'), 'release date uses Taiwan time');
      assert.ok(html.includes('https://10.1.24.90:45671/downloads/latest/multica-desktop-latest-mac-arm64.zip'));
      assert.ok(html.includes('https://10.1.24.90:45671/downloads/latest/SHA256SUMS'));
      assert.ok(html.includes('https://github.com/hyc5566/multica/releases/latest'));
      assert.ok(html.includes(`Release ${releaseLabel}: 修復登入回跳 &#38; &#60;安全文字&#62;`),
        'release summary comes from this release, with HTML escaped');
      assert.ok(!html.includes('macOS Claude 用量修復'), 'no hardcoded previous-release summary');
      const channel = JSON.parse(readFileSync(join(published, 'latest.json')));
      assert.deepEqual(channel.desktop, {}, 'old Apps cannot download through their unsafe swap helper');
      assert.equal(channel.coordinated_desktop['darwin-arm64'].url, manifest.coordinated_desktop['darwin-arm64'].url);
      writeFileSync(join(release, appName), 'corrupt');
      const rejected = join(root, `rejected-${version}`);
      assert.notEqual(run(process.execPath, [staging, release, rejected]).status, 0);
      assert.ok(!existsSync(rejected));
      assert.equal(readFileSync(join(published, 'download.html'), 'utf8'), html);
    }
    const backups = readdirSync(published).filter((name) => name.startsWith('.before-latest-'));
    assert.equal(backups.length, 2);
    assert.ok(backups.some((name) => readFileSync(join(published, name, 'download.html'), 'utf8') === 'previous-page'));
  } finally {
    rmSync(root, {recursive: true, force: true});
  }
});
