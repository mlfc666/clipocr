package main

import "testing"

func TestLatexToPlain(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// 用户明确要求的两个例子
		{
			name: "下标 + text",
			in:   `计算最短测地距离 $d_{\text{dev}}$。`,
			want: `计算最短测地距离 d_dev。`,
		},
		{
			name: "text 单位",
			in:   `当 $d_{\text{dev}} \le 3.0\text{m}$ 时`,
			want: `当 d_dev ≤ 3.0m 时`,
		},
		{
			name: "裸 text 命令",
			in:   `阈值 3.0\text{m} 以内`,
			want: `阈值 3.0m 以内`,
		},

		// 常见数学写法
		{name: "frac", in: `$\frac{a}{b}$`, want: `a/b`},
		{name: "sqrt", in: `$\sqrt{x}$`, want: `√x`},
		{name: "sqrt 带次数", in: `$\sqrt[3]{x}$`, want: `3√x`},
		{name: "上标", in: `$x^{2}$`, want: `x^2`},
		{name: "希腊字母", in: `$\alpha + \beta$`, want: `α + β`},
		{name: "箭头", in: `$A \to B$`, want: `A → B`},
		{name: "求和", in: `$\sum_{i=1}^{n} x_i$`, want: `∑_i=1^n x_i`},
		{name: "双美元", in: `$$E = mc^2$$`, want: `E = mc^2`},
		{name: "left right", in: `$\left( \frac{1}{2} \right)$`, want: `( 1/2 )`},
		{name: "mathrm", in: `$\mathrm{d}x$`, want: `dx`},
		{name: "mathbb", in: `$\mathbb{R}$`, want: `R`},
		{name: "textcolor", in: `$\textcolor{red}{警告}$`, want: `警告`},
		{name: "百分号", in: `$50\%$`, want: `50%`},
		{name: "换行", in: `$a \\ b$`, want: "a \n b"},

		// 不该被破坏的内容
		{name: "价格", in: `价格 $5 元`, want: `价格 $5 元`},
		{name: "价格带小数", in: `共 $12.50`, want: `共 $12.50`},
		{name: "Windows 路径", in: `C:\Users\test\file.txt`, want: `C:\Users\test\file.txt`},
		{name: "纯中文", in: `这是一段普通中文，没有公式。`, want: `这是一段普通中文，没有公式。`},
		{name: "下划线文本", in: `变量名 d_dev 保持不变`, want: `变量名 d_dev 保持不变`},
		{name: "花括号文本", in: `集合 {a, b} 保持不变`, want: `集合 {a, b} 保持不变`},
		{name: "空字符串", in: ``, want: ``},

		// 混合
		{
			name: "中英混排",
			in:   `当 $d_{\text{dev}} \le 3.0\text{m}$ 时，路线约束专家能准确教模型如何“拉回正轨”；当 $d_{\text{dev}} > 3.0\text{m}$ 时不要硬灌。`,
			want: `当 d_dev ≤ 3.0m 时，路线约束专家能准确教模型如何“拉回正轨”；当 d_dev > 3.0m 时不要硬灌。`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := latexToPlain(c.in)
			if got != c.want {
				t.Errorf("latexToPlain(%q)\n  got  = %q\n  want = %q", c.in, got, c.want)
			}
		})
	}
}

func TestLatexToPlainIdempotent(t *testing.T) {
	// 转换结果再转一次不应继续变化
	inputs := []string{
		`$d_{\text{dev}} \le 3.0\text{m}$`,
		`$\frac{a}{b} + \sqrt{x}$`,
		`价格 $5 元`,
	}
	for _, in := range inputs {
		once := latexToPlain(in)
		twice := latexToPlain(once)
		if once != twice {
			t.Errorf("不幂等: %q → %q → %q", in, once, twice)
		}
	}
}

func TestStripOCRMarks(t *testing.T) {
	cases := []struct{ in, want string }{
		{`<｜ref｜>标题<｜/ref｜><｜box｜>[[52, 40, 948, 96]]<｜/box｜>`, `标题[[52, 40, 948, 96]]`},
		{`<ref>正文</ref>`, `正文`},
		{`普通文本`, `普通文本`},
	}
	for _, c := range cases {
		if got := stripOCRMarks(c.in); got != c.want {
			t.Errorf("stripOCRMarks(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPlainTextLatexToggle(t *testing.T) {
	res := &OCRResult{
		PageCount: 1,
		Pages: []OCRPage{
			{Index: 0, Text: `距离 $d_{\text{dev}} \le 3.0\text{m}$`, FinishReason: "stop"},
		},
	}

	plain := plainText(res, false, false)
	if plain != `距离 d_dev ≤ 3.0m` {
		t.Errorf("默认应转纯文本，got %q", plain)
	}

	kept := plainText(res, false, true)
	if kept != `距离 $d_{\text{dev}} \le 3.0\text{m}$` {
		t.Errorf("-latex 应保留原文，got %q", kept)
	}
}
