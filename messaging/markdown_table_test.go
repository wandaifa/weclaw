package messaging

import "testing"

func TestWeChatTableRow(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{`| 试香纸\|单张 | 50 | 0.20 |`, `| 试香纸｜单张 | 50 | 0.20 |`},
		{`| A\\|B | C\\\|D |`, `| A\\|B | C\\｜D |`},
		{"| `a\\|b` | `c` |", "| `a｜b` | `c` |"},
		{`| a\|b\|c | |`, `| a｜b｜c | |`},
		{`| A｜B | C:\temp\ |`, `| A｜B | C:\temp\ |`},
	} {
		if got := weChatTableRow(tt.in); got != tt.want {
			t.Errorf("weChatTableRow(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFormatWeChatMarkdownEscapedTablePipes(t *testing.T) {
	const outsideTable = "\n\n正文 \\| 原样\n\n```md\n型号 \\| 颜色 | 价格\n--- | ---\nA \\| B | 0.20\n```"
	const input = "规格 \\| 单位 | 数量 | 单价\n" +
		":--- | :---: | ---:\n" +
		"试香纸 \\| 单张 | `50` | 0.20" + outsideTable
	const want = "规格 ｜ 单位 | 数量 | 单价\n" +
		":--- | :---: | ---:\n" +
		"试香纸 ｜ 单张 | `50` | 0.20" + outsideTable
	if got := FormatWeChatMarkdown(input); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}
