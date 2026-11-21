#!/usr/bin/env python3
"""Package the pinned official macOS Orca runtime for offline installation."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import runpy
import shutil
import subprocess
import tarfile
import tempfile
import zipfile

from upstream_cache import ROOT, cache_dir, fetch_pinned, fetch_source, file_digest

VERSION_FILE = ROOT / 'core-version.macos-arm64.json'
INVENTORY_SOURCE = 'src/shared/remote-runtime/orcad-distribution/orcad-artifacts.ts'
SQLITE_MIN_MACOS = '13.5'
DATA_PLUGIN_BUNDLE = r"""
import { existsSync } from 'node:fs';
import { dirname, join } from 'node:path';
import {
  buildOrcadEntry, buildOrcadWorker, ORCAD_CHILD_ENTRY_POINTS, orcadChildOutputFilename,
} from './config/scripts/orcad-distribution/server-bundle/orcad-entry-build.mjs';

const server = process.argv[1];
const slot = dirname(server);
const workers = Object.values(ORCAD_CHILD_ENTRY_POINTS)
  .map(entry => [entry, orcadChildOutputFilename(entry)])
  .filter(([, filename]) => !existsSync(join(slot, filename)));
workers.push(['src/main/plugins/workers/plugin-host-entry.ts', 'plugin-host-entry.js']);
await Promise.all([
  buildOrcadEntry(server),
  ...workers.map(([entry, filename]) => buildOrcadWorker(entry, join(slot, filename))),
]);
process.stdout.write(JSON.stringify(workers.map(([, filename]) => filename).sort()));
"""
SLOT_VERSION = r"""
const fs = require('node:fs'), path = require('node:path'), crypto = require('node:crypto');
const inventory = require(process.argv[1]), slot = process.argv[2], browser = process.argv[3];
const mobile = inventory.orcadMobileWebAssetFilenames(fs.readFileSync(path.join(slot, 'out/mobile-web/manifest.json'), 'utf8'));
const digest = crypto.createHash('sha256');
for (const name of inventory.orcadArtifactFilenames('darwin-arm64', mobile)) {
  digest.update(fs.readFileSync(path.join(slot, name)));
}
digest.update(fs.readFileSync(path.join(slot, browser)));
process.stdout.write(inventory.ORCAD_VERSION + '+' + digest.digest('hex').slice(0, 12));
"""
PTY_CHECK = r"""
const pty = require(process.argv[1]);
const terminal = pty.spawn('/bin/sh', ['-c', 'printf ORCA_MAC_PTY_OK'], {
  cols: 80, rows: 24, cwd: process.cwd(), env: process.env
});
let output = '';
const deadline = setTimeout(() => process.exit(2), 8000);
terminal.onData(chunk => output += chunk);
terminal.onExit(({exitCode}) => {
  clearTimeout(deadline);
  process.exit(exitCode === 0 && output.includes('ORCA_MAC_PTY_OK') ? 0 : 3);
});
"""


def require(condition, message):
    if not condition:
        raise ValueError(message)


def check_hash(path, expected):
    require(file_digest(path) == expected, f'SHA-256 mismatch for {path.name}')


def safe_path(root, relative):
    require(isinstance(relative, str) and '\\' not in relative and
            all(part not in ('', '.', '..') for part in relative.split('/')),
            f'Invalid bundle path: {relative!r}')
    path = root / relative
    require(path.resolve().is_relative_to(root.resolve()), f'Bundle path leaves its root: {relative}')
    return path


def copy_resource(source, destination, digest=None):
    if digest is not None:
        check_hash(source, digest)
    destination.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(source, destination)


def stage_bun(lock, work, stage, bun_archive, sqlite_archive, cache):
    pin = lock['bun']
    version = pin['version']
    if bun_archive is None:
        bun_archive = fetch_pinned(
            f'https://registry.npmjs.org/@oven/bun-darwin-aarch64/-/bun-darwin-aarch64-{version}.tgz',
            pin['archive_sha256'], cache,
        )
    else:
        check_hash(bun_archive, pin['archive_sha256'])
    bun = stage / f'runtimes/bun-{version}/bun'
    bun.parent.mkdir(parents=True)
    with tarfile.open(bun_archive, 'r:gz') as archive:
        with archive.extractfile('package/bin/bun') as source, bun.open('wb') as output:
            shutil.copyfileobj(source, output)
    check_hash(bun, pin['executable_sha256'])
    bun.chmod(0o755)
    license_file = fetch_pinned(f'https://raw.githubusercontent.com/oven-sh/bun/bun-v{version}/LICENSE.md',
                                pin['license_sha256'], cache)
    copy_resource(license_file, stage / 'licenses/BUN-LICENSE')

    sqlite_pin = lock['sqlite']
    major, minor, patch = map(int, sqlite_pin['version'].split('.'))
    name = f'sqlite-amalgamation-{major * 1000000 + minor * 10000 + patch * 100}'
    if sqlite_archive is None:
        sqlite_archive = fetch_pinned(f'https://sqlite.org/2026/{name}.zip', sqlite_pin['archive_sha3_256'],
                                      cache, algorithm='sha3_256')
    else:
        require(file_digest(sqlite_archive, 'sha3_256') == sqlite_pin['archive_sha3_256'],
                'SHA3-256 mismatch for SQLite amalgamation')
    with zipfile.ZipFile(sqlite_archive) as archive:
        source = work / 'sqlite3.c'
        source.write_bytes(archive.read(f'{name}/sqlite3.c'))
        notice = archive.read(f'{name}/sqlite3.h').decode().splitlines()[2:9]
    sqlite = bun.with_name('libsqlite3.dylib')
    subprocess.run(['/usr/bin/xcrun', 'clang', '-arch', 'arm64', '-dynamiclib', '-O2',
                    f'-mmacosx-version-min={SQLITE_MIN_MACOS}', '-install_name', '@rpath/libsqlite3.dylib',
                    '-DSQLITE_ENABLE_SESSION', '-DSQLITE_ENABLE_PREUPDATE_HOOK',
                    '-DSQLITE_ENABLE_COLUMN_METADATA', '-DSQLITE_ENABLE_FTS5',
                    source, '-o', sqlite], check=True, timeout=180)
    (stage / 'licenses/SQLITE-PUBLIC-DOMAIN').write_text(
        '\n'.join(line.removeprefix('**').lstrip(' ') for line in notice).strip() + '\n'
    )
    return bun


def build_data_plugin_core(args, upstream, work, stage):
    source_archive = args.upstream_source_archive
    if source_archive is None:
        source_archive = fetch_source(upstream['commit'], args.cache, upstream['source_archive_sha256'])
    else:
        check_hash(source_archive, upstream['source_archive_sha256'])

    checkout = work / 'upstream'
    checkout.mkdir()
    # The archive was pinned in full before unpacking. Edits go into a disposable directory.
    subprocess.run(['/usr/bin/tar', '-xzf', source_archive, '--strip-components=1', '-C', checkout],
                   check=True, timeout=90)
    copy_resource(checkout / INVENTORY_SOURCE, work / 'orcad-artifacts.ts', upstream['artifact_source_sha256'])
    copy_resource(checkout / 'LICENSE', stage / 'licenses/ORCA-LICENSE', upstream['license_sha256'])
    env = {k: v for k, v in os.environ.items()
           if not k.startswith(('ELECTRON_', 'ORCA_', 'NODE_', 'BUN_', 'GIT_'))}
    # The local runtime patch builds on the data-plugin settings navigation.
    for name in (
        'macos-data-plugins.patch',
        'macos-tab-drag.patch',
        'macos-local-runtime.patch',
        'macos-plugin-catalog.patch',
    ):
        subprocess.run(['/usr/bin/git', 'apply', '--whitespace=error-all', ROOT / 'scripts' / name],
                       cwd=checkout, env={**env, 'GIT_CEILING_DIRECTORIES': str(work)},
                       check=True, timeout=30)
    env['ORCA_TAURI_SOURCE_COMMIT'] = upstream['commit']
    # Just upstream’s locked dependencies. Native runtime binaries come from the signed app.
    pnpm = ['npm', 'exec', '--yes', '--package=pnpm@12.14.0', '--', 'pnpm']
    subprocess.run([*pnpm, 'install', '--frozen-lockfile', '--ignore-scripts'],
                   cwd=checkout, env=env, check=True, timeout=1200)
    subprocess.run([*pnpm, 'run', 'build:web'], cwd=checkout, env=env, check=True, timeout=300)
    server = stage / 'orcad/orcad-server.js'
    workers = json.loads(subprocess.check_output(
        ['node', '--input-type=module', '-e', DATA_PLUGIN_BUNDLE, server],
        cwd=checkout, env=env, timeout=180))

    # The separately served Web files must participate in the derived slot identity too.
    digest = hashlib.sha256()
    web = checkout / 'out/web'
    require((web / 'web-index.html').is_file(), 'Built Web entry is missing')
    for file in sorted(web.rglob('*')):
        if file.is_file():
            relative = file.relative_to(web).as_posix()
            digest.update(relative.encode() + b'\0' + bytes.fromhex(file_digest(file)))
            copy_resource(file, safe_path(stage / 'web', relative))
    with server.open('a') as bundled:
        bundled.write(f'\n/* Orca Tauri Web SHA-256 {digest.hexdigest()} */\n')
        for filename in workers:
            bundled.write(f'/* Orca worker {filename} SHA-256 {file_digest(server.with_name(filename))} */\n')


def adapt_bun_slot(slot, runtime, lock, verifier):
    version = lock['bun']['version']
    relative = f'../runtimes/bun-{version}'
    server = slot / 'orcad-server.js'

    sqlite_relative = relative + '/libsqlite3.dylib'
    # Include the native runtime inputs in the slot content identity.
    prefix = (
        f'/* Bun {lock["bun"]["executable_sha256"]}, SQLite {file_digest(runtime.with_name("libsqlite3.dylib"))} */\n'
        'require("bun:sqlite").Database.setCustomSQLite('
        f'require("node:path").join(__dirname,{json.dumps(sqlite_relative)}));\n'
    )
    for entry in slot.glob('*.js'):
        source = entry.read_text()
        if entry == server or 'node:sqlite' in source:
            entry.write_text(prefix + source)
    terminal = slot / 'node_modules/node-pty/lib/unixTerminal.js'
    verifier['patch_file'](terminal, verifier['TERMINAL_SHA256'],
                           'new tty.ReadStream(term.fd)', verifier['BUN_PTY_STREAM'])


def stage_core(args, lock, work, stage):
    app = args.orca_app.resolve()
    upstream = lock['upstream']
    signing_requirement = ('identifier "com.stablyai.orca" and anchor apple generic '
                           f'and certificate leaf[subject.OU] = "{upstream["team_id"]}"')
    subprocess.run(['/usr/bin/codesign', '--verify', '--deep', '--strict',
                    '-R=' + signing_requirement, app], check=True)
    resources = app / 'Contents/Resources'
    build = json.loads((resources / 'orca-local-build.json').read_text())
    # The official app records the workflow commit. We rebuild from its tag checkout.
    require((build['version'], build['commit'], build['architecture']) ==
            (lock['version'].removeprefix('v'), upstream['app_build_commit'], 'arm64'),
            'Orca.app does not match core-version.macos-arm64.json')

    template = resources / 'orcad-template'
    inventory = template / 'orcad-template.json'
    check_hash(inventory, upstream['template_sha256'])
    template_data = json.loads(inventory.read_text())
    target = lock['upstream_target']
    native = template_data['targets'][target]
    native_root = template / 'targets' / target
    slot = stage / 'orcad'
    for source_root, files in ((template, template_data['commonSha256']), (native_root, native['files'])):
        for file, digest in files.items():
            copy_resource(safe_path(source_root, file), safe_path(slot, file), digest)

    browser = native['browserName']
    browser_root = resources if native.get('packagedBrowser') else native_root
    copy_resource(safe_path(browser_root, browser), safe_path(slot, browser), native['browserSha256'])
    for name in (browser, 'node_modules/node-pty/build/Release/spawn-helper',
                 'node_modules/node-pty/build/Release/journal-file-lock',
                 'bin/orca-provider-exec', 'ripgrep/darwin-arm64/rg'):
        binary = safe_path(slot, name)
        binary.chmod(binary.stat().st_mode | 0o111)

    renderer = resources / 'app.asar.unpacked/out/renderer'
    web_inventory = renderer / '.orca-web-client-files.json'
    check_hash(web_inventory, upstream['web_inventory_sha256'])
    web_files = json.loads(web_inventory.read_text())
    require('web-index.html' in web_files, 'Missing browser entry in upstream inventory')
    build_data_plugin_core(args, upstream, work, stage)

    require((slot / '.runtime-node').read_text().strip() == upstream['node_executable_sha256'] and
            (slot / '.server-target').read_text().strip() == target,
            'Official Orcad slot does not match the upstream runtime pin or target')

    verifier = runpy.run_path(str(ROOT / 'scripts/verify-bun-macos.py'))
    runtime = stage_bun(lock, work, stage, args.bun_archive, args.sqlite_archive, args.cache)
    adapt_bun_slot(slot, runtime, lock, verifier)

    source = work / 'orcad-artifacts.ts'
    env = {k: v for k, v in os.environ.items() if not k.startswith(('ELECTRON_', 'ORCA_', 'NODE_', 'BUN_'))}
    runtime_env = {'BUN_OPTIONS': '--no-env-file', 'DO_NOT_TRACK': '1'}
    env.update(runtime_env)
    version = subprocess.check_output([runtime, '-e', SLOT_VERSION, source, slot, browser], env=env, text=True)
    (slot / '.version').write_text(version + '\n')
    env.update(ORCA_USER_DATA=str(work / 'smoke-profile'), ORCA_VERSION=version, ORCA_SERVE_RUNTIME='orcad',
               ORCA_TAURI_DISABLE_ELECTRON_BROWSER='1')
    subprocess.run([runtime, slot / 'orcad-server.js', '--orcad-smoke-load-check'],
                   env=env, cwd=slot, check=True, timeout=40)
    subprocess.run([runtime, '-e', PTY_CHECK, slot / 'node_modules/node-pty'],
                   env=env, cwd=slot, check=True, timeout=15)

    manifest = {
        'schema_version': 1, 'version': lock['version'], 'target': lock['target'],
        'package_revision': lock['package_revision'],
        'executable': runtime.relative_to(stage).as_posix(), 'working_directory': 'orcad',
        'args': ['{core_dir}/orcad/orcad-server.js', '--bind', '{host}', '--port', '{port}',
                 '--web-client-root', '{core_dir}/web', '--json'],
        'environment': {'ORCA_USER_DATA': '{data_dir}', 'ORCA_VERSION': version,
                        'ORCA_SERVE_RUNTIME': 'orcad', 'ORCA_TAURI_DISABLE_ELECTRON_BROWSER': '1',
                        'ORCA_TAURI_DATA_PLUGINS': '1',
                        'ORCA_TAURI_PLUGIN_VERSION': lock['version'].removeprefix('v'), **runtime_env},
        'health_path': '/web-index.html', 'ui_path': '/web-index.html',
        'upstream_commit': upstream['commit'], 'slot_version': version,
        'plugin_host': 'web-plugins-v2',
        'runtime': 'bun', 'bun_version': lock['bun']['version'],
        'bun_sha256': lock['bun']['executable_sha256'],
    }
    (stage / 'core-manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
    (stage / 'upstream-lock.json').write_text(json.dumps(lock, indent=2) + '\n')
    profile = work / 'boot-profile'
    profile.mkdir(mode=0o700)
    verifier['verify_start'](runtime, stage, manifest, profile)
    verifier['verify_start'](runtime, stage, manifest, profile)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    source = parser.add_mutually_exclusive_group()
    source.add_argument('--orca-app', type=Path, default=Path('/Applications/Orca.app'))
    source.add_argument('--download-orca', action='store_true', help='Download the pinned official Orca.app')
    parser.add_argument('--upstream-source-archive', type=Path)
    parser.add_argument('--bun-archive', type=Path)
    parser.add_argument('--sqlite-archive', type=Path)
    parser.add_argument('--version-file', type=Path, default=VERSION_FILE)
    parser.add_argument('--output', type=Path)
    args = parser.parse_args()
    args.cache = cache_dir()

    require(platform.system() == 'Darwin' and platform.machine() == 'arm64', 'Use an Apple Silicon Mac')
    lock = json.loads(args.version_file.read_text())
    stable = r'(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)'
    require(re.fullmatch(f'v{stable}', lock['version'])
            and re.fullmatch(stable, lock['package_revision'])
            and lock['package_revision'] != '0.0.0', 'Invalid core package version')
    output = (args.output or ROOT / 'dist-core/macos-arm64-bun-plugins').resolve()
    output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='.macos-core-', dir=output) as directory:
        work = Path(directory)
        if args.download_orca:
            upstream = lock['upstream']
            archive = fetch_pinned(
                f'https://github.com/stablyai/orca/releases/download/{lock["version"]}/{upstream["archive"]}',
                upstream['archive_sha256'], args.cache, timeout=900,
            )
            unpacked = work / 'official-app'
            subprocess.run(['/usr/bin/ditto', '-x', '-k', archive, unpacked], check=True)
            args.orca_app = unpacked / 'Orca.app'
        stage = work / 'stage'
        stage_core(args, lock, work, stage)
        package = f"{lock['package_revision']}-{lock['version'].removeprefix('v')}"
        archive = work / f"orca-core-v{package}-{lock['target']}.zip"
        with zipfile.ZipFile(archive, 'w', compression=zipfile.ZIP_DEFLATED, compresslevel=4) as bundle:
            for path in sorted(stage.rglob('*')):
                if path.is_file():
                    bundle.write(path, path.relative_to(stage).as_posix())
        seed = work / 'bootstrap-core.json'
        identity = {key: lock[key] for key in ('version', 'package_revision', 'target')}
        identity['sha256'] = file_digest(archive)
        seed.write_text(json.dumps(identity, indent=2) + '\n')
        checksum = archive.with_suffix('.zip.sha256')
        checksum.write_text(f"{identity['sha256']}  {archive.name}\n")
        bootstrap = work / 'bootstrap-core.zip'
        os.link(archive, bootstrap)
        archive.replace(output / archive.name)
        checksum.replace(output / checksum.name)
        bootstrap.replace(output / bootstrap.name)
        seed.replace(output / seed.name)
    print(f'Mac Core (Bun) packaged at {output}')


if __name__ == '__main__':
    main()
