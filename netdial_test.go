package main

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

func TestParseDialMode(t *testing.T) {
	cases := []struct {
		in      string
		want    dialMode
		wantErr bool
	}{
		{"", dialAuto, false},
		{"auto", dialAuto, false},
		{"4", dialV4, false},
		{"ipv4", dialV4, false},
		{"v4", dialV4, false},
		{"6", dialV6, false},
		{"ipv6", dialV6, false},
		{"v6", dialV6, false},
		{"bogus", dialAuto, true},
		{"5", dialAuto, true},
	}

	for _, c := range cases {
		got, err := parseDialMode(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseDialMode(%q) 应该报错，却返回 %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseDialMode(%q) 意外报错: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseDialMode(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestDialModeString(t *testing.T) {
	if s := dialAuto.String(); s != "自动" {
		t.Errorf("dialAuto.String() = %q", s)
	}
	if s := dialV4.String(); s != "IPv4" {
		t.Errorf("dialV4.String() = %q", s)
	}
	if s := dialV6.String(); s != "IPv6" {
		t.Errorf("dialV6.String() = %q", s)
	}
}

func TestNewDialFuncIPLiteral(t *testing.T) {
	// IP 字面量应直接拨号，不走 DNS 解析
	dial := newDialFunc(dialAuto)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("无法监听本地端口: %v", err)
	}
	defer ln.Close()

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	conn, err := dial(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("拨号 IP 字面量失败: %v", err)
	}
	conn.Close()
}

func TestNewDialFuncRespectsContext(t *testing.T) {
	dial := newDialFunc(dialAuto)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	// 用一个不可路由的地址，确保不会真的连上
	_, err := dial(ctx, "tcp", "192.0.2.1:9")
	if err == nil {
		t.Fatal("已取消的 context 应该返回错误")
	}
}

func TestWrapNetErrorTLS(t *testing.T) {
	err := errors.New(`Post "https://api.deepseek.com/v1/tools/ocr": net/http: TLS handshake timeout`)

	// auto 模式：提示两种协议都试过了
	msg := wrapNetError(err, dialAuto).Error()
	if !strings.Contains(msg, "代理软件") {
		t.Errorf("应提示代理软件问题，got: %s", msg)
	}
	if !strings.Contains(msg, "-net 6") || !strings.Contains(msg, "-net 4") {
		t.Errorf("auto 模式应提示 -net 6 / -net 4，got: %s", msg)
	}

	// 强制 IPv4：提示换协议
	msg4 := wrapNetError(err, dialV4).Error()
	if !strings.Contains(msg4, "IPv4") {
		t.Errorf("应说明当前是 IPv4，got: %s", msg4)
	}
	if !strings.Contains(msg4, "-net auto") {
		t.Errorf("应提示 -net auto，got: %s", msg4)
	}
}

func TestWrapNetErrorEOF(t *testing.T) {
	err := errors.New(`Post "https://api.deepseek.com/v1/tools/ocr": EOF`)
	msg := wrapNetError(err, dialAuto).Error()
	if !strings.Contains(msg, "连接被中断") {
		t.Errorf("EOF 应提示连接被中断，got: %s", msg)
	}
	if !strings.Contains(msg, "api.deepseek.com") {
		t.Errorf("应提示检查代理规则，got: %s", msg)
	}
}

func TestWrapNetErrorOther(t *testing.T) {
	err := errors.New("some other error")
	msg := wrapNetError(err, dialAuto).Error()
	if !strings.Contains(msg, "请求失败") {
		t.Errorf("其他错误应保持通用提示，got: %s", msg)
	}
}
