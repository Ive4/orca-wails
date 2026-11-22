#!/usr/bin/env python3
"""Run a macOS Orca-Wails desktop smoke test in an existing Lume VM."""

import argparse
import json
import os
from pathlib import Path
import plistlib
import re
import shlex
import subprocess
import tempfile
import time


def run(command, *, timeout=30, **kwargs):
    return subprocess.run(command, check=True, capture_output=True, text=True, timeout=timeout, **kwargs).stdout


def wait_for(probe, timeout):
    deadline = time.monotonic() + timeout
    while True:
        try:
            return probe()
        except (subprocess.SubprocessError, ValueError, RuntimeError) as error:
            if time.monotonic() >= deadline:
                detail = getattr(error, 'stderr', '') or getattr(error, 'stdout', '') or str(error)
                raise RuntimeError(detail.strip()) from error
        time.sleep(1)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--app', type=Path, required=True, help='Orca-Wails smoke-test .app with its bundled Core')
    parser.add_argument('--vm', required=True, help='Existing Lume virtual machine name')
    parser.add_argument('--ssh', required=True, help='Guest SSH alias configured in the controller Orca Wails settings')
    parser.add_argument('--storage', type=Path)
    parser.add_argument('--lume', default='lume')
    parser.add_argument('--orca', default='orca', help='Orca CLI for the currently running controller app')
    parser.add_argument('--evidence', type=Path, default=Path('dist-core/lume-desktop-smoke'))
    args = parser.parse_args()
    if not re.fullmatch(r'[A-Za-z0-9_][A-Za-z0-9_.-]*', args.ssh):
        parser.error('--ssh must be a configured SSH alias')

    contents = args.app.resolve() / 'Contents'
    bundle = plistlib.loads((contents / 'Info.plist').read_bytes())['CFBundleIdentifier']
    if not bundle.startswith(('dev.orca-wails.smoke.', 'dev.orca-wails.verified.')):
        parser.error('use an isolated test bundle built with build-macos.sh --smoke-test')
    if not (contents / 'Resources/bootstrap-core.zip').is_file():
        parser.error('the app must include bootstrap-core.zip')
    subprocess.run(['codesign', '--verify', '--strict', args.app], check=True, timeout=30)
    args.evidence.mkdir(parents=True, exist_ok=True)
    evidence = Path(tempfile.mkdtemp(prefix='run-', dir=args.evidence)).resolve()

    def lume(*command):
        storage = ['--storage', str(args.storage.resolve())] if args.storage else []
        return [args.lume, *command, *storage]

    raw = run(lume('get', args.vm, '--format', 'json'))
    start = re.search(r'^\[\s*(?:\{|\])', raw, re.MULTILINE)
    if start is None or not (machines := json.loads(raw[start.start():])):
        parser.error('Lume did not find the VM in the selected storage')
    vm = machines[0]
    if vm['name'] != args.vm or vm['os'] != 'macOS':
        parser.error('the test requires an existing macOS VM')
    if vm['status'] != 'running':
        with (evidence / 'lume.log').open('wb') as log:
            subprocess.Popen(lume('run', args.vm, '--display', 'none'), start_new_session=True,
                             stdin=subprocess.DEVNULL, stdout=log, stderr=log)

    ssh_options = ['-o', 'BatchMode=yes', '-o', 'ConnectTimeout=5']

    def guest(command, timeout=30):
        return run(['/usr/bin/ssh', '-T', *ssh_options, args.ssh, command], timeout=timeout).strip()

    def computer(*command):
        env = {key: value for key, value in os.environ.items() if key not in
               ('ORCA_PAIRING_CODE', 'ORCA_ENVIRONMENT')}
        raw = run([args.orca, 'computer', *command, '--environment', 'local',
                   '--backend', 'cua-vm', '--json'], env=env, timeout=25)
        response = json.loads(raw)
        if not response.get('ok'):
            raise RuntimeError(response['error']['message'])
        return response['result']

    def guest_ready():
        owner, user, version = guest('stat -f %Su /dev/console; id -un; cua-driver --version').splitlines()
        if owner != user or not version.startswith('cua-driver '):
            raise RuntimeError('Log in to the guest desktop as the SSH user and verify cua-driver is on PATH')

    print(f'Waiting for {args.vm} through SSH alias {args.ssh}', flush=True)
    wait_for(guest_ready, 90)
    backend = computer('capabilities')
    if (backend.get('backend'), backend.get('provider')) != ('cua-vm', 'cua-driver'):
        raise RuntimeError('The controller did not select the guest Cua Driver')
    old_apps = computer('list-apps')['apps']
    if any(row.get('isRunning') and row.get('bundleId') == bundle for row in old_apps):
        raise RuntimeError('Close the previous instance of this smoke-test bundle before launching it again')

    remote = guest('mktemp -d /tmp/orca-wails-lume.XXXXXX')
    if not re.fullmatch(r'/tmp/orca-wails-lume\.[A-Za-z0-9]+', remote):
        raise RuntimeError('Unexpected guest test directory')
    home = guest('printf %s "$HOME"')
    if not home.startswith('/Users/') or '\n' in home:
        raise RuntimeError('Expected the home directory of a macOS desktop user')
    app = f'{home}/Applications/Orca-Wails Lume {remote.rsplit(".", 1)[1]}.app'
    profile = f'{remote}/profile'
    fixture = f'{remote}/Lume Desktop Fixture'
    with tempfile.TemporaryDirectory(prefix='orca-lume-archive-') as staging:
        archive = Path(staging) / 'app.zip'
        subprocess.run(['ditto', '-c', '-k', '--keepParent', args.app, archive], check=True, timeout=90)
        run(['/usr/bin/scp', *ssh_options, archive, f'{args.ssh}:{remote}/app.zip'], timeout=120)
    q = shlex.quote
    config = json.dumps({'open_window_on_start': True, 'notifications': False,
                         'macos_entries': {'menu_bar': False, 'notch': False}})
    guest(f'ditto -x -k {remote}/app.zip {remote} && mkdir -p {q(home + "/Applications")} && '
          f'test ! -e {q(app)} && mv {q(remote + "/" + args.app.name)} {q(app)} && '
          f'mkdir -m 700 {profile} && mkdir {q(fixture)} && printf %s {q(config)} > {profile}/config.json')
    guest(f'open -g -a {q(app)} --args --data-dir {profile}')
    print(f'Guest profile directory {profile}', flush=True)
    pid = None
    try:
        def find_app():
            for row in computer('list-apps')['apps']:
                if row.get('isRunning') and row.get('bundleId') == bundle and row.get('pid'):
                    return int(row['pid'])
            raise RuntimeError('The deployed app did not appear in the guest')

        pid = wait_for(find_app, 45)
        cli = f'{profile}/orca-data/cli/bin/orca'
        wait_for(lambda: guest(f'test -s {profile}/core/active.json'), 75)
        guest(f'{cli} repo add --path {q(fixture)} --kind folder --json')
        window = ['--app', f'pid:{pid}', '--no-screenshot']

        def state_with_project():
            state = computer('get-app-state', *window, '--max-elements', '800')
            tree = state['snapshot']['treeText']
            (evidence / 'desktop.json').write_text(json.dumps(state, indent=2, ensure_ascii=False) + '\n')
            if 'Lume Desktop Fixture' not in tree:
                # First-run dialogs temporarily hide the sidebar in the accessibility tree.
                close = re.search(r'^\s*(\d+) AXButton "Close" ', tree, re.MULTILINE)
                if close:
                    computer('click', *window, '--element-index', close.group(1))
                raise RuntimeError('The test project did not appear in the desktop window')
            return state

        state = wait_for(state_with_project, 75)
        tree = state['snapshot']['treeText']
        worktree = re.search(r'^\s*(\d+) AXStaticText ".*Lume Desktop Fixture', tree, re.MULTILINE)
        if not worktree:
            raise RuntimeError('The test project has no local workspace row')
        computer('click', *window, '--element-index', worktree.group(1))

        def terminal_input():
            state = computer('get-app-state', *window, '--max-elements', '800')
            tree = state['snapshot']['treeText']
            (evidence / 'terminal-ui.json').write_text(json.dumps(state, indent=2, ensure_ascii=False) + '\n')
            match = re.search(r'^\s*(\d+) AXTextArea "Terminal input"', tree, re.MULTILINE)
            if not match:
                close = re.search(r'^\s*(\d+) AXButton "Close" ', tree, re.MULTILINE)
                if close and 'Lume Desktop Fixture' not in tree:
                    computer('click', *window, '--element-index', close.group(1))
                raise RuntimeError('Opening the local workspace did not show a terminal')
            return match.group(1)

        computer('click', *window, '--element-index', wait_for(terminal_input, 45))
        for key in ('p', 'w', 'd', 'Return'):
            computer('press-key', *window, '--key', key)

        def terminal_result():
            result = json.loads(guest(f'{cli} terminal list --json'))['result']
            for terminal in result['terminals']:
                lines = (terminal.get('preview') or '').splitlines()
                if terminal['executionHostId'] == 'local' and any(
                    line in {fixture, '/private' + fixture} for line in lines
                ):
                    return terminal
            raise RuntimeError('The terminal did not report the fixture path under the local host')

        terminal = wait_for(terminal_result, 25)
        (evidence / 'terminal.json').write_text(json.dumps(terminal, indent=2) + '\n')
        print(f'Lume CUA desktop and local terminal passed. Evidence in {evidence}')
    finally:
        executable = f'{app}/Contents/MacOS/orca-wails'
        if pid is not None and guest(f'ps -p {pid} -o comm= || true') in {executable, '/private' + executable}:
            guest(f'kill -TERM {pid}')


if __name__ == '__main__':
    main()
