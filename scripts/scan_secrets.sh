#!/usr/bin/env bash
# scan_secrets：仓库敏感信息泄漏扫描（轻量自研，零外部工具依赖）。
#
# 检测模式：云厂商/平台凭证样式（AWS AKIA、GitHub、Slack、OpenAI sk-、
# Google API key）、PEM 私钥块、硬编码 JWT、≥45 位高熵 hex 串。
#
# 排除：node_modules/dist/.git、锁文件（go.sum 模块哈希）、测试
# fixture（*_test.go）、演示口令标注文件（*.md/*.example/seed.go）。
#
# 命中即退出码 1。用法：scripts/scan_secrets.sh（或 make scan-secrets）
set -u

cd "$(dirname "$0")/.."

# 合并正则（ERE），每文件单次 grep -nE——避免逐行多次 fork。
PATTERN_AWS='AKIA[0-9A-Z]{16}|ASIA[0-9A-Z]{16}'
PATTERN_TOKENS='ghp_[0-9A-Za-z]{36}|github_pat_[0-9A-Za-z_]{22,}|xox[bpars]-[0-9A-Za-z-]{10,}|sk-[0-9A-Za-z]{20,}[^0-9A-Za-z]|AIza[0-9A-Za-z_-]{35}'
PATTERN_PEM='-----BEGIN [A-Z ]*PRIVATE KEY-----'
PATTERN_JWT='eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9\.eyJ[A-Za-z0-9_-]{20,}\.'
ALL_PATTERNS="$PATTERN_AWS|$PATTERN_TOKENS|$PATTERN_PEM|$PATTERN_JWT"
HIGH_ENTROPY='[0-9a-f]{45,}'

hits=0
files_scanned=0

FILES=$(mktemp -t secfiles)
HITSFILE=$(mktemp -t sechits)
trap 'rm -f "$FILES" "$HITSFILE"' EXIT

find . -type f \( -name '*.go' -o -name '*.ts' -o -name '*.vue' -o -name '*.js' \
	-o -name '*.py' -o -name '*.sh' -o -name '*.yaml' -o -name '*.yml' \
	-o -name '*.json' -o -name '*.toml' -o -name '*.env' -o -name '*.conf' -o -name '*.ini' \) 2>/dev/null \
	| grep -v -E 'console/node_modules/|console/dist/|\.git/|go\.sum|go\.work\.sum|package-lock\.json|pnpm-lock\.yaml|scan_secrets\.sh' \
	> "$FILES"

total=$(wc -l < "$FILES" | tr -d ' ')
while read -r f; do
	files_scanned=$((files_scanned + 1))
	case "$f" in
	# 豁免：文档/示例/演示数据/测试 fixture（人工评审过的演示值与测试向量）
	*.md|*.example|*/seed.go|*_test.go|*/seed_test.go)
		continue ;;
	esac
	# 1) 凭证模式（单次 grep 多模式）
	if grep -qE "$ALL_PATTERNS" "$f" 2>/dev/null; then
		grep -nE "$ALL_PATTERNS" "$f" >> "$HITSFILE" 2>/dev/null
	fi
	# 2) 高熵 hex（全 0/全 f 占位跳过）
	if grep -qE "$HIGH_ENTROPY" "$f" 2>/dev/null; then
		grep -noE "$HIGH_ENTROPY" "$f" 2>/dev/null | grep -vE '000000|ffffffff' | head -3 | sed "s|^|$f:|" >> "$HITSFILE"
	fi
done < "$FILES"

if [ -s "$HITSFILE" ]; then
	sed 's/^/  [✗] /' "$HITSFILE"
	hits=$(wc -l < "$HITSFILE" | tr -d ' ')
fi

echo
echo "敏感信息扫描：${total} 个候选文件，实际扫描 ${files_scanned}（豁免 fixture/演示文档/锁文件/产物）"
if [ "$hits" -gt 0 ]; then
	echo "✗ 扫描未通过：${hits} 处疑似泄漏（人工复核）"
	exit 1
fi
echo "✓ 敏感信息扫描通过（0 命中）"
