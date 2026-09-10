import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, existsSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';

test('installer verifies artifacts, isolates daemon and preserves existing installs', () => {
  const root = mkdtempSync(join(tmpdir(), 'multica-installer-'));
  try {
    const fixture = join(root, 'fixture');
    const fakebin = join(root, 'fakebin');
    mkdirSync(fixture); mkdirSync(fakebin);
    writeFileSync(join(fixture, 'multica'), '#!/usr/bin/env bash\nprintf "%s\\n" "$MULTICA_DAEMON_AUTO_UPDATE" "$MULTICA_DAEMON_AUTO_RELOAD" "$@"\n', {mode: 0o755});
    for (const name of ['LICENSE', 'NOTICE']) writeFileSync(join(fixture, name), 'fixture');
    const archive = join(root, 'fixture.tar.gz');
    assert.equal(spawnSync('tar', ['-czf', archive, '-C', fixture, 'multica', 'LICENSE', 'NOTICE']).status, 0);
    const hash = createHash('sha256').update(readFileSync(archive)).digest('hex');
    writeFileSync(join(fakebin, 'uname'), '#!/bin/sh\ncase "$1" in -s) echo Linux;; -m) echo x86_64;; esac\n', {mode: 0o755});
    writeFileSync(join(fakebin, 'curl'), '#!/bin/sh\nfor arg do target="$arg"; done\ncp "$FIXTURE_ARCHIVE" "$target"\n', {mode: 0o755});
    const template = readFileSync(new URL('./install.sh.in', import.meta.url), 'utf8');
    const installer = join(root, 'install.sh');
    const render = (checksum) => template.replaceAll('@VERSION@', '0.4.41-zh-tw.4-rc.1')
      .replaceAll('@BASE_URL@', 'https://example.com/release')
      .replace(/@(LINUX|DARWIN)_(AMD64|ARM64)@/g, checksum);
    const installRoot = join(root, "local space ' $literal");
    const env = {...process.env, PATH: `${fakebin}:${process.env.PATH}`, MULTICA_ZH_TW_INSTALL_ROOT: installRoot, FIXTURE_ARCHIVE: archive};
    writeFileSync(installer, render('0'.repeat(64)));
    assert.equal(spawnSync('bash', [installer], {env}).status, 1);
    assert.equal(existsSync(installRoot), false);
    writeFileSync(installer, render(hash));
    const installed = spawnSync('bash', [installer], {env, encoding: 'utf8'});
    assert.equal(installed.status, 0, installed.stderr);
    const wrapper = join(installRoot, 'bin/multica-zh-tw');
    const result = spawnSync(wrapper, ['version'], {env, encoding: 'utf8'});
    assert.equal(result.status, 0, result.stderr);
    assert.equal(result.stdout, 'false\nfalse\n--profile\nmultica-zh-tw\nversion\n');
    const previous = readFileSync(wrapper, 'utf8');
    assert.equal(spawnSync('bash', [installer], {env}).status, 2);
    assert.equal(readFileSync(wrapper, 'utf8'), previous);
  } finally { rmSync(root, {recursive: true, force: true}); }
});
