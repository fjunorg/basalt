import re, os, json, glob

mods = {}
for f in glob.glob('pkg/modules/**/*.go', recursive=True):
    if '_test' in f: continue
    mod = f.split('/')[2]
    src = open(f).read()
    # 收集本文件内的 Group 前缀与 Handle
    groups = {}  # var name -> prefix
    lines = src.split('\n')
    handlers = []  # (lineno, method, path, perm, hname, prefixvar)
    for i, line in enumerate(lines):
        m = re.search(r'(\w+)\s*:?=\s*\w+\.?Router\(\)?\.Group\("([^"]+)"\)', line)
        if m and m.group(2):
            groups[m.group(1)] = m.group(2)
        g = re.search(r'\.Group\("([^"]+)"\)', line)
        h = re.search(r'Handle\(http\.Method(\w+),\s*"([^"]*)",\s*(.*)\)', line)
        if h:
            method, path, rest = h.group(1), h.group(2), h.group(3)
            # 权限点
            perm = ''
            pm = re.search(r'req\(([\w.]+)', rest)
            if pm: perm = pm.group(1)
            hm = re.search(r'(?:auth|req\([^,]+,\s*)?\(?([a-z]\w*\.[A-Z]\w+|[hm]\.\w+)\)?\s*\)?\s*$', rest)
            hname = rest
            # 找本行所用 group 变量（向前找最近的 g.Handle 属于哪个 var）
            gv = re.search(r'^(\w+)\.Handle', line)
            prefix = groups.get(gv.group(1), '') if gv else ''
            handlers.append((method, prefix + path, perm, rest[:60]))
    if handlers:
        mods.setdefault(mod, []).extend(handlers)

total = 0
out = {}
for mod in sorted(mods):
    out[mod] = []
    for method, path, perm, rest in mods[mod]:
        # 权限点符号解析（PermXxx 常量在同包）
        out[mod].append({'m': method, 'p': path, 'perm': perm})
        total += 1
print(json.dumps({'total': total, 'modules': out}, ensure_ascii=False, indent=1))
