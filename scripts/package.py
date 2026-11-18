#!/usr/bin/env python3
"""Cross-package one immutable Lithe Tomcat plugin per Go target.

Only source files and the freshly compiled backend are included. The backend
binary is built for the selected target, never copied from a previous package.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import zipfile

ROOT = Path(__file__).resolve().parent.parent
PLUGIN_ID = 'io.github.lithe-idea.tomcat'
TARGETS = {
    'linux-amd64': ('linux', 'amd64'),
    'linux-arm64': ('linux', 'arm64'),
    'darwin-amd64': ('darwin', 'amd64'),
    'darwin-arm64': ('darwin', 'arm64'),
    'windows-amd64': ('windows', 'amd64'),
}


def build_one(target):
    goos, goarch = TARGETS[target]
    out = ROOT / '.dist'
    out.mkdir(exist_ok=True)
    exe = 'lithe-tomcat-backend.exe' if goos == 'windows' else 'lithe-tomcat-backend'
    binary = out / 'bin' / target / exe
    binary.parent.mkdir(parents=True, exist_ok=True)
    env = {**os.environ, 'GOOS': goos, 'GOARCH': goarch, 'CGO_ENABLED': '0'}
    subprocess.run(['go', 'build', '-trimpath', '-ldflags=-s -w', '-o', str(binary), './cmd/tomcat-backend'], cwd=ROOT, env=env, check=True)

    manifest = json.loads((ROOT / 'plugin.json').read_text(encoding='utf-8'))
    assert manifest['id'] == PLUGIN_ID
    assert manifest['backend']['launch']['kind'] == 'packaged'
    manifest['backend']['launch']['path'] = 'bin/' + exe
    contents = {
        'plugin.json': (json.dumps(manifest, ensure_ascii=False, indent=2) + '\n').encode('utf-8'),
        'LICENSE': (ROOT / 'LICENSE').read_bytes(),
        'README.md': (ROOT / 'README.md').read_bytes(),
        'ui/index.html': (ROOT / 'ui/index.html').read_bytes(),
        'ui/style.css': (ROOT / 'ui/style.css').read_bytes(),
        'ui/app.js': (ROOT / 'ui/app.js').read_bytes(),
        'bin/' + exe: binary.read_bytes(),
    }
    archive = out / f'lithe-plugin-tomcat-{manifest["version"]}-{target}.zip'
    # Archive paths include the plugin directory (the installer expects it).
    with zipfile.ZipFile(archive, 'w', compression=zipfile.ZIP_DEFLATED, compresslevel=9) as z:
        for rel, content in sorted(contents.items()):
            name = PLUGIN_ID + '/' + rel
            entry = zipfile.ZipInfo(name, date_time=(1980, 1, 1, 0, 0, 0))
            entry.compress_type = zipfile.ZIP_DEFLATED
            entry.external_attr = (0o100755 if rel.startswith('bin/') else 0o100644) << 16
            z.writestr(entry, content, compress_type=zipfile.ZIP_DEFLATED, compresslevel=9)
    check_archive(archive)
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    archive.with_suffix(archive.suffix + '.sha256').write_text(digest + '  ' + archive.name + '\n', encoding='utf-8')
    print(f'{archive}  {digest}')
    return archive


def check_archive(archive):
    with zipfile.ZipFile(archive) as z:
        names = z.namelist()
        assert len(names) == 7 and len(names) == len(set(names))
        assert all(n.startswith(PLUGIN_ID + '/') and '/..' not in n for n in names)
        assert not any((m.external_attr >> 16) & 0o170000 == 0o120000 for m in z.infolist())
        manifest = json.loads(z.read(PLUGIN_ID + '/plugin.json'))
        backend = PLUGIN_ID + '/' + manifest['backend']['launch']['path']
        assert backend in names
        exe = z.read(backend)
        assert len(exe) > 100 * 1024
        assert (exe[:2] == b'MZ') if archive.name.endswith('windows-amd64.zip') else (exe[:4] in {b'\x7fELF', b'\xcf\xfa\xed\xfe', b'\xfe\xed\xfa\xcf', b'\xca\xfe\xba\xbe'})
        html = z.read(PLUGIN_ID + '/ui/index.html')
        assert b'app.js' in html and b'style.css' in html


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--target', choices=TARGETS)
    parser.add_argument('--all', action='store_true')
    args = parser.parse_args()
    if args.all == bool(args.target):
        parser.error('specify exactly one of --target or --all')
    for target in TARGETS if args.all else [args.target]:
        build_one(target)


if __name__ == '__main__':
    main()
