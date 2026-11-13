#!/usr/bin/env python3
"""Verify release build metadata without executing release binaries."""
import hashlib
import os
from pathlib import Path
import plistlib
import re
import shlex
import shutil
import subprocess
import tempfile
import zipfile

EXPECTED_GO = 'go1.26.9'
MAX_BINARY = 256 * 1024 * 1024
BUNDLE_ID = 'ai.multica.desktop.zh-tw.local'


def sha(path):
    digest = hashlib.sha256()
    with open(path, 'rb') as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b''):
            digest.update(chunk)
    return digest.hexdigest()


def build_identity(release):
    fields = {}
    build_file = Path(release) / 'BUILD.txt'
    if build_file.is_symlink() or not build_file.is_file():
        raise ValueError('BUILD.txt must be a regular file')
    for line in build_file.read_text(encoding='utf-8').splitlines():
        key, sep, value = line.partition('=')
        if not sep or key in fields:
            raise ValueError('invalid or duplicate BUILD.txt field')
        fields[key] = value
    if not re.fullmatch(r'\d+\.\d+\.\d+-zh-tw\.\d+(?:-rc\.\d+)?(?:\+up\.[0-9a-f]{7})?', fields.get('version', '')):
        raise ValueError('invalid BUILD.txt version')
    if not re.fullmatch(r'[0-9a-f]{40}', fields.get('commit', '')):
        raise ValueError('invalid BUILD.txt commit')
    return fields['version'], fields['commit']


def verify_binary(path, version, commit, goos, goarch):
    path = Path(path)
    if path.is_symlink() or not path.is_file() or not 0 < path.stat().st_size <= MAX_BINARY:
        raise ValueError('release binary missing, symlinked or too large')
    go = shutil.which('go')
    if not go:
        raise ValueError('go tool is required for metadata inspection')
    tool = subprocess.run([go, 'version'], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=30, check=False,
                          env={'PATH': os.defpath, 'GOTOOLCHAIN': 'local'})
    if tool.returncode != 0 or not re.match(r'^go version go1\.26\.9(?:\s|$)', tool.stdout):
        raise ValueError('Go 1.26.9 inspector is required')
    result = subprocess.run([go, 'version', '-m', str(path)], stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, text=True, timeout=30, check=False,
                            env={'PATH': os.defpath, 'GOTOOLCHAIN': 'local'})
    if result.returncode != 0:
        raise ValueError('go build metadata unavailable')
    lines = result.stdout.splitlines()
    if not lines or not lines[0].endswith(': ' + EXPECTED_GO):
        raise ValueError('release binary uses an unexpected Go toolchain')
    values = {}
    for line in lines[1:]:
        if not line.startswith('\tbuild\t'):
            continue
        key, sep, value = line[len('\tbuild\t'):].partition('=')
        if not sep or key in values:
            raise ValueError('ambiguous build metadata')
        values[key] = value
    if values.get('GOOS') != goos or values.get('GOARCH') != goarch:
        raise ValueError('binary platform mismatch')
    words = shlex.split(values.get('-ldflags', ''))
    if len(words) == 1:
        words = shlex.split(words[0])
    flags = {}
    for index, word in enumerate(words):
        item = words[index + 1] if word == '-X' and index + 1 < len(words) else word[3:] if word.startswith('-X=') else ''
        key, sep, value = item.partition('=')
        if sep and key in ('main.version', 'main.buildCommit'):
            if key in flags:
                raise ValueError('duplicate binary identity flag')
            flags[key] = value
    if flags != {'main.version': version, 'main.buildCommit': commit}:
        raise ValueError('binary version or commit mismatch')
    return {'sha256': sha(path), 'size': path.stat().st_size}


