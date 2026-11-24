package main

import "strings"

// latexToPlain 把 OCR 结果里的 LaTeX 片段转成可读的纯文本。
//
//	$d_{\text{dev}} \le 3.0\text{m}$  →  d_dev ≤ 3.0m
//	3.0\text{m}                       →  3.0m
//	$\frac{a}{b}$                     →  a/b
//
// 只处理能确定是 LaTeX 的部分：$...$ / $$...$$ 里的内容，以及反斜杠命令。
// 普通文本里的 $（例如价格）、Windows 路径里的反斜杠都原样保留。
func latexToPlain(s string) string {
	if !strings.ContainsAny(s, `$\_^{}`) {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))

	i := 0
	for i < len(s) {
		if s[i] == '$' {
			// $$ ... $$
			if strings.HasPrefix(s[i:], "$$") {
				if end := strings.Index(s[i+2:], "$$"); end >= 0 {
					inner := s[i+2 : i+2+end]
					if looksLikeMath(inner) {
						b.WriteString(convertSegment(inner, true))
						i += 2 + end + 2
						continue
					}
				}
				b.WriteString("$$")
				i += 2
				continue
			}
			// $ ... $
			if end := findClosingDollar(s, i+1); end >= 0 {
				inner := s[i+1 : end]
				if looksLikeMath(inner) {
					b.WriteString(convertSegment(inner, true))
					i = end + 1
					continue
				}
			}
			b.WriteByte('$')
			i++
			continue
		}

		j := i
		for j < len(s) && s[j] != '$' {
			j++
		}
		b.WriteString(convertSegment(s[i:j], false))
		i = j
	}
	return b.String()
}

// looksLikeMath 判断 $...$ 里的内容是不是公式，避免把价格 $5 当成公式。
func looksLikeMath(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return false
	}
	// 明确的 LaTeX 特征
	if strings.ContainsAny(t, `\_^{}`) {
		return true
	}
	// 不含空白、带字母的短片段，如 $x$、$n=10$
	if !strings.ContainsAny(t, " \t\n") && len(t) <= 20 {
		for i := 0; i < len(t); i++ {
			if isLetter(t[i]) {
				return true
			}
		}
	}
	return false
}

// findClosingDollar 找到下一个未被转义的 $。
func findClosingDollar(s string, start int) int {
	for i := start; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == '$' {
			return i
		}
	}
	return -1
}

// convertSegment 转换一段文本。inMath 为 true 时按公式处理（去掉分组花括号等）。
func convertSegment(s string, inMath bool) string {
	var b strings.Builder
	b.Grow(len(s))

	i := 0
	for i < len(s) {
		c := s[i]
		switch c {
		case '\\':
			i = handleCommand(s, i, &b, inMath)

		case '{':
			if inMath {
				if inner, next, ok := readGroup(s, i); ok {
					b.WriteString(convertSegment(inner, true))
					i = next
					continue
				}
			}
			b.WriteByte(c)
			i++

		case '}':
			if inMath {
				i++
				continue
			}
			b.WriteByte(c)
			i++

		case '_', '^':
			// 下标 / 上标：_{...} → _...，^{...} → ^...
			if i+1 < len(s) && s[i+1] == '{' {
				if inner, next, ok := readGroup(s, i+1); ok {
					b.WriteByte(c)
					b.WriteString(convertSegment(inner, inMath))
					i = next
					continue
				}
			}
			b.WriteByte(c)
			i++

		case '~':
			if inMath {
				b.WriteByte(' ')
			} else {
				b.WriteByte('~')
			}
			i++

		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// handleCommand 处理从 s[i]（反斜杠）开始的一个命令，返回处理后的位置。
func handleCommand(s string, i int, b *strings.Builder, inMath bool) int {
	j := i + 1
	if j >= len(s) {
		return j
	}

	// 非字母命令：转义字符、间距、换行
	if !isLetter(s[j]) {
		c := s[j]
		switch c {
		case '\\': // \\ 换行
			b.WriteByte('\n')
		case ',', ';', ':', ' ': // 各种间距
			b.WriteByte(' ')
		case '!': // 负间距
			// 丢弃
		case '%', '&', '_', '#', '$', '{', '}':
			b.WriteByte(c)
		case '(', ')', '[', ']': // 公式定界符
			// 丢弃
		default:
			b.WriteByte(c)
		}
		return j + 1
	}

	// 字母命令
	k := j
	for k < len(s) && isLetter(s[k]) {
		k++
	}
	name := s[j:k]
	if k < len(s) && s[k] == '*' { // 带星号的变体
		k++
	}

	// 间距命令
	if sp, ok := spaceCmds[name]; ok {
		b.WriteString(sp)
		return k
	}

	// 符号命令
	if sym, ok := symbolCmds[name]; ok {
		b.WriteString(sym)
		return k
	}

	// 直接丢弃的命令（连同参数）
	if dropCmds[name] {
		k = skipOptional(s, k)
		if name == "begin" || name == "end" {
			// \begin{array}{cc} 这类要吃掉两个参数
			for n := 0; n < 2; n++ {
				if _, next, ok := readGroup(s, k); ok {
					k = next
				} else {
					break
				}
			}
			return k
		}
		if _, next, ok := readGroup(s, k); ok {
			k = next
		}
		return k
	}

	// \frac{a}{b} → a/b
	if name == "frac" || name == "dfrac" || name == "tfrac" || name == "cfrac" {
		a, next, ok1 := readGroup(s, k)
		if ok1 {
			if bb, next2, ok2 := readGroup(s, next); ok2 {
				b.WriteString(convertSegment(a, inMath))
				b.WriteByte('/')
				b.WriteString(convertSegment(bb, inMath))
				return next2
			}
		}
		return k
	}

	// \sqrt{x} → √x，\sqrt[3]{x} → 3√x
	if name == "sqrt" {
		idx := ""
		if k < len(s) && s[k] == '[' {
			if e := strings.IndexByte(s[k:], ']'); e > 0 {
				idx = s[k+1 : k+e]
				k = k + e + 1
			}
		}
		if inner, next, ok := readGroup(s, k); ok {
			if idx != "" {
				b.WriteString(convertSegment(idx, inMath))
			}
			b.WriteString("√")
			b.WriteString(convertSegment(inner, inMath))
			return next
		}
		return k
	}

	// \textcolor{red}{内容} → 内容
	if name == "textcolor" {
		if _, next, ok := readGroup(s, k); ok {
			if inner, next2, ok2 := readGroup(s, next); ok2 {
				b.WriteString(convertSegment(inner, inMath))
				return next2
			}
		}
		return k
	}

	// 保留内容的命令：\text{...}、\mathrm{...}、\operatorname{...} 等
	if keepContentCmds[name] {
		if inner, next, ok := readGroup(s, k); ok {
			b.WriteString(convertSegment(inner, inMath))
			return next
		}
		return k
	}

	// 未知命令：原样保留（保护 Windows 路径 C:\Users 这类文本）
	b.WriteByte('\\')
	b.WriteString(name)
	return k
}

// readGroup 读取 s[i] 处的 {...}，返回内部内容与结束位置。
func readGroup(s string, i int) (string, int, bool) {
	if i >= len(s) || s[i] != '{' {
		return "", i, false
	}
	depth := 0
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++ // 跳过被转义的字符
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[i+1 : j], j + 1, true
			}
		}
	}
	return "", i, false
}

