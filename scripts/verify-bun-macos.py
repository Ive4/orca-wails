#!/usr/bin/env python3
"""Verify a packaged macOS Bun Core in a disposable profile."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import plistlib
import queue
import signal
import socket
import subprocess
import tempfile
import threading
import time
import urllib.request
import zipfile

ROOT = Path(__file__).resolve().parent.parent

TERMINAL_SHA256 = '1eeb6ec476884b533f1648f50d9571eda031b0928f35174c0d74feabc3e03bd6'
CODEX_WORKERS = {
    'codex-rollout-history-references-worker-entry.js': 'references',
    'codex-history-disk-usage-worker-entry.js': 'disk-usage',
}
CODEX_WORKER_CHECK = r"""
const { join } = require('node:path');
const { Worker } = require('node:worker_threads');
const home = process.argv[2];
const references = process.argv[3] === 'references';
const worker = new Worker(process.argv[1], { workerData: { cachePath: join(home, 'references.json') } });
const request = references ? { id: 1, op: 'prepare', homes: [] } : {
  id: 1, homes: [home], accounts: [], systemCodexHomePath: home,
  budget: { maxEntries: 100, timeMs: 2000 },
};
const timer = setTimeout(() => process.exit(2), 5000);
worker.once('message', (response) => {
  clearTimeout(timer);
  const result = references ? response.value === true :
    response.usage?.complete === true && response.usage.rollouts === 1;
  process.exit(response.id === 1 && response.ok && result ? 0 : 3);
});
worker.postMessage(request);
"""

# Bun's TTY stream aborts on EAGAIN. Retry on the same fd until bytes arrive,
# EOF is reached, or node-pty destroys the stream. Keep native resize and writes.
BUN_PTY_STREAM = '''fs.createReadStream(null, { fd: term.fd, fs: {
            read(fd, buffer, offset, length, position, callback) {
                function attempt() {
                    if (_this._socket.destroyed) {
                        callback(null, 0, buffer);
                        return;
                    }
                    fs.read(fd, buffer, offset, length, position, (error, count) => {
                        if (error?.code === "EAGAIN") {
                            setTimeout(attempt, 8);
                        } else {
                            callback(error?.code === "EIO" ? null : error, count || 0, buffer);
                        }
                    });
                }
                attempt();
            },
            close: fs.close,
        } })'''


def verify(path, expected):
    with path.open('rb') as stream:
        digest = hashlib.sha256()
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(block)
    if digest.hexdigest() != expected:
        raise ValueError(f'SHA-256 mismatch for {path.name}')


def patch_file(path, expected, original, replacement):
    verify(path, expected)
    source = path.read_text()
    if source.count(original) != 1:
        raise ValueError(f'Compatibility patch no longer matches {path.name}')
    path.write_text(source.replace(original, replacement))


def verify_cua_driver(core, manifest, pin):
    version = pin['version']
    bundle = f'runtimes/cua-driver-{version}/CuaDriver.app'
    executable = f'{bundle}/Contents/MacOS/cua-driver'
    if manifest['environment'].get('ORCA_WAILS_CUA_DRIVER_PATH') != f'{{core_dir}}/{executable}':
        raise ValueError('Core does not reference its packaged Cua Driver')
    requirement = ('identifier "com.trycua.driver" and anchor apple generic '
                   f'and certificate leaf[subject.OU] = "{pin["team_id"]}"')
    subprocess.run(['/usr/bin/codesign', '--verify', '--deep', '--strict',
                    '-R=' + requirement, core / bundle], check=True, timeout=30)
    if subprocess.check_output([core / executable, '--version'], text=True, timeout=15).strip() != f'cua-driver {version}':
        raise ValueError('Packaged Cua Driver version differs from the lock')
    verify(core / 'licenses/CUA-DRIVER-LICENSE', pin['license_sha256'])
    verify(core / 'licenses/CUA-DRIVER-THIRD-PARTY-NOTICES.md', pin['third_party_notices_sha256'])


def unpack_core(archive, destination):
    with zipfile.ZipFile(archive) as bundle:
        for file in bundle.infolist():
            path = Path(file.filename)
            if path.is_absolute() or '..' in path.parts:
                raise ValueError('Core archive contains an unsafe path')
            extracted = Path(bundle.extract(file, destination))
            mode = (file.external_attr >> 16) & 0o777
            if mode:
                extracted.chmod(mode)


def runtime_processes(runtime):
    rows = subprocess.check_output(['/bin/ps', '-axww', '-o', 'pid=,command='], text=True)
    prefix = str(runtime) + ' '
    return [
        int(pid)
        for pid, command in (row.split(None, 1) for row in rows.splitlines())
        if command.startswith(prefix)
    ]


def stop(process, runtime):
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait(timeout=5)
    deadline = time.monotonic() + 5
    while children := runtime_processes(runtime):
        for pid in children:
            try:
                os.kill(pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
        if time.monotonic() >= deadline:
            raise RuntimeError('Temporary Core process tree did not terminate')
        time.sleep(0.05)


def verify_start(runtime, core, manifest, profile):
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        port = listener.getsockname()[1]
    variables = dict(host='127.0.0.1', port=port, core_dir=core, data_dir=profile)
    env = {
        'PATH': '/usr/bin:/bin',
        'HOME': str(profile),
        'TMPDIR': str(profile),
        **{key: value.format_map(variables) for key, value in manifest['environment'].items()},
    }
    ready = queue.Queue()

    def read_events(stream):
        for line in stream:
            try:
                event = json.loads(line)
            except ValueError:
                continue
            if isinstance(event, dict) and event.get('type') == 'orca_server_ready':
                # Pairing credentials never leave the stdout reader.
                ready.put((event.get('boundEndpoint'), event.get('health', {})))
        ready.put(None)

    with subprocess.Popen(
        [str(runtime), *(arg.format_map(variables) for arg in manifest['args'])],
        cwd=core / manifest['working_directory'], env=env, stdout=subprocess.PIPE,
        stderr=subprocess.DEVNULL,
        start_new_session=True,
    ) as process:
        reader = threading.Thread(target=read_events, args=(process.stdout,), daemon=True)
        reader.start()
        try:
            try:
                result = ready.get(timeout=30)
            except queue.Empty:
                raise RuntimeError('Core readiness timed out') from None
            if result is None:
                raise RuntimeError(f'Core closed stdout before readiness (exit {process.wait(timeout=5)})')
            endpoint, health = result
            test = health.get('terminalDaemon', {}).get('selfTest', {})
            if (endpoint != f'ws://127.0.0.1:{port}' or health.get('pid') != process.pid
                    or health.get('buildVersion') != manifest['slot_version']
                    or test.get('ok') is not True or test.get('coverage') != 'pty-spawn'):
                raise RuntimeError('Core readiness or real PTY self-test failed')
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            with opener.open(f'http://127.0.0.1:{port}/web-index.html', timeout=3) as response:
                if response.status != 200 or response.headers.get_content_type() != 'text/html':
                    raise RuntimeError('Core did not serve the Web entry')
        finally:
            stop(process, runtime)
            reader.join(timeout=3)


def verify_computer_use(app, archive, executable):
    contents = app.resolve() / 'Contents'
    info = plistlib.loads((contents / 'Info.plist').read_bytes())
    if not info['CFBundleIdentifier'].startswith('dev.orca-wails.smoke.'):
        raise ValueError('Build the test app with build-macos.sh --smoke-test')

    with tempfile.TemporaryDirectory(prefix='orca-computer-', dir='/tmp') as directory:
        root = Path(directory).resolve()
        with socket.socket() as listener:
            listener.bind(('127.0.0.1', 0))
            port = listener.getsockname()[1]
        config_path = root / 'config.json'
        config = {
            'port': port, 'notifications': False, 'open_window_on_start': False,
            'macos_entries': {'menu_bar': False, 'notch': False},
        }
        config_path.write_text(json.dumps(config))
        env = {'PATH': '/usr/bin:/bin', 'HOME': str(root), 'TMPDIR': directory,
               'ORCA_DISABLE_MACOS_LOGIN_SHELL': '1'}
        with subprocess.Popen(
            [contents / 'MacOS/orca-wails', '--background', '--data-dir', root,
             '--bootstrap', archive.resolve()],
            env=env, start_new_session=True,
        ) as process:
            try:
                active = root / 'core/active.json'
                deadline = time.monotonic() + 60
                while not active.is_file():
                    if process.poll() is not None or time.monotonic() >= deadline:
                        raise RuntimeError('Wails did not activate the test Core')
                    time.sleep(0.1)
                cli = root / 'orca-data/cli/bin/orca'

                def computer(*args, overrides=None, expect_error=None):
                    probe = subprocess.run([cli, 'computer', *args, '--json'],
                                           env={**env, **(overrides or {})},
                                           capture_output=True, text=True, timeout=25)
                    if expect_error:
                        if probe.returncode == 0 or expect_error not in probe.stdout + probe.stderr:
                            raise RuntimeError(f'Expected {expect_error}: {probe.stdout or probe.stderr}')
                        return None
                    if probe.returncode != 0:
                        raise RuntimeError(f'Wails Computer Use probe failed: {(probe.stdout or probe.stderr).strip()}')
                    return json.loads(probe.stdout)

                initial = computer('capabilities')
                if (initial['result']['backend'], initial['result']['provider']) != ('cua', 'cua-driver'):
                    raise RuntimeError('Fresh Wails settings did not select local Cua')
                runtime_id = initial['_meta']['runtimeId']

                def check_backend(backend, provider, *args, overrides=None):
                    result = computer('capabilities', *args, overrides=overrides)
                    if (result['result']['backend'], result['result']['provider'],
                        result['_meta']['runtimeId']) != (backend, provider, runtime_id):
                        raise RuntimeError(f'Backend or Core runtime changed unexpectedly: {result}')

                if not computer('list-apps')['result']['apps']:
                    raise RuntimeError('Cua returned no installed or running apps')
                check_backend('orca', 'orca-computer-use-macos', '--backend', 'orca')
                check_backend('cua', 'cua-driver')
                computer('capabilities', '--backend', 'cua-vm', expect_error='invalid_argument')
                computer('capabilities', '--backend', 'typo', expect_error='invalid_argument')

                def save_settings(**settings):
                    temporary = config_path.with_suffix('.tmp')
                    temporary.write_text(json.dumps({**config, **settings}))
                    temporary.replace(config_path)

                save_settings(computer_use_backend='orca', computer_use_vm_ssh='orca-wails-no-guest.invalid')
                check_backend('orca', 'orca-computer-use-macos')
                check_backend('cua', 'cua-driver', overrides={'ORCA_COMPUTER_USE_BACKEND': 'cua'})
                check_backend('orca', 'orca-computer-use-macos', '--backend', 'orca',
                              overrides={'ORCA_COMPUTER_USE_BACKEND': 'cua'})
                computer('capabilities', '--backend', 'cua-vm', expect_error='provider_incompatible')
                check_backend('orca', 'orca-computer-use-macos')
            finally:
                runtime = next((root / 'core/versions').glob(f'*/{executable}'), root / 'uninstalled-bun')
                stop(process, runtime)
    print('Wails Computer Use: Cua and Orca handshakes, live defaults and explicit backend overrides passed')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--core-dir', type=Path, default=ROOT / 'dist-core/macos-arm64-bun-plugins')
    parser.add_argument('--version-file', type=Path, default=ROOT / 'core-version.macos-arm64.json')
    parser.add_argument('--app', type=Path, help='Also probe Computer Use through a Wails smoke-test app')
    args = parser.parse_args()

    if platform.system() != 'Darwin' or platform.machine() != 'arm64':
        raise SystemExit('This check requires Apple Silicon macOS')
    archive = args.core_dir / 'bootstrap-core.zip'
    seed = json.loads((args.core_dir / 'bootstrap-core.json').read_text())
    verify(archive, seed['sha256'])

    with tempfile.TemporaryDirectory(prefix='orca-bun-smoke-') as directory:
        scratch = Path(directory).resolve()
        core = scratch / 'core'
        unpack_core(archive, core)
        manifest = json.loads((core / 'core-manifest.json').read_text())
        lock = json.loads(args.version_file.read_text())
        bun = lock['bun']
        executable = f'runtimes/bun-{bun["version"]}/bun'
        if (manifest['version'], manifest['package_revision'], manifest['target'],
            manifest['upstream_commit'], manifest['runtime'], manifest['executable'],
            manifest['bun_version'], manifest['bun_sha256']) != (
            lock['version'], lock['package_revision'], lock['target'],
            lock['upstream']['commit'], 'bun', executable,
            bun['version'], bun['executable_sha256'],
        ):
            raise ValueError('Core archive differs from the pinned macOS release')
        runtime = core / executable
        verify(runtime, bun['executable_sha256'])
        if subprocess.check_output([runtime, '--version'], text=True).strip() != bun['version']:
            raise ValueError('Packaged Bun version differs from the lock')
        verify_cua_driver(core, manifest, lock['cua_driver'])
        profile = scratch / 'profile'
        profile.mkdir(mode=0o700)

        slot = core / manifest['working_directory']
        helper = slot / 'Orca Computer Use.app'
        helper_path = manifest['environment'].get('ORCA_COMPUTER_MACOS_HELPER_APP_PATH')
        if helper_path != f'{{core_dir}}/{manifest["working_directory"]}/{helper.name}':
            raise ValueError('Core does not reference its packaged Computer Use helper')
        subprocess.run(['/usr/bin/codesign', '--verify', '--strict',
                        '-R=identifier "dev.orca-wails.computer-use"', helper], check=True)
        server = (slot / 'orcad-server.js').read_bytes()
        worker_fixture = scratch / 'codex-worker-check'
        (worker_fixture / 'sessions').mkdir(parents=True)
        (worker_fixture / 'sessions/rollout-smoke.jsonl').write_text('{}\n')
        for filename, check in CODEX_WORKERS.items():
            if filename.encode() not in server:
                continue
            worker = slot / filename
            if not worker.is_file():
                raise ValueError(f'Core references a worker missing from the bundle: {worker.name}')
            subprocess.run([runtime, '-e', CODEX_WORKER_CHECK, worker, worker_fixture, check],
                           cwd=slot, check=True, timeout=10)
            print(f'Bun Codex {check} worker answered successfully')

        for stage in ('fresh profile', 'same-profile restart'):
            verify_start(runtime, core, manifest, profile)
            print(f'Bun {stage}: readiness, real PTY, HTTP and process cleanup passed')

    if args.app is not None:
        verify_computer_use(args.app, archive, executable)


if __name__ == '__main__':
    main()
