package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// defaultCredentialsPath 返回 ~/.dsh/.credentials.yaml。
func defaultCredentialsPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".dsh", ".credentials.yaml")
}

// loadAPIKey 从 DSH 凭据文件里读取 refs.DEEPSEEK_API_KEY。
// 环境变量 DEEPSEEK_API_KEY 优先，方便临时覆盖。
func loadAPIKey(path string) (string, error) {
	if v := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY")); v != "" {
		return v, nil
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("凭据文件不存在: %s（可用 -key-file 指定，或设置 DEEPSEEK_API_KEY）", path)
		}
		return "", fmt.Errorf("打开凭据文件失败: %w", err)
	}
	defer f.Close()

	key, err := parseRefsKey(f, "DEEPSEEK_API_KEY")
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return key, nil
}

// parseRefsKey 只解析 refs: 段下的顶层键值，够用且不引入 YAML 依赖。
func parseRefsKey(f *os.File, want string) (string, error) {
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	inRefs := false
	refsIndent := -1
	found := false
	var value string

	for sc.Scan() {
		raw := sc.Text()
		line := stripComment(raw)
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		trimmed := strings.TrimSpace(line)

		if !inRefs {
			if indent == 0 && strings.HasPrefix(trimmed, "refs:") {
				inRefs = true
				refsIndent = indent
			}
			continue
		}

		// 离开 refs 段（出现同级或更浅缩进的键）
		if indent <= refsIndent {
			break
		}

		k, v, ok := splitYAMLPair(trimmed)
		if !ok {
			continue
		}
		if k == want {
			found = true
			value = v
			break
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("读取失败: %w", err)
	}
	if !found {
		return "", fmt.Errorf("未找到 refs.%s", want)
	}
	if value == "" {
		return "", fmt.Errorf("refs.%s 为空", want)
	}
	return value, nil
}

// splitYAMLPair 拆分 "KEY: value"，并去掉包裹的引号。
func splitYAMLPair(s string) (key, value string, ok bool) {
	i := strings.Index(s, ":")
	if i <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(s[:i])
	value = strings.TrimSpace(s[i+1:])
	value = unquoteYAML(value)
	return key, value, true
}

func unquoteYAML(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			inner := v[1 : len(v)-1]
			if v[0] == '"' {
				inner = strings.ReplaceAll(inner, `\"`, `"`)
				inner = strings.ReplaceAll(inner, `\\`, `\`)
			} else {
				inner = strings.ReplaceAll(inner, "''", "'")
			}
			return inner
		}
	}
	return v
}

// stripComment 去掉行尾注释，但保留引号内的 #。
func stripComment(line string) string {
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '#':
			if i == 0 || line[i-1] == ' ' || line[i-1] == '\t' {
				return strings.TrimRight(line[:i], " \t")
			}
		}
	}
	return line
}
