import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, existsSync, rmSync, statSync, readdirSync } from 'node:fs';
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
    const render = (checksum, version = '0.4.41-zh-tw.4-rc.1') => template.replaceAll('@VERSION@', version)
      .replaceAll('@BASE_URL@', 'https://example.com/release')
      .replaceAll('@MANIFEST_URL@', 'https://example.com/latest.json')
      .replaceAll('@DOWNLOAD_CA_SHA256@', hash)
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
    assert.equal(result.stdout, 'true\ntrue\nversion\n');
    const launcherText = readFileSync(wrapper, 'utf8');
    assert.match(launcherText, /MULTICA_DAEMON_AUTO_UPDATE_INTERVAL=\$\{MULTICA_DAEMON_AUTO_UPDATE_INTERVAL:-24h\}/);
    assert.match(launcherText, /MULTICA_ZH_TW_RELEASE_MANIFEST_URL=https:\/\/example\.com\/latest\.json/);
    assert.ok(launcherText.includes('MULTICA_ZH_TW_LAUNCHER='));
    const optOut = spawnSync(wrapper, ['version'], {env: {...env, MULTICA_DAEMON_AUTO_UPDATE: 'false'}, encoding: 'utf8'});
    assert.equal(optOut.stdout, 'false\ntrue\nversion\n');
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
    assert.equal(explicitProfile.stdout, 'true\ntrue\n--profile\noptional\nversion\n');
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
    assert.match(unit, /Environment=MULTICA_DAEMON_SUPERVISOR=systemd/);
    assert.match(readFileSync(systemctlLog, 'utf8'), /--user enable --now multica.service/);
    assert.match(service.stdout, /Service ready/);
    const previous = readFileSync(wrapper, 'utf8');
    assert.equal(spawnSync('bash', [installer], {env}).status, 2);
    assert.equal(readFileSync(wrapper, 'utf8'), previous);

    // --upgrade keeps configuration, swaps the launcher, retains one rollback.
    mkdirSync(join(home, '.multica'), {recursive: true});
    writeFileSync(join(home, '.multica/config.json'), '{"token":"keep"}');
    const share = join(installRoot, 'share/multica-zh-tw');
    const upgradeTo = (version) => {
      const next = join(root, `install-${version}.sh`);
      writeFileSync(next, render(hash, version));
      return spawnSync('bash', [next, '--upgrade'], {env, encoding: 'utf8'});
    };
    const second = upgradeTo('0.4.41-zh-tw.5');
    assert.equal(second.status, 0, second.stderr);
    assert.match(second.stdout, /Upgraded 0\.4\.41-zh-tw\.4-rc\.1 -> 0\.4\.41-zh-tw\.5/);
    assert.equal(statSync(wrapper).ino, statSync(join(share, '0.4.41-zh-tw.5/launcher')).ino);
    assert.ok(existsSync(join(share, '0.4.41-zh-tw.4-rc.1/multica')), 'previous version kept for rollback');
    assert.equal(readFileSync(join(home, '.multica/config.json'), 'utf8'), '{"token":"keep"}');
    const again = upgradeTo('0.4.41-zh-tw.5');
    assert.equal(again.status, 0);
    assert.match(again.stdout, /Already at 0\.4\.41-zh-tw\.5/);
    const third = upgradeTo('0.4.41-zh-tw.6');
    assert.equal(third.status, 0, third.stderr);
    assert.deepEqual(readdirSync(share).sort(), ['0.4.41-zh-tw.5', '0.4.41-zh-tw.6']);
    const badUpgrade = join(root, 'install-bad.sh');
    writeFileSync(badUpgrade, render('0'.repeat(64), '0.4.41-zh-tw.7'));
    assert.equal(spawnSync('bash', [badUpgrade, '--upgrade'], {env}).status, 1);
    assert.equal(statSync(wrapper).ino, statSync(join(share, '0.4.41-zh-tw.6/launcher')).ino, 'failed upgrade leaves launcher');
    const noInstall = spawnSync('bash', [badUpgrade, '--upgrade'], {env: {...env, MULTICA_ZH_TW_INSTALL_ROOT: join(root, 'none')}});
    assert.equal(noInstall.status, 2);
    rmSync(join(home, '.multica'), {recursive: true});

    // `curl ... | bash` reads the whole script before running anything.
    const piped = spawnSync('bash', [], {env: {...env, MULTICA_ZH_TW_INSTALL_ROOT: join(root, 'piped')}, input: render(hash), encoding: 'utf8'});
    assert.equal(piped.status, 0, piped.stderr);
    assert.match(piped.stdout, /Installed 0\.4\.41-zh-tw\.4-rc\.1/);
    assert.match(piped.stdout, /Add to PATH/);
    mkdirSync(join(home, '.multica'));
    const config = join(home, '.multica/config.json');
    writeFileSync(config, '{"workspaces_root":"/keep"}');
    const preserve = spawnSync('bash', [installer], {env: {...env, MULTICA_ZH_TW_INSTALL_ROOT: join(root, 'existing-config')}});
    assert.equal(preserve.status, 2);
    assert.equal(readFileSync(config, 'utf8'), '{"workspaces_root":"/keep"}');
  } finally { rmSync(root, {recursive: true, force: true}); }
});

