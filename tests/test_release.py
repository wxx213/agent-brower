"""Release packaging and installer behavior, with local download fixtures only."""
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('prepare_release', ROOT / 'scripts/prepare_release.py')
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)

# Synthetic executable lets each platform branch run on any developer machine.
BINARY = b'''#!/bin/sh
set -eu
[ "$1" = install ] && [ "$2" = --dir ]
[ ! -e "$3" ]
mkdir -p "$3/bin"
cp "$0" "$3/bin/agent-web"
printf 'fixture skill' > "$3/SKILL.md"
'''


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.dist = self.root / 'dist'
        self.dist.mkdir()
        self.lock = json.loads((ROOT / 'release/browsers.json').read_text())
        for target in release.TARGETS:
            self.package(target)
        self.bin = self.root / 'commands'
        self.bin.mkdir()
        self.command('curl', '''#!/usr/bin/env python3
import os, shutil, sys
from pathlib import Path
args=sys.argv[1:]
url=next(a for a in args if a.startswith('https://'))
assert '/owner/repo/releases/download/v1.2.3/' in url
output=args[args.index('-o')+1]
shutil.copyfile(Path(os.environ['FIXTURE_DIST'])/url.rsplit('/',1)[-1],output)
''')
        self.env = {**os.environ, 'PATH': str(self.bin)+os.pathsep+os.environ['PATH'],
                    'FIXTURE_DIST': str(self.dist), 'HOME': str(self.root / 'home')}
        self.env.pop('CODEX_HOME', None)

    def command(self, name, content):
        p = self.bin / name
        p.write_text(content)
        p.chmod(0o755)

    def package(self, target, version='1.2.3'):
        manifest = {'version': version, 'target': target,
                    'browser_sha256': self.lock['browsers'][target]['sha256'],
                    'provenance': {'chrome_version': self.lock['chrome_version']},
                    'binary_sha256': hashlib.sha256(BINARY).hexdigest()}
        path = self.dist / f'agent-web-1.2.3-{target}.tar.gz'
        with tarfile.open(path, 'w:gz') as tar:
            files = {'bin/agent-web': BINARY, 'BUILD.json': json.dumps(manifest).encode(),
                     'SKILL.md': b'skill', 'THIRD_PARTY_NOTICES.txt': b'notices'}
            for name, data in files.items():
                info = tarfile.TarInfo('agent-web/' + name)
                info.size = len(data)
                info.mode = 0o755 if name == 'bin/agent-web' else 0o644
                tar.addfile(info, io.BytesIO(data))
        return path

    def generate(self):
        return release.prepare('v1.2.3', 'owner/repo', self.dist)

    def install(self, *args):
        return subprocess.run(['sh', str(self.dist / 'install.sh'), *args],
                              env=self.env, text=True, capture_output=True)

    def test_supported_platforms_and_destinations(self):
        self.generate()
        for index, (system, arch, args) in enumerate([
            ('Linux', 'x86_64', ['--agent', 'codex']),
            ('Darwin', 'x86_64', ['--agent', 'claude']),
            ('Darwin', 'arm64', ['--dir', str(self.root / 'custom skill')]),
        ]):
            with self.subTest(system=system, arch=arch):
                self.command('uname', f'#!/bin/sh\ncase "$1" in -s) echo {system};; -m) echo {arch};; esac\n')
                result = self.install(*args)
                self.assertEqual(result.returncode, 0, result.stderr)
        for folder in ['home/.codex/skills/agent-web', 'home/.claude/skills/agent-web', 'custom skill']:
            self.assertTrue((self.root / folder / 'SKILL.md').is_file())

    def test_checksum_failure_does_not_install(self):
        self.generate()
        for p in self.dist.glob('*.tar.gz'):
            p.write_bytes(b'corrupted download')
        dest = self.root / 'never-installed'
        result = self.install('--dir', str(dest))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('SHA-256', result.stderr)
        self.assertFalse(dest.exists())

    def test_existing_directory_is_preserved(self):
        self.generate()
        dest = self.root / 'existing'
        dest.mkdir()
        (dest / 'sentinel').write_text('keep')
        self.assertNotEqual(self.install('--dir', str(dest)).returncode, 0)
        self.assertEqual((dest / 'sentinel').read_text(), 'keep')

    def test_codex_home(self):
        self.generate()
        self.env['CODEX_HOME'] = str(self.root / 'codex home')
        result = self.install()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue((Path(self.env['CODEX_HOME']) / 'skills/agent-web/SKILL.md').is_file())

    def test_unsupported_platform(self):
        self.generate()
        self.command('uname', '#!/bin/sh\necho unsupported\n')
        self.assertNotEqual(self.install().returncode, 0)

    def test_release_notes_and_checksums(self):
        notes = self.generate()
        self.assertIn('https://github.com/owner/repo/releases/download/v1.2.3/install.sh', notes)
        self.assertIn('--agent codex', notes)
        self.assertIn('--agent claude', notes)
        for line in (self.dist / 'SHA256SUMS').read_text().splitlines():
            digest, name = line.split('  ')
            self.assertEqual(release.sha256(self.dist / name), digest)
        subprocess.run(['sh', '-n', str(self.dist / 'install.sh')], check=True)

    def test_rejects_wrong_version_or_missing_platform(self):
        self.package('linux-amd64', version='9.9.9')
        with self.assertRaises(ValueError):
            self.generate()
        self.package('linux-amd64').unlink()
        with self.assertRaises(FileNotFoundError):
            self.generate()

    def test_tag_and_repository_validation(self):
        for tag in ['main', 'v01.2.3', 'v1.2.3;echo bad', 'v1.2.3-beta', 'v1.2']:
            with self.assertRaises(ValueError):
                release.version_from_tag(tag)
        with self.assertRaises(ValueError):
            release.prepare('v1.2.3', 'owner/$(bad)', self.dist)


if __name__ == '__main__':
    unittest.main()
