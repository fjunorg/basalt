#!/usr/bin/env python3
"""
check-embed-hash：前端源码 → 产物 hash 一致性守护。

Basalt 的 console/dist 产物是 go:embed 嵌入物（单二进制交付）。
开发者改了 console/src 里的 Vue/TS/CSS 后如果忘了 `npm run build`，
go build 会静默嵌入过期产物——本脚本在 CI + make build 前置校验：
  1. 计算 console/src 下所有源码文件的 hash 并落盘到 .src-hash
  2. 计算 console/dist 下所有产物文件的 hash 并落盘到 .dist-hash
  3. 两者独立比对上一次记录：dist hash 变了但 src hash 没变 → 正常
     （独立触发 npm build）；src hash 变了但 dist hash 没变 → **阻断**
     （意味着源码有改动但未重 build）；都没变或都变 → 通过。

首次运行：两条 hash 都不存在 → 只写入并输出 PASS（首次无基线）。

退出码：0 = 通过，1 = 阻断。
用法：scripts/check-embed-hash.py（或 make check-embed-hash）
"""

from __future__ import annotations

import hashlib
import os
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
CONSOLE = REPO / "console"
SRC_HASH = CONSOLE / ".src-hash"
DIST_HASH = CONSOLE / ".dist-hash"

# 参与计算的前端源码/产物扩展名
SRC_EXTS = {".vue", ".ts", ".tsx", ".js", ".jsx", ".css", ".scss", ".html", ".json"}
DIST_EXTS = {".js", ".css", ".html", ".map", ".svg", ".png", ".ico", ".woff", ".woff2", ".ttf"}

SRC_EXCLUDE = {"node_modules", "dist", ".git", ".idea", ".vscode"}
DIST_EXCLUDE = {".DS_Store"}


def _hash_file(p: Path) -> str:
    h = hashlib.sha256()
    try:
        with open(p, "rb") as f:
            for chunk in iter(lambda: f.read(1 << 16), b""):
                h.update(chunk)
    except OSError as e:
        return f"ERR:{e}"
    return h.hexdigest()


def dir_hash(root: Path, exts: set[str], exclude: set[str]) -> tuple[str, int]:
    """递归收集文件 → {relpath: sha256} → 合并成单一 hash 字符串。"""
    if not root.is_dir():
        return "", 0
    files: list[tuple[str, str]] = []
    for dp, dns, fns in os.walk(root):
        dns[:] = [d for d in dns if d not in exclude]
        for fn in fns:
            p = Path(dp) / fn
            if p.suffix.lower() not in exts:
                continue
            rel = p.relative_to(root).as_posix()
            if any(x in rel for x in exclude):
                continue
            files.append((rel, _hash_file(p)))
    files.sort()  # 确定序
    joined = "\n".join(f"{r}={h}" for r, h in files)
    return hashlib.sha256(joined.encode()).hexdigest(), len(files)


def load(path: Path) -> str:
    if not path.exists():
        return ""
    try:
        return path.read_text().strip()
    except OSError:
        return ""


def save(path: Path, s: str) -> None:
    path.write_text(s)


def main() -> int:
    src_root = CONSOLE / "src"
    dist_root = CONSOLE / "dist"

    # 1. dist 是否存在
    if not dist_root.is_dir():
        print("✗ console/dist 不存在 → 请先 cd console && npm run build")
        return 1

    # 2. 计算当前 hash
    src_cur, n_src = dir_hash(src_root, SRC_EXTS, SRC_EXCLUDE)
    dist_cur, n_dist = dir_hash(dist_root, DIST_EXTS, DIST_EXCLUDE)

    if n_src == 0:
        print("✗ console/src 目录下无源码文件（检查路径是否对）")
        return 1
    if n_dist == 0:
        print("✗ console/dist 目录下无产物 → 请先 npm run build")
        return 1

    src_prev = load(SRC_HASH)
    dist_prev = load(DIST_HASH)

    # 3. 三种场景判断
    first_run = src_prev == "" and dist_prev == ""
    if first_run:
        save(SRC_HASH, src_cur)
        save(DIST_HASH, dist_cur)
        print(f"首次建立基线：src {n_src} 个文件 → dist {n_dist} 个产物")
        print("✓ embed hash 校验通过（首次无历史，已写入 .src-hash / .dist-hash）")
        return 0

    src_changed = src_cur != src_prev
    dist_changed = dist_cur != dist_prev

    print(f"src  {n_src:4d} 文件  {'✓' if not src_changed else '✗ 已变更'}")
    print(f"dist {n_dist:4d} 产物  {'✓' if not dist_changed else '✗ 已变更'}")

    # 核心逻辑：src 变了 → dist 必须也变（意味着有人改了源码但忘了 npm build）
    if src_changed and not dist_changed:
        print()
        print("✗ 阻断：前端源码有改动但 dist 产物未更新！")
        print("  → 请执行 cd console && npm run build 后再 go build")
        print("  → 若确实不想触发 rebuild（例如只改了无关文档），可手动删除")
        print("    console/.dist-hash 让脚本在下次 build 时重写基线")
        return 1

    # src 没变、dist 变了 → OK（独立触发了 npm build）
    # 都没变 → OK
    # 都变了 → OK（正常开发流程）

    save(SRC_HASH, src_cur)
    save(DIST_HASH, dist_cur)
    print("✓ embed hash 校验通过（dist 与 src 同步）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
