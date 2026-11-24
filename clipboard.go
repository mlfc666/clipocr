package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ClipboardImage 是从剪贴板读到的图片。
type ClipboardImage struct {
	Data   []byte
	MIME   string
	Source string // 例如 "wl-paste (Wayland)"
}

// 剪贴板里可能出现的图片 MIME，按优先级排列（越靠前越优先）。
var clipboardImageMIMEs = []string{
	"image/png",
	"image/jpeg",
	"image/webp",
	"image/gif",
	"image/bmp",
	"image/tiff",
}

// readClipboardImage 读取剪贴板中的图片；剪贴板没有图片时给出明确提示。
func readClipboardImage() (*ClipboardImage, error) {
	backend, err := detectClipboardBackend()
	if err != nil {
		return nil, err
	}

	types, err := backend.listTypes()
	if err != nil {
		return nil, fmt.Errorf("读取剪贴板类型失败: %w", err)
	}
	if len(types) == 0 {
		return nil, errors.New("剪贴板为空，请先复制一张图片")
	}

	// 找出剪贴板里可用的图片类型
	var candidates []string
	for _, want := range clipboardImageMIMEs {
		for _, t := range types {
			if strings.EqualFold(t, want) {
				candidates = append(candidates, want)
				break
			}
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("剪贴板里没有图片（当前类型: %s），请先复制一张图片", strings.Join(types, ", "))
	}

	var lastErr error
	for _, mime := range candidates {
		data, err := backend.read(mime)
		if err != nil {
			lastErr = err
			continue
		}
		if len(data) == 0 {
			lastErr = fmt.Errorf("%s 数据为空", mime)
			continue
		}
		return &ClipboardImage{Data: data, MIME: mime, Source: backend.name()}, nil
	}
	if lastErr == nil {
		lastErr = errors.New("未知原因")
	}
	return nil, fmt.Errorf("读取剪贴板图片失败: %w", lastErr)
}

// writeClipboardText 把文本写回剪贴板，返回使用的后端名称。
func writeClipboardText(ctx context.Context, text string) (string, error) {
	backend, err := detectClipboardBackend()
	if err != nil {
		return "", err
	}
	if err := backend.writeText(ctx, text); err != nil {
		return "", err
	}
	return backend.name(), nil
}

// ---------- 后端抽象 ----------

type clipboardBackend interface {
	name() string
	listTypes() ([]string, error)
	read(mime string) ([]byte, error)
	writeText(ctx context.Context, text string) error
}

func detectClipboardBackend() (clipboardBackend, error) {
	wayland := os.Getenv("WAYLAND_DISPLAY") != ""
	hasWayland := hasCommand("wl-paste") && hasCommand("wl-copy")
	hasX11 := hasCommand("xclip")

	switch {
	case wayland && hasWayland:
		return waylandBackend{}, nil
	case hasX11:
		return xclipBackend{}, nil
	case hasWayland:
		return waylandBackend{}, nil
	}
	return nil, errors.New("未找到剪贴板工具：Wayland 需要 wl-clipboard（wl-paste/wl-copy），X11 需要 xclip")
}

func hasCommand(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// ---------- Wayland ----------

type waylandBackend struct{}

func (waylandBackend) name() string { return "wl-paste (Wayland)" }

func (waylandBackend) listTypes() ([]string, error) {
	out, err := runWithTimeout(5*time.Second, "wl-paste", "--list-types")
	if err != nil {
		return nil, err
	}
	var types []string
	for _, line := range strings.Split(string(out), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			types = append(types, s)
		}
	}
	return types, nil
}

func (waylandBackend) read(mime string) ([]byte, error) {
	return runWithTimeout(15*time.Second, "wl-paste", "--no-newline", "--type", mime)
}

func (waylandBackend) writeText(ctx context.Context, text string) error {
	// wl-copy 会 fork 一个守护进程持有选区，父进程可能一直不退出，
	// 因此不接管道、不等待退出，只等它把数据接走。
	cmd := exec.CommandContext(ctx, "wl-copy", "--type", "text/plain;charset=utf-8")
	cmd.Stdin = strings.NewReader(text)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		// 守护进程已接管数据，视为成功
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ---------- X11 ----------

type xclipBackend struct{}

func (xclipBackend) name() string { return "xclip (X11)" }

func (xclipBackend) listTypes() ([]string, error) {
	out, err := runWithTimeout(5*time.Second, "xclip", "-selection", "clipboard", "-t", "TARGETS", "-o")
	if err != nil {
		// 剪贴板为空时 xclip 会报错，这里当作空剪贴板处理
		return nil, nil
	}
	var types []string
	for _, line := range strings.Split(string(out), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			types = append(types, s)
		}
	}
	return types, nil
}

func (xclipBackend) read(mime string) ([]byte, error) {
	return runWithTimeout(15*time.Second, "xclip", "-selection", "clipboard", "-t", mime, "-o")
}

func (xclipBackend) writeText(ctx context.Context, text string) error {
	cmd := exec.CommandContext(ctx, "xclip", "-selection", "clipboard")
	cmd.Stdin = strings.NewReader(text)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return nil
	case <-time.After(3 * time.Second):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ---------- 通用 ----------

func runWithTimeout(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("%s 超时（%s）", name, timeout)
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("%s: %s", name, msg)
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return stdout.Bytes(), nil
}