def verify_app(archive_path, cli_path, version, commit):
    archive_path = Path(archive_path)
    if archive_path.is_symlink() or not archive_path.is_file():
        raise ValueError('App ZIP missing or symlinked')
    cli_info = verify_binary(cli_path, version, commit, 'darwin', 'arm64')
    with zipfile.ZipFile(archive_path) as archive:
        plists = [x for x in archive.infolist() if len(x.filename.split('/')) == 3
                  and x.filename.endswith('.app/Contents/Info.plist')]
        binaries = [x for x in archive.infolist() if x.filename.endswith('/bin/multica')]
        if len(plists) != 1 or len(binaries) != 1 or plists[0].file_size > 1024 * 1024 or binaries[0].file_size > MAX_BINARY:
            raise ValueError('App ZIP must contain one bounded plist and bundled CLI')
        prefix = plists[0].filename[:-len('Contents/Info.plist')]
        if binaries[0].filename != prefix + 'Contents/Resources/app.asar.unpacked/resources/bin/multica':
            raise ValueError('App plist and bundled CLI do not belong to the same bundle')
        info = plistlib.loads(archive.read(plists[0]))
        if info.get('CFBundleIdentifier') != BUNDLE_ID or info.get('CFBundleShortVersionString') != version:
            raise ValueError('App bundle identity or version mismatch')
        with tempfile.TemporaryDirectory(prefix='multica-app-check-') as temp:
            extracted = Path(temp) / 'multica'
            with archive.open(binaries[0]) as src, extracted.open('xb') as dst:
                shutil.copyfileobj(src, dst, length=1024 * 1024)
            embedded = verify_binary(extracted, version, commit, 'darwin', 'arm64')
        if embedded != cli_info:
            raise ValueError('App bundled CLI differs from release Darwin arm64 CLI')
    return {'sha256': sha(archive_path), 'size': archive_path.stat().st_size,
            'binary_sha256': cli_info['sha256']}


def emit_manifest(release, base, notes, app_zip=None):
    import datetime
    import json
    from urllib.parse import urlsplit
    release = Path(release)
    version, commit = build_identity(release)
    if not re.fullmatch(r'\d+\.\d+\.\d+-zh-tw\.\d+(?:\+up\.[0-9a-f]{7})?', version):
        raise ValueError('channel requires a stable release')
    expected_base = 'https://10.1.24.90:45671/downloads/' + version
    note_url = urlsplit(notes)
    if base != expected_base or note_url.scheme != 'https' or not note_url.netloc or note_url.username or note_url.password:
        raise ValueError('invalid release base or notes URL')
    package = json.loads((release / 'update-package.json').read_text(encoding='utf-8'))
    if (package.get('source'), package.get('version'), package.get('commit')) != ('hyc5566/multica', version, commit):
        raise ValueError('updater package identity mismatch')
    script = release / 'multica-update.sh'
    if not script.is_file() or script.is_symlink():
        raise ValueError('updater script missing or symlinked')
    script_hash = sha(script)
    if package.get('script', {}).get('sha256') != script_hash or package['script'].get('size') != script.stat().st_size or package['script'].get('url') != base + '/multica-update.sh':
        raise ValueError('updater script does not match package metadata')
    desktop = {}
    if app_zip:
        app_zip = Path(app_zip)
        name = f'multica-desktop-{version}-mac-arm64.zip'
        if app_zip.name != name:
            raise ValueError('App ZIP name does not match version')
        cli = release / f'multica-{version}-darwin-arm64'
        info = verify_app(app_zip, cli, version, commit)
        desktop['darwin-arm64'] = {'url': base + '/' + name, **info,
                                   'binary_version': version, 'binary_commit': commit,
                                   'data_compatibility': 'multica-profile-v1', 'bundle_id': BUNDLE_ID}
    data = {'source_id': 'hyc5566/multica', 'coordinated_update_protocol': 1,
            'data_compatibility': 'multica-profile-v1', 'version': version, 'commit': commit,
            'published_at': datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ'),
            'notes_url': notes, 'cli': {'install_url': base + '/multica-update.sh', 'install_sha256': script_hash},
            'desktop': {}, 'coordinated_desktop': desktop}
    output = release / 'latest.json'
    with output.open('x', encoding='utf-8') as f:
        json.dump(data, f, ensure_ascii=False, separators=(',', ':'))
        f.write('\n')
    return output


