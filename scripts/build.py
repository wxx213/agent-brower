#!/usr/bin/env python3
"""Build a native binary with an embedded, pinned chrome-headless-shell archive."""
import argparse
import hashlib
import json
import os
import re
from pathlib import Path
import shutil
import struct
import subprocess
import tarfile
import urllib.request
import zipfile

ROOT=Path(__file__).resolve().parents[1]
TARGETS={'linux-amd64':('linux','amd64','linux64'), 'darwin-amd64':('darwin','amd64','mac-x64'), 'darwin-arm64':('darwin','arm64','mac-arm64')}

def main():
    p=argparse.ArgumentParser()
    p.add_argument('--target',choices=TARGETS,required=True)
    p.add_argument('--version',default='0.1.0',help='release version without v prefix')
    p.add_argument('--locked-browser',action='store_true',help='use release/browsers.json')
    p.add_argument('--browser-zip',type=Path,help='cached official archive; requires --sha256')
    p.add_argument('--browser-dir',type=Path,help='existing chrome-headless-shell directory (offline build)')
    p.add_argument('--chrome-version',help='pinned Chrome for Testing version to download')
    p.add_argument('--sha256',help='required checksum for downloaded Chrome archive')
    a=p.parse_args();goos,goarch,platform=TARGETS[a.target]
    if a.locked_browser:
        if a.browser_dir or a.chrome_version or a.sha256:raise SystemExit('--locked-browser cannot override pinned browser settings')
        lock=json.loads((ROOT/'release/browsers.json').read_text())
        a.chrome_version=lock['chrome_version'];a.sha256=lock['browsers'][a.target]['sha256']
    if not re.fullmatch(r'(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)',a.version):raise SystemExit('version must be X.Y.Z')
    if a.browser_dir and a.browser_zip:raise SystemExit('choose one browser source')
    asset=ROOT/'internal/assets/browser/browser.zip'
    cache=ROOT/'.cache';cache.mkdir(exist_ok=True)
    if asset.exists():raise SystemExit('browser.zip already exists; another build may be running')
    try:
        if a.browser_dir:
            source=a.browser_dir.resolve()
            exes=list(source.rglob('chrome-headless-shell'))
            if not exes:raise SystemExit('no chrome-headless-shell executable found')
            # The executable and its sibling resources are the complete runtime.
            source=exes[0].parent
            with zipfile.ZipFile(asset,'w',zipfile.ZIP_DEFLATED,compresslevel=6) as z:
                for f in sorted(source.rglob('*')):
                    if f.is_symlink():raise SystemExit(f'unsupported symlink: {f}')
                    if f.is_file():z.write(f,Path('chrome-headless-shell')/f.relative_to(source))
            provenance={'source':'local browser directory','directory_name':source.name}
        else:
            if not a.chrome_version or not a.sha256:raise SystemExit('provide --browser-dir OR --chrome-version and --sha256')
            url=f'https://storage.googleapis.com/chrome-for-testing-public/{a.chrome_version}/{platform}/chrome-headless-shell-{platform}.zip'
            if a.browser_zip:shutil.copyfile(a.browser_zip,asset)
            else:urllib.request.urlretrieve(url,asset)
            actual=hashlib.sha256(asset.read_bytes()).hexdigest()
            if actual!=a.sha256:raise SystemExit(f'checksum mismatch: {actual}')
            provenance={'source':url,'chrome_version':a.chrome_version}
        digest=hashlib.sha256(asset.read_bytes()).hexdigest()
        with zipfile.ZipFile(asset) as z:
            candidates=[n for n in z.namelist() if Path(n).name=='chrome-headless-shell']
            if len(candidates)!=1:raise SystemExit('archive must contain exactly one browser executable')
            with z.open(candidates[0]) as f:header=f.read(32)
            if goos=='linux':
                valid=header[:4]==b'\x7fELF' and struct.unpack_from('<H',header,18)[0]==62
            else:
                valid=header[:4]==b'\xcf\xfa\xed\xfe' and struct.unpack_from('<I',header,4)[0]==({'amd64':0x1000007,'arm64':0x100000c}[goarch])
            if not valid:raise SystemExit('browser executable architecture does not match target')
        out=ROOT/'dist'/a.target/'agent-web'
        if out.exists():shutil.rmtree(out)
        out.mkdir(parents=True,exist_ok=True)
        (out/'bin').mkdir(exist_ok=True)
        env={**os.environ,'GOOS':goos,'GOARCH':goarch,'CGO_ENABLED':'0','GOMODCACHE':str(cache/'gomod'),'GOCACHE':str(cache/'go-build')}
        subprocess.run(['go','build','-trimpath',f'-ldflags=-s -w -X agent-web/internal/app.Version={a.version}','-o',str(out/'bin/agent-web'),'./cmd/agent-web'],cwd=ROOT,env=env,check=True)
        shutil.copyfile(ROOT/'internal/assets/SKILL.md',out/'SKILL.md')
        shutil.copyfile(ROOT/'internal/assets/THIRD_PARTY_NOTICES.txt',out/'THIRD_PARTY_NOTICES.txt')
        manifest={'version':a.version,'target':a.target,'browser_sha256':digest,'provenance':provenance,'binary_sha256':hashlib.sha256((out/'bin/agent-web').read_bytes()).hexdigest()}
        (out/'BUILD.json').write_text(json.dumps(manifest,indent=2)+'\n')
        # Preserve all browser notices, in addition to those embedded with the runtime.
        with zipfile.ZipFile(asset) as z:
            for f in z.infolist():
                if 'LICENSE' in Path(f.filename).name.upper() or Path(f.filename).name=='ABOUT':
                    dst=out/'licenses'/Path(f.filename).name;dst.parent.mkdir(exist_ok=True);dst.write_bytes(z.read(f))
        license_dir=out/'licenses';license_dir.mkdir(exist_ok=True)
        for module in ['github.com/go-rod/rod@v0.116.2','github.com/ysmood/goob@v0.4.0','github.com/ysmood/gson@v0.7.3','github.com/ysmood/fetchup@v0.2.3','github.com/ysmood/leakless@v0.9.0','github.com/ysmood/got@v0.40.0']:
            src=cache/'gomod'/module/'LICENSE'
            if src.exists():shutil.copyfile(src,license_dir/(module.replace('/','_')+'.txt'))
        archive=ROOT/'dist'/f'agent-web-{a.version}-{a.target}.tar.gz'
        with tarfile.open(archive,'w:gz') as tar:tar.add(out,arcname='agent-web')
        print(json.dumps({'artifact':str(archive),'binary':str(out/'bin/agent-web'),'bytes':archive.stat().st_size,'sha256':hashlib.sha256(archive.read_bytes()).hexdigest()},indent=2))
    finally:asset.unlink(missing_ok=True)

if __name__=='__main__':main()