// skipOptional 跳过 [...] 可选参数。
func skipOptional(s string, i int) int {
	if i < len(s) && s[i] == '[' {
		if e := strings.IndexByte(s[i:], ']'); e > 0 {
			return i + e + 1
		}
	}
	return i
}

func isLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// ---------- 命令表 ----------

// spaceCmds：间距命令
var spaceCmds = map[string]string{
	"quad":         " ",
	"qquad":        "  ",
	"enspace":      " ",
	"enskip":       " ",
	"thinspace":    " ",
	"medspace":     " ",
	"thickspace":   " ",
	"negthinspace": "",
	"space":        " ",
}

// dropCmds：整条丢弃（连同参数）
var dropCmds = map[string]bool{
	// 定界符尺寸
	"left": true, "right": true, "middle": true,
	"big": true, "Big": true, "bigg": true, "Bigg": true,
	"bigl": true, "bigr": true, "Bigl": true, "Bigr": true,
	"biggl": true, "biggr": true, "Biggl": true, "Biggr": true,
	// 样式
	"displaystyle": true, "textstyle": true, "scriptstyle": true,
	"scriptscriptstyle": true, "limits": true, "nolimits": true,
	// 环境
	"begin": true, "end": true, "item": true,
	// 排版
	"label": true, "nonumber": true, "notag": true, "protect": true,
	"noindent": true, "centering": true, "hfill": true, "vfill": true,
	"smallskip": true, "medskip": true, "bigskip": true, "relax": true,
	"hline": true, "cline": true, "toprule": true, "midrule": true,
	"bottomrule": true, "par": true, "newline": true, "linebreak": true,
	"pagebreak": true, "clearpage": true, "newpage": true,
	// 占位与间距
	"phantom": true, "hphantom": true, "vphantom": true, "mathstrut": true,
	"strut": true, "hspace": true, "vspace": true, "kern": true,
	"mkern": true, "hskip": true, "vskip": true, "rule": true,
	"color": true, "includegraphics": true,
	// 其他
	"index": true, "footnote": true, "thanks": true,
}

