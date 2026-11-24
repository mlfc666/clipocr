// clipocr 读取剪贴板中的图片，校验后调用 DeepSeek OCR 接口识别，
// 把结果打印到终端并自动复制回剪贴板。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultBaseURL = "https://api.deepseek.com"
	purposeText    = "text_recognition"
	purposeLayout  = "rich_layout_recognition"
)

// 由 GoReleaser 在构建时通过 -ldflags 注入。
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s %v\n", paint(colorRed+colorBold, "错误:"), err)
		os.Exit(1)
	}
}

func run() error {
	showVersion := flag.Bool("version", false, "显示版本信息")
	filePath := flag.String("file", "", "直接识别指定文件（PDF/Word/PPT/图片），默认读取剪贴板图片")
	purpose := flag.String("purpose", purposeText, "识别用途：text_recognition 或 rich_layout_recognition")
	savePath := flag.String("save", "", "把剪贴板图片另存到指定路径（默认存到缓存目录）")
	rawMode := flag.Bool("raw", false, "保留 <｜ref｜>/<｜box｜> 等标记与坐标")
	keepLatex := flag.Bool("latex", false, "保留 LaTeX 原文（默认转成纯文本，如 $d_{\\text{dev}}$ → d_dev）")
	noCopy := flag.Bool("no-copy", false, "不把识别结果复制到剪贴板")
	jsonOut := flag.Bool("json", false, "输出服务端原始 JSON")
	quiet := flag.Bool("q", false, "安静模式：只输出识别文本")
	keyFile := flag.String("key-file", "", "凭据文件路径（默认 ~/.dsh/.credentials.yaml）")
	timeout := flag.Duration("timeout", 200*time.Second, "整体超时时间")
	baseURL := flag.String("base-url", defaultBaseURL, "API 基地址")
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Printf("clipocr %s (commit %s, built %s)\n", version, commit, date)
		return nil
	}

	if *purpose != purposeText && *purpose != purposeLayout {
		return fmt.Errorf("purpose 只能是 %s 或 %s，收到 %q", purposeText, purposeLayout, *purpose)
	}

	// ---------- 1. 取得待识别内容 ----------
	var (
		data   []byte
		name   string
		ctype  string
		origin string
		info   *ImageInfo
		src    *uploadSource
	)

	if *filePath != "" {
		p := expandPath(*filePath)
		fi, err := os.Stat(p)
		if err != nil {
			return fmt.Errorf("读取文件失败: %w", err)
		}
		if fi.IsDir() {
			return fmt.Errorf("%s 是目录，不是文件", p)
		}
		if fi.Size() > maxFileSize {
			return fmt.Errorf("文件 %.1f MiB 超过 100 MiB 上限", float64(fi.Size())/(1<<20))
		}
		name = filepath.Base(p)
		ctype = contentTypeForName(name)
		origin = "文件 " + p
		src = sourceFromFile(p, name, ctype, fi.Size())

		// 图片文件额外做一次完整性校验；PDF/Office 交给服务端判断。
		// 扩展名声称是图片但内容不是图片时，本地直接拦下，不浪费一次请求。
		if head, err := readHead(p, 512); err == nil {
			if looksLikeImage(head) {
				b, err := os.ReadFile(p)
				if err != nil {
					return fmt.Errorf("读取文件失败: %w", err)
				}
				info, err = validateImage(b)
				if err != nil {
					return fmt.Errorf("图片校验失败: %w", err)
				}
			} else if isImageExt(name) {
				return fmt.Errorf("图片校验失败: %s 的扩展名是图片，但文件内容不是可识别的图片格式（支持 PNG / JPEG / GIF / WebP / BMP / TIFF）", name)
			}
		}
	} else {
		img, err := readClipboardImage()
		if err != nil {
			return err
		}
		data = img.Data
		name = "clipboard" + extForMIME(img.MIME)
		ctype = img.MIME
		origin = "剪贴板 · " + img.Source
		info, err = validateImage(data)
		if err != nil {
			return fmt.Errorf("剪贴板内容不是有效图片: %w", err)
		}
		src = sourceFromBytes(name, ctype, data)
	}

	// ---------- 2. 备份剪贴板图片（结果会覆盖剪贴板，先留一份） ----------
	savedPath := ""
	if data != nil {
		p := *savePath
		if p == "" {
			p = defaultImageSavePath(name)
		} else {
			p = expandPath(p)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			warnf("创建目录失败: %v", err)
		} else if err := os.WriteFile(p, data, 0o644); err != nil {
			warnf("保存图片失败: %v", err)
		} else {
			savedPath = p
			pruneOldImages(filepath.Dir(p), 20)
		}
	}

	// ---------- 3. 读取 API Key ----------
	keyPath := *keyFile
	if keyPath == "" {
		keyPath = defaultCredentialsPath()
	} else {
		keyPath = expandPath(keyPath)
	}
	apiKey, err := loadAPIKey(keyPath)
	if err != nil {
		return err
	}

	// ---------- 4. 打印元信息（走 stderr，方便 stdout 管道） ----------
	if !*quiet {
		fmt.Fprintf(os.Stderr, "%s %s\n", paint(colorDim, "来源:"), origin)
		if info != nil {
			fmt.Fprintf(os.Stderr, "%s %s %s\n", paint(colorDim, "图片:"), info.Summary(), paint(colorGreen, "✓ 校验通过"))
		}
		if savedPath != "" {
			fmt.Fprintf(os.Stderr, "%s %s\n", paint(colorDim, "已存图片:"), savedPath)
		}
	}

	// ---------- 5. 调用 OCR ----------
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	stop := startSpinner(os.Stderr, "上传并识别中")
	res, err := doOCR(ctx, OCRConfig{BaseURL: *baseURL, APIKey: apiKey, Purpose: *purpose}, src)
	stop()
	if err != nil {
		return err
	}

	// ---------- 6. 输出 ----------
	if *jsonOut {
		var buf bytes.Buffer
		if err := json.Indent(&buf, res.Raw, "", "  "); err != nil {
			buf.Write(res.Raw)
		}
		fmt.Println(buf.String())
	} else {
		printResult(os.Stdout, res, resultMeta{Origin: origin, Info: info, SavedPath: savedPath}, *rawMode, *keepLatex, *quiet)
	}

	// ---------- 7. 复制结果 ----------
	text := plainText(res, *rawMode, *keepLatex)
	if *noCopy {
		return nil
	}
	if strings.TrimSpace(text) == "" {
		warnf("识别结果为空，跳过复制")
		return nil
	}
	cctx, ccancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer ccancel()
	how, err := writeClipboardText(cctx, text)
	if err != nil {
		warnf("复制到剪贴板失败: %v", err)
		return nil
	}
	if !*quiet {
		fmt.Fprintf(os.Stderr, "%s %s\n", paint(colorGreen, "已复制到剪贴板"),
			paint(colorDim, fmt.Sprintf("(%s, %d 字符)", how, utf8.RuneCountInString(text))))
	}
	return nil
}

