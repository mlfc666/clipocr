package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// ---------- 终端颜色 ----------

const (
	colorReset  = "\033[0m"
	colorBold   = "\033[1m"
	colorDim    = "\033[2m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorCyan   = "\033[36m"
)

// useColor 判断是否应该输出 ANSI 颜色。
func useColor(f *os.File) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("CLICOLOR_FORCE") != "" && os.Getenv("CLICOLOR_FORCE") != "0" {
		return true
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	if fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	term := os.Getenv("TERM")
	return term != "" && term != "dumb"
}

func paint(code, s string) string {
	if !useColor(os.Stderr) {
		return s
	}
	return code + s + colorReset
}

func warnf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%s %s\n", paint(colorYellow, "提示:"), fmt.Sprintf(format, args...))
}

// ---------- 加载动画 ----------

// startSpinner 在 stderr 上显示一个转圈动画，返回停止函数。
func startSpinner(w io.Writer, label string) func() {
	if !useColor(os.Stderr) {
		fmt.Fprintf(w, "%s...\n", label)
		return func() {}
	}

	stop := make(chan struct{})
	var once sync.Once
	start := time.Now()

	go func() {
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		i := 0
		for {
			select {
			case <-stop:
				// 清掉这一行
				fmt.Fprintf(w, "\r\033[K")
				return
			case <-ticker.C:
				elapsed := time.Since(start).Round(100 * time.Millisecond)
				fmt.Fprintf(w, "\r\033[K%s %s %s", paint(colorCyan, frames[i%len(frames)]), label,
					paint(colorDim, elapsed.String()))
				i++
			}
		}
	}()

	return func() {
		once.Do(func() { close(stop) })
		// 等动画协程把行清掉
		time.Sleep(120 * time.Millisecond)
	}
}

// ---------- 结果渲染 ----------

type resultMeta struct {
	Origin    string
	Info      *ImageInfo
	SavedPath string
}

func printResult(w io.Writer, res *OCRResult, meta resultMeta, raw, keepLatex, quiet bool) {
	text := plainText(res, raw, keepLatex)

	if !quiet {
		line := strings.Repeat("─", 60)
		fmt.Fprintf(w, "%s\n", paint(colorDim, line))
		fmt.Fprintf(w, "%s %s\n", paint(colorBold+colorCyan, "OCR 识别结果"), paint(colorDim, "· "+meta.Origin))
		meta2 := fmt.Sprintf("%d 页 · %s", res.PageCount, res.Purpose)
		if res.Usage != nil {
			meta2 += fmt.Sprintf(" · %d tokens", res.Usage.TotalTokens)
		}
		fmt.Fprintf(w, "%s\n", paint(colorDim, meta2))
		if res.ID != "" {
			fmt.Fprintf(w, "%s\n", paint(colorDim, "id: "+res.ID))
		}
		fmt.Fprintf(w, "%s\n\n", paint(colorDim, line))
	}

	if strings.TrimSpace(text) == "" {
		fmt.Fprintf(w, "%s\n", paint(colorYellow, "（未识别到文字）"))
	} else {
		fmt.Fprintln(w, text)
	}

	if !quiet {
		fmt.Fprintf(w, "\n%s\n", paint(colorDim, strings.Repeat("─", 60)))

		// 提示不完整的页
		for _, p := range res.Pages {
			if p.FinishReason == "length" {
				fmt.Fprintf(w, "%s 第 %d 页输出达到上限，文本不完整\n",
					paint(colorYellow, "注意:"), p.Index+1)
			}
		}
	}
}
