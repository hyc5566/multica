import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, existsSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';

test('installer verifies artifacts, uses default configuration and preserves existing installs', () => {
  const root = mkdtempSync(join(tmpdir(), 'multica-installer-'));
  try {
    const fixture = join(root, 'fixture');
    const fakebin = join(root, 'fakebin');
    mkdirSync(fixture); mkdirSync(fakebin);
    writeFileSync(join(fixture, 'multica'), '#!/usr/bin/env bash\nif [[ ${1:-} == daemon && ${2:-} == status ]]; then echo \'{"status":"running"}\'; exit 0; fi\n[[ ${1:-} != login || ${FIXTURE_LOGIN_FAIL:-} != 1 ]] || exit 7\nprintf "%s\\n" "$MULTICA_DAEMON_AUTO_UPDATE" "$MULTICA_DAEMON_AUTO_RELOAD" "$@"\n', {mode: 0o755});
    for (const name of ['LICENSE', 'NOTICE', 's90-ca.crt']) writeFileSync(join(fixture, name), 'fixture');
    const archive = join(root, 'fixture.tar.gz');
    assert.equal(spawnSync('tar', ['-czf', archive, '-C', fixture, 'multica', 'LICENSE', 'NOTICE', 's90-ca.crt']).status, 0);
    const hash = createHash('sha256').update(readFileSync(archive)).digest('hex');
    writeFileSync(join(fakebin, 'uname'), '#!/bin/sh\ncase "$1" in -s) echo Linux;; -m) echo x86_64;; esac\n', {mode: 0o755});
    writeFileSync(join(fakebin, 'curl'), '#!/bin/sh\nfor arg do target="$arg"; done\ncp "$FIXTURE_ARCHIVE" "$target"\n', {mode: 0o755});
    const template = readFileSync(new URL('./install.sh.in', import.meta.url), 'utf8');
    const installer = join(root, 'install.sh');
    const render = (checksum) => template.replaceAll('@VERSION@', '0.4.41-zh-tw.4-rc.1')
      .replaceAll('@BASE_URL@', 'https://example.com/release')
      .replaceAll('@DOWNLOAD_CA_PEM@', '')
      .replace(/@(LINUX|DARWIN)_(AMD64|ARM64)@/g, checksum);
    const installRoot = join(root, "local space ' $literal");
    const home = join(root, 'home'); mkdirSync(home);
    const env = {...process.env, HOME: home, MULTICA_PROFILE: '', PATH: `${fakebin}:${process.env.PATH}`, MULTICA_ZH_TW_INSTALL_ROOT: installRoot, FIXTURE_ARCHIVE: archive};
    writeFileSync(installer, render('0'.repeat(64)));
    assert.equal(spawnSync('bash', [installer], {env}).status, 1);
    assert.equal(existsSync(installRoot), false);
    writeFileSync(installer, render(hash));
    const installed = spawnSync('bash', [installer], {env, encoding: 'utf8'});
    assert.equal(installed.status, 0, installed.stderr);
    assert.match(installed.stdout, /config\nset\nworkspaces_root\n/);
    assert.ok(installed.stdout.includes(`${env.HOME}/.multica/workspaces`));
    const wrapper = join(installRoot, 'bin/multica');
    const result = spawnSync(wrapper, ['version'], {env, encoding: 'utf8'});
    assert.equal(result.status, 0, result.stderr);
    assert.equal(result.stdout, 'false\nfalse\nversion\n');
    assert.match(readFileSync(wrapper, 'utf8'), /SSL_CERT_FILE=/);
    assert.match(readFileSync(wrapper, 'utf8'), /MULTICA_SERVER_URL=https:\/\/10\.1\.24\.90:45671/);
    assert.match(readFileSync(join(installRoot, 'share/multica-zh-tw/0.4.41-zh-tw.4-rc.1/ca-bundle.crt'), 'utf8'), /BEGIN CERTIFICATE/);
    const guided = spawnSync('bash', [installer, '--login'], {env: {...env, MULTICA_ZH_TW_INSTALL_ROOT: join(root, 'guided')}, encoding: 'utf8'});
    assert.equal(guided.status, 0, guided.stderr);
    assert.match(guided.stdout, /login\n--token\n/);
    assert.match(guided.stdout, /daemon\nstart\n/);
    const failedLogin = spawnSync('bash', [installer, '--login'], {env: {...env, FIXTURE_LOGIN_FAIL: '1', MULTICA_ZH_TW_INSTALL_ROOT: join(root, 'failed-login')}, encoding: 'utf8'});
    assert.equal(failedLogin.status, 7);
    assert.doesNotMatch(failedLogin.stdout, /daemon\nstart\n/);
    const explicitProfile = spawnSync(wrapper, ['--profile', 'optional', 'version'], {env, encoding: 'utf8'});
    assert.equal(explicitProfile.stdout, 'false\nfalse\n--profile\noptional\nversion\n');
    assert.doesNotMatch(readFileSync(wrapper, 'utf8'), /--profile/);
    writeFileSync(join(fakebin, 'systemctl'), '#!/bin/sh\nprintf "%s\\n" "$*" >> "$FIXTURE_SYSTEMCTL_LOG"\n', {mode: 0o755});
    const serviceHome = join(root, 'service-home'); mkdirSync(serviceHome);
    const serviceRoot = join(root, 'service % space $path');
    const systemctlLog = join(root, 'systemctl.log');
    const service = spawnSync('bash', [installer, '--service'], {env: {...env, HOME: serviceHome, FIXTURE_SYSTEMCTL_LOG: systemctlLog, MULTICA_ZH_TW_INSTALL_ROOT: serviceRoot}, encoding: 'utf8'});
    assert.equal(service.status, 0, service.stderr);
    const unit = readFileSync(join(serviceHome, '.config/systemd/user/multica.service'), 'utf8');
    assert.ok(unit.includes('service %% space $$path/bin/multica" daemon start --foreground'));
    assert.match(unit, /Restart=on-failure/);
    assert.match(readFileSync(systemctlLog, 'utf8'), /--user enable --now multica.service/);
    assert.match(service.stdout, /Service ready/);
    const previous = readFileSync(wrapper, 'utf8');
    assert.equal(spawnSync('bash', [installer], {env}).status, 2);
    assert.equal(readFileSync(wrapper, 'utf8'), previous);
    mkdirSync(join(home, '.multica'));
    const config = join(home, '.multica/config.json');
    writeFileSync(config, '{"workspaces_root":"/keep"}');
    const preserve = spawnSync('bash', [installer], {env: {...env, MULTICA_ZH_TW_INSTALL_ROOT: join(root, 'existing-config')}});
    assert.equal(preserve.status, 2);
    assert.equal(readFileSync(config, 'utf8'), '{"workspaces_root":"/keep"}');
  } finally { rmSync(root, {recursive: true, force: true}); }
});
