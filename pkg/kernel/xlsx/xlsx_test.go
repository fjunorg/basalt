package xlsx

import (
	"bytes"
	"strings"
	"testing"
)

// TestRoundTrip 写→读→比对（含中文/换行/引号/空单元格）。
func TestRoundTrip(t *testing.T) {
	headers := []string{"用户名", "部门", "金额", "备注"}
	rows := [][]string{
		{"张三", "技术部", "128.5", "含 \"引号\" 与 <尖括号>"},
		{"李四", "市场部", "99", "换行\n第二行"},
		{"&wang", "", "0", ""},
	}
	data, err := Build(headers, rows)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	got, err := Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 4 || len(got[0]) != 4 {
		t.Fatalf("shape = %d rows", len(got))
	}
	for i, h := range headers {
		if got[0][i] != h {
			t.Fatalf("header %d = %q", i, got[0][i])
		}
	}
	for r, want := range rows {
		for c, w := range want {
			if got[r+1][c] != w {
				t.Fatalf("cell[%d][%d] = %q, want %q", r+1, c, got[r+1][c], w)
			}
		}
	}
}

// TestColName 列名序列。
func TestColName(t *testing.T) {
	cases := map[int]string{0: "A", 25: "Z", 26: "AA", 27: "AB", 51: "AZ", 52: "BA", 701: "ZZ"}
	for i, want := range cases {
		if got := colName(i); got != want {
			t.Fatalf("colName(%d) = %q, want %q", i, got, want)
		}
	}
}

// TestReadBadInput 非 zip 输入报 ErrBadFile。
func TestReadBadInput(t *testing.T) {
	if _, err := Read(strings.NewReader("not a zip")); err != ErrBadFile {
		t.Fatalf("err = %v", err)
	}
}

// TestWideColumns 超 26 列（AA 命名）往返。
func TestWideColumns(t *testing.T) {
	headers := make([]string, 30)
	rows := [][]string{make([]string, 30)}
	for i := range headers {
		headers[i] = "H" + colName(i)
		rows[0][i] = "V" + colName(i)
	}
	data, err := Build(headers, rows)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	got, err := Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got[1][29] != "VAD" || got[0][29] != "HAD" {
		t.Fatalf("col 29 = %q/%q", got[0][29], got[1][29])
	}
}

// TestParseSharedStringsRef 模拟 sharedStrings 风格 sheet 的解析
//（手工构造 t="s" + sst 引用，覆盖 Excel 原生文件形态）。
func TestParseSharedStringsRef(t *testing.T) {
	sheet := `<?xml version="1.0"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c><c r="C1"><v>42</v></c></row>
</sheetData></worksheet>`
	sst := []string{"姓名", "Name"}
	got, err := parseSheet([]byte(sheet), sst)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got[0][0] != "姓名" || got[0][1] != "Name" || got[0][2] != "42" {
		t.Fatalf("row = %v", got[0])
	}
}
