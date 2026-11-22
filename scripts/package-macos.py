#!/usr/bin/env python3
"""Build and verify the Apple Silicon app and publish its ZIP locally."""

import fcntl
from pathlib import Path
import subprocess
import sys
import tempfile

from upstream_cache import ROOT, file_digest


def main():
    output = ROOT / 'bin'
    output.mkdir(exist_ok=True)
    app = output / 'Orca-Wails.app'
    archive = output / 'Orca-Wails-macos-arm64.zip'

    with (output / '.package-macos.lock').open('a') as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise SystemExit('macOS 打包已在运行。')

        with tempfile.TemporaryDirectory(prefix='.macos-package-', dir=output) as directory:
            stage = Path(directory)
            version_file = stage / 'core-version.macos-arm64.json'
            version_file.write_bytes((ROOT / version_file.name).read_bytes())

            print('构建 Apple Silicon 应用和内核…', flush=True)
            subprocess.run(['/bin/bash', ROOT / 'scripts/build-macos.sh', '--download-orca',
                            '--version-file', version_file], cwd=ROOT, check=True)

            print('压缩应用并验证解压结果…', flush=True)
            candidate = stage / archive.name
            unpacked = stage / 'unpacked'
            subprocess.run(['/usr/bin/ditto', '-c', '-k', '--sequesterRsrc', '--keepParent',
                            app, candidate], check=True)
            subprocess.run(['/usr/bin/ditto', '-x', '-k', candidate, unpacked], check=True)

            restored = unpacked / app.name
            subprocess.run(['/usr/bin/codesign', '--verify', '--deep', '--strict', restored],
                           check=True)
            contents = restored / 'Contents'
            binary = contents / 'MacOS/orca-wails'
            arch = subprocess.check_output(['/usr/bin/lipo', '-archs', binary], text=True).strip()
            if arch != 'arm64':
                raise SystemExit(f'应用架构应为 arm64，实际为 {arch}。')
            subprocess.run([sys.executable, ROOT / 'scripts/verify-bun-macos.py',
                            '--core-dir', contents / 'Resources',
                            '--version-file', version_file], check=True)
            digest = file_digest(candidate)
            candidate.replace(archive)

        print(f'打包完成\nAPP  {app}\nZIP  {archive}\nSHA-256  {digest}', flush=True)


if __name__ == '__main__':
    main()
