package main

import (
	"bytes"
	"fmt"
	"image"

	// 注册解码器：标准库 + x/image 扩展格式
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// 像素上限：超过 1 亿像素直接拒绝，避免解码时吃光内存。
const maxPixels = 100_000_000

// 完整解码校验的像素上限：再大就只校验文件头，防止 OOM。
const fullDecodePixels = 40_000_000

// ImageInfo 描述一张通过校验的图片。
type ImageInfo struct {
	Format string // 由文件头嗅探出的格式，如 png / jpeg
	MIME   string
	Width  int
	Height int
	Size   int64
}

func (i *ImageInfo) Summary() string {
	return fmt.Sprintf("%s, %d×%d, %s", upper(i.Format), i.Width, i.Height, humanSize(i.Size))
}

// sniffImage 通过文件头魔数判断格式，不依赖扩展名。
func sniffImage(data []byte) (format, mime string, ok bool) {
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		return "png", "image/png", true
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "jpeg", "image/jpeg", true
	case len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return "gif", "image/gif", true
	case len(data) >= 2 && data[0] == 'B' && data[1] == 'M':
		return "bmp", "image/bmp", true
	case len(data) >= 4 && (bytes.Equal(data[:4], []byte{'I', 'I', 0x2A, 0x00}) ||
		bytes.Equal(data[:4], []byte{'M', 'M', 0x00, 0x2A})):
		return "tiff", "image/tiff", true
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "webp", "image/webp", true
	}
	return "", "", false
}

func looksLikeImage(data []byte) bool {
	_, _, ok := sniffImage(data)
	return ok
}

// validateImage 校验一段字节是否是一张正常、可解码的图片。
func validateImage(data []byte) (*ImageInfo, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("内容为空")
	}
	format, mime, ok := sniffImage(data)
	if !ok {
		return nil, fmt.Errorf("不是可识别的图片格式（支持 PNG / JPEG / GIF / WebP / BMP / TIFF）")
	}

	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("图片头解析失败，文件可能已损坏: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, fmt.Errorf("图片尺寸非法: %d×%d", cfg.Width, cfg.Height)
	}
	px := int64(cfg.Width) * int64(cfg.Height)
	if px > maxPixels {
		return nil, fmt.Errorf("图片像素过多（%d×%d，约 %.0f 万像素），超过 1 亿像素上限", cfg.Width, cfg.Height, float64(px)/10000)
	}
	if cfg.Width < 8 || cfg.Height < 8 {
		return nil, fmt.Errorf("图片过小（%d×%d），可能不是正常截图", cfg.Width, cfg.Height)
	}

	// 完整解码一遍，确认数据没有截断/损坏（大图跳过，避免占用过多内存）。
	if px <= fullDecodePixels {
		if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
			return nil, fmt.Errorf("图片解码失败，数据不完整或已损坏: %w", err)
		}
	}

	return &ImageInfo{Format: format, MIME: mime, Width: cfg.Width, Height: cfg.Height, Size: int64(len(data))}, nil
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func upper(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'a' && b[i] <= 'z' {
			b[i] -= 'a' - 'A'
		}
	}
	return string(b)
}
