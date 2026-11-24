package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// dialMode 控制拨号时优先使用的地址族。
type dialMode int

const (
	// dialAuto 优先 IPv6，失败再回退 IPv4。
	// 代理软件（Clash/Mihomo 等）TUN 模式的 fake-IP 段（198.18.0.0/15）
	// 会让 IPv4 的 TCP 握手被本地栈直接应答，但 TLS 握手转发不出去而卡死；
	// 优先走 IPv6 可以绕开这个问题。
	dialAuto dialMode = iota
	dialV4
	dialV6
)

func (m dialMode) String() string {
	switch m {
	case dialV4:
		return "IPv4"
	case dialV6:
		return "IPv6"
	default:
		return "自动"
	}
}

// parseDialMode 解析 -net 参数。
func parseDialMode(s string) (dialMode, error) {
	switch s {
	case "", "auto":
		return dialAuto, nil
	case "4", "ipv4", "v4":
		return dialV4, nil
	case "6", "ipv6", "v6":
		return dialV6, nil
	}
	return dialAuto, fmt.Errorf("-net 只能是 auto / 4 / 6，收到 %q", s)
}

// 单个地址的拨号超时。TLS 握手另有 TLSHandshakeTimeout 兜底。
const perAddrDialTimeout = 6 * time.Second

// newDialFunc 返回一个按地址族偏好拨号的 DialContext 实现。
func newDialFunc(mode dialMode) func(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: perAddrDialTimeout, KeepAlive: 30 * time.Second}

	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return dialer.DialContext(ctx, network, addr)
		}

		// 已经是 IP 字面量，直接拨
		if ip := net.ParseIP(host); ip != nil {
			return dialer.DialContext(ctx, network, addr)
		}

		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			// 解析失败就交给系统默认逻辑
			return dialer.DialContext(ctx, network, addr)
		}

		var v6, v4 []net.IPAddr
		for _, ip := range ips {
			if ip.IP.To4() != nil {
				v4 = append(v4, ip)
			} else {
				v6 = append(v6, ip)
			}
		}

		// 按偏好排列候选地址
		var candidates []net.IPAddr
		switch mode {
		case dialV4:
			candidates = v4
		case dialV6:
			candidates = v6
		default: // dialAuto：IPv6 优先
			candidates = append(candidates, v6...)
			candidates = append(candidates, v4...)
		}
		if len(candidates) == 0 {
			// 指定了地址族但解析不到，退回系统默认
			return dialer.DialContext(ctx, network, addr)
		}

		var lastErr error
		for _, ip := range candidates {
			target := net.JoinHostPort(ip.IP.String(), port)
			conn, err := dialer.DialContext(ctx, network, target)
			if err == nil {
				return conn, nil
			}
			lastErr = err
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("无法连接到 %s", host)
		}
		return nil, lastErr
	}
}

// wrapNetError 把底层网络错误翻译成可操作的提示。
func wrapNetError(err error, mode dialMode) error {
	msg := err.Error()

	// TLS 握手超时/失败：多半是代理软件 fake-IP 或防火墙拦了某个地址族
	if strings.Contains(msg, "TLS handshake timeout") ||
		strings.Contains(msg, "tls:") ||
		strings.Contains(msg, "i/o timeout") {
		hint := "网络连接失败，可能是代理软件（Clash/Mihomo 等）的 fake-IP 或防火墙拦截了该地址族。"
		if mode == dialAuto {
			hint += "\n  已尝试 IPv6 优先、IPv4 回退。可试试 -net 6 或 -net 4 指定协议。"
		} else {
			hint += fmt.Sprintf("\n  当前强制使用 %s，可试试 -net auto 或另一种协议。", mode)
		}
		return fmt.Errorf("%s\n  原始错误: %w", hint, err)
	}

	// 连接被重置/EOF：服务端或中间设备提前断开
	if strings.Contains(msg, "EOF") || strings.Contains(msg, "connection reset") {
		return fmt.Errorf("连接被中断（服务端或中间设备提前关闭）。若使用代理，请检查代理规则是否放行 api.deepseek.com\n  原始错误: %w", err)
	}

	return fmt.Errorf("请求失败: %w", err)
}
