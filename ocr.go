package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

const (
	maxFileSize = 100 << 20 // 100 MiB
	maxPages    = 1000
)

// OCRConfig 是调用 OCR 接口所需的配置。
type OCRConfig struct {
	BaseURL  string
	APIKey   string
	Purpose  string
	DialMode dialMode
}

// OCRPage 是单页识别结果。
type OCRPage struct {
	Index        int    `json:"index"`
	Text         string `json:"text"`
	FinishReason string `json:"finish_reason"`
}

// OCRUsage 是 token 用量，仅供参考。
type OCRUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// APIError 是服务端返回的错误对象。
type APIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   any    `json:"param"`
	Code    string `json:"code"`
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("%s (%s)", e.Message, e.Code)
	}
	return e.Message
}

// OCRResult 是一次识别的完整结果。
type OCRResult struct {
	ID        string    `json:"id"`
	Object    string    `json:"object"`
	Created   int64     `json:"created"`
	Purpose   string    `json:"purpose"`
	PageCount int       `json:"page_count"`
	Pages     []OCRPage `json:"pages"`
	Usage     *OCRUsage `json:"usage"`
	Error     *APIError `json:"error"`

	Raw json.RawMessage `json:"-"`
}

// uploadSource 抽象上传内容：内存字节或磁盘文件。
type uploadSource struct {
	Filename string
	MIME     string
	Size     int64
	open     func() (io.ReadCloser, error)
}

func sourceFromBytes(name, mime string, data []byte) *uploadSource {
	return &uploadSource{
		Filename: name,
		MIME:     mime,
		Size:     int64(len(data)),
		open:     func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil },
	}
}

func sourceFromFile(path, name, mime string, size int64) *uploadSource {
	return &uploadSource{
		Filename: name,
		MIME:     mime,
		Size:     size,
		open:     func() (io.ReadCloser, error) { return os.Open(path) },
	}
}

// doOCR 调用 POST /v1/tools/ocr，对暂时性错误自动重试。
func doOCR(ctx context.Context, cfg OCRConfig, src *uploadSource) (*OCRResult, error) {
	if src.Size > maxFileSize {
		return nil, fmt.Errorf("文件 %.1f MiB 超过 100 MiB 上限", float64(src.Size)/(1<<20))
	}

	const maxAttempts = 3
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			wait := time.Duration(attempt-1) * 3 * time.Second
			fmt.Fprintf(os.Stderr, "%s %v 后重试（第 %d/%d 次）\n",
				paint(colorYellow, "重试:"), wait, attempt, maxAttempts)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		res, err := doOCROnce(ctx, cfg, src)
		if err == nil {
			return res, nil
		}
		lastErr = err

		var apiErr *APIError
		if errors.As(err, &apiErr) && !isRetryable(apiErr) {
			return nil, err
		}
		var httpErr *httpStatusError
		if errors.As(err, &httpErr) && !httpErr.retryable {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("重试 %d 次后仍失败: %w", maxAttempts, lastErr)
}

func doOCROnce(ctx context.Context, cfg OCRConfig, src *uploadSource) (*OCRResult, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)

	// 后台写 multipart 请求体，主协程负责发请求，避免大文件全量进内存。
	writeErr := make(chan error, 1)
	go func() {
		defer close(writeErr)
		defer pw.Close()

		// 只允许 file 与 purpose 两个字段，多传会 400。
		if err := mw.WriteField("purpose", cfg.Purpose); err != nil {
			writeErr <- err
			return
		}
		h := make(map[string][]string)
		h["Content-Disposition"] = []string{
			fmt.Sprintf(`form-data; name="file"; filename="%s"`, escapeQuotes(src.Filename)),
		}
		h["Content-Type"] = []string{src.MIME}
		part, err := mw.CreatePart(h)
		if err != nil {
			writeErr <- err
			return
		}
		f, err := src.open()
		if err != nil {
			writeErr <- err
			return
		}
		defer f.Close()

		var sent atomic.Int64
		buf := make([]byte, 256*1024)
		for {
			n, rerr := f.Read(buf)
			if n > 0 {
				if _, werr := part.Write(buf[:n]); werr != nil {
					writeErr <- werr
					return
				}
				sent.Add(int64(n))
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				writeErr <- rerr
				return
			}
		}
		writeErr <- mw.Close()
	}()

	url := strings.TrimRight(cfg.BaseURL, "/") + "/v1/tools/ocr"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, pr)
	if err != nil {
		pr.CloseWithError(err)
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "clipocr/1.0")

	client := &http.Client{
		// 不设整体 Timeout，交给 ctx 控制（识别可能持续 180 秒）。
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           newDialFunc(cfg.DialMode),
			MaxIdleConns:          4,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   15 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ForceAttemptHTTP2:     true,
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		pr.CloseWithError(err)
		<-writeErr
		if ctx.Err() == context.DeadlineExceeded {
			return nil, errors.New("请求超时：OCR 总时长上限 180 秒，请换更小的文件")
		}
		return nil, wrapNetError(err, cfg.DialMode)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}
	// 上传协程可能因服务端提前返回而报错，这里只取第一个错误，忽略管道关闭类噪音。
	if werr := <-writeErr; werr != nil && resp.StatusCode == http.StatusOK {
		if !errors.Is(werr, io.ErrClosedPipe) && !strings.Contains(werr.Error(), "pipe") {
			return nil, fmt.Errorf("上传失败: %w", werr)
		}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, newHTTPStatusError(resp.StatusCode, body)
	}

	// 响应体前面可能有换行符（保活），属于 JSON 前导空白，可直接解析。
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, errors.New("服务端返回空响应")
	}

	var res OCRResult
	if err := json.Unmarshal(trimmed, &res); err != nil {
		snippet := string(trimmed)
		if len(snippet) > 300 {
			snippet = snippet[:300] + "..."
		}
		return nil, fmt.Errorf("解析响应失败: %w（响应片段: %s）", err, snippet)
	}
	res.Raw = append(json.RawMessage(nil), trimmed...)

	// 识别开始后状态码是 200，失败时响应体是 error 对象。
	if res.Error != nil {
		return nil, res.Error
	}
	if res.PageCount == 0 && len(res.Pages) == 0 {
		return nil, errors.New("服务端未返回任何页面结果")
	}
	if res.PageCount > maxPages {
		return nil, fmt.Errorf("文档超过 %d 页（%d 页）", maxPages, res.PageCount)
	}
	return &res, nil
}

