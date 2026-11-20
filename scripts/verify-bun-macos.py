#!/usr/bin/env python3
"""Verify a packaged macOS Bun Core in a disposable profile."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
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


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--core-dir', type=Path, default=ROOT / 'dist-core/macos-arm64-bun-plugins')
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
        lock = json.loads((ROOT / 'core-version.macos-arm64.json').read_text())
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
        profile = scratch / 'profile'
        profile.mkdir(mode=0o700)

        for stage in ('fresh profile', 'same-profile restart'):
            verify_start(runtime, core, manifest, profile)
            print(f'Bun {stage}: readiness, real PTY, HTTP and process cleanup passed')


if __name__ == '__main__':
    main()
