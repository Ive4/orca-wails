#!/usr/bin/env python3
"""Check the stable upstream Orca release or prepare candidate Core locks."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import zipfile

from upstream_cache import ROOT, cache_dir, curl, fetch_pinned, fetch_source, file_digest

GITHUB = 'https://github.com/stablyai/orca'
API = 'https://api.github.com/repos/stablyai/orca'
MAC_LOCK = 'core-version.macos-arm64.json'
WINDOWS_LOCK = 'core-version.json'
INVENTORY_SOURCE = 'src/shared/remote-runtime/orcad-distribution/orcad-artifacts.ts'
NODE_SOURCE = 'src/shared/node-runtime/node-runtime-pin.ts'
STABLE_TAG = r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)'
MAC_RESOURCES = 'Orca.app/Contents/Resources/'


def github_json(path):
    return json.loads(curl(API + path, 30, '--header', 'Accept: application/vnd.github+json',
                           stdout=subprocess.PIPE).stdout)


def release_asset(release, name):
    assets = [asset for asset in release['assets'] if asset['name'] == name]
    digest = assets[0].get('digest') if len(assets) == 1 else None
    if not isinstance(digest, str) or not re.fullmatch('sha256:[a-f0-9]{64}', digest):
        raise ValueError(f'Expected one GitHub asset with a SHA-256 digest for {name}')
    return f'{GITHUB}/releases/download/{release["tag_name"]}/{name}', digest.removeprefix('sha256:')


def tag_commit(tag):
    target = github_json(f'/git/ref/tags/{tag}')['object']
    for _ in range(5):
        oid = target['sha']
        if not re.fullmatch('[a-f0-9]{40}', oid):
            break
        if target['type'] == 'commit':
            return oid
        if target['type'] != 'tag':
            break
        target = github_json(f'/git/tags/{oid}')['object']
    raise ValueError(f'Cannot resolve {tag} to a commit')


def mac_release_pins(archive, tag, lock):
    with zipfile.ZipFile(archive) as bundle:
        def read(name):
            return bundle.read(MAC_RESOURCES + name)

        build = json.loads(read('orca-local-build.json'))
        commit = build['commit']
        if ((build['version'], build['architecture']) != (tag.removeprefix('v'), 'arm64')
                or not re.fullmatch('[a-f0-9]{40}', commit)):
            raise ValueError('The official macOS archive has unexpected build metadata')
        template = read('orcad-template/orcad-template.json')
        data = json.loads(template)
        target = lock['upstream_target']
        if data['schemaVersion'] not in (3, 4) or target not in data['targets']:
            raise ValueError('Review the new official orcad-template format')
        node = read(f'orcad-template/targets/{target}/.runtime-node').decode().strip()
        if node != lock['upstream']['node_executable_sha256']:
            raise ValueError('Review the new macOS Node runtime pin')
        web = read('app.asar.unpacked/out/renderer/.orca-web-client-files.json')
        if 'web-index.html' not in json.loads(web):
            raise ValueError('Review the new official Web inventory')
    return {
        'app_build_commit': commit,
        'template_sha256': hashlib.sha256(template).hexdigest(),
        'web_inventory_sha256': hashlib.sha256(web).hexdigest(),
    }


def write_candidate(path, lock):
    if path.is_file() and 'validation' in lock:
        previous = json.loads(path.read_text())
        validation = previous.pop('validation', None)
        if validation is not None and previous == {key: value for key, value in lock.items() if key != 'validation'}:
            lock = {**lock, 'validation': validation}
    with tempfile.TemporaryDirectory(dir=path.parent) as directory:
        staged = Path(directory) / path.name
        staged.write_text(json.dumps(lock, indent=2) + '\n')
        staged.replace(path)


def prepare(release, mac, windows):
    tag = release['tag_name']
    name = f'Orca-{tag.removeprefix("v")}-arm64-mac.zip'
    mac_url, mac_sha256 = release_asset(release, name)
    _, windows_sha256 = release_asset(release, windows['upstream']['asset'])
    commit = tag_commit(tag)
    cache = cache_dir()
    prior = mac['upstream']
    if mac['version'] == tag and (prior['commit'], prior['archive_sha256']) != (commit, mac_sha256):
        raise ValueError(f'The published macOS inputs for {tag} differ from the existing lock')
    windows_prior = windows['upstream']
    if windows['version'] == tag and (windows_prior['commit'], windows_prior['sha256']) != (commit, windows_sha256):
        raise ValueError(f'The published Windows inputs for {tag} differ from the existing lock')

    source = fetch_source(commit, cache, prior['source_archive_sha256'] if prior['commit'] == commit else None)
    with tempfile.TemporaryDirectory(prefix='orca-follow-') as directory:
        checkout = Path(directory) / 'source'
        checkout.mkdir()
        subprocess.run(['tar', '-xzf', source, '--strip-components=1', '-C', checkout], check=True, timeout=120)
        if json.loads((checkout / 'package.json').read_text())['version'] != tag.removeprefix('v'):
            raise ValueError('The upstream source package version differs from its release tag')
        node_pin = file_digest(checkout / NODE_SOURCE)
        if node_pin != windows['source_sha256'][NODE_SOURCE]:
            raise ValueError(f'Review {NODE_SOURCE} and update the runtime pins before following {tag}')
        env = {key: value for key, value in os.environ.items() if not key.startswith('GIT_')}
        for patch_name in (
            'macos-data-plugins.patch',
            'macos-tab-drag.patch',
            'macos-local-runtime.patch',
            'macos-plugin-catalog.patch',
            'macos-task-timing.patch',
            'macos-task-output-metrics.patch',
            'macos-computer-use.patch',
            'macos-cua-driver.patch',
            'macos-bot-team.patch',
            'macos-emulator-pane.patch',
            'macos-plugin-status-bar.patch',
            'macos-wechat-task-owner.patch',
            'macos-local-desktop.patch',
            'macos-chat-image-preview.patch',
            'macos-chromium-workspace.patch',
            'macos-shell-settings.patch',
            'macos-local-host-regressions.patch',
        ):
            subprocess.run(['git', 'apply', '--whitespace=error-all', ROOT / 'scripts' / patch_name],
                           cwd=checkout, env={**env, 'GIT_CEILING_DIRECTORIES': directory}, check=True, timeout=30)
        print(f'{tag} macOS Core patches apply', flush=True)
        artifact_sha256 = file_digest(checkout / INVENTORY_SOURCE)
        license_sha256 = file_digest(checkout / 'LICENSE')
    archive = fetch_pinned(mac_url, mac_sha256, cache, timeout=900)
    app_pins = mac_release_pins(archive, tag, mac)
    mac['version'] = tag
    mac['upstream'].update(archive=name, archive_sha256=mac_sha256, commit=commit,
                           source_archive_sha256=source.name, artifact_source_sha256=artifact_sha256,
                           license_sha256=license_sha256, **app_pins)
    if windows['version'] != tag:
        windows['validation'] = {
            'status': 'awaiting-windows-validation',
            'note': 'GitHub installer digest recorded. Run extract-headless.ps1 on Windows to qualify the Core.',
        }
    windows['version'] = tag
    windows['upstream'].update(commit=commit, sha256=windows_sha256)
    windows['source_sha256'].update({INVENTORY_SOURCE: artifact_sha256, 'LICENSE': license_sha256})

    output = ROOT / 'dist-core/follow' / tag
    output.mkdir(parents=True, exist_ok=True)
    write_candidate(output / MAC_LOCK, mac)
    write_candidate(output / WINDOWS_LOCK, windows)
    print(f'Candidate locks: {output}')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--tag', help='stable tag to inspect instead of latest')
    parser.add_argument('--prepare', action='store_true', help='cache macOS inputs and write candidate lock files')
    args = parser.parse_args()
    if args.tag and not re.fullmatch(STABLE_TAG, args.tag):
        parser.error('--tag must be a stable tag such as v1.4.267')
    release = github_json(f'/releases/tags/{args.tag}' if args.tag else '/releases/latest')
    tag = release['tag_name']
    if release['draft'] or release['prerelease'] or not re.fullmatch(STABLE_TAG, tag) or (args.tag and args.tag != tag):
        raise ValueError('Expected the requested stable Orca release')
    mac = json.loads((ROOT / MAC_LOCK).read_text())
    windows = json.loads((ROOT / WINDOWS_LOCK).read_text())
    print(f'Official release: {tag} ({GITHUB}/releases/tag/{tag})', flush=True)
    print(f'Pinned versions: macOS {mac["version"]}, Windows {windows["version"]}', flush=True)
    if args.prepare:
        prepare(release, mac, windows)


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, KeyError, subprocess.SubprocessError, zipfile.BadZipFile) as error:
        raise SystemExit(f'follow-orca: {error}') from None