// keepContentCmds：保留花括号里的内容
var keepContentCmds = map[string]bool{
	// 文本样式
	"text": true, "textrm": true, "textbf": true, "textit": true,
	"textsf": true, "texttt": true, "textnormal": true, "textup": true,
	"textsl": true, "textsc": true, "emph": true, "mbox": true, "hbox": true,
	// 数学样式
	"mathrm": true, "mathbf": true, "mathit": true, "mathsf": true,
	"mathtt": true, "mathcal": true, "mathbb": true, "mathfrak": true,
	"mathscr": true, "mathnormal": true, "boldsymbol": true, "bm": true,
	"operatorname": true, "ensuremath": true,
	// 重音
	"hat": true, "bar": true, "tilde": true, "vec": true, "dot": true,
	"ddot": true, "acute": true, "grave": true, "breve": true, "check": true,
	"widehat": true, "widetilde": true, "overline": true, "underline": true,
	"overrightarrow": true, "overleftarrow": true,
	// 文档结构
	"title": true, "author": true, "section": true, "subsection": true,
	"subsubsection": true, "paragraph": true, "caption": true,
	"ref": true, "cite": true, "eqref": true, "url": true, "href": true,
}

// symbolCmds：命令 → 符号
var symbolCmds = map[string]string{
	// 希腊字母
	"alpha": "α", "beta": "β", "gamma": "γ", "delta": "δ",
	"epsilon": "ε", "varepsilon": "ε", "zeta": "ζ", "eta": "η",
	"theta": "θ", "vartheta": "ϑ", "iota": "ι", "kappa": "κ",
	"lambda": "λ", "mu": "μ", "nu": "ν", "xi": "ξ", "pi": "π",
	"varpi": "ϖ", "rho": "ρ", "varrho": "ϱ", "sigma": "σ",
	"varsigma": "ς", "tau": "τ", "upsilon": "υ", "phi": "φ",
	"varphi": "φ", "chi": "χ", "psi": "ψ", "omega": "ω",
	"Gamma": "Γ", "Delta": "Δ", "Theta": "Θ", "Lambda": "Λ",
	"Xi": "Ξ", "Pi": "Π", "Sigma": "Σ", "Upsilon": "Υ",
	"Phi": "Φ", "Psi": "Ψ", "Omega": "Ω",

	// 关系与运算
	"le": "≤", "leq": "≤", "ge": "≥", "geq": "≥",
	"ne": "≠", "neq": "≠", "approx": "≈", "equiv": "≡",
	"sim": "∼", "simeq": "≃", "cong": "≅", "propto": "∝",
	"ll": "≪", "gg": "≫", "lll": "⋘", "ggg": "⋙",
	"times": "×", "div": "÷", "cdot": "·", "pm": "±", "mp": "∓",
	"ast": "∗", "star": "⋆", "circ": "∘", "bullet": "•",
	"oplus": "⊕", "ominus": "⊖", "otimes": "⊗", "odot": "⊙",
	"cup": "∪", "cap": "∩", "setminus": "∖", "sqcup": "⊔", "sqcap": "⊓",

	// 箭头
	"to": "→", "rightarrow": "→", "leftarrow": "←",
	"Rightarrow": "⇒", "Leftarrow": "⇐",
	"leftrightarrow": "↔", "Leftrightarrow": "⇔",
	"mapsto": "↦", "uparrow": "↑", "downarrow": "↓",
	"longrightarrow": "⟶", "longleftarrow": "⟵",
	"Longrightarrow": "⟹", "Longleftarrow": "⟸",
	"implies": "⟹", "iff": "⟺",

	// 集合与逻辑
	"in": "∈", "notin": "∉", "ni": "∋",
	"subset": "⊂", "subseteq": "⊆", "supset": "⊃", "supseteq": "⊇",
	"emptyset": "∅", "varnothing": "∅",
	"forall": "∀", "exists": "∃", "nexists": "∄",
	"neg": "¬", "land": "∧", "lor": "∨", "wedge": "∧", "vee": "∨",

	// 其他符号
	"infty": "∞", "partial": "∂", "nabla": "∇",
	"sum": "∑", "prod": "∏", "int": "∫", "iint": "∬", "iiint": "∭",
	"oint": "∮", "angle": "∠", "perp": "⊥", "parallel": "∥",
	"degree": "°", "prime": "′", "ell": "ℓ", "hbar": "ℏ",
	"aleph": "ℵ", "therefore": "∴", "because": "∵",
	"cdots": "⋯", "ldots": "…", "dots": "…", "vdots": "⋮", "ddots": "⋱",
	"langle": "⟨", "rangle": "⟩", "lfloor": "⌊", "rfloor": "⌋",
	"lceil": "⌈", "rceil": "⌉", "lbrace": "{", "rbrace": "}",
	"vert": "|", "Vert": "‖", "mid": "|", "backslash": "\\",
	"triangle": "△", "square": "□", "checkmark": "✓",
	"dag": "†", "ddag": "‡", "S": "§", "P": "¶",
	"copyright": "©", "pounds": "£", "euro": "€",
	"textbackslash": "\\", "textasciitilde": "~",
	"textless": "<", "textgreater": ">", "textbar": "|",
	"quad_": " ",
}
