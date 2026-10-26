#!/usr/bin/env python3
"""把发布版本号写进仓库里所有声明版本的位置。

用法: python set-version.py 1.2.3

改动这些地方(发布流程里 Go 侧版本由 ldflags 注入, 不在这里改):
  1. build/config.yml          info.version            —— Wails 构建信息(单一真源)
  2. build/windows/info.json   四个版本字段(四段式)    —— 嵌入 exe 属性
  3. build/darwin|ios 的 Info.plist / Info.dev.plist    —— macOS / iOS 包版本
  4. build/linux/nfpm/nfpm.yaml version                 —— deb/rpm 包版本
  5. frontend/package.json 与 package-lock.json         —— 前端包版本
  (Android 的 versionName/versionCode 由 gradle 直接读 build/config.yml, 无需改动)

Windows 的 exe 属性只认四段式版本号(如 1.2.3.0), 三段式会被完全忽略,
所以这里统一补一段 0; 预发布后缀(-beta.1)只保留在 semver 字符串里。
"""

import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]

# macOS / iOS 的应用包 plist, 存在才改。
PLIST_FILES = (
    "build/darwin/Info.plist",
    "build/darwin/Info.dev.plist",
    "build/ios/Info.plist",
    "build/ios/Info.dev.plist",
)


def read(path: Path) -> str:
    return path.read_text(encoding="utf-8")


def write(path: Path, text: str) -> None:
    path.write_text(text, encoding="utf-8", newline="\n")


def four_part(version: str) -> str:
    """1.2.3 -> 1.2.3.0; 1.2 -> 1.2.0.0; 1.2.3-beta.1 -> 1.2.3.0"""
    base = re.split(r"[-+]", version, maxsplit=1)[0]
    parts = [p for p in base.split(".") if p != ""]
    while len(parts) < 4:
        parts.append("0")
    return ".".join(parts[:4])


def update_config(version: str) -> str:
    path = ROOT / "build" / "config.yml"
    text = read(path)
    new_text, count = re.subn(
        r'(?m)^(\s{2}version:\s*)"[^"]*"[ \t]*$',
        lambda m: f'{m.group(1)}"{version}"',
        text,
    )
    if count != 1:
        raise SystemExit(f"build/config.yml: 期望匹配 1 处 info.version, 实际 {count} 处")
    write(path, new_text)
    return f'build/config.yml  info.version -> "{version}"'


def update_info_json(version: str) -> str:
    path = ROOT / "build" / "windows" / "info.json"
    data = json.loads(read(path))
    ver4 = four_part(version)
    data["fixed"]["file_version"] = ver4
    data["fixed"]["product_version"] = ver4
    for locale in data["info"].values():
        locale["FileVersion"] = ver4
        locale["ProductVersion"] = ver4
    write(path, json.dumps(data, ensure_ascii=False, indent="\t") + "\n")
    return f"build/windows/info.json  四个版本字段 -> {ver4}"


def update_package_json(version: str) -> str:
    path = ROOT / "frontend" / "package.json"
    data = json.loads(read(path))
    data["version"] = version
    write(path, json.dumps(data, ensure_ascii=False, indent=2) + "\n")
    return f"frontend/package.json  version -> {version}"


def update_package_lock(version: str) -> str:
    """只改本包自己的两处 version(顶层 + packages[""]), 不碰依赖版本。"""
    path = ROOT / "frontend" / "package-lock.json"
    text = read(path)
    name = json.loads(text)["name"]
    pattern = re.compile(
        r'("name":\s*"' + re.escape(name) + r'",\s*\n\s*"version":\s*")[^"]*(")'
    )
    new_text, count = pattern.subn(lambda m: m.group(1) + version + m.group(2), text)
    if count == 0:
        return "frontend/package-lock.json  未找到本包版本字段, 跳过"
    write(path, new_text)
    return f"frontend/package-lock.json  本包 version -> {version} ({count} 处)"


def update_plists(version: str) -> str:
    """改 macOS / iOS 包里的 CFBundleShortVersionString 与 CFBundleVersion。"""
    results = []
    pattern = re.compile(
        r"(<key>CFBundle(?:ShortVersionString|Version)</key>\s*<string>)[^<]*(</string>)"
    )
    for rel in PLIST_FILES:
        path = ROOT / rel
        if not path.exists():
            continue
        text = read(path)
        new_text, count = pattern.subn(lambda m: m.group(1) + version + m.group(2), text)
        if count:
            write(path, new_text)
            results.append(f"{rel} ({count} 处)")
    if not results:
        return "Info.plist  没有需要改的文件, 跳过"
    return "Info.plist  版本号 -> " + ", ".join(results)


def update_nfpm(version: str) -> str:
    """改 Linux 打包配置里的 version(deb/rpm/AppImage 共用)。"""
    path = ROOT / "build" / "linux" / "nfpm" / "nfpm.yaml"
    if not path.exists():
        return "build/linux/nfpm/nfpm.yaml  不存在, 跳过"
    text = read(path)
    new_text, count = re.subn(
        r'(?m)^(version:\s*)"[^"]*"[ \t]*$',
        lambda m: f'{m.group(1)}"{version}"',
        text,
    )
    if count == 0:
        return "build/linux/nfpm/nfpm.yaml  未找到 version 字段, 跳过"
    write(path, new_text)
    return f'build/linux/nfpm/nfpm.yaml  version -> "{version}" ({count} 处)'


def main() -> int:
    # Windows 的 CI 里 stdout 默认是 cp1252，直接打印中文会 UnicodeEncodeError
    if hasattr(sys.stdout, "reconfigure"):
        sys.stdout.reconfigure(encoding="utf-8", errors="replace")

    if len(sys.argv) != 2:
        print(__doc__)
        return 2
    version = sys.argv[1].strip()
    version = version[1:] if version.startswith("v") else version
    if not re.fullmatch(r"\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.\-]+)?", version):
        raise SystemExit(f"版本号格式不对: {version!r}, 期望 1.2.3 或 1.2.3-beta.1")

    for line in (
        update_config(version),
        update_info_json(version),
        update_plists(version),
        update_nfpm(version),
        update_package_json(version),
        update_package_lock(version),
    ):
        print(line)

    print(f"\n版本号已写入: {version} (exe 属性用 {four_part(version)})")
    return 0


if __name__ == "__main__":
    sys.exit(main())