// httpStatusError 表示上传阶段的 HTTP 错误。
type httpStatusError struct {
	Status    int
	Message   string
	Code      string
	retryable bool
}

func (e *httpStatusError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, http.StatusText(e.Status))
}

func newHTTPStatusError(status int, body []byte) error {
	msg, code := "", ""
	var payload struct {
		Error *APIError `json:"error"`
	}
	if json.Unmarshal(bytes.TrimSpace(body), &payload) == nil && payload.Error != nil {
		msg, code = payload.Error.Message, payload.Error.Code
	}
	if msg == "" {
		msg = strings.TrimSpace(string(body))
		if len(msg) > 300 {
			msg = msg[:300] + "..."
		}
	}

	e := &httpStatusError{Status: status, Message: msg, Code: code}
	switch status {
	case http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		e.retryable = true
	}
	return e
}

// isRetryable 判断服务端 error 对象是否属于「稍后重试即可」的暂时性错误。
func isRetryable(e *APIError) bool {
	msg := strings.ToLower(e.Message)
	code := strings.ToLower(e.Code)

	// 明确不可重试的情况
	nonRetryable := []string{
		"password-protected",
		"could not be parsed",
		"format is not supported",
		"image could not be decoded",
		"more than 1000 pages",
		"file is too large",
	}
	for _, s := range nonRetryable {
		if strings.Contains(msg, s) {
			return false
		}
	}

	retryable := []string{
		"temporarily unavailable",
		"internal server error",
		"did not finish in time",
		"did not finish within",
		"gateway_timeout",
		"timeout",
		"rate limit",
		"busy",
	}
	for _, s := range retryable {
		if strings.Contains(msg, s) || strings.Contains(code, s) {
			return true
		}
	}
	return false
}

// plainText 把各页文本拼成纯文本。
// raw=false 时去掉 <｜ref｜>/<｜box｜> 等标记；keepLatex=false 时把 LaTeX 转成纯文本。
func plainText(res *OCRResult, raw, keepLatex bool) string {
	var b strings.Builder
	multi := len(res.Pages) > 1
	for _, p := range res.Pages {
		text := p.Text
		if !raw {
			text = stripOCRMarks(text)
		}
		if !keepLatex {
			text = latexToPlain(text)
		}
		text = strings.TrimRight(text, " \t\r\n")
		if text == "" {
			continue
		}
		if multi {
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			fmt.Fprintf(&b, "── 第 %d 页 ──\n", p.Index+1)
		}
		b.WriteString(text)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// stripOCRMarks 去掉 <｜ref｜>…<｜/ref｜>、<｜box｜>…<｜/box｜> 等标记，只留文字。
func stripOCRMarks(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == '<' {
			// 形如 <｜...｜> 的标记
			if j := strings.Index(s[i:], "｜>"); j > 0 && strings.Contains(s[i:i+j], "｜") {
				i += j + len("｜>")
				continue
			}
			// 形如 <ref> / </ref> / <box> / </box>
			if j := strings.IndexByte(s[i:], '>'); j > 0 && j < 24 {
				inner := strings.ToLower(s[i+1 : i+j])
				inner = strings.TrimPrefix(inner, "/")
				if inner == "ref" || inner == "box" || inner == "caption" || inner == "table" || inner == "figure" {
					i += j + 1
					continue
				}
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func escapeQuotes(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}

// ensure filepath is used even if future edits drop it
var _ = filepath.Base
