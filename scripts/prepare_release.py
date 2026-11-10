#!/usr/bin/env python3
"""Validate release packages and generate a pinned installer and GitHub release notes."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import shlex
import tarfile

ROOT = Path(__file__).resolve().parents[1]
TARGETS = ('linux-amd64', 'darwin-amd64', 'darwin-arm64')
TAG_PATTERN = r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)'


def version_from_tag(tag):
    if not re.fullmatch(TAG_PATTERN, tag):
        raise ValueError('release tag must be vX.Y.Z (for example v0.1.0)')
    return tag[1:]


def sha256(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def render_installer(version, packages):
    cases = []
    for target, data in packages.items():
        if target not in TARGETS:
            raise ValueError('unsupported target')
        cases.append(f'    {target})\n        archive_url={shlex.quote(data["url"])}\n'
                     f'        archive_sha={shlex.quote(data["sha256"])} ;;')
    return ((ROOT / 'scripts/install.sh.in').read_text()
            .replace('@@VERSION@@', version).replace('@@PLATFORMS@@', '\n'.join(cases)))


def prepare(tag, repository, dist):
    version = version_from_tag(tag)
    if not re.fullmatch(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+', repository):
        raise ValueError('repository must be OWNER/REPO')
    lock = json.loads((ROOT / 'release/browsers.json').read_text())
    base = f'https://github.com/{repository}/releases/download/{tag}'
    packages, sums = {}, []
    for target in TARGETS:
        archive = dist / f'agent-web-{version}-{target}.tar.gz'
        with tarfile.open(archive, 'r:gz') as tar:
            manifest = json.load(tar.extractfile('agent-web/BUILD.json'))
            if (manifest['version'] != version or manifest['target'] != target
                    or manifest['browser_sha256'] != lock['browsers'][target]['sha256']
                    or manifest['provenance'].get('chrome_version') != lock['chrome_version']):
                raise ValueError(f'package does not match release version / browser lock: {target}')
            with tar.extractfile('agent-web/bin/agent-web') as binary:
                digest = hashlib.file_digest(binary, 'sha256').hexdigest()
            if digest != manifest['binary_sha256']:
                raise ValueError(f'binary checksum mismatch: {target}')
            for name in ('agent-web/SKILL.md', 'agent-web/THIRD_PARTY_NOTICES.txt'):
                if not tar.getmember(name).isfile():
                    raise ValueError(f'package missing {name}')
        digest = sha256(archive)
        packages[target] = {'url': f'{base}/{archive.name}', 'sha256': digest}
        sums.append(f'{digest}  {archive.name}')
    installer = dist / 'install.sh'
    installer.write_text(render_installer(version, packages))
    installer.chmod(0o755)
    sums.append(f'{sha256(installer)}  install.sh')
    (dist / 'SHA256SUMS').write_text('\n'.join(sums) + '\n')
    command = f'curl -fsSL {base}/install.sh | sh -s --'
    notes = f'''# Agent Web {tag}

无 UI 的 Agent 浏览器：一个原生二进制文件和一份 Skill，内置 Chrome Headless Shell {lock['chrome_version']}。

## 一键安装

自动识别 Linux x86_64、macOS Intel / Apple Silicon，下载对应安装包并校验 SHA-256。无需 Node.js、Python 或 sudo。以下命令适用于公开仓库。

**Codex**（默认安装到 `${{CODEX_HOME:-$HOME/.codex}}/skills/agent-web`）：

```sh
{command} --agent codex
```

**Claude Code**：

```sh
{command} --agent claude
```

**其他 Agent / 自定义目录**：

```sh
{command} --dir "$HOME/my-agent/skills/agent-web"
```

安装后重新加载 Agent 技能列表。安装器拒绝覆盖已有目录；升级时先用旧二进制执行 `stop`，备份旧 Skill 目录，再安装。账号数据保存在独立的 `AGENT_WEB_HOME` 中，不随 Skill 目录替换而删除。

## 手动安装与校验

下载对应 `.tar.gz` 和 `SHA256SUMS`，使用 `sha256sum`（Linux）或 `shasum -a 256`（macOS）核对归档摘要，解压后运行：

```sh
./agent-web/bin/agent-web install --dir "$HOME/.codex/skills/agent-web"
```

Linux 仍需要 Chromium 系统动态库；安装脚本不自动修改系统软件。参见[运行说明](https://github.com/{repository}/blob/{tag}/README.md#linux-服务器)和 [Dockerfile](https://github.com/{repository}/blob/{tag}/Dockerfile)。macOS 产物未做开发者签名和公证。

包含三个平台安装包、固定版本安装脚本与 SHA256SUMS。流水线在各平台运行 Go 检查与真实浏览器测试后才发布。
'''
    (dist / 'RELEASE_NOTES.md').write_text(notes)
    return notes


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--tag', required=True)
    parser.add_argument('--repository', required=True)
    parser.add_argument('--dist', type=Path, default=ROOT / 'dist/release')
    args = parser.parse_args()
    prepare(args.tag, args.repository, args.dist)
    print(f'Release files ready: {args.dist}')


if __name__ == '__main__':
    main()
