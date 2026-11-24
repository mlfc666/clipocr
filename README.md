# clipocr

读取剪贴板里的图片 → 校验是否为正常图片 → 调用 DeepSeek OCR 识别 → 结果打印到终端并自动复制回剪贴板。

## 安装

```bash
cd clipocr
go build -o clipocr .
# 可选：装到 PATH
install -Dm755 clipocr ~/.local/bin/clipocr
```

依赖：Go 1.21+；Wayland 需要 `wl-clipboard`（`wl-paste`/`wl-copy`），X11 需要 `xclip`。

## 用法

```bash
clipocr                                   # 识别剪贴板里的图片
clipocr -purpose rich_layout_recognition  # 逐页富版面识别（保留版面结构）
clipocr -file 合同.pdf                     # 识别 PDF / Word / PPT / 图片
clipocr -q | wc -c                        # 只输出纯文本，便于管道
clipocr -no-copy                          # 只看结果，不动剪贴板
clipocr -json                             # 输出服务端原始 JSON
clipocr -raw                              # 保留 <｜ref｜>/<｜box｜> 标记与坐标
clipocr -latex                            # 保留 LaTeX 原文（默认转纯文本）
```

### 选项

| 选项 | 说明 |
| --- | --- |
| `-file` | 直接识别指定文件，默认读剪贴板图片 |
| `-purpose` | `text_recognition`（默认）或 `rich_layout_recognition` |
| `-save` | 剪贴板图片另存路径，默认 `~/.cache/clipocr/<时间戳>.png` |
| `-raw` | 保留 OCR 标记与坐标 |
| `-latex` | 保留 LaTeX 原文；默认转成纯文本 |
| `-no-copy` | 不把结果复制到剪贴板 |
| `-json` | 输出原始 JSON |
| `-q` | 安静模式，stdout 只有识别文本 |
| `-key-file` | 凭据文件路径，默认 `~/.dsh/.credentials.yaml` |
| `-timeout` | 整体超时，默认 200s |
| `-base-url` | API 基地址，默认 `https://api.deepseek.com` |

## LaTeX 转纯文本

默认开启，把 OCR 输出里的公式转成可读文本：

| 原文 | 转换后 |
| --- | --- |
| `$d_{\text{dev}}$` | `d_dev` |
| `$3.0\text{m}$` | `3.0m` |
| `$\frac{a}{b}$` | `a/b` |
| `$\sqrt{x}$` | `√x` |
| `$x^{2}$` | `x^2` |
| `$\alpha + \beta$` | `α + β` |
| `$A \to B$` | `A → B` |
| `$\sum_{i=1}^{n} x_i$` | `∑_i=1^n x_i` |
| `$\mathbb{R}$` | `R` |
| `$\left( \frac{1}{2} \right)$` | `( 1/2 )` |

只转换能确定是公式的部分，以下内容原样保留：

- 价格 `$5 元`、`$12.50`（`$...$` 里没有公式特征）
- Windows 路径 `C:\Users\test\file.txt`（未知命令原样保留）
- 普通文本里的 `_`、`^`、`{`、`}`

需要原文时加 `-latex`。转换是幂等的，重复处理不会继续变化。

## API Key

按顺序取：

1. 环境变量 `DEEPSEEK_API_KEY`
2. `~/.dsh/.credentials.yaml` 里的 `refs.DEEPSEEK_API_KEY`

```yaml
refs:
  DEEPSEEK_API_KEY: sk-xxxx
```

## 图片校验

剪贴板内容会经过三道检查，任一不过直接报错、不发请求：

1. **魔数嗅探** —— 按文件头判断真实格式（PNG/JPEG/GIF/WebP/BMP/TIFF），不看扩展名
2. **头部解析** —— `image.DecodeConfig` 读出宽高，拒绝 0 尺寸、小于 8×8、超过 1 亿像素
3. **完整解码** —— 4000 万像素以内再完整解码一遍，能抓出被截断/损坏的文件

`-file` 模式下，扩展名是图片但内容不是图片的，同样在本地拦下。

## 行为说明

- 识别结果会**覆盖剪贴板**，所以剪贴板图片会先备份到 `~/.cache/clipocr/`（只留最近 20 张）
- 元信息、进度、提示都走 stderr，stdout 只有识别文本，方便 `clipocr -q > out.txt`
- 暂时性错误（503、429、超时、服务内部错误）自动重试 3 次；密码保护、格式不支持、超过 1000 页等不重试
- 响应体前面的换行符是服务端保活用的，属于 JSON 前导空白，直接解析
- `finish_reason` 为 `length` 的页会在末尾提示「文本不完整」

## 限制（服务端）

| 限制 | 取值 |
| --- | --- |
| 文件大小 | 100 MiB |
| 页数 | 1000 页 |
| 上传 | 120 秒内完成，中途停顿不超过 30 秒 |
| 总时长 | 180 秒 |
| 并发 | 每账号 15 个识别 |