test('repository entry points fetch the fixed release with verified HTTPS and forward arguments', () => {
  const root = mkdtempSync(join(tmpdir(), 'multica-installer-entry-'));
  try {
    const fakebin = join(root, 'bin'); mkdirSync(fakebin);
    const home = join(root, 'home'); mkdirSync(home);
    const log = join(root, 'curl.log');
    const fixture = join(root, 'release-installer');
    writeFileSync(fixture, '#!/usr/bin/env bash\nprintf "%s\\n" "$@"\nexit "${FIXTURE_INSTALL_STATUS:-0}"\n');
    writeFileSync(join(fakebin, 'curl'), '#!/usr/bin/env bash\nprintf "%s\\n" "$@" > "$FIXTURE_CURL_LOG"\nfor arg do destination="$arg"; done\ncp "$FIXTURE_INSTALLER" "$destination"\nexit "${FIXTURE_CURL_STATUS:-0}"\n', {mode: 0o755});
    const env = {...process.env, HOME: home, PATH: `${fakebin}:${process.env.PATH}`,
      CURL_CA_BUNDLE: '', SSL_CERT_FILE: '', MULTICA_INSTALL_CA_FILE: '',
      FIXTURE_CURL_LOG: log, FIXTURE_INSTALLER: fixture};
    for (const name of ['install-zh-tw.sh', 'install-zh-tw-s90.sh']) {
      const script = new URL(`../${name}`, import.meta.url).pathname;
      for (const args of [[], ['--login'], ['--service'], ['--upgrade']]) {
        const result = spawnSync('bash', [script, ...args], {env, encoding: 'utf8'});
        assert.equal(result.status, 0, result.stderr);
        assert.equal(result.stdout.trim(), args.join('\n'));
        const curlArgs = readFileSync(log, 'utf8').trim().split('\n');
        assert.equal(curlArgs[0], '-q');
        assert.ok(curlArgs.includes('--cacert'));
        assert.ok(curlArgs.includes('--proxy-cacert'));
        assert.ok(curlArgs.includes('--proto-redir'));
        assert.ok(curlArgs.includes('=https'));
        assert.ok(!curlArgs.includes('--insecure'));
        assert.ok(curlArgs.includes('https://github.com/hyc5566/multica/releases/latest/download/install.sh'));
        assert.equal(existsSync(curlArgs.at(-1)), false, 'temporary installer is cleaned up');
      }
      const failure = spawnSync('bash', [script, '--login'], {env: {...env, FIXTURE_CURL_STATUS: '60'}, encoding: 'utf8'});
      assert.equal(failure.status, 60);
      assert.equal(failure.stdout, '', 'failed download must never execute');
      assert.equal(spawnSync('bash', [script], {env: {...env, FIXTURE_INSTALL_STATUS: '7'}}).status, 7);
      assert.equal(spawnSync('bash', [script, '--invalid'], {env}).status, 2);
      assert.equal(spawnSync('bash', [script, '--login', '--service'], {env}).status, 2);
    }
  } finally { rmSync(root, {recursive: true, force: true}); }
});

test('manifest.sh writes the release channel for one build', () => {
  const root = mkdtempSync(join(tmpdir(), 'multica-manifest-'));
  try {
    const release = join(root, 'release'); mkdirSync(release);
    const version = '0.6.1-zh-tw.2', base = `https://s90.example:45671/downloads/${version}`;
    writeFileSync(join(release, 'BUILD.txt'), `version=${version}\ncommit=${'a'.repeat(40)}\n`);
    writeFileSync(join(release, 'install.sh'), `#!/usr/bin/env bash\nversion='${version}'\n`);
    const zip = join(root, `multica-desktop-${version}-mac-arm64.zip`);
    writeFileSync(zip, 'zip-bytes');
    const script = new URL('./manifest.sh', import.meta.url).pathname;
    const notes = 'https://github.com/hyc5566/multica/releases/tag/zh-tw-v0.6.1-zh-tw.2';
    const result = spawnSync('bash', [script, release, base, notes, zip], {encoding: 'utf8'});
    assert.equal(result.status, 0, result.stderr);
    const manifest = JSON.parse(readFileSync(join(release, 'latest.json'), 'utf8'));
    const sha = (path) => createHash('sha256').update(readFileSync(path)).digest('hex');
    assert.equal(manifest.version, version);
    assert.equal(manifest.cli.install_url, `${base}/install.sh`);
    assert.equal(manifest.cli.install_sha256, sha(join(release, 'install.sh')));
    assert.deepEqual(manifest.desktop['darwin-arm64'], {url: `${base}/multica-desktop-${version}-mac-arm64.zip`, sha256: sha(zip), size: 9});
    assert.equal(spawnSync('bash', [script, release, 'https://s90.example/downloads/0.6.1-zh-tw.3', notes]).status, 2);
    writeFileSync(join(release, 'BUILD.txt'), `version=0.6.1-zh-tw.2-rc.1\ncommit=${'a'.repeat(40)}\n`);
    assert.equal(spawnSync('bash', [script, release, base, notes]).status, 2, 'release candidates never enter the channel');
  } finally { rmSync(root, {recursive: true, force: true}); }
});
