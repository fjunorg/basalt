// Package xlsx 提供最小 xlsx 读写（表头 + 行集模型）。
//
// 选型：不自引 excelize——功能全但带来 ~1.5MB 二进制增量与 10+
// 传递依赖，违背本项目零重依赖基线（信创离线交付）。本包以标准库
// archive/zip + encoding/xml 覆盖导入导出所需子集：
//
//   - Write：生成最小合法 OOXML（inlineStr 单元格，Excel/WPS 兼容）
//   - Read：解析首个工作表，支持 inlineStr / sharedStrings 引用 /
//     数字与公式结果单元格；非目标：样式/多 sheet/图表/列宽
//
// API 契约：Read(Write(h, rows)) == rows（往返保真，字符串模型）。
package xlsx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ErrBadFile 表示输入不是合法的 xlsx（缺 zip 结构或 sheet）。
var ErrBadFile = errors.New("xlsx: not a valid spreadsheet")

// ---------- 写 ----------

// Write 生成单 sheet 的 xlsx：首行为表头，其余为数据行。
// 单元格一律以内联字符串（inlineStr）写入，规避 sharedStrings。
func Write(w io.Writer, headers []string, rows [][]string) error {
	zw := zip.NewWriter(w)
	files := []struct {
		name string
		body string
	}{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
</Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>`},
		{"xl/workbook.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets>
</workbook>`},
		{"xl/_rels/workbook.xml.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
</Relationships>`},
		{"xl/worksheets/sheet1.xml", sheetXML(headers, rows)},
	}
	for _, f := range files {
		fw, err := zw.Create(f.name)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(fw, f.body); err != nil {
			return err
		}
	}
	return zw.Close()
}

// sheetXML 生成 sheet 正文（inlineStr 单元格）。
func sheetXML(headers []string, rows [][]string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	writeRow := func(rowIdx int, cells []string) {
		fmt.Fprintf(&b, `<row r="%d">`, rowIdx)
		for i, v := range cells {
			fmt.Fprintf(&b, `<c r="%s%d" t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`,
				colName(i), rowIdx, xmlEscape(v))
		}
		b.WriteString(`</row>`)
	}
	writeRow(1, headers)
	for i, r := range rows {
		writeRow(i+2, r)
	}
	b.WriteString(`</sheetData></worksheet>`)
	return b.String()
}

// colName 列序号 → 字母名（0=A，25=Z，26=AA）。
func colName(i int) string {
	name := ""
	for n := i; ; {
		name = string(rune('A'+n%26)) + name
		n = n/26 - 1
		if n < 0 {
			return name
		}
	}
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// ---------- 读 ----------

// Read 解析 xlsx 首个工作表，返回行集（含表头行）。
func Read(r io.Reader) ([][]string, error) {
	raw, err := io.ReadAll(io.LimitReader(r, 64<<20))
	if err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, ErrBadFile
	}
	var sheet, shared []byte
	for _, f := range zr.File {
		switch f.Name {
		case "xl/worksheets/sheet1.xml":
			sheet, err = readZipFile(f)
		case "xl/sharedStrings.xml":
			shared, err = readZipFile(f)
		}
		if err != nil {
			return nil, err
		}
	}
	if sheet == nil {
		return nil, ErrBadFile
	}
	var sst []string
	if shared != nil {
		sst, err = parseSharedStrings(shared)
		if err != nil {
			return nil, err
		}
	}
	return parseSheet(sheet, sst)
}

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, 64<<20))
}

// parseSharedStrings 提取共享字符串表。
func parseSharedStrings(data []byte) ([]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var out []string
	var inSI, inT bool
	var cur strings.Builder
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				inSI, cur = true, strings.Builder{}
			case "t":
				inT = true
			}
		case xml.CharData:
			if inSI && inT {
				cur.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "si":
				if inSI {
					out = append(out, cur.String())
					inSI = false
				}
			}
		}
	}
	return out, nil
}

// parseSheet 解析行/单元格：t="s" 查 sharedStrings，t="inlineStr"
// 取内联 <t>，否则取 <v>（数字/公式结果原样字符串化）。
func parseSheet(data []byte, sst []string) ([][]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var rows [][]string
	var cur []string
	var inRow, inCell, inV, inT bool
	var cellType string
	var buf strings.Builder
	appendCell := func() {
		v := buf.String()
		if cellType == "s" {
			if idx, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && idx < len(sst) {
				v = sst[idx]
			}
		}
		cur = append(cur, v)
		buf.Reset()
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				inRow, cur = true, nil
			case "c":
				inCell = true
				cellType = ""
				for _, a := range t.Attr {
					if a.Name.Local == "t" {
						cellType = a.Value
					}
				}
			case "v":
				inV = true
			case "t":
				if inCell {
					inT = true
				}
			}
		case xml.CharData:
			if inCell && (inV || inT) {
				buf.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
			case "t":
				inT = false
			case "c":
				if inCell {
					appendCell()
					inCell = false
				}
			case "row":
				if inRow {
					rows = append(rows, cur)
					inRow = false
				}
			}
		}
	}
	return rows, nil
}

// Build 构造导出响应体（helper：内存中生成完整 xlsx）。
func Build(headers []string, rows [][]string) ([]byte, error) {
	var buf bytes.Buffer
	if err := Write(&buf, headers, rows); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
