#!/usr/bin/env bash
# scan_licenses：依赖许可证白名单扫描（go + console）。
#
# 原理：go list -m all 的每个模块在其 GOMODCACHE 目录读 LICENSE 文件
# 首部判别许可类型；console 依赖读 node_modules/*/package.json 的
# license 字段。命中非白名单或许可证无法识别 → 退出码 1（CI 可消费）。
#
# 用法：scripts/scan_licenses.sh（或 make scan-licenses）
# 兼容 macOS bash 3.2（无关联数组）。
set -u

cd "$(dirname "$0")/.."

# 宽容许可白名单（OSI 认可且商用无义务冲突）。
ALLOWED="MIT Apache-2.0 BSD-2-Clause BSD-3-Clause ISC BSD Unlicense Zlib MPL-2.0 PostgreSQL Python-2.0 Go-BSD 0BSD"

fail=0
unknown=0
go_total=0
node_total=0
DIST="$(mktemp -t licdist)"
trap 'rm -f "$DIST"' EXIT

classify_text() { # $1 = LICENSE 文本首部 → 许可证标识
	local t="$1"
	case "$t" in
	*"Apache License"*) echo "Apache-2.0" ;;
	*"MIT License"*|*"Permission is hereby granted, free of charge"*) echo "MIT" ;;
	*"GNU LESSER GENERAL PUBLIC LICENSE"*) echo "LGPL" ;;
	*"GNU GENERAL PUBLIC LICENSE"*) echo "GPL" ;;
	*"Mozilla Public License"*|*"MPL/2.0"*) echo "MPL-2.0" ;;
	*"Regents of the University of California"*) echo "BSD-3-Clause" ;;
	*"Redistribution and use in source and binary forms"*)
		if echo "$t" | grep -q "Neither the name"; then
			echo "BSD-3-Clause"
		else
			echo "BSD-2-Clause"
		fi ;;
	*"ISC License"*|*"Permission to use, copy, modify, and/or distribute this software"*) echo "ISC" ;;
	*"zlib/libpng"*) echo "Zlib" ;;
	*"POSTGRESQL"*) echo "PostgreSQL" ;;
	*"PSF"*) echo "Python-2.0" ;;
	*"GO LICENSE"*|*"copyright notice and this permission notice"*) echo "Go-BSD" ;;
	*"unlicense.org/publicdomain"*) echo "Unlicense" ;;
	*) echo "UNKNOWN" ;;
	esac
}

allowed() {
	for a in $ALLOWED; do
		[ "$1" = "$a" ] && return 0
	done
	return 1
}

# modcache_escape：模块路径按 modcache 约定转义（大写字母 → !小写）。
# awk 实现保证 BSD/GNU sed 双兼容。
modcache_escape() {
	echo "$1" | awk '{ out = ""; for (i = 1; i <= length($0); i++) { c = substr($0, i, 1); if (c >= "A" && c <= "Z") out = out "!" tolower(c); else out = out c } print out }'
}

# ---------- Go 依赖 ----------
CACHE="$(go env GOMODCACHE)"
while read -r mod ver; do
	go_total=$((go_total + 1))
	esc="$(modcache_escape "$mod")"
	dir="$CACHE/$esc@$ver"
	if [ ! -d "$dir" ]; then
		# 懒下载模块仅有 .mod 元数据：优先回退同模块任意已有版本目录
		#（同模块许可证跨版本不变），否则补拉该版本 zip。
		alt="$(ls -d "$CACHE/$esc"@* 2>/dev/null | head -1)"
		if [ -n "$alt" ]; then
			dir="$alt"
		else
			go mod download "$mod@$ver" >/dev/null 2>&1 || true
		fi
	fi
	lic="UNKNOWN"
	for f in "$dir"/LICENSE "$dir"/LICENSE.txt "$dir"/LICENSE.md "$dir"/COPYING "$dir"/COPYRIGHT; do
		if [ -f "$f" ]; then
			lic="$(classify_text "$(head -c 4000 "$f")")"
			break
		fi
	done
	echo "$lic" >> "$DIST"
	if [ "$lic" = "UNKNOWN" ]; then
		unknown=$((unknown + 1))
		echo "  [?] $mod@$ver （modcache 无 LICENSE 或无法识别）"
	elif ! allowed "$lic"; then
		fail=$((fail + 1))
		echo "  [✗] $mod@$ver → $lic（不在白名单）"
	fi
done < <(go list -m all 2>/dev/null | tail -n +2 | awk '$1 !~ /^github.com\/basalt-go/ {print $1, $2}')

# ---------- console 依赖（node_modules package.json license 字段） ----------
if [ -d console/node_modules ]; then
	for pkg in $(python3 -c '
import json
p = json.load(open("console/package.json"))
print(" ".join(list(p.get("dependencies", {})) + list(p.get("devDependencies", {}))))'); do
		pj="console/node_modules/$pkg/package.json"
		if [ -f "$pj" ]; then
			node_total=$((node_total + 1))
			lic=$(python3 -c "
import json
d = json.load(open('$pj'))
l = d.get('license', 'UNKNOWN')
if isinstance(l, list): l = 'OR-'.join(l)
print(l)" 2>/dev/null || echo UNKNOWN)
			echo "$lic" >> "$DIST"
			if ! allowed "$lic"; then
				fail=$((fail + 1))
				echo "  [✗] console:$pkg → $lic（不在白名单）"
			fi
		fi
	done
fi

# ---------- 汇总 ----------
echo
echo "依赖许可扫描：go $go_total 个模块 + console $node_total 个声明依赖"
echo "许可分布："
sort "$DIST" | uniq -c | sort -rn | sed 's/^/  /'

if [ "$fail" -gt 0 ] || { [ "$unknown" -gt 0 ] && [ "${SCAN_ALLOW_UNKNOWN:-0}" != "1" ]; }; then
	echo
	echo "✗ 扫描未通过：$fail 个非白名单，$unknown 个未识别（未知项复核后可用 SCAN_ALLOW_UNKNOWN=1 豁免）"
	exit 1
fi
echo
echo "✓ 许可证扫描通过（白名单：$ALLOWED）"
