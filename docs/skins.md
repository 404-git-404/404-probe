# 皮肤文件

皮肤可以覆盖浅色和深色模式中的颜色变量。在「设置 → 外观」可下载「示例 · 冷灰蓝」，也可从下列结构开始。

```json
{
  "format": "404-probe-skin",
  "version": 1,
  "name": "我的皮肤",
  "author": "作者",
  "description": "皮肤说明",
  "light": { "--color-page": "#F7F4EF" },
  "dark": { "--color-page": "#0E1012" }
}
```

文件须为 UTF-8 JSON，最多 64 KB。`format` 固定为 `404-probe-skin`，`version` 为数字 `1`。`name` 为 1–40 个字符，`author` 最多 40 个字符，`description` 最多 200 个字符。说明和作者可以省略。其他顶层字段会被忽略。

`light` 和 `dark` 可只写需要覆盖的变量，也可以省略其中一个。至少填写一个有效变量。每个值最多 64 个字符，支持以下形式。

- 十六进制色值，如 `#abc`、`#abcd`、`#aabbcc`、`#aabbccdd`。
- `rgb()`、`rgba()`、`hsl()`、`hsla()`，如 `rgb(36 51 70 / 90%)`、`rgba(36, 51, 70, 0.9)`、`hsl(210deg 30% 40%)`。
- `transparent`。

Server 启动时从内嵌 `style.css` 的 `:root` 和 `:root[data-theme=dark]` 提取 `--color-*` 与 `--tag-*` 变量作为白名单。导入会报告被忽略的未知变量。已知变量的值如不合法，Server 会退回整份文件，并指出模式、变量名和错误原因。

## 核心变量

| 变量 | 用途 | 浅色默认值 | 深色默认值 |
|---|---|---|---|
| `--color-page` | 页面底色 | `#F7F4EF` | `#0E1012` |
| `--color-card` | 卡片底色 | `#FFFDFA` | `#16191C` |
| `--color-surface-hover` | 悬停背景 | `#EFE9E0` | `#1C2024` |
| `--color-line` | 分隔线和边框 | `#E4DDD2` | `#2A2F34` |
| `--color-text` | 正文颜色 | `#26221E` | `#E7E4DF` |
| `--color-text-muted` | 次要文字 | `#71685E` | `#949AA1` |
| `--color-accent` | 强调色 | `#2A6F63` | `#55B89D` |
| `--color-accent-text` | 强调文字 | `#2A6F63` | `#55B89D` |
| `--color-success` | 成功状态 | `#3C8A6E` | `#4CB38A` |
| `--color-danger` | 危险状态 | `#B33A2A` | `#EA6B57` |

## 生效方式

在「设置 → 外观」导入文件，然后点击该皮肤的「使用」。皮肤选择是 Server 的全局设置，所有浏览器使用同一款皮肤。浅色、深色或跟随系统由各浏览器分别选择。皮肤未填写的变量沿用对应模式的默认值。删除正在使用的皮肤后会回到内置默认皮肤。Server 最多保存 20 款上传的皮肤。
