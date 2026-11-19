#!/usr/bin/env python3
"""
check-tokens：前端 semi-tokens.css 单一来源校验 + Go/TS/Vue 硬编码反向扫描。

三层职责：
  1. token 抽取     —— 从 console/src/styles/semi-tokens.css 解析 --ape-* 真源
  2. 反向硬编码扫描  —— Go/TS/Vue/HTML 源码若出现主色/语义色硬编码 → 报位置
  3. 导入链校验     —— main.ts 是否按序引入 semi-tokens.css → ape-theme.css

阻断性问题 → 退出码 1（CI 可消费）；仅警告 → 退出码 0。

用法：scripts/check-tokens.py（或 make verify / make check-tokens）
"""

from __future__ import annotations

import os
import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
TOKENS_CSS = REPO / "console/src/styles/semi-tokens.css"
MAIN_TS = REPO / "console/src/main.ts"

# ------- 关键 token 集合：前端的品牌色/语义色，后端禁止硬编码 -------
# 只关心在 Go/HTML 中会影响观感的颜色；阴影/字体不扫（Go 不写 CSS）。
INTEREST_KEYS = (
    "primary",
    "primary-hover",
    "primary-light",
    "success",
    "success-light",
    "warning",
    "warning-light",
    "danger",
    "danger-light",
    "info",
    "info-light",
    "border",
    "border-light",
    "body-bg",
    "card-bg",
    "grad-from",
    "grad-to",
)

# 扫描文件扩展名
SCAN_EXTS = {".go", ".ts", ".vue", ".html", ".tpl", ".tmpl"}
# 扫描时的排除目录/文件子串
SCAN_EXCLUDE = (
    ".git/",
    "console/node_modules/",
    "console/dist/",
    "go.sum",
    "package-lock.json",
    "pnpm-lock.yaml",
    "semi-tokens.css",  # 定义处豁免
    "ape-theme.css",    # 使用了 var()，不直接写颜色
    "semi-interactions.css",
    "check-tokens.py",
)


def parse_tokens(css_text: str) -> tuple[dict[str, str], dict[str, str]]:
    """
    从 CSS 文本中抽取 --ape-* token 值。
    返回 (light_map, dark_map)。dark_map 取 html.dark 块内定义；若没有则为空。
    """
    token_re = re.compile(r"(--ape-[\w-]+)\s*:\s*([^;]+?)\s*;")

    light: dict[str, str] = {}
    dark: dict[str, str] = {}

    # 切规则块：用简单启发式—— :root { ... } 和 html.dark { ... }
    # 为避免误把嵌套选择器当独立块，只取顶层（缩进 0 级大括号对）
    # 这里用朴素正则抓两个常见块即可；CSS 文件人工维护、结构稳定。
    root_block = re.search(r":root\s*\{([^}]+)\}", css_text, re.DOTALL)
    dark_block = re.search(
        r"(?:html\.dark|\.dark)\s*\{([^}]+)\}", css_text, re.DOTALL
    )

    target = light if root_block else {}
    for m in token_re.finditer(root_block.group(1) if root_block else ""):
        target[m.group(1)] = m.group(2)

    if dark_block:
        for m in token_re.finditer(dark_block.group(1)):
            dark[m.group(1)] = m.group(2)

    return light, dark


def collect_colors(tokens: dict[str, str]) -> set[str]:
    """从 token 值里抽 hex 颜色（#xxx / #xxxxxx，大小写都要）。"""
    colors: set[str] = set()
    hex_re = re.compile(r"#[0-9a-fA-F]{3,8}")
    for v in tokens.values():
        for m in hex_re.findall(v):
            colors.add(m.lower())
            colors.add(m.upper())
    return colors


def scan_hardcoded(colors: set[str]) -> list[tuple[str, int, str]]:
    """
    反向扫描 Go/TS/Vue/HTML 文件中是否硬编码了品牌色/语义色。
    返回 [(file, line_no, match_text), ...]
    """
    hits: list[tuple[str, int, str]] = []
    if not colors:
        return hits

    # 预编译：任一中的颜色都要命中
    # 按长度降序排，避免 #fff 抢先匹配 #ffffff（虽然两者都是白，但这里我们关心品牌色）
    sorted_colors = sorted(colors, key=len, reverse=True)
    pattern = re.compile("|".join(re.escape(c) for c in sorted_colors))

    for root, dirs, files in os.walk(REPO):
        # 剪枝
        dirs[:] = [
            d for d in dirs
            if d not in {".git", "node_modules", "dist", "vendor", "bin"}
        ]
        for fname in files:
            path = Path(root) / fname
            rel = path.relative_to(REPO).as_posix()
            if any(x in rel for x in SCAN_EXCLUDE):
                continue
            if path.suffix.lower() not in SCAN_EXTS:
                continue
            try:
                text = path.read_text(encoding="utf-8", errors="ignore")
            except OSError:
                continue
            for i, line in enumerate(text.splitlines(), start=1):
                m = pattern.search(line)
                if m:
                    # 再过滤：排除纯注释里的文档性引用（// token: #5A67F5 这类说明）
                    stripped = line.strip()
                    if stripped.startswith("//") or stripped.startswith("# "):
                        continue
                    hits.append((rel, i, m.group(0)))
    return hits


