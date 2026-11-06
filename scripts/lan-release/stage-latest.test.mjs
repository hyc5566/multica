import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, existsSync, rmSync, readdirSync } from 'node:fs';
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
    for (const version of ['0.6.1-zh-tw.1', '0.6.2-zh-tw.1']) {
      const release = join(published, version), stage = join(root, `stage-${version}`);
      mkdirSync(release);
      const appName = `multica-desktop-${version}-mac-arm64.zip`;
      const releaseLabel = version === '0.6.1-zh-tw.1' ? 'A' : 'B';
      const summary = `Release ${releaseLabel}: 修復登入回跳 & <安全文字>`;
      const files = {[appName]: `App-${version}`, 'install.sh': `installer-${version}`,
        'RELEASE-NOTES.md': `# Release notes\n\n${summary}\n\n- Detailed changes\n`,
        'BUILD.txt': `version=${version}\ncommit=${'a'.repeat(40)}\n`};
      for (const [name, bytes] of Object.entries(files)) writeFileSync(join(release, name), bytes);
      writeFileSync(join(release, 'SHA256SUMS'), Object.entries(files).map(([name, bytes]) => `${hash(bytes)}  ${name}\n`).join(''));
      const base = `https://s90.example/downloads/${version}`;
      const manifest = {version, commit: 'a'.repeat(40), published_at: '2026-10-12T18:30:00Z', notes_url: `https://github.com/hyc5566/multica/releases/tag/zh-tw-v${version}`,
        cli: {install_url: `${base}/install.sh`, install_sha256: hash(files['install.sh'])},
        desktop: {'darwin-arm64': {url: `${base}/${appName}`, sha256: hash(files[appName]), size: files[appName].length}}};
      writeFileSync(join(release, 'latest.json'), JSON.stringify(manifest));
      const result = run(process.execPath, [staging, release, stage]);
      assert.equal(result.status, 0, result.stderr);
      const oldPage = readFileSync(join(published, 'download.html'), 'utf8');
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
      assert.equal(html.split(version).length - 1, 1);
      assert.ok(html.includes('2026-10-13</time>'), 'release date uses Taiwan time');
      assert.ok(html.includes('https://s90.example/downloads/latest/multica-desktop-latest-mac-arm64.zip'));
      assert.ok(html.includes('https://s90.example/downloads/latest/SHA256SUMS'));
      assert.ok(html.includes('https://github.com/hyc5566/multica/releases/latest'));
      assert.ok(html.includes(`Release ${releaseLabel}: 修復登入回跳 &#38; &#60;安全文字&#62;`),
        'release summary comes from this release, with HTML escaped');
      assert.ok(!html.includes('macOS Claude 用量修復'), 'no hardcoded previous-release summary');
      assert.equal(JSON.parse(readFileSync(join(published, 'latest.json'))).desktop['darwin-arm64'].url, manifest.desktop['darwin-arm64'].url);
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