func usage() {
	out := flag.CommandLine.Output()
	fmt.Fprintf(out, `clipocr — 读取剪贴板图片，调用 DeepSeek OCR 识别，结果打印到终端并自动复制

用法:
  clipocr [选项]

选项:
`)
	flag.PrintDefaults()
	fmt.Fprintf(out, `
示例:
  clipocr                                   # 识别剪贴板里的图片
  clipocr -purpose rich_layout_recognition  # 逐页富版面识别
  clipocr -file 合同.pdf                     # 识别 PDF / Word / PPT
  clipocr -q | wc -c                        # 只输出文本，便于管道
  clipocr -no-copy                          # 只看结果，不动剪贴板
  clipocr -latex                            # 保留 LaTeX 原文
`)
}

// ---------- 小工具 ----------

func expandPath(p string) string {
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func readHead(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	m, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	return buf[:m], nil
}

func contentTypeForName(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	case ".tif", ".tiff":
		return "image/tiff"
	case ".pdf":
		return "application/pdf"
	case ".doc":
		return "application/msword"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".ppt":
		return "application/vnd.ms-powerpoint"
	case ".pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	}
	return "application/octet-stream"
}

func isImageExt(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tif", ".tiff":
		return true
	}
	return false
}

func extForMIME(mime string) string {
	switch mime {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/bmp":
		return ".bmp"
	case "image/tiff", "image/tif":
		return ".tiff"
	}
	return ".img"
}

func defaultImageSavePath(name string) string {
	dir, err := os.UserCacheDir()
	if err != nil || dir == "" {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, "clipocr")
	ext := filepath.Ext(name)
	if ext == "" {
		ext = ".png"
	}
	return filepath.Join(dir, time.Now().Format("20060102-150405.000")+ext)
}

// pruneOldImages 只保留最近 keep 张备份图片，避免缓存目录无限增长。
func pruneOldImages(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type item struct {
		name string
		mod  time.Time
	}
	var files []item
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, item{e.Name(), fi.ModTime()})
	}
	if len(files) <= keep {
		return
	}
	// 按修改时间从旧到新排序
	for i := 1; i < len(files); i++ {
		for j := i; j > 0 && files[j].mod.Before(files[j-1].mod); j-- {
			files[j], files[j-1] = files[j-1], files[j]
		}
	}
	for _, f := range files[:len(files)-keep] {
		_ = os.Remove(filepath.Join(dir, f.name))
	}
}