def check_imports(main_ts: Path) -> list[tuple[int, str]]:
    """校验 main.ts 是否按序引入 semi-tokens → ape-theme → 交互。"""
    issues: list[tuple[int, str]] = []
    if not main_ts.exists():
        return [(1, f"main.ts 不存在：{main_ts}")]

    text = main_ts.read_text(encoding="utf-8")
    semi_idx = text.find("semi-tokens.css")
    theme_idx = text.find("ape-theme.css")
    inter_idx = text.find("semi-interactions.css")

    if semi_idx == -1:
        issues.append((1, "main.ts 缺少 semi-tokens.css 引入（token 链起点）"))
    if theme_idx == -1:
        issues.append((1, "main.ts 缺少 ape-theme.css 引入（观感层组件样式）"))
    if semi_idx != -1 and theme_idx != -1 and theme_idx < semi_idx:
        issues.append((2, "ape-theme.css 引入在 semi-tokens.css 之前，顺序颠倒"))
    if inter_idx != -1:
        if semi_idx != -1 and inter_idx < semi_idx:
            issues.append((2, "semi-interactions.css 引入在 semi-tokens.css 之前，顺序颠倒"))

    theme_css = REPO / "console/src/styles/ape-theme.css"
    if theme_css.exists():
        ttext = theme_css.read_text(encoding="utf-8")
        if "--ape-" not in ttext:
            issues.append((2, "ape-theme.css 未使用任何 --ape-* token，疑似与 token 体系脱钩"))

    return issues


def check_css_chain(tokens_css: Path) -> list[str]:
    """
    校验 semi-tokens.css 自身的三层映射没有循环引用。

    规则（严格度递减）：
      1. 🔴 --ape-* 引用 var()            → 硬错误（真源层必须是具体值）
      2. 🟡 --semi-*/--el-* 写死了一个颜色值，且这个值恰好就是同作用域内某个 --ape-* 的值
                                             → 警告（应该走 var(--ape-*) 链）
      3. ✅ --semi-*/--el-* 写死了一个颜色值，但该值不匹配任何 --ape-*
                                             → 放行（这是组件库自身的扩展阶梯色，如
                                               Element Plus 的 --el-color-primary-light-3/5/7/8）
    """
    issues: list[tuple[int, str]] = []  # (severity 1=fail 2=warn, msg)
    if not tokens_css.exists():
        return [(1, f"semi-tokens.css 不存在：{tokens_css}")]

    text = tokens_css.read_text(encoding="utf-8")

    # ---------- 规则 1：--ape-* 不能引用 var() ----------
    ape_var_re = re.compile(r"(--ape-[\w-]+)\s*:\s*var\(")
    for m in ape_var_re.finditer(text):
        issues.append((1, f"--ape-* 真源层引用了 var()：{m.group(1)}（必须是具体值）"))

    # ---------- 规则 2 & 3：别名层硬编码颜色 ----------
    # 按作用域分别处理：:root 和 html.dark 各自有一套 --ape-* 值
    def _check_scope(block_text: str, scope_label: str) -> list[tuple[int, str]]:
        scoped: list[tuple[int, str]] = []
        # 先抽本作用域内所有 --ape-* → value 映射
        ape_map: dict[str, str] = {}
        for m in re.finditer(r"(--ape-[\w-]+)\s*:\s*([^;]+?)\s*;", block_text):
            ape_map[m.group(1)] = m.group(2).strip()
        # 再抽每个 --semi-*/--el-* 赋值
        for m in re.finditer(r"(--(?:semi|el)-[\w-]+)\s*:\s*([^;]+?)\s*;", block_text):
            alias = m.group(1)
            val = m.group(2).strip()
            # 只关心值是颜色的（hex 或 rgb）
            if not re.match(r"(#[\da-fA-F]{3,8}|rgba?\()", val):
                continue
            if val.startswith("var("):
                continue  # 已经走 var 链了，好
            # 这个硬编码值是否匹配本作用域内某个 --ape-* 的具体值？
            # 比较做归一化：小写 + 去空格
            val_norm = re.sub(r"\s+", "", val).lower()
            matched_ape = None
            for akey, aval in ape_map.items():
                aval_norm = re.sub(r"\s+", "", aval).lower()
                if aval_norm == val_norm:
                    matched_ape = akey
                    break
            if matched_ape:
                scoped.append(
                    (2, f"[{scope_label}] {alias} 写死了 {val}，该值已存在于 {matched_ape} → 应改为 var({matched_ape})")
                )
            # 否则是组件库扩展色，放行
        return scoped

    root_block = re.search(r":root\s*\{([^}]+)\}", text, re.DOTALL)
    dark_block = re.search(r"(?:html\.dark|\.dark)\s*\{([^}]+)\}", text, re.DOTALL)
    if root_block:
        issues.extend(_check_scope(root_block.group(1), ":root"))
    if dark_block:
        issues.extend(_check_scope(dark_block.group(1), "html.dark"))

    return issues


