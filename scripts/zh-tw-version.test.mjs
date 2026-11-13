import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { execFileSync } from 'node:child_process';
import { deriveVersion, releaseVersion } from './zh-tw-version.mjs';

function fixture(t) {
  const repo = mkdtempSync(join(process.env.TMPDIR, 'version-test-'));
  t.after(() => rmSync(repo, { recursive: true, force: true }));
  const git = (...args) => execFileSync('git', ['-C', repo, ...args], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim();
  git('init', '-q'); git('config', 'user.name', 'Test'); git('config', 'user.email', 'test@example.invalid');
  mkdirSync(join(repo, 'apps/desktop'), { recursive: true });
  const commitVersion = version => {
    writeFileSync(join(repo, 'apps/desktop/package.json'), JSON.stringify({ version }));
    git('add', 'apps/desktop/package.json'); git('commit', '-qm', version);
    return git('rev-parse', 'HEAD');
  };
  const base = commitVersion('0.8.2'); git('tag', 'v0.8.2');
  git('branch', 'upstream');
  return { repo, git, commitVersion, base };
}

test('使用 merge-base 而非 fork HEAD；固定 upstream commit 可重現', t => {
  const f = fixture(t);
  f.commitVersion('0.8.2-zh-tw.1');
  const result = deriveVersion({ repo: f.repo, upstream: f.base });
  assert.equal(result.version, `0.8.2-zh-tw.1+up.${f.base.slice(0, 7)}`);
  assert.notEqual(result.head, result.mergeBase);
  assert.equal(result.upstreamCommit, f.base);
});

test('同版 release tag 遞增，其他上游版不影響；繁中 tag 不作為上游 tag', t => {
  const f = fixture(t);
  f.git('tag', 'v0.8.2-zh-tw.3'); f.git('tag', '0.8.1-zh-tw.99');
  assert.equal(deriveVersion({ repo: f.repo, upstream: 'upstream' }).version, `0.8.2-zh-tw.4+up.${f.base.slice(0, 7)}`);
});

test('既定候選序號須大於已發布序號並可保留較大值', t => {
  const f = fixture(t);
  f.git('tag', 'v0.8.2-zh-tw.3+up.abcdef0');
  f.commitVersion('0.8.2-zh-tw.3');
  assert.throws(() => deriveVersion({ repo: f.repo, upstream: 'upstream' }), /大於既有 release/);
  f.commitVersion('0.8.2-zh-tw.5');
  assert.match(deriveVersion({ repo: f.repo, upstream: 'upstream' }).version, /^0\.8\.2-zh-tw\.5\+up\./);
});

test('拒絕過期的 package 基版、錯誤 SHA 和無效序號', t => {
  const f = fixture(t);
  f.commitVersion('0.8.1-zh-tw.1');
  assert.throws(() => deriveVersion({ repo: f.repo, upstream: 'upstream' }), /不符/);
  f.commitVersion('0.8.2-zh-tw.1+up.0000000');
  assert.throws(() => deriveVersion({ repo: f.repo, upstream: 'upstream' }), /SHA/);
  f.commitVersion('0.8.2-zh-tw.0');
  assert.throws(() => deriveVersion({ repo: f.repo, upstream: 'upstream' }), /不符/);
});

test('換上游版重設序號，未提交 package 變更必須驗證', t => {
  const f = fixture(t);
  f.git('tag', 'zh-tw-v0.8.1-zh-tw.9');
  writeFileSync(join(f.repo, 'apps/desktop/package.json'), JSON.stringify({ version: '0.8.1' }));
  assert.throws(() => deriveVersion({ repo: f.repo, upstream: 'upstream' }), /不符/);
});

test('發行入口必須完整一致且拒絕版本覆寫', t => {
  const f = fixture(t);
  const expected = `0.8.2-zh-tw.1+up.${f.base.slice(0, 7)}`;
  assert.throws(() => releaseVersion({ repo: f.repo, upstream: 'upstream', overrides: [] }), /完全符合/);
  writeFileSync(join(f.repo, 'apps/desktop/package.json'), JSON.stringify({ version: expected }));
  assert.equal(releaseVersion({ repo: f.repo, upstream: 'upstream', overrides: [expected], requested: expected }), expected);
  assert.throws(() => releaseVersion({ repo: f.repo, upstream: 'upstream', overrides: ['bogus'] }), /不符/);
  assert.throws(() => releaseVersion({ repo: f.repo, upstream: 'upstream', overrides: [], requested: 'bogus' }), /不符/);
});

test('辨識三種正式 tag 前綴並按同基版最大序號遞增', t => {
  const f = fixture(t);
  f.git('tag', 'zh-tw-v0.8.2-zh-tw.7');
  f.git('tag', 'v0.8.2-zh-tw.3');
  f.git('tag', '0.8.2-zh-tw.5');
  f.git('tag', 'zh-tw-v0.8.1-zh-tw.99');
  assert.equal(deriveVersion({ repo: f.repo, upstream: 'upstream' }).version, `0.8.2-zh-tw.8+up.${f.base.slice(0, 7)}`);
});

test('已發布的同一 HEAD 可重建，其他 HEAD 或錯誤上游 SHA 不可重用序號', t => {
  const f = fixture(t);
  const head = f.commitVersion('0.8.2-zh-tw.1');
  f.git('tag', `zh-tw-v0.8.2-zh-tw.1+up.${f.base.slice(0, 7)}`);
  assert.equal(deriveVersion({ repo: f.repo, upstream: 'upstream' }).version, `0.8.2-zh-tw.1+up.${f.base.slice(0, 7)}`);
  f.git('tag', 'v0.8.2-zh-tw.1+up.0000000', head);
  assert.throws(() => deriveVersion({ repo: f.repo, upstream: 'upstream' }), /相同提交及上游基底/);
  f.git('tag', '-d', 'v0.8.2-zh-tw.1+up.0000000');
  f.commitVersion('0.8.2-zh-tw.1+up.' + f.base.slice(0, 7));
  assert.throws(() => deriveVersion({ repo: f.repo, upstream: 'upstream' }), /相同提交及上游基底/);
});
