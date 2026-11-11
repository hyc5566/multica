#!/usr/bin/env python3
"""Validate the complete fixed release and generate its flat checksum list."""
import argparse
import json
from pathlib import Path
import verify_release_binary as verify


def finalize(release):
    release = Path(release)
    version, commit = verify.build_identity(release)
    base = f'https://10.1.24.90:45671/downloads/{version}'
    app_name = f'multica-desktop-{version}-mac-arm64.zip'
    raw = {platform: f'multica-{version}-{platform}' for platform in
           ('linux-amd64', 'linux-arm64', 'darwin-amd64', 'darwin-arm64')}
    required = ['BUILD.txt', 'RELEASE-NOTES.md', 's90-ca.crt', *raw.values(), app_name,
                'multica-update.sh', 'update-package.json', 'update-manifest-linux-amd64.json',
                'update-manifest-linux-arm64.json', 'UPDATE-SHA256SUMS', 'latest.json']
    optional = ['multica-linux-amd64.tar.gz', 'multica-linux-arm64.tar.gz', 'multica-darwin-amd64.tar.gz',
                'multica-darwin-arm64.tar.gz', 'server-linux-amd64.tar.gz', 'source.tar.gz']
    for name in required:
        path = release / name
        if path.is_symlink() or not path.is_file():
            raise ValueError('missing regular release asset: ' + name)
    for platform, name in raw.items():
        goos, arch = platform.split('-')
        verify.verify_binary(release / name, version, commit, goos, arch)
    app = verify.verify_app(release / app_name, release / raw['darwin-arm64'], version, commit)
    package = json.loads((release / 'update-package.json').read_text(encoding='utf-8'))
    latest = json.loads((release / 'latest.json').read_text(encoding='utf-8'))
    if (package.get('source'), package.get('version'), package.get('commit')) != ('hyc5566/multica', version, commit):
        raise ValueError('package identity mismatch')
    if (latest.get('source_id'), latest.get('version'), latest.get('commit'), latest.get('coordinated_update_protocol'), latest.get('desktop')) != ('hyc5566/multica', version, commit, 1, {}):
        raise ValueError('channel identity or legacy isolation mismatch')
    channel_app = latest.get('coordinated_desktop', {}).get('darwin-arm64', {})
    if any(channel_app.get(k) != v for k, v in dict(app, url=base + '/' + app_name, binary_version=version,
                                                   binary_commit=commit, data_compatibility='multica-profile-v1',
                                                   bundle_id=verify.BUNDLE_ID).items()):
        raise ValueError('channel App differs from validated ZIP')
    script = release / 'multica-update.sh'
    script_sha = verify.sha(script)
    if package.get('script') != {'url': base + '/multica-update.sh', 'size': script.stat().st_size, 'sha256': script_sha} or latest.get('cli') != {'install_url': base + '/multica-update.sh', 'install_sha256': script_sha}:
        raise ValueError('script metadata mismatch')
    for arch in ('amd64', 'arm64'):
        name = f'update-manifest-linux-{arch}.json'
        path = release / name
        manifest = json.loads(path.read_text(encoding='utf-8'))
        package_entry = package.get('manifests', {}).get('linux-' + arch)
        if package_entry != {'url': base + '/' + name, 'size': path.stat().st_size, 'sha256': verify.sha(path)}:
            raise ValueError('manifest metadata mismatch: ' + name)
        if (manifest.get('source'), manifest.get('version'), manifest.get('commit'), manifest.get('platform'), manifest.get('data_compatibility')) != ('hyc5566/multica', version, commit, 'linux-' + arch, 'multica-profile-v1'):
            raise ValueError('manifest identity mismatch: ' + name)
        assets = manifest.get('assets')
        if not isinstance(assets, list) or len([x for x in assets if x.get('role') == 'binary']) != 1:
            raise ValueError('manifest binary missing or duplicated')
        seen = set()
        for entry in assets:
            role = entry.get('role')
            if role not in ('binary', 'launcher') or role in seen:
                raise ValueError('invalid manifest asset role')
            seen.add(role)
            asset_name = raw['linux-' + arch] if role == 'binary' else f'multica-launcher-{version}-linux-{arch}'
            asset = release / asset_name
            if asset.is_symlink() or not asset.is_file() or entry != {'role': role, 'url': base + '/' + asset_name, 'size': asset.stat().st_size, 'sha256': verify.sha(asset)}:
                raise ValueError('manifest asset differs: ' + asset_name)
            if role == 'launcher':
                required.append(asset_name)
    if package.get('checksums_url') != base + '/UPDATE-SHA256SUMS':
        raise ValueError('updater checksums URL mismatch')
    expected_updates = ''.join(f'{verify.sha(release / name)}  {name}\n' for name in
                               ('multica-update.sh', 'update-manifest-linux-amd64.json', 'update-manifest-linux-arm64.json'))
    if (release / 'UPDATE-SHA256SUMS').read_text(encoding='utf-8') != expected_updates:
        raise ValueError('updater checksum list mismatch')
    for name in optional:
        path = release / name
        if path.is_symlink():
            raise ValueError('symlinked optional asset: ' + name)
        if path.is_file():
            required.append(name)
    output = release / 'SHA256SUMS'
    with output.open('x', encoding='utf-8') as f:
        for name in required:
            f.write(f'{verify.sha(release / name)}  {name}\n')
    return output


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('release', type=Path)
    args = parser.parse_args()
    print(finalize(args.release))
