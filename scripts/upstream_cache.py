"""Shared downloads for the upstream Orca maintenance and macOS build scripts."""

import fcntl
import hashlib
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parent.parent


def cache_dir():
    return Path(os.environ.get('ORCA_CORE_DOWNLOAD_CACHE') or ROOT / 'dist-core/upstream-cache').expanduser().resolve()


def file_digest(path, algorithm='sha256'):
    digest = hashlib.new(algorithm)
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(block)
    return digest.hexdigest()


def curl(url, timeout, *args, **kwargs):
    return subprocess.run(
        ['curl', '-fsSL', '--retry', '3', '--connect-timeout', '10', '--max-time', str(timeout),
         *args, '--', url],
        check=True, **kwargs,
    )


def curl_download(url, partial, timeout, resume=False):
    if resume and partial.is_file():
        try:
            curl(url, timeout, '--continue-at', '-', '-o', str(partial))
            return
        except subprocess.CalledProcessError as error:
            if error.returncode != 33:  # The server rejected the byte range.
                raise
            partial.unlink()
    curl(url, timeout, '--retry-all-errors', '-o', str(partial))


def fetch_pinned(url, digest, cache, timeout=180, algorithm='sha256'):
    if not re.fullmatch('[a-f0-9]{64}', digest):
        raise ValueError(f'Invalid {algorithm} digest')
    cache.mkdir(parents=True, exist_ok=True)
    result = cache / digest
    with (cache / (digest + '.lock')).open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        if result.is_file() and file_digest(result, algorithm) == digest:
            return result
        release = url.startswith('https://github.com/stablyai/orca/releases/download/')
        transfer = shutil.which('aria2c') if release and algorithm == 'sha256' else None
        if transfer:
            partial = result.with_suffix('.aria2-part')
            try:
                subprocess.run(
                    [transfer, '--no-conf', '--continue=true', '--file-allocation=none', '--auto-file-renaming=false',
                     '--show-console-readout=false', '--summary-interval=15', '--connect-timeout=10', '--timeout=25',
                     '--max-tries=8', '--retry-wait=2', '-x16', '-s16', '-k1M', f'--checksum=sha-256={digest}',
                     '-d', str(cache), '-o', partial.name, url],
                    check=True, timeout=timeout,
                )
            except subprocess.CalledProcessError as error:
                if error.returncode == 32:  # aria2 rejected the checksum.
                    partial.unlink(missing_ok=True)
                    partial.with_suffix('.aria2-part.aria2').unlink(missing_ok=True)
                raise
        else:
            partial = result.with_suffix('.part')
            if not (release and partial.is_file() and file_digest(partial, algorithm) == digest):
                curl_download(url, partial, timeout, resume=release)
        if file_digest(partial, algorithm) != digest:
            partial.unlink()
            label = 'SHA-256' if algorithm == 'sha256' else 'SHA3-256'
            raise ValueError(f'{label} mismatch for {url}')
        partial.replace(result)
    return result


def fetch_source(commit, cache, expected=None):
    if not re.fullmatch('[a-f0-9]{40}', commit):
        raise ValueError(f'Invalid source commit {commit}')
    url = f'https://codeload.github.com/stablyai/orca/tar.gz/{commit}'
    if expected is not None:
        return fetch_pinned(url, expected, cache, timeout=600)
    cache.mkdir(parents=True, exist_ok=True)
    identity = cache / (commit + '.source')
    with (cache / (commit + '.lock')).open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        if identity.is_file():
            return fetch_pinned(url, identity.read_text().strip(), cache, timeout=600)
        with tempfile.TemporaryDirectory(dir=cache) as work:
            archive = Path(work) / 'source.tar.gz'
            curl_download(url, archive, 600)
            subprocess.run(['tar', '-tzf', archive], stdout=subprocess.DEVNULL, check=True, timeout=120)
            digest = file_digest(archive)
            result = cache / digest
            archive.replace(result)
            marker = Path(work) / 'source.sha256'
            marker.write_text(digest + '\n')
            marker.replace(identity)
            return result