def verify_immutable(stage, immutable):
    import json
    stage, immutable = Path(stage), Path(immutable)
    version, commit = build_identity(stage)
    if build_identity(immutable) != (version, commit):
        raise ValueError('immutable release identity mismatch')
    def read_sums(root):
        result = {}
        checksum_path = root / 'SHA256SUMS'
        if checksum_path.is_symlink() or not checksum_path.is_file():
            raise ValueError('checksum list must be a regular file')
        for line in checksum_path.read_text(encoding='utf-8').splitlines():
            match = re.fullmatch(r'([0-9a-f]{64})  ([A-Za-z0-9._+-]+)', line)
            if not match or match.group(2) in result or match.group(2) in ('.', '..', 'SHA256SUMS'):
                raise ValueError('invalid checksum entry')
            digest, name = match.groups()
            file = root / name
            if file.is_symlink() or not file.is_file() or sha(file) != digest:
                raise ValueError('checksum failure: ' + name)
            result[name] = digest
        return result
    published, staged = read_sums(immutable), read_sums(stage)
    app_name = f'multica-desktop-{version}-mac-arm64.zip'
    required = {'BUILD.txt', 'RELEASE-NOTES.md', 's90-ca.crt', 'multica-update.sh', 'update-package.json',
                'UPDATE-SHA256SUMS', 'update-manifest-linux-amd64.json', 'update-manifest-linux-arm64.json',
                'latest.json', app_name} | {f'multica-{version}-{p}' for p in ('linux-amd64', 'linux-arm64', 'darwin-amd64', 'darwin-arm64')}
    if {'install.sh', 'INSTALL.md'} & published.keys():
        raise ValueError('legacy installer assets must not be published')
    if not required <= published.keys():
        raise ValueError('immutable checksum list omits required fixed assets')
    base = f'https://10.1.24.90:45671/downloads/{version}'
    for arch in ('amd64', 'arm64'):
        metadata = json.loads((immutable / f'update-manifest-linux-{arch}.json').read_text(encoding='utf-8'))
        if (metadata.get('source'), metadata.get('version'), metadata.get('commit'), metadata.get('platform'), metadata.get('data_compatibility')) != ('hyc5566/multica', version, commit, 'linux-' + arch, 'multica-profile-v1'):
            raise ValueError('immutable manifest identity mismatch')
        entries = metadata.get('assets')
        if not isinstance(entries, list) or len([e for e in entries if e.get('role') == 'binary']) != 1:
            raise ValueError('immutable manifest binary missing or duplicated')
        roles = set()
        for entry in entries:
            role = entry.get('role')
            if role not in ('binary', 'launcher') or role in roles:
                raise ValueError('immutable manifest role invalid')
            roles.add(role)
            name = f'multica-{version}-linux-{arch}' if role == 'binary' else f'multica-launcher-{version}-linux-{arch}'
            item = immutable / name
            if name not in published or entry != {'role': role, 'url': base + '/' + name, 'size': item.stat().st_size, 'sha256': published[name]}:
                raise ValueError('immutable manifest asset mismatch')
            required.add(name)
    for name in required - {'latest.json'}:
        stage_name = 'multica-desktop-latest-mac-arm64.zip' if name == app_name else name
        if staged.get(stage_name) != published[name]:
            raise ValueError('staged asset differs from immutable release: ' + name)
    if sha(stage / 'latest.json') != published['latest.json']:
        raise ValueError('staged channel differs from immutable release')
    channel = json.loads((immutable / 'latest.json').read_text(encoding='utf-8'))
    if (channel.get('source_id'), channel.get('version'), channel.get('commit'), channel.get('desktop'), channel.get('coordinated_update_protocol')) != ('hyc5566/multica', version, commit, {}, 1):
        raise ValueError('immutable channel identity mismatch')
    return version


if __name__ == '__main__':
    import argparse
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--verify-immutable', nargs=2, metavar=('STAGE', 'RELEASE'))
    parser.add_argument('release', nargs='?')
    parser.add_argument('base', nargs='?')
    parser.add_argument('notes', nargs='?')
    parser.add_argument('app_zip', nargs='?')
    args = parser.parse_args()
    if args.verify_immutable:
        if args.release or args.base or args.notes or args.app_zip:
            parser.error('immutable verification accepts no positional arguments')
        print(verify_immutable(*args.verify_immutable))
    else:
        if not args.release or not args.base or not args.notes:
            parser.error('release, base and notes are required')
        print(emit_manifest(args.release, args.base, args.notes, args.app_zip))
