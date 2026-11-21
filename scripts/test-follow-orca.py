import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import runpy
import tempfile
import threading
import unittest

from upstream_cache import fetch_pinned


class CacheTests(unittest.TestCase):
    def test_cache_hit_works_offline_and_bad_bytes_are_never_published(self):
        data = b'official content'
        digest = hashlib.sha256(data).hexdigest()

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(200)
                self.end_headers()
                self.wfile.write(data if self.path == '/valid' else b'bad content')

            def log_message(self, *_):
                pass

        with tempfile.TemporaryDirectory() as directory, ThreadingHTTPServer(('127.0.0.1', 0), Handler) as server:
            cache = Path(directory)
            url = f'http://127.0.0.1:{server.server_port}'
            thread = threading.Thread(target=server.serve_forever)
            thread.start()
            try:
                with self.assertRaisesRegex(ValueError, 'SHA-256 mismatch'):
                    fetch_pinned(url + '/bad', digest, cache, timeout=3)
                self.assertFalse((cache / digest).exists())
                self.assertEqual(fetch_pinned(url + '/valid', digest, cache, timeout=3).read_bytes(), data)
            finally:
                server.shutdown()
                thread.join()
            server.server_close()
            self.assertEqual(fetch_pinned(url + '/valid', digest, cache, timeout=3).read_bytes(), data)

    def test_curl_restarts_when_server_ignores_range(self):
        from upstream_cache import curl_download

        data = b'complete release asset'
        ranges = []

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                ranges.append(self.headers.get('Range'))
                self.send_response(200)
                self.send_header('Content-Length', str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def log_message(self, *_):
                pass

        with tempfile.TemporaryDirectory() as directory, ThreadingHTTPServer(('127.0.0.1', 0), Handler) as server:
            partial = Path(directory) / 'asset.part'
            partial.write_bytes(data[:3])
            thread = threading.Thread(target=server.serve_forever)
            thread.start()
            try:
                curl_download(f'http://127.0.0.1:{server.server_port}/asset', partial, 3, resume=True)
            finally:
                server.shutdown()
                thread.join()
            self.assertEqual(ranges, ['bytes=3-', None])
            self.assertEqual(partial.read_bytes(), data)


class ReleaseTests(unittest.TestCase):
    def test_asset_requires_github_digest_and_uses_canonical_release_url(self):
        release_asset = runpy.run_path(str(Path(__file__).with_name('follow-orca.py')))['release_asset']
        digest = 'a' * 64
        asset = {'name': 'orca-windows-setup.exe', 'digest': 'sha256:' + digest,
                 'browser_download_url': 'https://example.com/untrusted.exe'}
        release = {'tag_name': 'v1.4.268', 'assets': [asset]}
        self.assertEqual(release_asset(release, asset['name']), (
            'https://github.com/stablyai/orca/releases/download/v1.4.268/orca-windows-setup.exe', digest,
        ))
        asset['digest'] = None
        with self.assertRaisesRegex(ValueError, 'SHA-256'):
            release_asset(release, asset['name'])

    def test_rerun_preserves_windows_validation_for_identical_candidate(self):
        write_candidate = runpy.run_path(str(Path(__file__).with_name('follow-orca.py')))['write_candidate']
        active = {'version': 'v1.4.268', 'upstream': {'sha256': 'one'}}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'core-version.json'
            write_candidate(path, {**active, 'validation': {'status': 'verified'}})
            write_candidate(path, {**active, 'validation': {'status': 'pending'}})
            self.assertEqual(json.loads(path.read_text())['validation']['status'], 'verified')
            write_candidate(path, {**active, 'upstream': {'sha256': 'two'}, 'validation': {'status': 'pending'}})
            self.assertEqual(json.loads(path.read_text())['validation']['status'], 'pending')


if __name__ == '__main__':
    unittest.main()
