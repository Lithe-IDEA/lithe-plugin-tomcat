"""Archive layout and compiled-binary protocol smoke tests.

This is deliberately not a native Lithe or real Apache Tomcat e2e test.
"""
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
import zipfile

ROOT = Path(__file__).resolve().parent.parent
DIST = ROOT / '.dist'
ID = 'io.github.lithe-idea.tomcat'
TARGETS = ('linux-amd64', 'linux-arm64', 'darwin-amd64', 'darwin-arm64', 'windows-amd64')


class PluginArchiveTests(unittest.TestCase):
    def test_all_platform_archives_are_structurally_valid(self):
        for target in TARGETS:
            with self.subTest(target=target):
                archive = DIST / f'lithe-plugin-tomcat-0.2.0-{target}.zip'
                self.assertTrue(archive.is_file(), archive)
                with zipfile.ZipFile(archive) as z:
                    self.assertEqual(len(z.namelist()), 7)
                    prefix = ID + '/'
                    self.assertTrue(all(n.startswith(prefix) for n in z.namelist()))
                    manifest = json.loads(z.read(prefix + 'plugin.json'))
                    self.assertEqual(manifest['id'], ID)
                    self.assertEqual(manifest['requires'], {'backend': 0, 'ui': 0})
                    self.assertEqual(manifest['requiresTools'], ['java'])
                    self.assertEqual(set(manifest['activationEvents']), {f'onView:{ID}.panel'})
                    self.assertEqual(manifest['permissions'], ['backend.launch', 'ui.contribute', 'ui.web', 'workspace.metadata', 'run.start'])
                    exe = 'bin/lithe-tomcat-backend.exe' if target.startswith('windows') else 'bin/lithe-tomcat-backend'
                    self.assertEqual(manifest['backend']['launch']['path'], exe)
                    self.assertIn(prefix + exe, z.namelist())
                    self.assertIn(b'<script src="app.js" defer>', z.read(prefix + 'ui/index.html'))
                    self.assertEqual((z.getinfo(prefix + exe).external_attr >> 16) & 0o111, 0o111)
                    for item in z.infolist():
                        self.assertEqual(item.date_time, (1980, 1, 1, 0, 0, 0))
                check = archive.with_suffix('.zip.sha256').read_text().split()[0]
                self.assertEqual(check, hashlib.sha256(archive.read_bytes()).hexdigest())

    def test_linux_compiled_binary_negotiates_its_functions(self):
        archive = DIST / 'lithe-plugin-tomcat-0.2.0-linux-amd64.zip'
        with tempfile.TemporaryDirectory() as tmp:
            with zipfile.ZipFile(archive) as z:
                z.extractall(tmp)
                relative = f'{ID}/bin/lithe-tomcat-backend'
                binary = Path(tmp) / relative
                binary.chmod(0o755)
            process = subprocess.Popen([str(binary)], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            try:
                self.write_frame(process.stdin, {'jsonrpc': '2.0', 'id': 1, 'method': 'initialize', 'params': {'capabilities': {'experimental': {'litheLogging': {'versions': [0]}, 'litheFunctions': {'versions': [0]}}}}})
                answer = self.read_frame(process.stdout)
                self.assertEqual(answer['id'], 1)
                caps = answer['result']['capabilities']['experimental']
                self.assertEqual(caps['litheFunctions']['version'], 0)
                function_ids = {a['id'] for a in caps['litheFunctions']['functions']}
                self.assertEqual(function_ids, {'tomcat.get', 'tomcat.save', 'tomcat.inspect', 'tomcat.discover', 'tomcat.planDeploy', 'tomcat.deploy', 'tomcat.start', 'tomcat.stop'})
                self.write_frame(process.stdin, {'jsonrpc': '2.0', 'id': 2, 'method': 'shutdown', 'params': {}})
                reply = self.read_frame(process.stdout)
                self.assertEqual(reply['id'], 2)
                self.write_frame(process.stdin, {'jsonrpc': '2.0', 'method': 'exit'})
                process.stdin.close()
                self.assertEqual(process.wait(timeout=5), 0)
                self.assertEqual(process.stderr.read(), b'')
            finally:
                if process.poll() is None:
                    process.kill()
                    process.wait(timeout=5)
                if process.stdin and not process.stdin.closed:
                    process.stdin.close()
                if process.stdout:
                    process.stdout.close()
                if process.stderr:
                    process.stderr.close()

    def test_linux_package_is_reproducible(self):
        archive = DIST / 'lithe-plugin-tomcat-0.2.0-linux-amd64.zip'
        first = hashlib.sha256(archive.read_bytes()).hexdigest()
        subprocess.run(['python3', 'scripts/package.py', '--target', 'linux-amd64'], cwd=ROOT, check=True, capture_output=True)
        second = hashlib.sha256(archive.read_bytes()).hexdigest()
        self.assertEqual(first, second)

    @staticmethod
    def write_frame(writer, msg):
        content = json.dumps(msg, ensure_ascii=False).encode('utf-8')
        writer.write(f'Content-Length: {len(content)}\r\n\r\n'.encode('ascii') + content)
        writer.flush()

    @staticmethod
    def read_frame(reader):
        length = None
        while True:
            line = reader.readline()
            if not line:
                raise EOFError('backend process closed before frame')
            if line == b'\r\n':
                break
            if line.startswith(b'Content-Length: '):
                length = int(line.split(b': ', 1)[1].strip())
        if length is None or length < 0 or length > 4<<20:
            raise ValueError('bad frame length')
        return json.loads(reader.read(length))


if __name__ == '__main__':
    unittest.main()
