import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, existsSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawn, execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import https from 'node:https';

const run = (file, args, env) => new Promise((resolve, reject) => {
  const child = spawn(file, args, {env, stdio: ['ignore', 'pipe', 'pipe']});
  let stdout = '', stderr = '';
  child.stdout.on('data', data => { stdout += data; });
  child.stderr.on('data', data => { stderr += data; });
  child.on('error', reject);
  child.on('close', status => resolve({status, stdout, stderr}));
});

test('real HTTPS keeps issuer/hostname verification and uses combined trust before installation', async () => {
  const root = mkdtempSync(join(tmpdir(), 'multica-install-tls-'));
  let server;
  try {
    const openssl = (...args) => execFileSync('openssl', args, {cwd: root, stdio: 'ignore'});
    for (const name of ['trusted', 'unrelated']) {
      openssl('req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
        '-subj', `/CN=${name}`, '-keyout', `${name}.key`, '-out', `${name}.crt`);
    }
    openssl('req', '-newkey', 'rsa:2048', '-nodes', '-subj', '/CN=localhost', '-keyout', 'leaf.key', '-out', 'leaf.csr');
    writeFileSync(join(root, 'leaf.ext'), 'subjectAltName=DNS:localhost\nbasicConstraints=CA:FALSE\nextendedKeyUsage=serverAuth\n');
    openssl('x509', '-req', '-in', 'leaf.csr', '-CA', 'trusted.crt', '-CAkey', 'trusted.key', '-CAcreateserial', '-days', '1', '-extfile', 'leaf.ext', '-out', 'leaf.crt');
    const fixture = join(root, 'fixture'); mkdirSync(fixture);
    writeFileSync(join(fixture, 'multica'), '#!/usr/bin/env bash\nexit 0\n', {mode: 0o755});
    for (const name of ['LICENSE', 'NOTICE']) writeFileSync(join(fixture, name), 'test');
    writeFileSync(join(fixture, 's90-ca.crt'), readFileSync(join(root, 'unrelated.crt')));
    const archive = join(root, 'fixture.tar.gz');
    execFileSync('tar', ['-czf', archive, '-C', fixture, 'multica', 'LICENSE', 'NOTICE', 's90-ca.crt']);
    const bytes = readFileSync(archive), checksum = createHash('sha256').update(bytes).digest('hex');
    server = https.createServer({key: readFileSync(join(root, 'leaf.key')), cert: readFileSync(join(root, 'leaf.crt'))}, (_req, res) => res.end(bytes));
    await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
    const home = join(root, 'home'); mkdirSync(home);
    // Neither inherited CA nor curlrc can disable verification or hide public roots.
    writeFileSync(join(home, '.curlrc'), 'insecure\n');
    const env = {...process.env, HOME: home, MULTICA_PROFILE: '',
      SSL_CERT_FILE: join(root, 'unrelated.crt'), CURL_CA_BUNDLE: join(root, 'unrelated.crt'),
      MULTICA_INSTALL_CA_FILE: '', MULTICA_ZH_TW_INSTALL_ROOT: join(root, 'install'),
      NO_PROXY: 'localhost,127.0.0.1', no_proxy: 'localhost,127.0.0.1'};
    const template = readFileSync(new URL('./install.sh.in', import.meta.url), 'utf8');
    const installer = join(root, 'install.sh');
    const render = (host, ca = '') => template.replaceAll('@VERSION@', 'tls-test')
      .replaceAll('@DOWNLOAD_CA_PEM@', ca)
      .replaceAll('@BASE_URL@', `https://${host}:${server.address().port}`)
      .replace(/@(LINUX|DARWIN)_(AMD64|ARM64)@/g, checksum);
    writeFileSync(installer, render('localhost'));
    const unknown = await run('bash', [installer], env);
    assert.equal(unknown.status, 60, unknown.stderr);
    assert.match(unknown.stderr, /MULTICA_INSTALL_CA_FILE/);
    assert.equal(existsSync(env.MULTICA_ZH_TW_INSTALL_ROOT), false);
    const missing = await run('bash', [installer], {...env, MULTICA_INSTALL_CA_FILE: join(root, 'absent')});
    assert.equal(missing.status, 1); assert.equal(existsSync(env.MULTICA_ZH_TW_INSTALL_ROOT), false);
    const privateKey = await run('bash', [installer], {...env, MULTICA_INSTALL_CA_FILE: join(root, 'trusted.key')});
    assert.equal(privateKey.status, 1); assert.match(privateKey.stderr, /private key/);
    assert.equal(existsSync(env.MULTICA_ZH_TW_INSTALL_ROOT), false);
    writeFileSync(installer, render('127.0.0.1'));
    const wrongHost = await run('bash', [installer], {...env, MULTICA_INSTALL_CA_FILE: join(root, 'trusted.crt')});
    assert.equal(wrongHost.status, 60, wrongHost.stderr);
    assert.equal(existsSync(env.MULTICA_ZH_TW_INSTALL_ROOT), false);
    writeFileSync(installer, render('localhost'));
    const customHome = join(root, 'custom-home'); mkdirSync(customHome);
    const custom = await run('bash', [installer], {...env, HOME: customHome,
      MULTICA_ZH_TW_INSTALL_ROOT: join(root, 'custom-install'), MULTICA_INSTALL_CA_FILE: join(root, 'trusted.crt')});
    assert.equal(custom.status, 0, custom.stderr);
    // A bundled public CA also bootstraps LAN downloads without user configuration.
    writeFileSync(installer, render('localhost', readFileSync(join(root, 'trusted.crt'), 'utf8')));
    const success = await run('bash', [installer], env);
    assert.equal(success.status, 0, success.stderr);
    const bundle = readFileSync(join(env.MULTICA_ZH_TW_INSTALL_ROOT, 'share/multica-zh-tw/tls-test/ca-bundle.crt'), 'utf8');
    assert.ok(bundle.includes(readFileSync(join(root, 'trusted.crt'), 'utf8').trim()));
    assert.ok(bundle.includes(readFileSync(join(root, 'unrelated.crt'), 'utf8').trim()));
    assert.ok((bundle.match(/BEGIN CERTIFICATE/g) || []).length > 3);
    const wrapper = readFileSync(join(env.MULTICA_ZH_TW_INSTALL_ROOT, 'bin/multica'), 'utf8');
    assert.match(wrapper, /export SSL_CERT_FILE=/); assert.match(wrapper, /export CURL_CA_BUNDLE=/);
  } finally {
    if (server) await new Promise(resolve => server.close(resolve));
    rmSync(root, {recursive: true, force: true});
  }
});
