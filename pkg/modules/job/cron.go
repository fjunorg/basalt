package job

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ---------- 标准 5 段 cron 解析器（自研，零依赖） ----------
//
// 选型说明：依赖树中无 cron 库；robfig/cron 虽属轻量但引入新依赖且
// 携带描述符/Job 包装等超出本模块需求的面积。5 段标准语义（分 时
// 日 月 周）自研约 200 行完全覆盖：每段支持 `*`、`*/n`、`a`、`a-b`、
// `a-b/n`、逗号列表混合；日(dom)与周(dow)同时受限时取 Vixie-cron
// 的 OR 语义（业界标准行为）。

// fieldMatcher 单段的命中集合。
type fieldMatcher struct {
	set    map[int]bool // 命中值（已按 min/max 归一）
	any    bool         // `*`（含 */n 的 any+step 归一后仍进 set）
	stepOf int          // 调试用：原 step（0 = 无）
}

type Cron struct {
	minute, hour, dom, month, dow fieldMatcher
	original                      string
}

var fieldRanges = [5][2]int{
	{0, 59}, // minute
	{0, 23}, // hour
	{1, 31}, // day of month
	{1, 12}, // month
	{0, 6},  // day of week（0=周日）
}

// ParseCron 解析 5 段标准 cron 表达式。
func ParseCron(expr string) (*Cron, error) {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return nil, fmt.Errorf("cron: want 5 fields (min hour dom month dow), got %d", len(parts))
	}
	ms := make([]fieldMatcher, 5)
	for i, p := range parts {
		m, err := parseField(p, fieldRanges[i][0], fieldRanges[i][1])
		if err != nil {
			return nil, fmt.Errorf("cron: field %d (%q): %w", i+1, p, err)
		}
		ms[i] = m
	}
	return &Cron{minute: ms[0], hour: ms[1], dom: ms[2], month: ms[3], dow: ms[4], original: expr}, nil
}

// parseField 解析单段：`*`、`*/n`、`a`、`a-b`、`a-b/n`、逗号列表。
func parseField(s string, min, max int) (fieldMatcher, error) {
	fm := fieldMatcher{set: map[int]bool{}}
	for _, piece := range strings.Split(s, ",") {
		piece = strings.TrimSpace(piece)
		if piece == "" {
			return fm, fmt.Errorf("empty list item")
		}
		rangePart, stepPart := piece, ""
		if i := strings.Index(piece, "/"); i >= 0 {
			rangePart, stepPart = piece[:i], piece[i+1:]
		}
		lo, hi := min, max
		switch {
		case rangePart == "*" || rangePart == "":
			// 全域
		default:
			if j := strings.Index(rangePart, "-"); j >= 0 {
				var err error
				if lo, err = strconv.Atoi(rangePart[:j]); err != nil {
					return fm, fmt.Errorf("bad range start %q", rangePart[:j])
				}
				if hi, err = strconv.Atoi(rangePart[j+1:]); err != nil {
					return fm, fmt.Errorf("bad range end %q", rangePart[j+1:])
				}
			} else {
				v, err := strconv.Atoi(rangePart)
				if err != nil {
					return fm, fmt.Errorf("bad value %q", rangePart)
				}
				lo, hi = v, v
			}
		}
		step := 1
		if stepPart != "" {
			v, err := strconv.Atoi(stepPart)
			if err != nil || v < 1 {
				return fm, fmt.Errorf("bad step %q", stepPart)
			}
			step = v
		}
		if lo < min || hi > max || lo > hi {
			return fm, fmt.Errorf("range %d-%d out of [%d,%d]", lo, hi, min, max)
		}
		for v := lo; v <= hi; v += step {
			fm.set[v] = true
		}
		if len(fm.set) == max-min+1 {
			fm.any = true
		}
	}
	if len(fm.set) == 0 {
		return fm, fmt.Errorf("no matching values")
	}
	return fm, nil
}

func (f fieldMatcher) match(v int) bool { return f.set[v] }

// Next 返回 after 之后（严格大于）的下一次触发时刻。
// 逐分钟扫描，上限 4 年（覆盖 2/29 与 dom/dow 组合的最坏空窗）；
// 扫描失败（4 年内无匹配，理论上仅当表达式无解）返回零值。
func (c *Cron) Next(after time.Time) time.Time {
	t := after.Truncate(time.Minute).Add(time.Minute) // 下一分钟起（秒清零）
	limit := t.AddDate(4, 0, 0)
	for t.Before(limit) {
		if c.matches(t) {
			return t
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}
}

func (c *Cron) matches(t time.Time) bool {
	if !c.month.match(int(t.Month())) || !c.hour.match(t.Hour()) || !c.minute.match(t.Minute()) {
		return false
	}
	domHit, dowHit := c.dom.match(t.Day()), c.dow.match(int(t.Weekday()))
	// Vixie 语义：dom 与 dow 同时受限时取 OR；任一为 * 时两者皆须命中
	//（此时受限一方已决定）。
	if c.dom.any || c.dow.any {
		return domHit && dowHit
	}
	return domHit || dowHit
}

// String 返回原始表达式。
func (c *Cron) String() string { return c.original }

// ValidCron 校验表达式（API 输入校验用）。
func ValidCron(expr string) error {
	_, err := ParseCron(expr)
	return err
}
