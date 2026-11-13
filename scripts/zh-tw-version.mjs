#!/usr/bin/env node
import { execFileSync } from 'node:child_process';
import { resolve } from 'node:path';
import { readFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';

export function deriveVersion({ repo = '.', upstream = 'upstream/main' } = {}) {
  const git = (...args) => execFileSync('git', ['-C', repo, ...args], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim();
  const head = git('rev-parse', '--verify', 'HEAD^{commit}');
  const upstreamCommit = git('rev-parse', '--verify', `${upstream}^{commit}`);
  const base = git('merge-base', head, upstreamCommit);
  const tag = git('describe', '--tags', '--match', 'v[0-9]*', '--exclude', '*-zh-tw.*', '--abbrev=0', base);
  const match = /^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/.exec(tag);
  if (!match) throw new Error(`上游標籤不是 vMAJOR.MINOR.PATCH：${tag}`);
  const version = match.slice(1).join('.');
  const sha = base.slice(0, 7);
  const pkg = JSON.parse(readFileSync(resolve(repo, 'apps/desktop/package.json'), 'utf8'));
  const pkgMatch = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-zh-tw\.([1-9]\d*)(?:\+up\.([0-9a-f]{7}))?)?$/.exec(pkg.version || '');
  if (!pkgMatch || pkgMatch.slice(1, 4).join('.') !== version) throw new Error(`Desktop 版本 ${pkg.version} 與上游 ${tag} 不符`);
  if (pkgMatch[5] && pkgMatch[5] !== sha) throw new Error('Desktop 版本中的上游 SHA 與 merge-base 不符');
  let max = 0;
  const releases = [];
  for (const name of git('tag', '--list').split('\n')) {
    const m = /^(?:zh-tw-v|v)?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)-zh-tw\.([1-9]\d*)(?:\+up\.([0-9a-f]{7}))?$/.exec(name);
    if (m && m.slice(1, 4).join('.') === version) {
      const n = Number(m[4]);
      if (!Number.isSafeInteger(n)) throw new Error(`release tag 序號過大：${name}`);
      max = Math.max(max, n);
      releases.push({ name, n, sha: m[5] });
    }
  }
  const n = pkgMatch[4] ? Number(pkgMatch[4]) : max + 1;
  if (!Number.isSafeInteger(n)) throw new Error('Desktop 候選序號過大');
  for (const release of releases.filter(item => item.n >= n)) {
    const target = git('rev-parse', '--verify', `refs/tags/${release.name}^{commit}`);
    if (target !== head || git('merge-base', target, upstreamCommit) !== base || (release.sha && release.sha !== sha)) {
      throw new Error(`Desktop 候選序號必須大於既有 release 序號 ${max}，或標籤須指向相同提交及上游基底：${release.name}`);
    }
  }
  return { version: `${version}-zh-tw.${n}+up.${sha}`, packageVersion: pkg.version, head, upstreamCommit, mergeBase: base, upstreamTag: tag };
}

export function releaseVersion({ repo = '.', upstream = process.env.MULTICA_UPSTREAM_REF || 'upstream/main', requested, overrides = [process.env.VERSION, process.env.MULTICA_BUILD_VERSION] } = {}) {
  const result = deriveVersion({ repo, upstream });
  if (result.packageVersion !== result.version) throw new Error(`Desktop package 版本必須完全符合推導版本 ${result.version}`);
  for (const value of [requested, ...overrides]) {
    if (value !== undefined && value !== '' && value !== result.version) throw new Error(`指定版本 ${value} 與推導版本 ${result.version} 不符`);
  }
  return result.version;
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try {
    const args = process.argv.slice(2);
    let repo = '.', upstream = process.env.MULTICA_UPSTREAM_REF || 'upstream/main', json = false;
    while (args.length) {
      const arg = args.shift();
      if (arg === '--repo' || arg === '--upstream') {
        const value = args.shift();
        if (!value || value.startsWith('--')) throw new Error(`${arg} 需要參數`);
        if (arg === '--repo') repo = value; else upstream = value;
      } else if (arg === '--json') json = true;
      else throw new Error(`未知參數：${arg}`);
    }
    const result = deriveVersion({ repo, upstream });
    releaseVersion({ repo, upstream });
    console.log(json ? JSON.stringify(result, null, 2) : result.version);
  } catch (error) {
    console.error(`版本推導失敗：${error.message}`);
    process.exitCode = 1;
  }
}