def main() -> int:
    fail = 0
    warn = 0

    # 0. 前置文件存在
    if not TOKENS_CSS.exists():
        print("✗ console/src/styles/semi-tokens.css 缺失（token 单一来源不存在）", file=sys.stderr)
        return 1

    css_text = TOKENS_CSS.read_text(encoding="utf-8")

    # 1. 三层链校验
    print("── token CSS 链路校验 ──")
    chain_issues = check_css_chain(TOKENS_CSS)
    chain_ok = len(chain_issues) == 0
    for sev, msg in chain_issues:
        if sev == 1:
            print(f"  [✗] {msg}")
            fail += 1
        else:
            print(f"  [△] {msg}")
            warn += 1
    if chain_ok:
        print("  ✓ 三层映射结构完好（--ape-* 真源 / --semi-* --el-* 别名）")
    elif fail == 0:
        print(f"  ✓ 无阻断问题（{warn} 条建议性修复）")

    # 2. main.ts 导入链
    print("\n── main.ts 导入链校验 ──")
    imports_ok = True
    import_issues = check_imports(MAIN_TS)
    for sev, msg in import_issues:
        if sev == 1:
            print(f"  [✗] {msg}")
            fail += 1
            imports_ok = False
        else:
            print(f"  [△] {msg}")
            warn += 1
    if imports_ok:
        print("  ✓ main.ts 导入顺序正确（semi-tokens → ape-theme → semi-interactions）")

    # 3. 硬编码反向扫描
    print("\n── 硬编码反向扫描 ──")
    light_tokens, dark_tokens = parse_tokens(css_text)

    # 只关心真源 token 的 hex 颜色，作为"禁止硬编码列表"
    def _interest_tokens(tokens: dict[str, str]) -> dict[str, str]:
        return {
            k: v for k, v in tokens.items()
            if any(
                k == f"--ape-{s}" or k.endswith(f"-{s}")
                for s in INTEREST_KEYS
            )
        }

    light_colors = collect_colors(_interest_tokens(light_tokens))
    dark_colors = collect_colors(_interest_tokens(dark_tokens))
    all_colors = light_colors | dark_colors

    # 剔除纯黑/纯白/纯灰通用值（避免误伤 Go 里正常的 #000/#fff 等对比色）
    NOISE = {"#0", "#00", "#000", "#0000", "#000000", "#00000000",
             "#f", "#ff", "#fff", "#ffff", "#ffffff", "#ffffffff"}
    all_colors -= NOISE

    # 但品牌白 --ape-card-bg / --ape-nav-bg 里的 #ffffff 是刻意选的品牌色，必须保留在扫描列表
    for k, v in {**light_tokens, **dark_tokens}.items():
        if k in ("--ape-card-bg", "--ape-nav-bg", "--ape-body-bg"):
            for m in re.findall(r"#[0-9a-fA-F]{3,8}", v):
                all_colors.add(m.lower())
                all_colors.add(m.upper())

    hits = scan_hardcoded(all_colors)
    dedup_hits: list[tuple[str, int, str]] = []
    seen_pos: set[tuple[str, int]] = set()
    for fpath, ln, match in hits:
        key = (fpath, ln)
        if key in seen_pos:
            continue
        seen_pos.add(key)
        dedup_hits.append((fpath, ln, match))

    if dedup_hits:
        for fpath, ln, match in dedup_hits:
            print(f"  [✗] {fpath}:{ln} 硬编码了品牌色/语义色 {match} → 请改为 CSS 变量引用")
            fail += 1
    else:
        print("  ✓ Go/TS/Vue 零品牌色硬编码（全部走 CSS 变量）")

    # 4. 汇总
    print()
    summary = (
        f"真源 token：{len(light_tokens)} 个亮色 + {len(dark_tokens)} 个暗色；"
        f"反向扫描命中 {len(dedup_hits)} 处；建议性警告 {warn} 条"
    )
    print(summary)

    if fail:
        print(f"✗ 校验未通过：{fail} 个阻断性问题（修复后再提交）")
        return 1
    if warn:
        print(f"△ 校验通过（退出码 0），但有 {warn} 条建议性修复（可后续迭代）")
    else:
        print("✓ token 体系完整：单一来源 + 别名映射 + 零后端硬编码")
    return 0


if __name__ == "__main__":
    sys.exit(main())
