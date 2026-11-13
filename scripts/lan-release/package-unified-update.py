#!/usr/bin/env python3
"""Package a commit-bound, single-file updater without installing or publishing."""
import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import re
import subprocess


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def shell_source(repo, version, commit):
    modules = {}
    for name, filename in (('_daemon_updater', 'update-daemon.py'),
                           ('_multica_update', 'multica_update.py')):
        source = (repo / 'scripts/deploy' / filename).read_text()
        compile(source, filename, 'exec')
        modules[name] = source
    if 'UPDATER_PROTOCOL = 1' not in modules['_multica_update']:
        raise ValueError('The updater transaction protocol is not ready for packaging')
    bootstrap = (
        'import sys, types\n'
        f'sources = {modules!r}\n'
        'for name, source in sources.items():\n'
        '    module = types.ModuleType(name)\n'
        '    module.__file__ = "embedded:" + name + ".py"\n'
        '    sys.modules[name] = module\n'
        '    exec(compile(source, module.__file__, "exec"), module.__dict__)\n'
        f'sys.modules["_multica_update"].EXPECTED_VERSION = {version!r}\n'
        f'sys.modules["_multica_update"].EXPECTED_COMMIT = {commit!r}\n'
        'sys.argv = [sys.argv[1], *sys.argv[2:]]\n'
        'sys.exit(sys.modules["_multica_update"].main())\n'
    )
    compile(bootstrap, '<updater-bootstrap>', 'exec')
    return ('#!/bin/sh\n'
            '# Source: hyc5566/multica. Verify the published SHA-256 before execution.\n'
            f'# Release: {version}; commit: {commit}\n'
            'exec python3 - "$0" "$@" <<\'MULTICA_UPDATE_PYTHON\'\n'
            + bootstrap + 'MULTICA_UPDATE_PYTHON\n')


def package(repo, release, version, commit, base):
    if not re.fullmatch(r'\d+\.\d+\.\d+-zh-tw\.\d+(?:-rc\.\d+)?(?:\+up\.[0-9a-f]{7})?', version):
        raise ValueError('A fixed Taiwan release version is required')
    if not re.fullmatch(r'[0-9a-f]{40}', commit):
        raise ValueError('A full source commit is required')
    if base != f'https://10.1.24.90:45671/downloads/{version}':
        raise ValueError('Use the approved HTTPS origin and immutable version directory')
    head = subprocess.check_output(['git', '-C', str(repo), 'rev-parse', 'HEAD'], text=True).strip()
    if commit != head:
        raise ValueError('The source commit differs from the checkout')
    dirty = subprocess.check_output(['git', '-C', str(repo), 'status', '--porcelain'], text=True)
    if dirty.strip():
        raise ValueError('Commit and review all source changes before packaging')
    spec = importlib.util.spec_from_file_location('verify_release_binary', repo / 'scripts/lan-release/verify_release_binary.py')
    verifier = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(verifier)
    if verifier.build_identity(release) != (version, commit):
        raise ValueError('BUILD.txt differs from requested source identity')
    outputs = ['multica-update.sh', 'update-manifest-linux-amd64.json', 'update-manifest-linux-arm64.json', 'UPDATE-SHA256SUMS', 'update-package.json']
    if any((release / name).exists() or (release / name).is_symlink() for name in outputs):
        raise FileExistsError('Refusing to overwrite packaged assets')
    script = release / 'multica-update.sh'
    if script.exists():
        raise FileExistsError('Refusing to overwrite a packaged updater')
    # All inputs are validated before writing release assets.
    assets = {}
    for arch in ('amd64', 'arm64'):
        binary = release / f'multica-{version}-linux-{arch}'
        if not binary.is_file() or binary.is_symlink():
            raise ValueError(f'Missing regular fixed binary: {binary.name}')
        verifier.verify_binary(binary, version, commit, 'linux', arch)
        launcher = release / f'multica-launcher-{version}-linux-{arch}'
        if launcher.is_symlink() or (launcher.exists() and not launcher.is_file()):
            raise ValueError('The launcher must be a regular program asset')
        assets[arch] = binary
    source = shell_source(repo, version, commit)
    script.write_text(source)
    manifests = []
    for arch, binary in assets.items():
        data = {'source': 'hyc5566/multica', 'version': version, 'commit': commit,
                'data_compatibility': 'multica-profile-v1',
                'platform': f'linux-{arch}',
                'assets': [{'role': 'binary', 'url': f'{base}/{binary.name}',
                            'size': binary.stat().st_size, 'sha256': sha(binary)}]}
        launcher = release / f'multica-launcher-{version}-linux-{arch}'
        if launcher.exists():
            if not launcher.is_file() or launcher.is_symlink():
                raise ValueError('The launcher must be a regular program asset')
            data['assets'].append({'role': 'launcher', 'url': f'{base}/{launcher.name}',
                                   'size': launcher.stat().st_size, 'sha256': sha(launcher)})
        path = release / f'update-manifest-linux-{arch}.json'
        path.write_text(json.dumps(data, indent=2, ensure_ascii=False) + '\n')
        manifests.append(path)
    checksum = release / 'UPDATE-SHA256SUMS'
    paths = [script, *manifests]
    checksum.write_text(''.join(f'{sha(path)}  {path.name}\n' for path in paths))
    metadata = {'source': 'hyc5566/multica', 'version': version, 'commit': commit,
                'data_compatibility': 'multica-profile-v1',
                'script': {'url': f'{base}/{script.name}', 'size': script.stat().st_size,
                           'sha256': sha(script)},
                'manifests': {path.stem.removeprefix('update-manifest-'):
                              {'url': f'{base}/{path.name}', 'sha256': sha(path),
                               'size': path.stat().st_size} for path in manifests},
                'checksums_url': f'{base}/{checksum.name}'}
    (release / 'update-package.json').write_text(json.dumps(metadata, indent=2) + '\n')
    return metadata


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--release-dir', required=True, type=Path)
    parser.add_argument('--version', required=True)
    parser.add_argument('--commit', required=True)
    parser.add_argument('--base-url', required=True)
    args = parser.parse_args()
    repo = Path(__file__).resolve().parents[2]
    result = package(repo, args.release_dir.resolve(), args.version, args.commit, args.base_url)
    print(json.dumps(result, indent=2))


if __name__ == '__main__':
    main()
