# Third-party notices / 第三方代码与数据声明

审计依据提交：`37c37352f224e45b9029d518bc0a882023edad42`（v1.0.5 开发结束时的代码状态；本文自身的修改在其后）。
首次审计：2026-10-19，任务 R06，基于提交 `36a1cb09157e9f2a5c6db49c08c54f553dca61fc`，
路线图依据 `origin/roadmap` 提交 `c6a407ef5cb0a83fba52f5700f8722063581f4bf`。
发布前复核：2026-10-22（R07、R20、R22、R23 之后），按最终的 `go.mod`、`go.sum` 和代码核对。

本文记录该提交的来源、许可证、修改和分发情况，不为 404-probe 本身选择许可证。
Komari 仅引用既有说明，不重新审计。发布前若依赖、工具链或第三方数据变化，
由发布流程复核；本文不自动覆盖后续提交。2026-10-22 已按 v1.0.5 最终的 `go.mod`
重新核对依赖版本和声明（见第 1、3 节和附录 E2）；此后再有依赖、工具链或第三方数据变化，
须再次复核并更新审计基准提交。

“已核实”指本次读取固定上游内容，或读取与 `go.sum` 对应且通过
`go mod verify` 的模块原文。“未核实”表示没有足够证据，不能作为授权已获得的结论。
许可证名称不能替代下文附录中的版权和条件原文。

## 1. 方法与分发边界

- 搜索 Git 跟踪文件中的 `copied from`、`adapted`、`derived from`、
  `copyright`、`license`、来源/参考说明及外部 URL，并检查 vendor、
  Go 模块、工具链、数据和测试样本。URL 本身不意味着复制了对方代码。
- 将本地 vendor 文件与固定上游文件比对，共 285 项，全部字节一致：
  271 个旗帜 SVG、7 个卡片 SVG、uPlot JS/CSS，以及 5 份许可证。
  选择子集、重命名和所在目录变化在下表单列。
- `go.mod` 的 6 个直接依赖和 15 个间接依赖逐项检查，版本与基准
  `go.sum` 对照；不将 `go.sum` 中历史、工具和测试依赖一律当成发布依赖。
  所有这些模块均未 vendoring 到仓库，也未发现 `replace` 或本地补丁。
- 用 Go 1.26.8、`GOOS=linux`、`GOARCH=amd64/arm64`、`CGO_ENABLED=0`
  分别检查 `cmd/server`、`cmd/agent` 的 `go list -deps`。表中的二进制归属
  是包依赖结果，两种架构的模块集合一致；它不是最终 ELF 的逐符号保留证明。
  包内嵌套来源以及尚不能判定是否进入最终机器码的内容另列。
- `web/embed.go` 的 `//go:embed static/*` 将 Web 静态资源及其 vendor
  许可证嵌入 Server，并可随页面提供；Agent 不嵌入这套 Web 资源。
  v1.0.5 起 Server 不再嵌入任何网络质量目标目录：R07 已删除 `internal/server/quality_catalog.json`
  及相关代码，目标由用户自己填写。
- 发布前复核（2026-10-22）只更新本文：删除已移除的 zstatic 一节，更新 gg-status、R20、R22 的处理状态，
  核对 `go.mod`、依赖归属、Web vendor 摘要和审计基准提交。没有新增/升级依赖、改代码、补丁、迁移或发布打包改动。
  临时下载的上游页面、归档及审计脚本不属于仓库交付物。

## 2. Web 静态资源

| 组件、版本与上游 | 仓库位置 | 许可证及原文 | 修改情况与核实 | 分发 |
| --- | --- | --- | --- | --- |
| Devicon v2.17.0，提交 `54cfe13ac10eaa1ef817a343ab0a9437eb3c2e08`；[上游](https://github.com/devicons/devicon/tree/54cfe13ac10eaa1ef817a343ab0a9437eb3c2e08) | `web/static/vendor/card-icons/debian.svg` | 集合 MIT；`DEVICON-LICENSE.txt` 及附录 V1；Debian 标志另见下一项 | 从 `icons/debian/debian-original.svg` 选取并改名，SVG 未改；上游 SVG、许可证均字节一致 | Server 内嵌、Web |
| Debian Open Use Logo，1999，Raul Silva / Software in the Public Interest, Inc.；无独立数字版本；[官方授权](https://www.debian.org/logos/) | 同上；来源记录 `web/static/vendor/card-icons/SOURCES.md` | 官方提供 LGPL-3.0-or-later / CC-BY-SA-3.0 双授权；本仓库既有记录选择 CC-BY-SA-3.0；原文 `DEBIAN-CC-BY-SA-3.0.txt` 及附录 V2 | 此 SVG 沿用 Devicon 的具体表现，未改；官方标志授权已核实，未宣称两种上游绘图字节相同 | Server 内嵌、Web；不表示背书 |
| Heroicons v2.2.0，提交 `0435d4ca364a608cc75e2f8683d374e55abbae26`；[上游](https://github.com/tailwindlabs/heroicons/tree/0435d4ca364a608cc75e2f8683d374e55abbae26) | `web/static/vendor/card-icons/{cpu,memory,disk,io,generic}.svg` | MIT；`HEROICONS-LICENSE.txt` 及附录 V3 | `optimized/24/outline/{cpu-chip,server,circle-stack,arrows-right-left,computer-desktop}.svg` 依次改名；5 个 SVG 和许可证均字节一致 | Server 内嵌、Web |
| Simple Icons 13.21.0，提交 `3fd92a7d0656c3d780bccdf7b5a3e9b9991dd6e5`；[上游](https://github.com/simple-icons/simple-icons/tree/3fd92a7d0656c3d780bccdf7b5a3e9b9991dd6e5)；品牌来源 [Alpine Linux](https://alpinelinux.org/) | `web/static/vendor/card-icons/alpine.svg` | 集合 CC0-1.0；`SIMPLE-ICONS-LICENSE.md` 及附录 V4；CC0 不授予商标权 | `icons/alpinelinux.svg` 改名，SVG 和许可证均字节一致；SVG SHA256 `d37d0a7873c65174308d63929f09ab93c3aaa7f5c5df79cdec4be532e015fc98` | Server 内嵌、Web；不表示背书 |
| flag-icons 7.5.0；[固定 tag](https://github.com/lipis/flag-icons/tree/v7.5.0) | `web/static/vendor/flag-icons/4x3/*.svg`、`LICENSE`；`internal/protocol/country.go` 是配套代码名单 | MIT；现存 `LICENSE` 及附录 V5 | 上游归档的 `package.json` 确认为 7.5.0；271 个本地 SVG 逐个与上游 `flags/4x3/` 同名文件比对，全部一致，许可证一致；只选取目录，未引入完整 CSS/1x1 集合 | Server 内嵌、Web；国家代码名单随二进制 |
| uPlot 1.6.32，提交 `e995b061e9fc5476a6d862cd2fb2ebc7452ca012`；[上游](https://github.com/leeoniya/uPlot/tree/e995b061e9fc5476a6d862cd2fb2ebc7452ca012) | `web/static/vendor/uplot/uPlot.iife.min.js`、`uPlot.min.css`、`LICENSE`、`SOURCES.md` | MIT；现存 `LICENSE` 及附录 V6 | 从 `dist/` 选取，JS/CSS/许可证均字节一致；无修改 | Server 内嵌、Web |

uPlot 本次实测 SHA256：JS 为
`19c8d4c6ad88929a79f4ae49d6f7161566dfd0ba3d15cc495e974f787eb78f1f`，
CSS 为 `df630c6a8d6f8eeaff264b50f73ce5b114f646ffd9a0bb74f049b0a00135fa04`，
许可证为 `8f989229699b4fe2f1a0432d0e9edc338a8a911e250e2d1b01ecd770a5f5b1bd`。

## 3. go.mod 直接和间接依赖

以下每行的仓库登记位置均为 `go.mod`、`go.sum`；源码实际由 Go 模块缓存提供，
不在仓库内。附录 M1–M22 保留实际版本模块中的 LICENSE 原文及 SHA256。
所有行均为本项目未修改、已核实模块原文；“不在 Linux 发布包依赖中”不代表
可以从 `go.mod` 删除，也不代表其他操作系统不会使用它。

| 模块、版本 | 类型 | 上游 | 许可证 | 当前 Linux 发布包依赖归属；用途 | 原文 |
| --- | --- | --- | --- | --- | --- |
| `github.com/shirou/gopsutil/v4 v4.25.6` | 直接 | [gopsutil](https://github.com/shirou/gopsutil/tree/v4.25.6) | BSD-3-Clause；嵌套来源另列 | Agent；`internal/agent` 系统指标 | M1 |
| `golang.org/x/crypto v0.40.0` | 直接 | [crypto](https://go.googlesource.com/crypto/+/refs/tags/v0.40.0) | BSD-3-Clause | Server；Argon2、BLAKE2b | M2 |
| `golang.org/x/net v0.42.0` | 直接 | [net](https://go.googlesource.com/net/+/refs/tags/v0.42.0) | Go 代码 BSD-3-Clause；PSL 数据另列 | Server、Agent；HTML、ICMP、IP、域名后缀 | M3 |
| `golang.org/x/sys v0.44.0` | 直接 | [sys](https://go.googlesource.com/sys/+/refs/tags/v0.44.0) | BSD-3-Clause | Server、Agent；系统接口 | M4 |
| `golang.org/x/text v0.27.0` | 直接 | [text](https://go.googlesource.com/text/+/refs/tags/v0.27.0) | Go 代码 BSD-3-Clause；CLDR/Unicode 数据见下 | Server；名称 NFC/case folding、ISO 货币校验 | M22 |
| `golang.org/x/term v0.33.0` | 直接 | [term](https://go.googlesource.com/term/+/refs/tags/v0.33.0) | BSD-3-Clause | Server；终端密码输入 | M5 |
| `modernc.org/sqlite v1.46.2` | 直接 | [sqlite](https://gitlab.com/cznic/sqlite/-/tree/v1.46.2) | Go 层 BSD-3-Clause；SQLite 公有领域声明另列 | Server；存储 | M6 |
| `github.com/dustin/go-humanize v1.0.1` | 间接 | [go-humanize](https://github.com/dustin/go-humanize/tree/v1.0.1) | MIT | Server；SQLite 依赖 | M7 |
| `github.com/ebitengine/purego v0.8.4` | 间接 | [purego](https://github.com/ebitengine/purego/tree/v0.8.4) | Apache-2.0 | 不在 Linux 发布包依赖中 | M8 |
| `github.com/go-ole/go-ole v1.2.6` | 间接 | [go-ole](https://github.com/go-ole/go-ole/tree/v1.2.6) | MIT | 不在 Linux 发布包依赖中；Windows 支持依赖 | M9 |
| `github.com/google/uuid v1.6.0` | 间接 | [uuid](https://github.com/google/uuid/tree/v1.6.0) | BSD-3-Clause | Server；SQLite 依赖 | M10 |
| `github.com/lufia/plan9stats v0.0.0-20211012122336-39d0f177ccd0` | 间接 | [plan9stats](https://github.com/lufia/plan9stats/tree/39d0f177ccd0) | BSD-3-Clause | 不在 Linux 发布包依赖中；Plan 9 支持依赖 | M11 |
| `github.com/mattn/go-isatty v0.0.20` | 间接 | [go-isatty](https://github.com/mattn/go-isatty/tree/v0.0.20) | MIT | 不在 Linux 发布包依赖中 | M12 |
| `github.com/ncruces/go-strftime v1.0.0` | 间接 | [go-strftime](https://github.com/ncruces/go-strftime/tree/v1.0.0) | MIT | 不在本次 Linux 发布包依赖中 | M13 |
| `github.com/power-devops/perfstat v0.0.0-20210106213030-5aafc221ea8c` | 间接 | [perfstat](https://github.com/power-devops/perfstat/tree/5aafc221ea8c) | MIT | 不在 Linux 发布包依赖中；AIX 支持依赖 | M14 |
| `github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec` | 间接 | [bigfft](https://github.com/remyoudompheng/bigfft/tree/24d4a6f8daec) | BSD-3-Clause | Server；mathutil 依赖 | M15 |
| `github.com/tklauser/go-sysconf v0.3.12` | 间接 | [go-sysconf](https://github.com/tklauser/go-sysconf/tree/v0.3.12) | BSD-3-Clause | Agent；gopsutil 依赖 | M16 |
| `github.com/tklauser/numcpus v0.6.1` | 间接 | [numcpus](https://github.com/tklauser/numcpus/tree/v0.6.1) | Apache-2.0 | Agent；go-sysconf 依赖 | M17 |
| `github.com/yusufpapurcu/wmi v1.2.4` | 间接 | [wmi](https://github.com/yusufpapurcu/wmi/tree/v1.2.4) | MIT | 不在 Linux 发布包依赖中；Windows 支持依赖 | M18 |
| `modernc.org/libc v1.70.0` | 间接 | [libc](https://gitlab.com/cznic/libc/-/tree/v1.70.0) | BSD-3-Clause；嵌套许可和待核实项另列 | Server；SQLite 运行支持 | M19 |
| `modernc.org/mathutil v1.7.1` | 间接 | [mathutil](https://gitlab.com/cznic/mathutil/-/tree/v1.7.1) | BSD-3-Clause | Server；SQLite 依赖 | M20 |
| `modernc.org/memory v1.11.0` | 间接 | [memory](https://gitlab.com/cznic/memory/-/tree/v1.11.0) | BSD-3-Clause；含其他 BSD 版权 | Server；SQLite 依赖 | M21 |

附录还保留 Go 模块的 PATENTS 文件（若有）、memory 的 `LICENSE-GO`、
`LICENSE-MMAP-GO`、mathutil/mersenne 的 LICENSE。memory 的 `LICENSE-LOGO`
仅为 logo 来源 URL，logo 不在本项目发布包中，不将它误当成代码许可证。
purego 和 numcpus 模块内未找到独立 NOTICE 文件；保留 Apache 原文及源文件作者说明。


v1.0.10 增量核对（2026-10-29）：新增直接依赖 x/text v0.27.0，读取本机与 go.sum 对应的离线模块缓存；不混用工具链 vendor 的 v0.32.0。`currency/tables.go` 标明 CLDR 32，`unicode/norm` 与 `cases` 包含 Unicode 生成数据。Go 源码的 BSD-3-Clause 与数据许可分别保留：CLDR/Unicode 数据受 Unicode 数据文件和软件许可约束，版权与许可声明随本文件附录 G4 分发；G4 已保留 Unicode 官方许可全文。模块没有独立 CLDR LICENSE，历史数据授权链沿用 Q7 的待核实记录，不以当前许可年份推断 CLDR 32 的发布日期。本项目没有修改上游模块或生成数据。前端 `web/static/organization-name.js` 的 Unicode full-fold 例外表由 `scripts/gen-organization-name.go` 从同一模块版本生成，用于与 Server 一致地复用已有标签名；该生成数据也随 Server/Web 分发，沿用本节及附录 G4 的数据许可声明。

### 3.1 包内嵌套代码和数据

| 来源、版本、上游 | 所在模块/位置与修改 | 许可及核实状态 | 分发与原文 |
| --- | --- | --- | --- |
| SQLite 3.51.3；[SQLite](https://sqlite.org/copyright.html) | sqlite v1.46.2 的 `lib/sqlite_linux_{amd64,arm64}.go`，`SQLITE_VERSION` 为 `3.51.3`；上游转译的 C 实现，本项目未改 | SQLite 公有领域声明已核实；Go 封装不是公有领域，而是 BSD-3-Clause | Server；附录 N1 为模块自带 `SQLITE-LICENSE` |
| musl 1.2.5，提交 `7ada6dde6f9dc6a2836c3d92c2f762d35fd229e0`；[固定上游](https://git.musl-libc.org/cgit/musl/tree/?id=7ada6dde6f9dc6a2836c3d92c2f762d35fd229e0) | libc v1.70.0 的 `builder.json` 固定该归档；`ccgo_linux_*.go` 等为上游转译及适配，本项目未改 | MIT 为主，包含 TRE BSD-2-Clause、数学函数等其他宽松授权；归档 `VERSION`、COPYRIGHT 及来源注释已读取。不能只用 libc 顶层 BSD 概括 | Server；N2 为 libc 自带第三方声明，N3 为精确 musl COPYRIGHT 及相关原始源码授权注释 |
| go-netdb、NixOS/nixpkgs；[go-netdb](https://github.com/dominikh/go-netdb)、[nixpkgs](https://github.com/NixOS/nixpkgs) | libc 的 `honnef.co/`、生成/构建内容；以 libc v1.70.0 为实际版本，独立上游提交**未核实**；项目未改 | MIT 声明见 libc 原文；不推定构建配置全部进入二进制 | 模块含有；本次 Linux 包依赖没有导入 `honnef.co` 子包；N2 保留完整声明 |
| UUID / Theodore Ts'o；[util-linux](https://github.com/util-linux/util-linux)；独立版本**未核实** | libc 的 `uuid/uuid/uuid_linux_{amd64,arm64}.go`，上游生成 | UUID BSD-3-Clause 版权声明已见；同文件还包含 GNU C Library 头文件的 LGPL-2.1-or-later 和 GCC 头文件的 GPL-3.0-or-later WITH GCC-exception-3.1 声明。交叉评审确认这些文件仅有常量和类型定义、没有函数；适用 LGPL-2.1 §5，Q3 已解决 | Server 包依赖中包含该包；N4 保留两架构生成文件的不同授权注释、LGPL-2.1/GPL-3.0 全文及 GCC Runtime Library Exception 3.1 |
| Public Suffix List，提交 `2c960dac3d39ba521eb5db9da192968f5be0aded`，2025-03-18；[固定原始数据](https://github.com/publicsuffix/list/blob/2c960dac3d39ba521eb5db9da192968f5be0aded/public_suffix_list.dat) | x/net v0.42.0 的 `publicsuffix/table.go` 和 `publicsuffix/data/*`，上游生成的压缩数据，本项目未改 | 原始列表头为 MPL-2.0，Go 实现为 BSD-3-Clause；版本字符串与固定数据头已核实。通过 R20 将许可声明及源码获取位置嵌入二进制，处理 MPL-2.0 §3.2 的告知要求，见 Q4 | Server 的域名后缀检查；N5 保留原始声明、数据来源及 MPL-2.0 全文 |
| ntgo v0.8.0；[固定源文件](https://github.com/ntrrg/ntgo/blob/v0.8.0/runtime/infrastructure.go) | gopsutil 的 `internal/common/endian.go` 明记 copied from；上游采纳为辅助函数，本项目未改 | 原始项目 MIT、Miguel Angel Rivera Notararigo 版权已核实；不能只保留 gopsutil 顶层 BSD | Agent；N6 原始 MIT 全文 |
| Go 标准库代码的上游复用 | gopsutil `internal/common/binary.go`、`readlink_linux.go`；bigfft `fermat.go`；memory 的 mmap/Go 适配 | Go BSD 版权已见；具体原始 Go 版本未在这些文件统一固定，不补猜版本 | 相关 Server/Agent 包；gopsutil LICENSE、M15、memory 附加 LICENSE 原文保留 |

## 4. Go 工具链、标准库与运行时

项目 `go.mod` 的 `toolchain go1.26.8` 是审计版本；`go 1.25.0` 是语言/最低版本行，
不是本次发布工具链版本。上游：[Go go1.26.8](https://go.googlesource.com/go/+/refs/tags/go1.26.8)。
读取的实际工具链模块为 `golang.org/toolchain@v0.0.1-go1.26.8.windows-amd64`；
该发行物用于本地验证和 Linux 目标包依赖分析。

运行时和被使用的标准库会进入 Server/Agent 二进制；本项目未修改工具链源码。
主许可证 BSD-3-Clause，版权 The Go Authors；原文和 PATENTS 保留在附录 G1。
编译器、链接器本体不随本项目分发；不能把工具链所有开发工具依赖都算作二进制内容。
默认发布构建未启用 `GOEXPERIMENT=boringcrypto`，不据其可选目录判断本项目使用 BoringSSL。

工具链内 `src/vendor/modules.txt` 还固定了：

| 内置模块 | 工具链内版本 | 许可 | 本次包依赖 |
| --- | --- | --- | --- |
| golang.org/x/crypto | `v0.46.1-0.20251210140736-7dacc380ba00` | BSD-3-Clause | 标准库 TLS/加密路径 |
| golang.org/x/net | `v0.47.1-0.20260731170545-cb1d721f7fb3` | BSD-3-Clause | 标准库 HTTP、DNS、IDNA 路径 |
| golang.org/x/sys | `v0.39.0` | BSD-3-Clause | 作为工具链 vendor 登记；不与项目 go.mod 的 v0.44.0 混淆 |
| golang.org/x/text | `v0.32.0` | BSD-3-Clause | 标准库 IDNA/bidi/norm 路径 |

四个内置模块各自的 LICENSE/PATENTS 均保留在 G2；这组版本不替代第 3 节的模块版本。
Go 标准库和内置 x/text 使用的 Unicode 数据版本为 15.0.0；来源
[Unicode 15.0.0 UCD](https://www.unicode.org/Public/15.0.0/ucd/)，生成代码在
`src/unicode/tables.go`、`src/vendor/golang.org/x/text/unicode/`。
其数据上游单独授权核实情况见 Q7，不将 Go 代码的 BSD 自动延伸到所有原始数据。

标准库包内还保留了其他来源声明，G3 摘录这些授权原文，均按 Go 1.26.8 所带内容记录：

- `runtime/memmove_amd64.s`：Inferno，Lucent Technologies / Vita Nuova，MIT 风格许可；Go 上游已适配，随 amd64 运行时。
- `math/*.go`：Sun Microsystems（1993/2004）允许保留声明后使用、复制、修改和分发；Cephes Release 2.8（2000）、Stephen L. Moshier 自定义宽松声明；随所用数学实现。
- `math/exp_amd64.s`：Naoki Shibata 的早期实现，Go 文件记载当时公有领域声明；不据此概括当前 SLEEF 项目的许可证，独立原始版本未进一步核实。
- `debug/elf/elf.go`：FreeBSD 派生定义，John D. Polstra / David E. O'Brien，BSD-2-Clause；包依赖含 ELF 检查代码。
- `crypto/internal/fips140/edwards25519/scalar.go`：fiat-crypto v0.0.9（`23d2dbc`）生成实现，原文为 BSD-1-Clause；随 Go 的加密路径。
- `crypto/internal/fips140/aes/aes_generic.go`：Rijndael 参考实现 3.0（2000-12），Vincent Rijmen、Antoon Bosselaers、Paulo Barreto，公有领域及免责声明。

## 5. 其他外部代码、数据及既有说明

### 5.1 Google 检测的历史来源

YouTube 页面检测最初参照
[404-vps-tools/gg-status](https://github.com/404-git-404/404-vps-tools/blob/b8830f4c86ddebc0be19bd4fadb07ca5a586dfb2/gg-status)
（提交 `b8830f4c86ddebc0be19bd4fadb07ca5a586dfb2`、blob
`50c5afa7f6fae163e48040c4624e0638c6250840`），从 shell/awk 重写为 Go。
404-vps-tools 属于维护者的同一 GitHub 账号，是公开项目，没有 LICENSE 文件；
维护者已确认两个项目均为本人同一作者的作品，无其他贡献者，无需另行授权（Q2 已解决）。
**R22 已删除**测试中的原脚本拷贝、代码里的来源注释以及未使用的 Search、Sign-in、Gemini 检测；
现行 `internal/agent/google_status.go` 是 Go 实现，仓库里没有该脚本的拷贝。
R23 的“送中”判定是新写的，不来自该脚本。本节只保留历史来源，该固定提交文件的摘要见附录 E1。

### 5.2 地区名单、页面样本和服务调用

| 项目、来源及版本 | 仓库位置、处理方式 | 许可状态 | 分发 |
| --- | --- | --- | --- |
| YouTube Premium 地区名单；[Google 官方帮助](https://support.google.com/youtube/answer/6307365?hl=en)，代码记载 2026-10-17、119 地区；无固定帮助页面版本 | `web/static/youtube-status.js` 的地区代码集合，手动整理，非原网页副本；本次读取现行帮助页，未重建 10-17 的历史快照 | 列表版本为仓库记录；历史对应关系及独立数据再分发许可**未核实**（Q6） | Server 内嵌、Web |
| YouTube Premium 页面/重定向的精简样本；[源页面](https://www.youtube.com/premium)；无上游构建版本 | `internal/agent/testdata/youtube/`，处理记录见该目录 README；删减页面并脱敏，不保留原始完整抓取 | 测试样本版权/再分发授权**未核实**（Q6）；不推断删减或脱敏等于获得许可 | 源码测试数据；`testdata` 不嵌入生产二进制 |
| 维护者提供的日志样本，来源日期 2026-09-05，无公开上游 URL | `internal/securitycollector/testdata/reality-overlapping-export.jsonl`；来源记录见同目录 README；转换器 `scripts/convert-security-fixture.go`；已加工 | 属用户提供数据，不是已识别的第三方软件库；记录未声明独立许可证，原始日志权利状态**未核实**（Q6） | 源码测试数据，不嵌入生产二进制 |
| [ipify](https://www.ipify.org/)、[IPWho](https://ipwho.is/)、Google/YouTube 在线服务 | `internal/egress/egress.go`、`internal/geoip/geoip.go`、`internal/agent/google_status.go`；HTTP 调用、运行时读取结果，没有 vendoring 服务实现或 GeoIP 数据库 | 不将 API 使用自动写成服务代码的开源授权；本项仅记录调用边界，服务条款和用户运行时数据另行适用 | 二进制含请求代码和 URL，不含这些服务的实现；Web/Server 可展示处理后的结果 |

### 5.3 OpenRC 和 Komari

OpenRC 的运行监督配置参考官方提交
`aec5dedb1ce30f8188b8ef1042e518651cc0fe33`，
[固定上游](https://github.com/OpenRC/openrc/tree/aec5dedb1ce30f8188b8ef1042e518651cc0fe33)。
原 BSD-2-Clause 已在 `docs/os1b-openrc-third-party-notices-20261004.md` 保留；
本次与该提交 LICENSE 核对原文一致，附录 O1 再保留原文。
`install.sh` 的 OpenRC 分支及生成服务配置经过项目适配，随安装器分发，
不是把 OpenRC 服务管理程序打包进本项目。

以下 Komari 部分按任务书仅引用既有说明和许可证，不重新审计：

- `internal/server/traffic-recent-SOURCES.md`：Komari 提交
  `f18ad72c173b8821711cba0f2fb238c2486ec9af`，内存流量采样模式的适配；
  MIT 原文 `internal/server/traffic-recent-LICENSE.txt`，随 Server 相关代码。
- `internal/storage/network_quality_history.go` 的来源注释：提交
  `9812acfd1106f29fa2f6d193207b005396bc78e0`；
  MIT 原文 `internal/storage/network_quality_KOMARI_LICENSE.txt`，随 Server 存储代码。
- `docs/os1b-openrc-third-party-notices-20261004.md`：Komari Agent 提交
  `828afafe7fb5b1b58c1232094e1e46320719016e`，MIT；随安装器适配内容。

## 6. 问题清单与本任务未处理事项

| 编号 | 问题、证据与限制 | 留待处理 |
| --- | --- | --- |
| Q1 | **已解决**：zstatic 内置目标数据没有已核实的再分发许可证。v1.0.5 的 R07 已移除全部内置目标，删除 `internal/server/quality_catalog.json`、目录相关代码和 `web/static` 中的使用；目标现在只来自用户自己的填写，仓库和二进制不再含有该数据 | 无。README 里对 zstaticcdn 的致谢属于发布准备提交处理 |
| Q2 | **已解决**：维护者确认 gg-status 与本项目为本人同一作者的作品，无其他贡献者；404-vps-tools 属于同一账号、公开且没有 LICENSE，本次移植无需另行授权 | 保留来源说明（5.1）；R22 已移除测试中的原脚本拷贝和代码里的来源注释 |
| Q3 | **已解决**：交叉评审确认 libc v1.70.0 的 UUID 相关生成文件只有常量和类型定义、没有函数。Go 1.26.8 的 Linux/amd64 Server 符号检查仅剩 `uuid.init`（60 字节）、`uuid.CAPI`（8 字节）及 init 任务，约 70 字节符号内容，没有可执行的 UUID 实现 | 适用 LGPL-2.1 §5 对数值参数、数据结构布局等使用的规定；GCC 头文件同时带 Runtime Library Exception 3.1，不产生本项所担心的源码提供或重新链接义务。Agent 不依赖 libc；N4 原文继续保留 |
| Q4 | **已解决**：x/net 的 PSL 原始数据明确 MPL-2.0；本文已保留 MPL 原文和固定源码获取位置 | R20 已将本文嵌入 Server 和 Agent 二进制并提供 `licenses` 子命令，MPL-2.0 §3.2 的告知要求由此处理。`notices_test.go` 与发布前的构建检查核对两个二进制的 `licenses` 输出与仓库根目录本文逐字节一致 |
| Q5 | **已解决**：7 文件发布清单（4 个 ELF、install.sh、元数据、校验表）不含本文，Web vendor 原文已嵌入 Server，其他 notices 原本没有交付渠道 | R20 把本文嵌入 Server、Agent 二进制（`licenses` 子命令），不增加第 8 个发布文件；`notices_test.go` 检查嵌入内容与仓库文件一致，发布前再次核对（见 Q4） |
| Q6 | Google 地区名单、YouTube 页面精简样本及用户提供日志未有可确认的独立再分发授权；原始版本/权利状态未完全核实 | 保留来源和加工说明，不将其标成 MIT/公有领域；不新增原始抓取或敏感样本 |
| Q7 | Unicode 15.0.0 数据、Go 中早期数学实现及 libc 的部分嵌套项目独立上游版本/授权链不全；已记录能确认的工具链/模块/来源原文，未完成对每个生成项的历史权利追溯 | 下文 G4 保留本次可取得的 Unicode 官方原文；历史适用授权及数据转换义务仍须明确，不据最新授权推断历史版本 |

本次未发现需要猜测的 go.mod 模块版本：21 个版本均已确认。未将未核实来源判定为
“无许可证风险”，也未宣称本文补齐原文即可自动解决发布合规问题。
没有为 R18 选择项目许可证，没有修改发布策略；首次审计（R06，2026-10-19）当时没有开始 R03-1、R02 或 R04，这是当时的范围，不是项目的最终状态。

以下附录保留许可证完整措辞；显示排版统一为 LF 并去除行末空白。
“原文 SHA256”指取得的源文件或来源摘录在这些排版处理前的字节摘要。
相同正文可出现多次，以保留各组件版权。

## 附录 V：Web 资源许可证原文

### V1 — Devicon MIT；web/static/vendor/card-icons/DEVICON-LICENSE.txt

原文 SHA256：`121194741d4a915b9f5890fdd6dd95121f9b1f816517c792358d72d7c838d664`。

```text
The MIT License (MIT)

Copyright (c) 2015 konpa

Permission is hereby granted, free of charge, to any person obtaining a copy of
this software and associated documentation files (the "Software"), to deal in
the Software without restriction, including without limitation the rights to
use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of
the Software, and to permit persons to whom the Software is furnished to do so,
subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS
FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR
COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER
IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN
CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
```

### V2 — Debian CC BY-SA 3.0；web/static/vendor/card-icons/DEBIAN-CC-BY-SA-3.0.txt

原文 SHA256：`3f941b3b89cf7b8370ceb83cc76d2120d471b58735d8ca60238a751a48d7f72f`。

```text
Creative Commons Legal Code

Attribution-ShareAlike 3.0 Unported

    CREATIVE COMMONS CORPORATION IS NOT A LAW FIRM AND DOES NOT PROVIDE
    LEGAL SERVICES. DISTRIBUTION OF THIS LICENSE DOES NOT CREATE AN
    ATTORNEY-CLIENT RELATIONSHIP. CREATIVE COMMONS PROVIDES THIS
    INFORMATION ON AN "AS-IS" BASIS. CREATIVE COMMONS MAKES NO WARRANTIES
    REGARDING THE INFORMATION PROVIDED, AND DISCLAIMS LIABILITY FOR
    DAMAGES RESULTING FROM ITS USE.

License

THE WORK (AS DEFINED BELOW) IS PROVIDED UNDER THE TERMS OF THIS CREATIVE
COMMONS PUBLIC LICENSE ("CCPL" OR "LICENSE"). THE WORK IS PROTECTED BY
COPYRIGHT AND/OR OTHER APPLICABLE LAW. ANY USE OF THE WORK OTHER THAN AS
AUTHORIZED UNDER THIS LICENSE OR COPYRIGHT LAW IS PROHIBITED.

BY EXERCISING ANY RIGHTS TO THE WORK PROVIDED HERE, YOU ACCEPT AND AGREE
TO BE BOUND BY THE TERMS OF THIS LICENSE. TO THE EXTENT THIS LICENSE MAY
BE CONSIDERED TO BE A CONTRACT, THE LICENSOR GRANTS YOU THE RIGHTS
CONTAINED HERE IN CONSIDERATION OF YOUR ACCEPTANCE OF SUCH TERMS AND
CONDITIONS.

1. Definitions

 a. "Adaptation" means a work based upon the Work, or upon the Work and
    other pre-existing works, such as a translation, adaptation,
    derivative work, arrangement of music or other alterations of a
    literary or artistic work, or phonogram or performance and includes
    cinematographic adaptations or any other form in which the Work may be
    recast, transformed, or adapted including in any form recognizably
    derived from the original, except that a work that constitutes a
    Collection will not be considered an Adaptation for the purpose of
    this License. For the avoidance of doubt, where the Work is a musical
    work, performance or phonogram, the synchronization of the Work in
    timed-relation with a moving image ("synching") will be considered an
    Adaptation for the purpose of this License.
 b. "Collection" means a collection of literary or artistic works, such as
    encyclopedias and anthologies, or performances, phonograms or
    broadcasts, or other works or subject matter other than works listed
    in Section 1(f) below, which, by reason of the selection and
    arrangement of their contents, constitute intellectual creations, in
    which the Work is included in its entirety in unmodified form along
    with one or more other contributions, each constituting separate and
    independent works in themselves, which together are assembled into a
    collective whole. A work that constitutes a Collection will not be
    considered an Adaptation (as defined below) for the purposes of this
    License.
 c. "Creative Commons Compatible License" means a license that is listed
    at https://creativecommons.org/compatiblelicenses that has been
    approved by Creative Commons as being essentially equivalent to this
    License, including, at a minimum, because that license: (i) contains
    terms that have the same purpose, meaning and effect as the License
    Elements of this License; and, (ii) explicitly permits the relicensing
    of adaptations of works made available under that license under this
    License or a Creative Commons jurisdiction license with the same
    License Elements as this License.
 d. "Distribute" means to make available to the public the original and
    copies of the Work or Adaptation, as appropriate, through sale or
    other transfer of ownership.
 e. "License Elements" means the following high-level license attributes
    as selected by Licensor and indicated in the title of this License:
    Attribution, ShareAlike.
 f. "Licensor" means the individual, individuals, entity or entities that
    offer(s) the Work under the terms of this License.
 g. "Original Author" means, in the case of a literary or artistic work,
    the individual, individuals, entity or entities who created the Work
    or if no individual or entity can be identified, the publisher; and in
    addition (i) in the case of a performance the actors, singers,
    musicians, dancers, and other persons who act, sing, deliver, declaim,
    play in, interpret or otherwise perform literary or artistic works or
    expressions of folklore; (ii) in the case of a phonogram the producer
    being the person or legal entity who first fixes the sounds of a
    performance or other sounds; and, (iii) in the case of broadcasts, the
    organization that transmits the broadcast.
 h. "Work" means the literary and/or artistic work offered under the terms
    of this License including without limitation any production in the
    literary, scientific and artistic domain, whatever may be the mode or
    form of its expression including digital form, such as a book,
    pamphlet and other writing; a lecture, address, sermon or other work
    of the same nature; a dramatic or dramatico-musical work; a
    choreographic work or entertainment in dumb show; a musical
    composition with or without words; a cinematographic work to which are
    assimilated works expressed by a process analogous to cinematography;
    a work of drawing, painting, architecture, sculpture, engraving or
    lithography; a photographic work to which are assimilated works
    expressed by a process analogous to photography; a work of applied
    art; an illustration, map, plan, sketch or three-dimensional work
    relative to geography, topography, architecture or science; a
    performance; a broadcast; a phonogram; a compilation of data to the
    extent it is protected as a copyrightable work; or a work performed by
    a variety or circus performer to the extent it is not otherwise
    considered a literary or artistic work.
 i. "You" means an individual or entity exercising rights under this
    License who has not previously violated the terms of this License with
    respect to the Work, or who has received express permission from the
    Licensor to exercise rights under this License despite a previous
    violation.
 j. "Publicly Perform" means to perform public recitations of the Work and
    to communicate to the public those public recitations, by any means or
    process, including by wire or wireless means or public digital
    performances; to make available to the public Works in such a way that
    members of the public may access these Works from a place and at a
    place individually chosen by them; to perform the Work to the public
    by any means or process and the communication to the public of the
    performances of the Work, including by public digital performance; to
    broadcast and rebroadcast the Work by any means including signs,
    sounds or images.
 k. "Reproduce" means to make copies of the Work by any means including
    without limitation by sound or visual recordings and the right of
    fixation and reproducing fixations of the Work, including storage of a
    protected performance or phonogram in digital form or other electronic
    medium.

2. Fair Dealing Rights. Nothing in this License is intended to reduce,
limit, or restrict any uses free from copyright or rights arising from
limitations or exceptions that are provided for in connection with the
copyright protection under copyright law or other applicable laws.

3. License Grant. Subject to the terms and conditions of this License,
Licensor hereby grants You a worldwide, royalty-free, non-exclusive,
perpetual (for the duration of the applicable copyright) license to
exercise the rights in the Work as stated below:

 a. to Reproduce the Work, to incorporate the Work into one or more
    Collections, and to Reproduce the Work as incorporated in the
    Collections;
 b. to create and Reproduce Adaptations provided that any such Adaptation,
    including any translation in any medium, takes reasonable steps to
    clearly label, demarcate or otherwise identify that changes were made
    to the original Work. For example, a translation could be marked "The
    original work was translated from English to Spanish," or a
    modification could indicate "The original work has been modified.";
 c. to Distribute and Publicly Perform the Work including as incorporated
    in Collections; and,
 d. to Distribute and Publicly Perform Adaptations.
 e. For the avoidance of doubt:

     i. Non-waivable Compulsory License Schemes. In those jurisdictions in
        which the right to collect royalties through any statutory or
        compulsory licensing scheme cannot be waived, the Licensor
        reserves the exclusive right to collect such royalties for any
        exercise by You of the rights granted under this License;
    ii. Waivable Compulsory License Schemes. In those jurisdictions in
        which the right to collect royalties through any statutory or
        compulsory licensing scheme can be waived, the Licensor waives the
        exclusive right to collect such royalties for any exercise by You
        of the rights granted under this License; and,
   iii. Voluntary License Schemes. The Licensor waives the right to
        collect royalties, whether individually or, in the event that the
        Licensor is a member of a collecting society that administers
        voluntary licensing schemes, via that society, from any exercise
        by You of the rights granted under this License.

The above rights may be exercised in all media and formats whether now
known or hereafter devised. The above rights include the right to make
such modifications as are technically necessary to exercise the rights in
other media and formats. Subject to Section 8(f), all rights not expressly
granted by Licensor are hereby reserved.

4. Restrictions. The license granted in Section 3 above is expressly made
subject to and limited by the following restrictions:

 a. You may Distribute or Publicly Perform the Work only under the terms
    of this License. You must include a copy of, or the Uniform Resource
    Identifier (URI) for, this License with every copy of the Work You
    Distribute or Publicly Perform. You may not offer or impose any terms
    on the Work that restrict the terms of this License or the ability of
    the recipient of the Work to exercise the rights granted to that
    recipient under the terms of the License. You may not sublicense the
    Work. You must keep intact all notices that refer to this License and
    to the disclaimer of warranties with every copy of the Work You
    Distribute or Publicly Perform. When You Distribute or Publicly
    Perform the Work, You may not impose any effective technological
    measures on the Work that restrict the ability of a recipient of the
    Work from You to exercise the rights granted to that recipient under
    the terms of the License. This Section 4(a) applies to the Work as
    incorporated in a Collection, but this does not require the Collection
    apart from the Work itself to be made subject to the terms of this
    License. If You create a Collection, upon notice from any Licensor You
    must, to the extent practicable, remove from the Collection any credit
    as required by Section 4(c), as requested. If You create an
    Adaptation, upon notice from any Licensor You must, to the extent
    practicable, remove from the Adaptation any credit as required by
    Section 4(c), as requested.
 b. You may Distribute or Publicly Perform an Adaptation only under the
    terms of: (i) this License; (ii) a later version of this License with
    the same License Elements as this License; (iii) a Creative Commons
    jurisdiction license (either this or a later license version) that
    contains the same License Elements as this License (e.g.,
    Attribution-ShareAlike 3.0 US)); (iv) a Creative Commons Compatible
    License. If you license the Adaptation under one of the licenses
    mentioned in (iv), you must comply with the terms of that license. If
    you license the Adaptation under the terms of any of the licenses
    mentioned in (i), (ii) or (iii) (the "Applicable License"), you must
    comply with the terms of the Applicable License generally and the
    following provisions: (I) You must include a copy of, or the URI for,
    the Applicable License with every copy of each Adaptation You
    Distribute or Publicly Perform; (II) You may not offer or impose any
    terms on the Adaptation that restrict the terms of the Applicable
    License or the ability of the recipient of the Adaptation to exercise
    the rights granted to that recipient under the terms of the Applicable
    License; (III) You must keep intact all notices that refer to the
    Applicable License and to the disclaimer of warranties with every copy
    of the Work as included in the Adaptation You Distribute or Publicly
    Perform; (IV) when You Distribute or Publicly Perform the Adaptation,
    You may not impose any effective technological measures on the
    Adaptation that restrict the ability of a recipient of the Adaptation
    from You to exercise the rights granted to that recipient under the
    terms of the Applicable License. This Section 4(b) applies to the
    Adaptation as incorporated in a Collection, but this does not require
    the Collection apart from the Adaptation itself to be made subject to
    the terms of the Applicable License.
 c. If You Distribute, or Publicly Perform the Work or any Adaptations or
    Collections, You must, unless a request has been made pursuant to
    Section 4(a), keep intact all copyright notices for the Work and
    provide, reasonable to the medium or means You are utilizing: (i) the
    name of the Original Author (or pseudonym, if applicable) if supplied,
    and/or if the Original Author and/or Licensor designate another party
    or parties (e.g., a sponsor institute, publishing entity, journal) for
    attribution ("Attribution Parties") in Licensor's copyright notice,
    terms of service or by other reasonable means, the name of such party
    or parties; (ii) the title of the Work if supplied; (iii) to the
    extent reasonably practicable, the URI, if any, that Licensor
    specifies to be associated with the Work, unless such URI does not
    refer to the copyright notice or licensing information for the Work;
    and (iv) , consistent with Ssection 3(b), in the case of an
    Adaptation, a credit identifying the use of the Work in the Adaptation
    (e.g., "French translation of the Work by Original Author," or
    "Screenplay based on original Work by Original Author"). The credit
    required by this Section 4(c) may be implemented in any reasonable
    manner; provided, however, that in the case of a Adaptation or
    Collection, at a minimum such credit will appear, if a credit for all
    contributing authors of the Adaptation or Collection appears, then as
    part of these credits and in a manner at least as prominent as the
    credits for the other contributing authors. For the avoidance of
    doubt, You may only use the credit required by this Section for the
    purpose of attribution in the manner set out above and, by exercising
    Your rights under this License, You may not implicitly or explicitly
    assert or imply any connection with, sponsorship or endorsement by the
    Original Author, Licensor and/or Attribution Parties, as appropriate,
    of You or Your use of the Work, without the separate, express prior
    written permission of the Original Author, Licensor and/or Attribution
    Parties.
 d. Except as otherwise agreed in writing by the Licensor or as may be
    otherwise permitted by applicable law, if You Reproduce, Distribute or
    Publicly Perform the Work either by itself or as part of any
    Adaptations or Collections, You must not distort, mutilate, modify or
    take other derogatory action in relation to the Work which would be
    prejudicial to the Original Author's honor or reputation. Licensor
    agrees that in those jurisdictions (e.g. Japan), in which any exercise
    of the right granted in Section 3(b) of this License (the right to
    make Adaptations) would be deemed to be a distortion, mutilation,
    modification or other derogatory action prejudicial to the Original
    Author's honor and reputation, the Licensor will waive or not assert,
    as appropriate, this Section, to the fullest extent permitted by the
    applicable national law, to enable You to reasonably exercise Your
    right under Section 3(b) of this License (right to make Adaptations)
    but not otherwise.

5. Representations, Warranties and Disclaimer

UNLESS OTHERWISE MUTUALLY AGREED TO BY THE PARTIES IN WRITING, LICENSOR
OFFERS THE WORK AS-IS AND MAKES NO REPRESENTATIONS OR WARRANTIES OF ANY
KIND CONCERNING THE WORK, EXPRESS, IMPLIED, STATUTORY OR OTHERWISE,
INCLUDING, WITHOUT LIMITATION, WARRANTIES OF TITLE, MERCHANTIBILITY,
FITNESS FOR A PARTICULAR PURPOSE, NONINFRINGEMENT, OR THE ABSENCE OF
LATENT OR OTHER DEFECTS, ACCURACY, OR THE PRESENCE OF ABSENCE OF ERRORS,
WHETHER OR NOT DISCOVERABLE. SOME JURISDICTIONS DO NOT ALLOW THE EXCLUSION
OF IMPLIED WARRANTIES, SO SUCH EXCLUSION MAY NOT APPLY TO YOU.

6. Limitation on Liability. EXCEPT TO THE EXTENT REQUIRED BY APPLICABLE
LAW, IN NO EVENT WILL LICENSOR BE LIABLE TO YOU ON ANY LEGAL THEORY FOR
ANY SPECIAL, INCIDENTAL, CONSEQUENTIAL, PUNITIVE OR EXEMPLARY DAMAGES
ARISING OUT OF THIS LICENSE OR THE USE OF THE WORK, EVEN IF LICENSOR HAS
BEEN ADVISED OF THE POSSIBILITY OF SUCH DAMAGES.

7. Termination

 a. This License and the rights granted hereunder will terminate
    automatically upon any breach by You of the terms of this License.
    Individuals or entities who have received Adaptations or Collections
    from You under this License, however, will not have their licenses
    terminated provided such individuals or entities remain in full
    compliance with those licenses. Sections 1, 2, 5, 6, 7, and 8 will
    survive any termination of this License.
 b. Subject to the above terms and conditions, the license granted here is
    perpetual (for the duration of the applicable copyright in the Work).
    Notwithstanding the above, Licensor reserves the right to release the
    Work under different license terms or to stop distributing the Work at
    any time; provided, however that any such election will not serve to
    withdraw this License (or any other license that has been, or is
    required to be, granted under the terms of this License), and this
    License will continue in full force and effect unless terminated as
    stated above.

8. Miscellaneous

 a. Each time You Distribute or Publicly Perform the Work or a Collection,
    the Licensor offers to the recipient a license to the Work on the same
    terms and conditions as the license granted to You under this License.
 b. Each time You Distribute or Publicly Perform an Adaptation, Licensor
    offers to the recipient a license to the original Work on the same
    terms and conditions as the license granted to You under this License.
 c. If any provision of this License is invalid or unenforceable under
    applicable law, it shall not affect the validity or enforceability of
    the remainder of the terms of this License, and without further action
    by the parties to this agreement, such provision shall be reformed to
    the minimum extent necessary to make such provision valid and
    enforceable.
 d. No term or provision of this License shall be deemed waived and no
    breach consented to unless such waiver or consent shall be in writing
    and signed by the party to be charged with such waiver or consent.
 e. This License constitutes the entire agreement between the parties with
    respect to the Work licensed here. There are no understandings,
    agreements or representations with respect to the Work not specified
    here. Licensor shall not be bound by any additional provisions that
    may appear in any communication from You. This License may not be
    modified without the mutual written agreement of the Licensor and You.
 f. The rights granted under, and the subject matter referenced, in this
    License were drafted utilizing the terminology of the Berne Convention
    for the Protection of Literary and Artistic Works (as amended on
    September 28, 1979), the Rome Convention of 1961, the WIPO Copyright
    Treaty of 1996, the WIPO Performances and Phonograms Treaty of 1996
    and the Universal Copyright Convention (as revised on July 24, 1971).
    These rights and subject matter take effect in the relevant
    jurisdiction in which the License terms are sought to be enforced
    according to the corresponding provisions of the implementation of
    those treaty provisions in the applicable national law. If the
    standard suite of rights granted under applicable copyright law
    includes additional rights not granted under this License, such
    additional rights are deemed to be included in the License; this
    License is not intended to restrict the license of any rights under
    applicable law.


Creative Commons Notice

    Creative Commons is not a party to this License, and makes no warranty
    whatsoever in connection with the Work. Creative Commons will not be
    liable to You or any party on any legal theory for any damages
    whatsoever, including without limitation any general, special,
    incidental or consequential damages arising in connection to this
    license. Notwithstanding the foregoing two (2) sentences, if Creative
    Commons has expressly identified itself as the Licensor hereunder, it
    shall have all rights and obligations of Licensor.

    Except for the limited purpose of indicating to the public that the
    Work is licensed under the CCPL, Creative Commons does not authorize
    the use by either party of the trademark "Creative Commons" or any
    related trademark or logo of Creative Commons without the prior
    written consent of Creative Commons. Any permitted use will be in
    compliance with Creative Commons' then-current trademark usage
    guidelines, as may be published on its website or otherwise made
    available upon request from time to time. For the avoidance of doubt,
    this trademark restriction does not form part of the License.

    Creative Commons may be contacted at https://creativecommons.org/.
```

### V3 — Heroicons MIT；web/static/vendor/card-icons/HEROICONS-LICENSE.txt

原文 SHA256：`60e0b68c0f35c078eef3a5d29419d0b03ff84ec1df9c3f9d6e39a519a5ae7985`。

```text
MIT License

Copyright (c) Tailwind Labs, Inc.

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

### V4 — Simple Icons CC0；web/static/vendor/card-icons/SIMPLE-ICONS-LICENSE.md

原文 SHA256：`e43ad2b5f43c4798f7ceb92c259f4b854d59eb9dee3f4321ea0185758d7dea7b`。

```text
# CC0 1.0 Universal

## Statement of Purpose

The laws of most jurisdictions throughout the world automatically confer exclusive Copyright and Related Rights (defined below) upon the creator and subsequent owner(s) (each and all, an “owner”) of an original work of authorship and/or a database (each, a “Work”).

Certain owners wish to permanently relinquish those rights to a Work for the purpose of contributing to a commons of creative, cultural and scientific works (“Commons”) that the public can reliably and without fear of later claims of infringement build upon, modify, incorporate in other works, reuse and redistribute as freely as possible in any form whatsoever and for any purposes, including without limitation commercial purposes. These owners may contribute to the Commons to promote the ideal of a free culture and the further production of creative, cultural and scientific works, or to gain reputation or greater distribution for their Work in part through the use and efforts of others.

For these and/or other purposes and motivations, and without any expectation of additional consideration or compensation, the person associating CC0 with a Work (the “Affirmer”), to the extent that he or she is an owner of Copyright and Related Rights in the Work, voluntarily elects to apply CC0 to the Work and publicly distribute the Work under its terms, with knowledge of his or her Copyright and Related Rights in the Work and the meaning and intended legal effect of CC0 on those rights.

1. Copyright and Related Rights. A Work made available under CC0 may be protected by copyright and related or neighboring rights (“Copyright and Related Rights”). Copyright and Related Rights include, but are not limited to, the following:
    1. the right to reproduce, adapt, distribute, perform, display, communicate, and translate a Work;
    2. moral rights retained by the original author(s) and/or performer(s);
    3. publicity and privacy rights pertaining to a person’s image or likeness depicted in a Work;
    4. rights protecting against unfair competition in regards to a Work, subject to the limitations in paragraph 4(i), below;
    5. rights protecting the extraction, dissemination, use and reuse of data in a Work;
    6. database rights (such as those arising under Directive 96/9/EC of the European Parliament and of the Council of 11 March 1996 on the legal protection of databases, and under any national implementation thereof, including any amended or successor version of such directive); and
    7. other similar, equivalent or corresponding rights throughout the world based on applicable law or treaty, and any national implementations thereof.

2. Waiver. To the greatest extent permitted by, but not in contravention of, applicable law, Affirmer hereby overtly, fully, permanently, irrevocably and unconditionally waives, abandons, and surrenders all of Affirmer’s Copyright and Related Rights and associated claims and causes of action, whether now known or unknown (including existing as well as future claims and causes of action), in the Work (i) in all territories worldwide, (ii) for the maximum duration provided by applicable law or treaty (including future time extensions), (iii) in any current or future medium and for any number of copies, and (iv) for any purpose whatsoever, including without limitation commercial, advertising or promotional purposes (the “Waiver”). Affirmer makes the Waiver for the benefit of each member of the public at large and to the detriment of Affirmer’s heirs and successors, fully intending that such Waiver shall not be subject to revocation, rescission, cancellation, termination, or any other legal or equitable action to disrupt the quiet enjoyment of the Work by the public as contemplated by Affirmer’s express Statement of Purpose.

3. Public License Fallback. Should any part of the Waiver for any reason be judged legally invalid or ineffective under applicable law, then the Waiver shall be preserved to the maximum extent permitted taking into account Affirmer’s express Statement of Purpose. In addition, to the extent the Waiver is so judged Affirmer hereby grants to each affected person a royalty-free, non transferable, non sublicensable, non exclusive, irrevocable and unconditional license to exercise Affirmer’s Copyright and Related Rights in the Work (i) in all territories worldwide, (ii) for the maximum duration provided by applicable law or treaty (including future time extensions), (iii) in any current or future medium and for any number of copies, and (iv) for any purpose whatsoever, including without limitation commercial, advertising or promotional purposes (the “License”). The License shall be deemed effective as of the date CC0 was applied by Affirmer to the Work. Should any part of the License for any reason be judged legally invalid or ineffective under applicable law, such partial invalidity or ineffectiveness shall not invalidate the remainder of the License, and in such case Affirmer hereby affirms that he or she will not (i) exercise any of his or her remaining Copyright and Related Rights in the Work or (ii) assert any associated claims and causes of action with respect to the Work, in either case contrary to Affirmer’s express Statement of Purpose.

4. Limitations and Disclaimers.
    1. No trademark or patent rights held by Affirmer are waived, abandoned, surrendered, licensed or otherwise affected by this document.
    2. Affirmer offers the Work as-is and makes no representations or warranties of any kind concerning the Work, express, implied, statutory or otherwise, including without limitation warranties of title, merchantability, fitness for a particular purpose, non infringement, or the absence of latent or other defects, accuracy, or the present or absence of errors, whether or not discoverable, all to the greatest extent permissible under applicable law.
    3. Affirmer disclaims responsibility for clearing rights of other persons that may apply to the Work or any use thereof, including without limitation any person’s Copyright and Related Rights in the Work. Further, Affirmer disclaims responsibility for obtaining any necessary consents, permissions or other rights required for any use of the Work.
    4. Affirmer understands and acknowledges that Creative Commons is not a party to this document and has no duty or obligation with respect to this CC0 or use of the Work.

For more information, please see <https://creativecommons.org/publicdomain/zero/1.0/>.
```

### V5 — flag-icons MIT；web/static/vendor/flag-icons/LICENSE

原文 SHA256：`8f1195d55a2fd315a07d812328470ca9ba2abb78c8d317ff19619d5125e00cea`。

```text
The MIT License (MIT)

Copyright (c) 2013 Panayiotis Lipiridis

Permission is hereby granted, free of charge, to any person obtaining a copy of
this software and associated documentation files (the "Software"), to deal in
the Software without restriction, including without limitation the rights to
use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies
of the Software, and to permit persons to whom the Software is furnished to do
so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

### V6 — uPlot MIT；web/static/vendor/uplot/LICENSE

原文 SHA256：`8f989229699b4fe2f1a0432d0e9edc338a8a911e250e2d1b01ecd770a5f5b1bd`。

```text
The MIT License (MIT)

Copyright (c) 2022 Leon Sorokin

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
THE SOFTWARE.
```

## 附录 M：Go 模块许可证原文

### M1 — github.com/shirou/gopsutil/v4 v4.25.6 / LICENSE

原文 SHA256：`ad1e64b82c04fb2ee6bfe521bff01266971ffaa70500024d4ac767c6033aafb9`。

```text
gopsutil is distributed under BSD license reproduced below.

Copyright (c) 2014, WAKAYAMA Shirou
All rights reserved.

Redistribution and use in source and binary forms, with or without modification,
are permitted provided that the following conditions are met:

 * Redistributions of source code must retain the above copyright notice, this
   list of conditions and the following disclaimer.
 * Redistributions in binary form must reproduce the above copyright notice,
   this list of conditions and the following disclaimer in the documentation
   and/or other materials provided with the distribution.
 * Neither the name of the gopsutil authors nor the names of its contributors
   may be used to endorse or promote products derived from this software without
   specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS" AND
ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED
WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT OWNER OR CONTRIBUTORS BE LIABLE FOR
ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES
(INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES;
LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON
ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS
SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.


-------
internal/common/binary.go in the gopsutil is copied and modified from golang/encoding/binary.go.



Copyright (c) 2009 The Go Authors. All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google Inc. nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

### M2 — golang.org/x/crypto v0.40.0 / LICENSE

原文 SHA256：`911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad`。

```text
Copyright 2009 The Go Authors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google LLC nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

### M2 — golang.org/x/crypto / PATENTS

原文 SHA256：`96f408bfae65bf137fc2525d3ecb030271c50c1e90799f87abf8846d8dd505cc`。

```text
Additional IP Rights Grant (Patents)

"This implementation" means the copyrightable works distributed by
Google as part of the Go project.

Google hereby grants to You a perpetual, worldwide, non-exclusive,
no-charge, royalty-free, irrevocable (except as stated in this section)
patent license to make, have made, use, offer to sell, sell, import,
transfer and otherwise run, modify and propagate the contents of this
implementation of Go, where such license applies only to those patent
claims, both currently owned or controlled by Google and acquired in
the future, licensable by Google that are necessarily infringed by this
implementation of Go.  This grant does not include claims that would be
infringed only as a consequence of further modification of this
implementation.  If you or your agent or exclusive licensee institute or
order or agree to the institution of patent litigation against any
entity (including a cross-claim or counterclaim in a lawsuit) alleging
that this implementation of Go or any code incorporated within this
implementation of Go constitutes direct or contributory patent
infringement, or inducement of patent infringement, then any patent
rights granted to you under this License for this implementation of Go
shall terminate as of the date such litigation is filed.
```

### M3 — golang.org/x/net v0.42.0 / LICENSE

原文 SHA256：`911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad`。

```text
Copyright 2009 The Go Authors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google LLC nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

### M3 — golang.org/x/net / PATENTS

原文 SHA256：`96f408bfae65bf137fc2525d3ecb030271c50c1e90799f87abf8846d8dd505cc`。

```text
Additional IP Rights Grant (Patents)

"This implementation" means the copyrightable works distributed by
Google as part of the Go project.

Google hereby grants to You a perpetual, worldwide, non-exclusive,
no-charge, royalty-free, irrevocable (except as stated in this section)
patent license to make, have made, use, offer to sell, sell, import,
transfer and otherwise run, modify and propagate the contents of this
implementation of Go, where such license applies only to those patent
claims, both currently owned or controlled by Google and acquired in
the future, licensable by Google that are necessarily infringed by this
implementation of Go.  This grant does not include claims that would be
infringed only as a consequence of further modification of this
implementation.  If you or your agent or exclusive licensee institute or
order or agree to the institution of patent litigation against any
entity (including a cross-claim or counterclaim in a lawsuit) alleging
that this implementation of Go or any code incorporated within this
implementation of Go constitutes direct or contributory patent
infringement, or inducement of patent infringement, then any patent
rights granted to you under this License for this implementation of Go
shall terminate as of the date such litigation is filed.
```

### M4 — golang.org/x/sys v0.44.0 / LICENSE

原文 SHA256：`911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad`。

```text
Copyright 2009 The Go Authors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google LLC nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

### M4 — golang.org/x/sys / PATENTS

原文 SHA256：`96f408bfae65bf137fc2525d3ecb030271c50c1e90799f87abf8846d8dd505cc`。

```text
Additional IP Rights Grant (Patents)

"This implementation" means the copyrightable works distributed by
Google as part of the Go project.

Google hereby grants to You a perpetual, worldwide, non-exclusive,
no-charge, royalty-free, irrevocable (except as stated in this section)
patent license to make, have made, use, offer to sell, sell, import,
transfer and otherwise run, modify and propagate the contents of this
implementation of Go, where such license applies only to those patent
claims, both currently owned or controlled by Google and acquired in
the future, licensable by Google that are necessarily infringed by this
implementation of Go.  This grant does not include claims that would be
infringed only as a consequence of further modification of this
implementation.  If you or your agent or exclusive licensee institute or
order or agree to the institution of patent litigation against any
entity (including a cross-claim or counterclaim in a lawsuit) alleging
that this implementation of Go or any code incorporated within this
implementation of Go constitutes direct or contributory patent
infringement, or inducement of patent infringement, then any patent
rights granted to you under this License for this implementation of Go
shall terminate as of the date such litigation is filed.
```

### M5 — golang.org/x/term v0.33.0 / LICENSE

原文 SHA256：`911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad`。

```text
Copyright 2009 The Go Authors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google LLC nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

### M5 — golang.org/x/term / PATENTS

原文 SHA256：`96f408bfae65bf137fc2525d3ecb030271c50c1e90799f87abf8846d8dd505cc`。

```text
Additional IP Rights Grant (Patents)

"This implementation" means the copyrightable works distributed by
Google as part of the Go project.

Google hereby grants to You a perpetual, worldwide, non-exclusive,
no-charge, royalty-free, irrevocable (except as stated in this section)
patent license to make, have made, use, offer to sell, sell, import,
transfer and otherwise run, modify and propagate the contents of this
implementation of Go, where such license applies only to those patent
claims, both currently owned or controlled by Google and acquired in
the future, licensable by Google that are necessarily infringed by this
implementation of Go.  This grant does not include claims that would be
infringed only as a consequence of further modification of this
implementation.  If you or your agent or exclusive licensee institute or
order or agree to the institution of patent litigation against any
entity (including a cross-claim or counterclaim in a lawsuit) alleging
that this implementation of Go or any code incorporated within this
implementation of Go constitutes direct or contributory patent
infringement, or inducement of patent infringement, then any patent
rights granted to you under this License for this implementation of Go
shall terminate as of the date such litigation is filed.
```

### M6 — modernc.org/sqlite v1.46.2 / LICENSE

原文 SHA256：`c6fe05491a60ae13bcd223088d2705e36dede24e5587226231d2459ada5c4822`。

```text
Copyright (c) 2017 The Sqlite Authors. All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

1. Redistributions of source code must retain the above copyright notice, this
list of conditions and the following disclaimer.

2. Redistributions in binary form must reproduce the above copyright notice,
this list of conditions and the following disclaimer in the documentation
and/or other materials provided with the distribution.

3. Neither the name of the copyright holder nor the names of its contributors
may be used to endorse or promote products derived from this software without
specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS" AND
ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED
WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE LIABLE
FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR
SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER
CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY,
OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

### M7 — github.com/dustin/go-humanize v1.0.1 / LICENSE

原文 SHA256：`a973b4498c13eb74baa2a8e5c351426a6826f2fcdd909916dbe53ee2e755fd71`。

```text
Copyright (c) 2005-2008  Dustin Sallings <dustin@spy.net>

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

<http://www.opensource.org/licenses/mit-license.php>
```

### M8 — github.com/ebitengine/purego v0.8.4 / LICENSE

原文 SHA256：`b40930bbcf80744c86c46a12bc9da056641d722716c378f5659b9e555ef833e1`。

```text
                                 Apache License
                           Version 2.0, January 2004
                        http://www.apache.org/licenses/

   TERMS AND CONDITIONS FOR USE, REPRODUCTION, AND DISTRIBUTION

   1. Definitions.

      "License" shall mean the terms and conditions for use, reproduction,
      and distribution as defined by Sections 1 through 9 of this document.

      "Licensor" shall mean the copyright owner or entity authorized by
      the copyright owner that is granting the License.

      "Legal Entity" shall mean the union of the acting entity and all
      other entities that control, are controlled by, or are under common
      control with that entity. For the purposes of this definition,
      "control" means (i) the power, direct or indirect, to cause the
      direction or management of such entity, whether by contract or
      otherwise, or (ii) ownership of fifty percent (50%) or more of the
      outstanding shares, or (iii) beneficial ownership of such entity.

      "You" (or "Your") shall mean an individual or Legal Entity
      exercising permissions granted by this License.

      "Source" form shall mean the preferred form for making modifications,
      including but not limited to software source code, documentation
      source, and configuration files.

      "Object" form shall mean any form resulting from mechanical
      transformation or translation of a Source form, including but
      not limited to compiled object code, generated documentation,
      and conversions to other media types.

      "Work" shall mean the work of authorship, whether in Source or
      Object form, made available under the License, as indicated by a
      copyright notice that is included in or attached to the work
      (an example is provided in the Appendix below).

      "Derivative Works" shall mean any work, whether in Source or Object
      form, that is based on (or derived from) the Work and for which the
      editorial revisions, annotations, elaborations, or other modifications
      represent, as a whole, an original work of authorship. For the purposes
      of this License, Derivative Works shall not include works that remain
      separable from, or merely link (or bind by name) to the interfaces of,
      the Work and Derivative Works thereof.

      "Contribution" shall mean any work of authorship, including
      the original version of the Work and any modifications or additions
      to that Work or Derivative Works thereof, that is intentionally
      submitted to Licensor for inclusion in the Work by the copyright owner
      or by an individual or Legal Entity authorized to submit on behalf of
      the copyright owner. For the purposes of this definition, "submitted"
      means any form of electronic, verbal, or written communication sent
      to the Licensor or its representatives, including but not limited to
      communication on electronic mailing lists, source code control systems,
      and issue tracking systems that are managed by, or on behalf of, the
      Licensor for the purpose of discussing and improving the Work, but
      excluding communication that is conspicuously marked or otherwise
      designated in writing by the copyright owner as "Not a Contribution."

      "Contributor" shall mean Licensor and any individual or Legal Entity
      on behalf of whom a Contribution has been received by Licensor and
      subsequently incorporated within the Work.

   2. Grant of Copyright License. Subject to the terms and conditions of
      this License, each Contributor hereby grants to You a perpetual,
      worldwide, non-exclusive, no-charge, royalty-free, irrevocable
      copyright license to reproduce, prepare Derivative Works of,
      publicly display, publicly perform, sublicense, and distribute the
      Work and such Derivative Works in Source or Object form.

   3. Grant of Patent License. Subject to the terms and conditions of
      this License, each Contributor hereby grants to You a perpetual,
      worldwide, non-exclusive, no-charge, royalty-free, irrevocable
      (except as stated in this section) patent license to make, have made,
      use, offer to sell, sell, import, and otherwise transfer the Work,
      where such license applies only to those patent claims licensable
      by such Contributor that are necessarily infringed by their
      Contribution(s) alone or by combination of their Contribution(s)
      with the Work to which such Contribution(s) was submitted. If You
      institute patent litigation against any entity (including a
      cross-claim or counterclaim in a lawsuit) alleging that the Work
      or a Contribution incorporated within the Work constitutes direct
      or contributory patent infringement, then any patent licenses
      granted to You under this License for that Work shall terminate
      as of the date such litigation is filed.

   4. Redistribution. You may reproduce and distribute copies of the
      Work or Derivative Works thereof in any medium, with or without
      modifications, and in Source or Object form, provided that You
      meet the following conditions:

      (a) You must give any other recipients of the Work or
          Derivative Works a copy of this License; and

      (b) You must cause any modified files to carry prominent notices
          stating that You changed the files; and

      (c) You must retain, in the Source form of any Derivative Works
          that You distribute, all copyright, patent, trademark, and
          attribution notices from the Source form of the Work,
          excluding those notices that do not pertain to any part of
          the Derivative Works; and

      (d) If the Work includes a "NOTICE" text file as part of its
          distribution, then any Derivative Works that You distribute must
          include a readable copy of the attribution notices contained
          within such NOTICE file, excluding those notices that do not
          pertain to any part of the Derivative Works, in at least one
          of the following places: within a NOTICE text file distributed
          as part of the Derivative Works; within the Source form or
          documentation, if provided along with the Derivative Works; or,
          within a display generated by the Derivative Works, if and
          wherever such third-party notices normally appear. The contents
          of the NOTICE file are for informational purposes only and
          do not modify the License. You may add Your own attribution
          notices within Derivative Works that You distribute, alongside
          or as an addendum to the NOTICE text from the Work, provided
          that such additional attribution notices cannot be construed
          as modifying the License.

      You may add Your own copyright statement to Your modifications and
      may provide additional or different license terms and conditions
      for use, reproduction, or distribution of Your modifications, or
      for any such Derivative Works as a whole, provided Your use,
      reproduction, and distribution of the Work otherwise complies with
      the conditions stated in this License.

   5. Submission of Contributions. Unless You explicitly state otherwise,
      any Contribution intentionally submitted for inclusion in the Work
      by You to the Licensor shall be under the terms and conditions of
      this License, without any additional terms or conditions.
      Notwithstanding the above, nothing herein shall supersede or modify
      the terms of any separate license agreement you may have executed
      with Licensor regarding such Contributions.

   6. Trademarks. This License does not grant permission to use the trade
      names, trademarks, service marks, or product names of the Licensor,
      except as required for reasonable and customary use in describing the
      origin of the Work and reproducing the content of the NOTICE file.

   7. Disclaimer of Warranty. Unless required by applicable law or
      agreed to in writing, Licensor provides the Work (and each
      Contributor provides its Contributions) on an "AS IS" BASIS,
      WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
      implied, including, without limitation, any warranties or conditions
      of TITLE, NON-INFRINGEMENT, MERCHANTABILITY, or FITNESS FOR A
      PARTICULAR PURPOSE. You are solely responsible for determining the
      appropriateness of using or redistributing the Work and assume any
      risks associated with Your exercise of permissions under this License.

   8. Limitation of Liability. In no event and under no legal theory,
      whether in tort (including negligence), contract, or otherwise,
      unless required by applicable law (such as deliberate and grossly
      negligent acts) or agreed to in writing, shall any Contributor be
      liable to You for damages, including any direct, indirect, special,
      incidental, or consequential damages of any character arising as a
      result of this License or out of the use or inability to use the
      Work (including but not limited to damages for loss of goodwill,
      work stoppage, computer failure or malfunction, or any and all
      other commercial damages or losses), even if such Contributor
      has been advised of the possibility of such damages.

   9. Accepting Warranty or Additional Liability. While redistributing
      the Work or Derivative Works thereof, You may choose to offer,
      and charge a fee for, acceptance of support, warranty, indemnity,
      or other liability obligations and/or rights consistent with this
      License. However, in accepting such obligations, You may act only
      on Your own behalf and on Your sole responsibility, not on behalf
      of any other Contributor, and only if You agree to indemnify,
      defend, and hold each Contributor harmless for any liability
      incurred by, or claims asserted against, such Contributor by reason
      of your accepting any such warranty or additional liability.

   END OF TERMS AND CONDITIONS

   APPENDIX: How to apply the Apache License to your work.

      To apply the Apache License to your work, attach the following
      boilerplate notice, with the fields enclosed by brackets "{}"
      replaced with your own identifying information. (Don't include
      the brackets!)  The text should be enclosed in the appropriate
      comment syntax for the file format. We also recommend that a
      file or class name and description of purpose be included on the
      same "printed page" as the copyright notice for easier
      identification within third-party archives.

   Copyright {yyyy} {name of copyright owner}

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
```

### M8 — purego 源文件版权声明

原文 SHA256：`7df335000c82ee8a60f5cbbd3d5b29dfd8040cf4543a35cb6e65b79ae4089937`。

```text
// SPDX-License-Identifier: Apache-2.0
// SPDX-FileCopyrightText: 2022 The Ebitengine Authors

//go:build darwin || freebsd || linux || windows
```

### M9 — github.com/go-ole/go-ole v1.2.6 / LICENSE

原文 SHA256：`d883dbca28769ff23dd4368a8edb7ad9f9f23a8f86adde2875c6e41a828aac62`。

```text
The MIT License (MIT)

Copyright © 2013-2017 Yasuhiro Matsumoto, <mattn.jp@gmail.com>

Permission is hereby granted, free of charge, to any person obtaining a copy of
this software and associated documentation files (the “Software”), to deal in
the Software without restriction, including without limitation the rights to
use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies
of the Software, and to permit persons to whom the Software is furnished to do
so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED “AS IS”, WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

### M10 — github.com/google/uuid v1.6.0 / LICENSE

原文 SHA256：`0a8d61ed3cbfd5312326e8126c31ce9c627a283adc99131b56896d29ada04b2d`。

```text
Copyright (c) 2009,2014 Google Inc. All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google Inc. nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

### M11 — github.com/lufia/plan9stats v0.0.0-20211012122336-39d0f177ccd0 / LICENSE

原文 SHA256：`879acfe02d3790e0bcdd1ee2299fd8576995eaa94c7e14d1a94ee3e018ad7bc2`。

```text
BSD 3-Clause License

Copyright (c) 2019, KADOTA, Kyohei
All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

1. Redistributions of source code must retain the above copyright notice, this
   list of conditions and the following disclaimer.

2. Redistributions in binary form must reproduce the above copyright notice,
   this list of conditions and the following disclaimer in the documentation
   and/or other materials provided with the distribution.

3. Neither the name of the copyright holder nor the names of its
   contributors may be used to endorse or promote products derived from
   this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE LIABLE
FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR
SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER
CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY,
OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

### M12 — github.com/mattn/go-isatty v0.0.20 / LICENSE

原文 SHA256：`08eab1118c80885fa1fa6a6dd7303f65a379fcb3733e063d20d1bbc2c76e6fa1`。

```text
Copyright (c) Yasuhiro MATSUMOTO <mattn.jp@gmail.com>

MIT License (Expat)

Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated documentation files (the "Software"), to deal in the Software without restriction, including without limitation the rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the Software, and to permit persons to whom the Software is furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
```

### M13 — github.com/ncruces/go-strftime v1.0.0 / LICENSE

原文 SHA256：`38ae43959daf953a393a585b2988672cb65a5a541aca0d0be5e72595a0a16883`。

```text
MIT License

Copyright (c) 2022 Nuno Cruces

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

### M14 — github.com/power-devops/perfstat v0.0.0-20210106213030-5aafc221ea8c / LICENSE

原文 SHA256：`d2568033889c7eb3666ef1cb1f47f84bd49d25847aaad5dd87f18df384238079`。

```text
MIT License

Copyright (c) 2020 Power DevOps

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

### M15 — github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec / LICENSE

原文 SHA256：`dd26a7abddd02e2d0aba97805b31f248ef7835d9e10da289b22e3b8ab78b324d`。

```text
Copyright (c) 2012 The Go Authors. All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google Inc. nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

### M16 — github.com/tklauser/go-sysconf v0.3.12 / LICENSE

原文 SHA256：`fdaaa923d83d44f8b2ed1185600c5aa8c10673f11fba9feb78bd2816786fd6be`。

```text
BSD 3-Clause License

Copyright (c) 2018-2022, Tobias Klauser
All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

* Redistributions of source code must retain the above copyright notice, this
  list of conditions and the following disclaimer.

* Redistributions in binary form must reproduce the above copyright notice,
  this list of conditions and the following disclaimer in the documentation
  and/or other materials provided with the distribution.

* Neither the name of the copyright holder nor the names of its
  contributors may be used to endorse or promote products derived from
  this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE LIABLE
FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR
SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER
CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY,
OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

### M17 — github.com/tklauser/numcpus v0.6.1 / LICENSE

原文 SHA256：`cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30`。

```text

                                 Apache License
                           Version 2.0, January 2004
                        http://www.apache.org/licenses/

   TERMS AND CONDITIONS FOR USE, REPRODUCTION, AND DISTRIBUTION

   1. Definitions.

      "License" shall mean the terms and conditions for use, reproduction,
      and distribution as defined by Sections 1 through 9 of this document.

      "Licensor" shall mean the copyright owner or entity authorized by
      the copyright owner that is granting the License.

      "Legal Entity" shall mean the union of the acting entity and all
      other entities that control, are controlled by, or are under common
      control with that entity. For the purposes of this definition,
      "control" means (i) the power, direct or indirect, to cause the
      direction or management of such entity, whether by contract or
      otherwise, or (ii) ownership of fifty percent (50%) or more of the
      outstanding shares, or (iii) beneficial ownership of such entity.

      "You" (or "Your") shall mean an individual or Legal Entity
      exercising permissions granted by this License.

      "Source" form shall mean the preferred form for making modifications,
      including but not limited to software source code, documentation
      source, and configuration files.

      "Object" form shall mean any form resulting from mechanical
      transformation or translation of a Source form, including but
      not limited to compiled object code, generated documentation,
      and conversions to other media types.

      "Work" shall mean the work of authorship, whether in Source or
      Object form, made available under the License, as indicated by a
      copyright notice that is included in or attached to the work
      (an example is provided in the Appendix below).

      "Derivative Works" shall mean any work, whether in Source or Object
      form, that is based on (or derived from) the Work and for which the
      editorial revisions, annotations, elaborations, or other modifications
      represent, as a whole, an original work of authorship. For the purposes
      of this License, Derivative Works shall not include works that remain
      separable from, or merely link (or bind by name) to the interfaces of,
      the Work and Derivative Works thereof.

      "Contribution" shall mean any work of authorship, including
      the original version of the Work and any modifications or additions
      to that Work or Derivative Works thereof, that is intentionally
      submitted to Licensor for inclusion in the Work by the copyright owner
      or by an individual or Legal Entity authorized to submit on behalf of
      the copyright owner. For the purposes of this definition, "submitted"
      means any form of electronic, verbal, or written communication sent
      to the Licensor or its representatives, including but not limited to
      communication on electronic mailing lists, source code control systems,
      and issue tracking systems that are managed by, or on behalf of, the
      Licensor for the purpose of discussing and improving the Work, but
      excluding communication that is conspicuously marked or otherwise
      designated in writing by the copyright owner as "Not a Contribution."

      "Contributor" shall mean Licensor and any individual or Legal Entity
      on behalf of whom a Contribution has been received by Licensor and
      subsequently incorporated within the Work.

   2. Grant of Copyright License. Subject to the terms and conditions of
      this License, each Contributor hereby grants to You a perpetual,
      worldwide, non-exclusive, no-charge, royalty-free, irrevocable
      copyright license to reproduce, prepare Derivative Works of,
      publicly display, publicly perform, sublicense, and distribute the
      Work and such Derivative Works in Source or Object form.

   3. Grant of Patent License. Subject to the terms and conditions of
      this License, each Contributor hereby grants to You a perpetual,
      worldwide, non-exclusive, no-charge, royalty-free, irrevocable
      (except as stated in this section) patent license to make, have made,
      use, offer to sell, sell, import, and otherwise transfer the Work,
      where such license applies only to those patent claims licensable
      by such Contributor that are necessarily infringed by their
      Contribution(s) alone or by combination of their Contribution(s)
      with the Work to which such Contribution(s) was submitted. If You
      institute patent litigation against any entity (including a
      cross-claim or counterclaim in a lawsuit) alleging that the Work
      or a Contribution incorporated within the Work constitutes direct
      or contributory patent infringement, then any patent licenses
      granted to You under this License for that Work shall terminate
      as of the date such litigation is filed.

   4. Redistribution. You may reproduce and distribute copies of the
      Work or Derivative Works thereof in any medium, with or without
      modifications, and in Source or Object form, provided that You
      meet the following conditions:

      (a) You must give any other recipients of the Work or
          Derivative Works a copy of this License; and

      (b) You must cause any modified files to carry prominent notices
          stating that You changed the files; and

      (c) You must retain, in the Source form of any Derivative Works
          that You distribute, all copyright, patent, trademark, and
          attribution notices from the Source form of the Work,
          excluding those notices that do not pertain to any part of
          the Derivative Works; and

      (d) If the Work includes a "NOTICE" text file as part of its
          distribution, then any Derivative Works that You distribute must
          include a readable copy of the attribution notices contained
          within such NOTICE file, excluding those notices that do not
          pertain to any part of the Derivative Works, in at least one
          of the following places: within a NOTICE text file distributed
          as part of the Derivative Works; within the Source form or
          documentation, if provided along with the Derivative Works; or,
          within a display generated by the Derivative Works, if and
          wherever such third-party notices normally appear. The contents
          of the NOTICE file are for informational purposes only and
          do not modify the License. You may add Your own attribution
          notices within Derivative Works that You distribute, alongside
          or as an addendum to the NOTICE text from the Work, provided
          that such additional attribution notices cannot be construed
          as modifying the License.

      You may add Your own copyright statement to Your modifications and
      may provide additional or different license terms and conditions
      for use, reproduction, or distribution of Your modifications, or
      for any such Derivative Works as a whole, provided Your use,
      reproduction, and distribution of the Work otherwise complies with
      the conditions stated in this License.

   5. Submission of Contributions. Unless You explicitly state otherwise,
      any Contribution intentionally submitted for inclusion in the Work
      by You to the Licensor shall be under the terms and conditions of
      this License, without any additional terms or conditions.
      Notwithstanding the above, nothing herein shall supersede or modify
      the terms of any separate license agreement you may have executed
      with Licensor regarding such Contributions.

   6. Trademarks. This License does not grant permission to use the trade
      names, trademarks, service marks, or product names of the Licensor,
      except as required for reasonable and customary use in describing the
      origin of the Work and reproducing the content of the NOTICE file.

   7. Disclaimer of Warranty. Unless required by applicable law or
      agreed to in writing, Licensor provides the Work (and each
      Contributor provides its Contributions) on an "AS IS" BASIS,
      WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
      implied, including, without limitation, any warranties or conditions
      of TITLE, NON-INFRINGEMENT, MERCHANTABILITY, or FITNESS FOR A
      PARTICULAR PURPOSE. You are solely responsible for determining the
      appropriateness of using or redistributing the Work and assume any
      risks associated with Your exercise of permissions under this License.

   8. Limitation of Liability. In no event and under no legal theory,
      whether in tort (including negligence), contract, or otherwise,
      unless required by applicable law (such as deliberate and grossly
      negligent acts) or agreed to in writing, shall any Contributor be
      liable to You for damages, including any direct, indirect, special,
      incidental, or consequential damages of any character arising as a
      result of this License or out of the use or inability to use the
      Work (including but not limited to damages for loss of goodwill,
      work stoppage, computer failure or malfunction, or any and all
      other commercial damages or losses), even if such Contributor
      has been advised of the possibility of such damages.

   9. Accepting Warranty or Additional Liability. While redistributing
      the Work or Derivative Works thereof, You may choose to offer,
      and charge a fee for, acceptance of support, warranty, indemnity,
      or other liability obligations and/or rights consistent with this
      License. However, in accepting such obligations, You may act only
      on Your own behalf and on Your sole responsibility, not on behalf
      of any other Contributor, and only if You agree to indemnify,
      defend, and hold each Contributor harmless for any liability
      incurred by, or claims asserted against, such Contributor by reason
      of your accepting any such warranty or additional liability.

   END OF TERMS AND CONDITIONS

   APPENDIX: How to apply the Apache License to your work.

      To apply the Apache License to your work, attach the following
      boilerplate notice, with the fields enclosed by brackets "[]"
      replaced with your own identifying information. (Don't include
      the brackets!)  The text should be enclosed in the appropriate
      comment syntax for the file format. We also recommend that a
      file or class name and description of purpose be included on the
      same "printed page" as the copyright notice for easier
      identification within third-party archives.

   Copyright [yyyy] [name of copyright owner]

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
```

### M17 — numcpus 作者及 Apache 声明

原文 SHA256：`1acc87223610a8e462f00dbf58a514fa44747186f7a2e40a564c1269878f42b5`。

```text
// Copyright 2018-2022 Tobias Klauser
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package numcpus provides information about the number of CPUs in the system.
//
// It gets the number of CPUs (online, offline, present, possible or kernel
// maximum) on Linux, Darwin, FreeBSD, NetBSD, OpenBSD, DragonflyBSD,
// Solaris/Illumos or Windows systems.
//
// On Linux, the information is retrieved by reading the corresponding CPU
// topology files in /sys/devices/system/cpu.
//
// On BSD systems, the information is retrieved using the hw.ncpu and
// hw.ncpuonline sysctls, if supported.
//
// On Windows systems, the information is retrieved using the
// GetActiveProcessorCount and GetMaximumProcessorCount functions, respectively.
//
// Not all functions are supported on Darwin, FreeBSD, NetBSD, OpenBSD,
// DragonflyBSD, Solaris/Illumos and Windows. ErrNotSupported is returned in
// case a function is not supported on a particular platform.
```

### M18 — github.com/yusufpapurcu/wmi v1.2.4 / LICENSE

原文 SHA256：`66e24ffb77bdafac86bd5c47ea1c3d44e2d7546ca9caa219ee66a110584864e7`。

```text
The MIT License (MIT)

Copyright (c) 2013 Stack Exchange

Permission is hereby granted, free of charge, to any person obtaining a copy of
this software and associated documentation files (the "Software"), to deal in
the Software without restriction, including without limitation the rights to
use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of
the Software, and to permit persons to whom the Software is furnished to do so,
subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS
FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR
COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER
IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN
CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
```

### M19 — modernc.org/libc v1.70.0 / LICENSE

原文 SHA256：`95ff867eb55a56935fa7492406cfa953fb7c13ca73f4c0a86ae05756b4605600`。

```text
Copyright (c) 2017 The Libc Authors. All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the names of the authors nor the names of the
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

### M20 — modernc.org/mathutil v1.7.1 / LICENSE

原文 SHA256：`bfa9bf72a72ca009fd62a8f84fca3dca67e51d93af96352723646599898b6cf5`。

```text
Copyright (c) 2014 The mathutil Authors. All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the names of the authors nor the names of the
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

### M20 — mathutil / mersenne/LICENSE

原文 SHA256：`c05630f9ca899ea094620d37daea6ba66f99adbf9268ea5fea9db607e1102bd9`。

```text
Copyright (c) 2014 The mersenne Authors. All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the names of the authors nor the names of the
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

### M21 — modernc.org/memory v1.11.0 / LICENSE

原文 SHA256：`59895e669f48f168b6b858358f6005779cdf40a265f7828813061b56af67b496`。

```text
Copyright (c) 2017 The Memory Authors. All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the names of the authors nor the names of the
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

### M21 — memory / LICENSE-GO

原文 SHA256：`2d36597f7117c38b006835ae7f537487207d8ec407aa9d9980794b2030cbc067`。

```text
Copyright (c) 2009 The Go Authors. All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google Inc. nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

### M21 — memory / LICENSE-MMAP-GO

原文 SHA256：`c2eba69f20d05414538c3a5df7694dde392e065ff70882e1625e90f5d6659fff`。

```text
Copyright (c) 2011, Evan Shaw <edsrzf@gmail.com>
All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:
    * Redistributions of source code must retain the above copyright
      notice, this list of conditions and the following disclaimer.
    * Redistributions in binary form must reproduce the above copyright
      notice, this list of conditions and the following disclaimer in the
      documentation and/or other materials provided with the distribution.
    * Neither the name of the copyright holder nor the
      names of its contributors may be used to endorse or promote products
      derived from this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS" AND
ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED
WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
DISCLAIMED. IN NO EVENT SHALL <COPYRIGHT HOLDER> BE LIABLE FOR ANY
DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES
(INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES;
LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND
ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS
SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```


### M22 — golang.org/x/text v0.27.0 / LICENSE

原文 SHA256：`911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad`。

```text
Copyright 2009 The Go Authors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google LLC nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

### M22 — golang.org/x/text v0.27.0 / PATENTS

原文 SHA256：`96f408bfae65bf137fc2525d3ecb030271c50c1e90799f87abf8846d8dd505cc`。

```text
Additional IP Rights Grant (Patents)

"This implementation" means the copyrightable works distributed by
Google as part of the Go project.

Google hereby grants to You a perpetual, worldwide, non-exclusive,
no-charge, royalty-free, irrevocable (except as stated in this section)
patent license to make, have made, use, offer to sell, sell, import,
transfer and otherwise run, modify and propagate the contents of this
implementation of Go, where such license applies only to those patent
claims, both currently owned or controlled by Google and acquired in
the future, licensable by Google that are necessarily infringed by this
implementation of Go.  This grant does not include claims that would be
infringed only as a consequence of further modification of this
implementation.  If you or your agent or exclusive licensee institute or
order or agree to the institution of patent litigation against any
entity (including a cross-claim or counterclaim in a lawsuit) alleging
that this implementation of Go or any code incorporated within this
implementation of Go constitutes direct or contributory patent
infringement, or inducement of patent infringement, then any patent
rights granted to you under this License for this implementation of Go
shall terminate as of the date such litigation is filed.
```

## 附录 N：模块内嵌套来源原文

### N1 — sqlite v1.46.2 / SQLITE-LICENSE

原文 SHA256：`8438c9c89b849131ead81d5435cb97fcf052df5b0b286dda8a2d4c29e6cb3fd0`。

```text
SQLite Is Public Domain

All of the code and documentation in SQLite has been dedicated to the public
domain by the authors. All code authors, and representatives of the companies
they work for, have signed affidavits dedicating their contributions to the
public domain and originals of those signed affidavits are stored in a firesafe
at the main offices of Hwaci. Anyone is free to copy, modify, publish, use,
compile, sell, or distribute the original SQLite code, either in source code
form or as a compiled binary, for any purpose, commercial or non-commercial,
and by any means.

The previous paragraph applies to the deliverable code and documentation in
SQLite - those parts of the SQLite library that you actually bundle and ship
with a larger application. Some scripts used as part of the build process (for
example the "configure" scripts generated by autoconf) might fall under other
open-source licenses. Nothing from these build scripts ever reaches the final
deliverable SQLite library, however, and so the licenses associated with those
scripts should not be a factor in assessing your rights to copy and use the
SQLite library.

All of the deliverable code in SQLite has been written from scratch. No code
has been taken from other projects or from the open internet. Every line of
code can be traced back to its original author, and all of those authors have
public domain dedications on file. So the SQLite code base is clean and is
uncontaminated with licensed code from other projects.
```

### N2 — libc v1.70.0 / LICENSE-3RD-PARTY.md

原文 SHA256：`f597097efe3d97021f89170746bd3a0fb9a8b6fb26b82043ed68a4e0283bee6c`。

```text
# Third-Party Software Notices

This repository contains code and assets acquired from third-party sources.
While the main project is licensed under the BSD-3 License, the components
listed below are subject to their own specific license terms and copyright
notices.

The following is a list of third-party software included in this repository,
their locations, and their respective licenses.


----

## Go

* **URL:** https://github.com/golang/go
----

Copyright (c) 2009 The Go Authors. All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google Inc. nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

----

## musl libc

* **URL:** https://musl.libc.org/

----

musl as a whole is licensed under the following standard MIT license:

----------------------------------------------------------------------
Copyright © 2005-2020 Rich Felker, et al.

Permission is hereby granted, free of charge, to any person obtaining
a copy of this software and associated documentation files (the
"Software"), to deal in the Software without restriction, including
without limitation the rights to use, copy, modify, merge, publish,
distribute, sublicense, and/or sell copies of the Software, and to
permit persons to whom the Software is furnished to do so, subject to
the following conditions:

The above copyright notice and this permission notice shall be
included in all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
----------------------------------------------------------------------

Authors/contributors include:

A. Wilcox
Ada Worcester
Alex Dowad
Alex Suykov
Alexander Monakov
Andre McCurdy
Andrew Kelley
Anthony G. Basile
Aric Belsito
Arvid Picciani
Bartosz Brachaczek
Benjamin Peterson
Bobby Bingham
Boris Brezillon
Brent Cook
Chris Spiegel
Clément Vasseur
Daniel Micay
Daniel Sabogal
Daurnimator
David Carlier
David Edelsohn
Denys Vlasenko
Dmitry Ivanov
Dmitry V. Levin
Drew DeVault
Emil Renner Berthing
Fangrui Song
Felix Fietkau
Felix Janda
Gianluca Anzolin
Hauke Mehrtens
He X
Hiltjo Posthuma
Isaac Dunham
Jaydeep Patil
Jens Gustedt
Jeremy Huntwork
Jo-Philipp Wich
Joakim Sindholt
John Spencer
Julien Ramseier
Justin Cormack
Kaarle Ritvanen
Khem Raj
Kylie McClain
Leah Neukirchen
Luca Barbato
Luka Perkov
M Farkas-Dyck (Strake)
Mahesh Bodapati
Markus Wichmann
Masanori Ogino
Michael Clark
Michael Forney
Mikhail Kremnyov
Natanael Copa
Nicholas J. Kain
orc
Pascal Cuoq
Patrick Oppenlander
Petr Hosek
Petr Skocik
Pierre Carrier
Reini Urban
Rich Felker
Richard Pennington
Ryan Fairfax
Samuel Holland
Segev Finer
Shiz
sin
Solar Designer
Stefan Kristiansson
Stefan O'Rear
Szabolcs Nagy
Timo Teräs
Trutz Behn
Valentin Ochs
Will Dietz
William Haddon
William Pitcock

Portions of this software are derived from third-party works licensed
under terms compatible with the above MIT license:

The TRE regular expression implementation (src/regex/reg* and
src/regex/tre*) is Copyright © 2001-2008 Ville Laurikari and licensed
under a 2-clause BSD license (license text in the source files). The
included version has been heavily modified by Rich Felker in 2012, in
the interests of size, simplicity, and namespace cleanliness.

Much of the math library code (src/math/* and src/complex/*) is
Copyright © 1993,2004 Sun Microsystems or
Copyright © 2003-2011 David Schultz or
Copyright © 2003-2009 Steven G. Kargl or
Copyright © 2003-2009 Bruce D. Evans or
Copyright © 2008 Stephen L. Moshier or
Copyright © 2017-2018 Arm Limited
and labelled as such in comments in the individual source files. All
have been licensed under extremely permissive terms.

The ARM memcpy code (src/string/arm/memcpy.S) is Copyright © 2008
The Android Open Source Project and is licensed under a two-clause BSD
license. It was taken from Bionic libc, used on Android.

The AArch64 memcpy and memset code (src/string/aarch64/*) are
Copyright © 1999-2019, Arm Limited.

The implementation of DES for crypt (src/crypt/crypt_des.c) is
Copyright © 1994 David Burren. It is licensed under a BSD license.

The implementation of blowfish crypt (src/crypt/crypt_blowfish.c) was
originally written by Solar Designer and placed into the public
domain. The code also comes with a fallback permissive license for use
in jurisdictions that may not recognize the public domain.

The smoothsort implementation (src/stdlib/qsort.c) is Copyright © 2011
Valentin Ochs and is licensed under an MIT-style license.

The x86_64 port was written by Nicholas J. Kain and is licensed under
the standard MIT terms.

The mips and microblaze ports were originally written by Richard
Pennington for use in the ellcc project. The original code was adapted
by Rich Felker for build system and code conventions during upstream
integration. It is licensed under the standard MIT terms.

The mips64 port was contributed by Imagination Technologies and is
licensed under the standard MIT terms.

The powerpc port was also originally written by Richard Pennington,
and later supplemented and integrated by John Spencer. It is licensed
under the standard MIT terms.

All other files which have no copyright comments are original works
produced specifically for use as part of this library, written either
by Rich Felker, the main author of the library, or by one or more
contibutors listed above. Details on authorship of individual files
can be found in the git version control history of the project. The
omission of copyright and license comments in each file is in the
interest of source tree size.

In addition, permission is hereby granted for all public header files
(include/* and arch/*/bits/*) and crt files intended to be linked into
applications (crt/*, ldso/dlstart.c, and arch/*/crt_arch.h) to omit
the copyright notice and permission notice otherwise required by the
license, and to use these files without any requirement of
attribution. These files include substantial contributions from:

Bobby Bingham
John Spencer
Nicholas J. Kain
Rich Felker
Richard Pennington
Stefan Kristiansson
Szabolcs Nagy

all of whom have explicitly granted such permission.

This file previously contained text expressing a belief that most of
the files covered by the above exception were sufficiently trivial not
to be subject to copyright, resulting in confusion over whether it
negated the permissions granted in the license. In the spirit of
permissive licensing, and of not having licensing issues being an
obstacle to adoption, that text has been removed.

----

## go-netdb

* **URL:** https://github.com/dominikh/go-netdb

----

Copyright (c) 2012 Dominik Honnef

Permission is hereby granted, free of charge, to any person obtaining
a copy of this software and associated documentation files (the
"Software"), to deal in the Software without restriction, including
without limitation the rights to use, copy, modify, merge, publish,
distribute, sublicense, and/or sell copies of the Software, and to
permit persons to whom the Software is furnished to do so, subject to
the following conditions:

The above copyright notice and this permission notice shall be
included in all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.

----

## NixOS/nixpkgs

* **URL:** https://github.com/NixOS/nixpkgs

----

Copyright (c) 2003-2025 Eelco Dolstra and the Nixpkgs/NixOS contributors

Permission is hereby granted, free of charge, to any person obtaining
a copy of this software and associated documentation files (the
"Software"), to deal in the Software without restriction, including
without limitation the rights to use, copy, modify, merge, publish,
distribute, sublicense, and/or sell copies of the Software, and to
permit persons to whom the Software is furnished to do so, subject to
the following conditions:

The above copyright notice and this permission notice shall be
included in all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND
NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE
LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION
OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION
WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
```

### N3 — musl 7ada6dde6f9dc6a2836c3d92c2f762d35fd229e0 / COPYRIGHT

原文 SHA256：`f9bc4423732350eb0b3f7ed7e91d530298476f8fec0c6c427a1c04ade22655af`。

```text
musl as a whole is licensed under the following standard MIT license:

----------------------------------------------------------------------
Copyright © 2005-2020 Rich Felker, et al.

Permission is hereby granted, free of charge, to any person obtaining
a copy of this software and associated documentation files (the
"Software"), to deal in the Software without restriction, including
without limitation the rights to use, copy, modify, merge, publish,
distribute, sublicense, and/or sell copies of the Software, and to
permit persons to whom the Software is furnished to do so, subject to
the following conditions:

The above copyright notice and this permission notice shall be
included in all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
----------------------------------------------------------------------

Authors/contributors include:

A. Wilcox
Ada Worcester
Alex Dowad
Alex Suykov
Alexander Monakov
Andre McCurdy
Andrew Kelley
Anthony G. Basile
Aric Belsito
Arvid Picciani
Bartosz Brachaczek
Benjamin Peterson
Bobby Bingham
Boris Brezillon
Brent Cook
Chris Spiegel
Clément Vasseur
Daniel Micay
Daniel Sabogal
Daurnimator
David Carlier
David Edelsohn
Denys Vlasenko
Dmitry Ivanov
Dmitry V. Levin
Drew DeVault
Emil Renner Berthing
Fangrui Song
Felix Fietkau
Felix Janda
Gianluca Anzolin
Hauke Mehrtens
He X
Hiltjo Posthuma
Isaac Dunham
Jaydeep Patil
Jens Gustedt
Jeremy Huntwork
Jo-Philipp Wich
Joakim Sindholt
John Spencer
Julien Ramseier
Justin Cormack
Kaarle Ritvanen
Khem Raj
Kylie McClain
Leah Neukirchen
Luca Barbato
Luka Perkov
M Farkas-Dyck (Strake)
Mahesh Bodapati
Markus Wichmann
Masanori Ogino
Michael Clark
Michael Forney
Mikhail Kremnyov
Natanael Copa
Nicholas J. Kain
orc
Pascal Cuoq
Patrick Oppenlander
Petr Hosek
Petr Skocik
Pierre Carrier
Reini Urban
Rich Felker
Richard Pennington
Ryan Fairfax
Samuel Holland
Segev Finer
Shiz
sin
Solar Designer
Stefan Kristiansson
Stefan O'Rear
Szabolcs Nagy
Timo Teräs
Trutz Behn
Valentin Ochs
Will Dietz
William Haddon
William Pitcock

Portions of this software are derived from third-party works licensed
under terms compatible with the above MIT license:

The TRE regular expression implementation (src/regex/reg* and
src/regex/tre*) is Copyright © 2001-2008 Ville Laurikari and licensed
under a 2-clause BSD license (license text in the source files). The
included version has been heavily modified by Rich Felker in 2012, in
the interests of size, simplicity, and namespace cleanliness.

Much of the math library code (src/math/* and src/complex/*) is
Copyright © 1993,2004 Sun Microsystems or
Copyright © 2003-2011 David Schultz or
Copyright © 2003-2009 Steven G. Kargl or
Copyright © 2003-2009 Bruce D. Evans or
Copyright © 2008 Stephen L. Moshier or
Copyright © 2017-2018 Arm Limited
and labelled as such in comments in the individual source files. All
have been licensed under extremely permissive terms.

The ARM memcpy code (src/string/arm/memcpy.S) is Copyright © 2008
The Android Open Source Project and is licensed under a two-clause BSD
license. It was taken from Bionic libc, used on Android.

The AArch64 memcpy and memset code (src/string/aarch64/*) are
Copyright © 1999-2019, Arm Limited.

The implementation of DES for crypt (src/crypt/crypt_des.c) is
Copyright © 1994 David Burren. It is licensed under a BSD license.

The implementation of blowfish crypt (src/crypt/crypt_blowfish.c) was
originally written by Solar Designer and placed into the public
domain. The code also comes with a fallback permissive license for use
in jurisdictions that may not recognize the public domain.

The smoothsort implementation (src/stdlib/qsort.c) is Copyright © 2011
Valentin Ochs and is licensed under an MIT-style license.

The x86_64 port was written by Nicholas J. Kain and is licensed under
the standard MIT terms.

The mips and microblaze ports were originally written by Richard
Pennington for use in the ellcc project. The original code was adapted
by Rich Felker for build system and code conventions during upstream
integration. It is licensed under the standard MIT terms.

The mips64 port was contributed by Imagination Technologies and is
licensed under the standard MIT terms.

The powerpc port was also originally written by Richard Pennington,
and later supplemented and integrated by John Spencer. It is licensed
under the standard MIT terms.

All other files which have no copyright comments are original works
produced specifically for use as part of this library, written either
by Rich Felker, the main author of the library, or by one or more
contibutors listed above. Details on authorship of individual files
can be found in the git version control history of the project. The
omission of copyright and license comments in each file is in the
interest of source tree size.

In addition, permission is hereby granted for all public header files
(include/* and arch/*/bits/*) and crt files intended to be linked into
applications (crt/*, ldso/dlstart.c, and arch/*/crt_arch.h) to omit
the copyright notice and permission notice otherwise required by the
license, and to use these files without any requirement of
attribution. These files include substantial contributions from:

Bobby Bingham
John Spencer
Nicholas J. Kain
Rich Felker
Richard Pennington
Stefan Kristiansson
Szabolcs Nagy

all of whom have explicitly granted such permission.

This file previously contained text expressing a belief that most of
the files covered by the above exception were sufficiently trivial not
to be subject to copyright, resulting in confusion over whether it
negated the permissions granted in the license. In the spirit of
permissive licensing, and of not having licensing issues being an
obstacle to adoption, that text has been removed.
```

以下为同一 musl 归档相关 math/complex/regex/crypt/stdlib/internal/架构字符串代码中的不同版权与许可注释。它们保留上游各来源的不同原文；列出来源不代表其全部函数都被最终 ELF 保留。

### N3.1 — musl src/complex/__cexp.c, src/complex/__cexpf.c, src/complex/cexp.c, src/complex/cexpf.c

原文 SHA256：`929837e06c3b9c37e61b955742111a53906b7d3382fab6fd7f9a6d8c708b8e92`。

```text
/*-
 * Copyright (c) 2011 David Schultz <das@FreeBSD.ORG>
 * All rights reserved.
 *
 * Redistribution and use in source and binary forms, with or without
 * modification, are permitted provided that the following conditions
 * are met:
 * 1. Redistributions of source code must retain the above copyright
 *    notice, this list of conditions and the following disclaimer.
 * 2. Redistributions in binary form must reproduce the above copyright
 *    notice, this list of conditions and the following disclaimer in the
 *    documentation and/or other materials provided with the distribution.
 *
 * THIS SOFTWARE IS PROVIDED BY THE AUTHOR AND CONTRIBUTORS ``AS IS'' AND
 * ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
 * IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE
 * ARE DISCLAIMED.  IN NO EVENT SHALL THE AUTHOR OR CONTRIBUTORS BE LIABLE
 * FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
 * DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS
 * OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION)
 * HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT
 * LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY
 * OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF
 * SUCH DAMAGE.
 */
```

### N3.2 — musl src/complex/catan.c, src/complex/catanf.c, src/complex/catanl.c, src/math/__polevll.c, src/math/erfl.c, src/math/expl.c, src/math/expm1l.c, src/math/lgammal.c, src/math/log10l.c, src/math/log1pl.c, src/math/log2l.c, src/math/logl.c, src/math/powl.c, src/math/tgammal.c

原文 SHA256：`d9d69faa82c4f80279a3832834c9a1482c52b8b3b5895f2cc869c0e4db33f36e`。

```text
/*
 * Copyright (c) 2008 Stephen L. Moshier <steve@moshier.net>
 *
 * Permission to use, copy, modify, and distribute this software for any
 * purpose with or without fee is hereby granted, provided that the above
 * copyright notice and this permission notice appear in all copies.
 *
 * THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
 * WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
 * MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
 * ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
 * WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
 * ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
 * OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
 */
```

### N3.3 — musl src/complex/ccosh.c, src/complex/ccoshf.c, src/complex/csinh.c, src/complex/csinhf.c

原文 SHA256：`3a1692b72fb1f66d3f082862dea0bbf69cba16bba5c9d1ec144d683aba49cf53`。

```text
/*-
 * Copyright (c) 2005 Bruce D. Evans and Steven G. Kargl
 * All rights reserved.
 *
 * Redistribution and use in source and binary forms, with or without
 * modification, are permitted provided that the following conditions
 * are met:
 * 1. Redistributions of source code must retain the above copyright
 *    notice unmodified, this list of conditions, and the following
 *    disclaimer.
 * 2. Redistributions in binary form must reproduce the above copyright
 *    notice, this list of conditions and the following disclaimer in the
 *    documentation and/or other materials provided with the distribution.
 *
 * THIS SOFTWARE IS PROVIDED BY THE AUTHOR ``AS IS'' AND ANY EXPRESS OR
 * IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED WARRANTIES
 * OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE DISCLAIMED.
 * IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR ANY DIRECT, INDIRECT,
 * INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT
 * NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
 * DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
 * THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
 * (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF
 * THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
 */
```

### N3.4 — musl src/complex/csqrt.c, src/complex/csqrtf.c

原文 SHA256：`6679e9aa1a98bbb9f73d944ecc84eaf508b2df5d30f620a3af64aa40e5df55fc`。

```text
/*-
 * Copyright (c) 2007 David Schultz <das@FreeBSD.ORG>
 * All rights reserved.
 *
 * Redistribution and use in source and binary forms, with or without
 * modification, are permitted provided that the following conditions
 * are met:
 * 1. Redistributions of source code must retain the above copyright
 *    notice, this list of conditions and the following disclaimer.
 * 2. Redistributions in binary form must reproduce the above copyright
 *    notice, this list of conditions and the following disclaimer in the
 *    documentation and/or other materials provided with the distribution.
 *
 * THIS SOFTWARE IS PROVIDED BY THE AUTHOR AND CONTRIBUTORS ``AS IS'' AND
 * ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
 * IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE
 * ARE DISCLAIMED.  IN NO EVENT SHALL THE AUTHOR OR CONTRIBUTORS BE LIABLE
 * FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
 * DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS
 * OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION)
 * HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT
 * LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY
 * OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF
 * SUCH DAMAGE.
 */
```

### N3.5 — musl src/complex/ctanh.c, src/complex/ctanhf.c

原文 SHA256：`f11ac8260c79e3792b711323df93ff6dd6ef7866b95708c3a83626b65e579df2`。

```text
/*-
 * Copyright (c) 2011 David Schultz
 * All rights reserved.
 *
 * Redistribution and use in source and binary forms, with or without
 * modification, are permitted provided that the following conditions
 * are met:
 * 1. Redistributions of source code must retain the above copyright
 *    notice unmodified, this list of conditions, and the following
 *    disclaimer.
 * 2. Redistributions in binary form must reproduce the above copyright
 *    notice, this list of conditions and the following disclaimer in the
 *    documentation and/or other materials provided with the distribution.
 *
 * THIS SOFTWARE IS PROVIDED BY THE AUTHOR ``AS IS'' AND ANY EXPRESS OR
 * IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED WARRANTIES
 * OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE DISCLAIMED.
 * IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR ANY DIRECT, INDIRECT,
 * INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT
 * NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
 * DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
 * THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
 * (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF
 * THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
 */
```

### N3.6 — musl src/crypt/crypt_blowfish.c

原文 SHA256：`a17e0d141e431d3f15bd03901da69575531e23e4556847a1de209e79c6f3a1fb`。

```text
/*
 * The crypt_blowfish homepage is:
 *
 *	http://www.openwall.com/crypt/
 *
 * This code comes from John the Ripper password cracker, with reentrant
 * and crypt(3) interfaces added, but optimizations specific to password
 * cracking removed.
 *
 * Written by Solar Designer <solar at openwall.com> in 1998-2012.
 * No copyright is claimed, and the software is hereby placed in the public
 * domain.  In case this attempt to disclaim copyright and place the software
 * in the public domain is deemed null and void, then the software is
 * Copyright (c) 1998-2014 Solar Designer and it is hereby released to the
 * general public under the following terms:
 *
 * Redistribution and use in source and binary forms, with or without
 * modification, are permitted.
 *
 * There's ABSOLUTELY NO WARRANTY, express or implied.
 *
 * It is my intent that you should be able to use this on your system,
 * as part of a software package, or anywhere else to improve security,
 * ensure compatibility, or for any other purpose.  I would appreciate
 * it if you give credit where it is due and keep your modifications in
 * the public domain as well, but I don't require that in order to let
 * you place this code and any modifications you make under a license
 * of your choice.
 *
 * This implementation is fully compatible with OpenBSD's bcrypt.c for prefix
 * "$2b$", originally by Niels Provos <provos at citi.umich.edu>, and it uses
 * some of his ideas.  The password hashing algorithm was designed by David
 * Mazieres <dm at lcs.mit.edu>.  For information on the level of
 * compatibility for bcrypt hash prefixes other than "$2b$", please refer to
 * the comments in BF_set_key() below and to the included crypt(3) man page.
 *
 * There's a paper on the algorithm that explains its design decisions:
 *
 *	http://www.usenix.org/events/usenix99/provos.html
 *
 * Some of the tricks in BF_ROUND might be inspired by Eric Young's
 * Blowfish library (I can't be sure if I would think of something if I
 * hadn't seen his code).
 */
```

### N3.7 — musl src/crypt/crypt_des.c

原文 SHA256：`30c3222fecfdbda6900218b4da3fe68f4753cba41a3a71dac7e05345447c40fb`。

```text
/*
 * FreeSec: libcrypt for NetBSD
 *
 * Copyright (c) 1994 David Burren
 * Copyright (c) 2000,2002,2010,2012 Solar Designer
 * All rights reserved.
 *
 * Redistribution and use in source and binary forms, with or without
 * modification, are permitted provided that the following conditions
 * are met:
 * 1. Redistributions of source code must retain the above copyright
 *    notice, this list of conditions and the following disclaimer.
 * 2. Redistributions in binary form must reproduce the above copyright
 *    notice, this list of conditions and the following disclaimer in the
 *    documentation and/or other materials provided with the distribution.
 * 3. Neither the name of the author nor the names of other contributors
 *    may be used to endorse or promote products derived from this software
 *    without specific prior written permission.
 *
 * THIS SOFTWARE IS PROVIDED BY THE AUTHOR AND CONTRIBUTORS ``AS IS'' AND
 * ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
 * IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE
 * ARE DISCLAIMED.  IN NO EVENT SHALL THE AUTHOR OR CONTRIBUTORS BE LIABLE
 * FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
 * DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS
 * OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION)
 * HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT
 * LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY
 * OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF
 * SUCH DAMAGE.
 *
 *	$Owl: Owl/packages/glibc/crypt_freesec.c,v 1.6 2010/02/20 14:45:06 solar Exp $
 *	$Id: crypt.c,v 1.15 1994/09/13 04:58:49 davidb Exp $
 *
 * This is an original implementation of the DES and the crypt(3) interfaces
 * by David Burren.  It has been heavily re-worked by Solar Designer.
 */
```

### N3.8 — musl src/crypt/crypt_md5.c

原文 SHA256：`12f74e03b9ba352153ad48d4c0099a758506b2b90b75b34346a784ccbd4cf3ff`。

```text
/* public domain md5 implementation based on rfc1321 and libtomcrypt */
```

### N3.9 — musl src/crypt/crypt_md5.c

原文 SHA256：`0210a0ce49f8e7a9871b9133a050d978ed056e4aab2ffc7644fdf28076c72b5d`。

```text
/*-
 * Copyright (c) 2003 Poul-Henning Kamp
 * All rights reserved.
 *
 * Redistribution and use in source and binary forms, with or without
 * modification, are permitted provided that the following conditions
 * are met:
 * 1. Redistributions of source code must retain the above copyright
 *    notice, this list of conditions and the following disclaimer.
 * 2. Redistributions in binary form must reproduce the above copyright
 *    notice, this list of conditions and the following disclaimer in the
 *    documentation and/or other materials provided with the distribution.
 *
 * THIS SOFTWARE IS PROVIDED BY THE AUTHOR AND CONTRIBUTORS ``AS IS'' AND
 * ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
 * IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE
 * ARE DISCLAIMED.  IN NO EVENT SHALL THE AUTHOR OR CONTRIBUTORS BE LIABLE
 * FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
 * DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS
 * OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION)
 * HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT
 * LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY
 * OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF
 * SUCH DAMAGE.
 */
```

### N3.10 — musl src/crypt/crypt_sha256.c

原文 SHA256：`e89eaf7fd9fa79f2ff3ab53267841537180a4b233952f6d17aca8ad2d830107a`。

```text
/*
 * public domain sha256 crypt implementation
 *
 * original sha crypt design: http://people.redhat.com/drepper/SHA-crypt.txt
 * in this implementation at least 32bit int is assumed,
 * key length is limited, the $5$ prefix is mandatory, '\n' and ':' is rejected
 * in the salt and rounds= setting must contain a valid iteration count,
 * on error "*" is returned.
 */
```

### N3.11 — musl src/crypt/crypt_sha256.c

原文 SHA256：`7894cd037ed7157c9f588baab5fda771da62bedb415c0a9207e473eabf11ca2c`。

```text
/* public domain sha256 implementation based on fips180-3 */
```

### N3.12 — musl src/crypt/crypt_sha512.c

原文 SHA256：`36947bd567f088a0514fae5ecfce6d12a0d9ad51c4d3a6c89748250d03fc1f3b`。

```text
/*
 * public domain sha512 crypt implementation
 *
 * original sha crypt design: http://people.redhat.com/drepper/SHA-crypt.txt
 * in this implementation at least 32bit int is assumed,
 * key length is limited, the $6$ prefix is mandatory, '\n' and ':' is rejected
 * in the salt and rounds= setting must contain a valid iteration count,
 * on error "*" is returned.
 */
```

### N3.13 — musl src/crypt/crypt_sha512.c

原文 SHA256：`5b592324f887645816b7560a92752dd32b16b0d82f4da35dc15aacf52253bf77`。

```text
/* public domain sha512 implementation based on fips180-3 */
```

### N3.14 — musl src/math/__cos.c, src/math/__rem_pio2_large.c, src/math/__sin.c, src/math/acos.c, src/math/acosl.c, src/math/asin.c, src/math/asinl.c, src/math/j0.c, src/math/j1.c, src/math/jn.c, src/math/log10.c, src/math/scalb.c

原文 SHA256：`74344ebf347f27ca446e00f1c1921f705546f4b20e0fb31c183cd2c8464b1bc5`。

```text
/*
 * ====================================================
 * Copyright (C) 1993 by Sun Microsystems, Inc. All rights reserved.
 *
 * Developed at SunSoft, a Sun Microsystems, Inc. business.
 * Permission to use, copy, modify, and distribute this
 * software is freely granted, provided that this notice
 * is preserved.
 * ====================================================
 */
```

### N3.15 — musl src/math/__cosdf.c, src/math/__rem_pio2f.c, src/math/__sindf.c, src/math/acosf.c, src/math/asinf.c, src/math/atan.c, src/math/atan2f.c, src/math/atanf.c, src/math/atanl.c, src/math/cbrtf.c, src/math/cos.c, src/math/cosf.c, src/math/erf.c, src/math/erff.c, src/math/erfl.c, src/math/expm1.c, src/math/expm1f.c, src/math/j0f.c, src/math/j1f.c, src/math/jnf.c, src/math/lgammaf_r.c, src/math/lgammal.c, src/math/log10f.c, src/math/log1p.c, src/math/log1pf.c, src/math/scalbf.c, src/math/sin.c, src/math/sincos.c, src/math/sincosf.c, src/math/sinf.c, src/math/tan.c, src/math/tanf.c

原文 SHA256：`0c3d0381d2cd647be68590170490be5f624c962d9f95eb698489e578c1de8f05`。

```text
/*
 * ====================================================
 * Copyright (C) 1993 by Sun Microsystems, Inc. All rights reserved.
 *
 * Developed at SunPro, a Sun Microsystems, Inc. business.
 * Permission to use, copy, modify, and distribute this
 * software is freely granted, provided that this notice
 * is preserved.
 * ====================================================
 */
```

### N3.16 — musl src/math/__cosl.c, src/math/__sinl.c

原文 SHA256：`eba82a2e9f34f53e0faf5a5b6f4d9f588760d888e6aae20a2f8d7406eb256167`。

```text
/*
 * ====================================================
 * Copyright (C) 1993 by Sun Microsystems, Inc. All rights reserved.
 * Copyright (c) 2008 Steven G. Kargl, David Schultz, Bruce D. Evans.
 *
 * Developed at SunSoft, a Sun Microsystems, Inc. business.
 * Permission to use, copy, modify, and distribute this
 * software is freely granted, provided that this notice
 * is preserved.
 * ====================================================
 */
```

### N3.17 — musl src/math/__rem_pio2.c

原文 SHA256：`c90662135910b0f0edd97131d0e116a027ec79e46b2785b7b596fa617cca78e3`。

```text
/*
 * ====================================================
 * Copyright (C) 1993 by Sun Microsystems, Inc. All rights reserved.
 *
 * Developed at SunSoft, a Sun Microsystems, Inc. business.
 * Permission to use, copy, modify, and distribute this
 * software is freely granted, provided that this notice
 * is preserved.
 * ====================================================
 *
 * Optimized by Bruce D. Evans.
 */
```

### N3.18 — musl src/math/__rem_pio2l.c

原文 SHA256：`5660964828a36e1680c465c6c3ac8669926917d347dc66f803897708af319f29`。

```text
/*
 * ====================================================
 * Copyright (C) 1993 by Sun Microsystems, Inc. All rights reserved.
 * Copyright (c) 2008 Steven G. Kargl, David Schultz, Bruce D. Evans.
 *
 * Developed at SunSoft, a Sun Microsystems, Inc. business.
 * Permission to use, copy, modify, and distribute this
 * software is freely granted, provided that this notice
 * is preserved.
 * ====================================================
 *
 * Optimized by Bruce D. Evans.
 */
```

### N3.19 — musl src/math/__tan.c, src/math/__tandf.c

原文 SHA256：`5a534ed1f9266ed799e9f2e3f830f15411762021829da121b23df5edc498c6b6`。

```text
/*
 * ====================================================
 * Copyright 2004 Sun Microsystems, Inc.  All Rights Reserved.
 *
 * Permission to use, copy, modify, and distribute this
 * software is freely granted, provided that this notice
 * is preserved.
 * ====================================================
 */
```

### N3.20 — musl src/math/__tanl.c

原文 SHA256：`a8a75da52cf114b28d33e48992435876d121c7d410ebf740ce651821afcc79aa`。

```text
/*
 * ====================================================
 * Copyright 2004 Sun Microsystems, Inc.  All Rights Reserved.
 * Copyright (c) 2008 Steven G. Kargl, David Schultz, Bruce D. Evans.
 *
 * Permission to use, copy, modify, and distribute this
 * software is freely granted, provided that this notice
 * is preserved.
 * ====================================================
 */
```

### N3.21 — musl src/math/atan2.c, src/math/atan2l.c, src/math/lgamma_r.c

原文 SHA256：`11f476f899dacfef801a097465ad0755ad4546afe03b02c9a17aa38e228344e1`。

```text
/*
 * ====================================================
 * Copyright (C) 1993 by Sun Microsystems, Inc. All rights reserved.
 *
 * Developed at SunSoft, a Sun Microsystems, Inc. business.
 * Permission to use, copy, modify, and distribute this
 * software is freely granted, provided that this notice
 * is preserved.
 * ====================================================
 *
 */
```

### N3.22 — musl src/math/cbrt.c

原文 SHA256：`11a1dc1ce0b6b2879da93a266c5aebe81c4324e749c77a2ce3048b5b24ab58c0`。

```text
/*
 * ====================================================
 * Copyright (C) 1993 by Sun Microsystems, Inc. All rights reserved.
 *
 * Developed at SunPro, a Sun Microsystems, Inc. business.
 * Permission to use, copy, modify, and distribute this
 * software is freely granted, provided that this notice
 * is preserved.
 * ====================================================
 *
 * Optimized by Bruce D. Evans.
 */
```

### N3.23 — musl src/math/cbrtl.c

原文 SHA256：`57c0d6534118d909d6b3e78366981596013f999307b9d0d7711a8998a0410d61`。

```text
/*-
 * ====================================================
 * Copyright (C) 1993 by Sun Microsystems, Inc. All rights reserved.
 * Copyright (c) 2009-2011, Bruce D. Evans, Steven G. Kargl, David Schultz.
 *
 * Developed at SunPro, a Sun Microsystems, Inc. business.
 * Permission to use, copy, modify, and distribute this
 * software is freely granted, provided that this notice
 * is preserved.
 * ====================================================
 *
 * The argument reduction and testing for exceptional cases was
 * written by Steven G. Kargl with input from Bruce D. Evans
 * and David A. Schultz.
 */
```

### N3.24 — musl src/math/exp.c

原文 SHA256：`7e52a70e5de1143e4fdb710116f76b27eced3e41be7ab70310138928a3b34e5e`。

```text
/*
 * Double-precision e^x function.
 *
 * Copyright (c) 2018, Arm Limited.
 * SPDX-License-Identifier: MIT
 */
```

### N3.25 — musl src/math/exp2.c

原文 SHA256：`e8f314eb6ab51b691f02a126ad69d2c7996a5eeb091f95f5033b1a806288e5d1`。

```text
/*
 * Double-precision 2^x function.
 *
 * Copyright (c) 2018, Arm Limited.
 * SPDX-License-Identifier: MIT
 */
```

### N3.26 — musl src/math/exp2f.c

原文 SHA256：`9cd52ae0042ec9364291994dc497f2497414eeddaac23e792f1ab477576dfacf`。

```text
/*
 * Single-precision 2^x function.
 *
 * Copyright (c) 2017-2018, Arm Limited.
 * SPDX-License-Identifier: MIT
 */
```

### N3.27 — musl src/math/exp2f_data.c

原文 SHA256：`eb8e488b7148e706986f5be08c5efbfcc96edb4ed7415391c12be3e512b24c74`。

```text
/*
 * Shared data between expf, exp2f and powf.
 *
 * Copyright (c) 2017-2018, Arm Limited.
 * SPDX-License-Identifier: MIT
 */
```

### N3.28 — musl src/math/exp2f_data.h, src/math/log2f_data.h, src/math/logf_data.h, src/math/powf.c, src/math/powf_data.h

原文 SHA256：`72a9b46f0fe5f4ab99af914882d3ad50f72612ca79b3192db87f37a06b841853`。

```text
/*
 * Copyright (c) 2017-2018, Arm Limited.
 * SPDX-License-Identifier: MIT
 */
```

### N3.29 — musl src/math/exp2l.c

原文 SHA256：`a0f470d9d239f56f7d6504b1bfcd80a0dbe8f90a895558d4040d4395100ad607`。

```text
/*-
 * Copyright (c) 2005-2008 David Schultz <das@FreeBSD.ORG>
 * All rights reserved.
 *
 * Redistribution and use in source and binary forms, with or without
 * modification, are permitted provided that the following conditions
 * are met:
 * 1. Redistributions of source code must retain the above copyright
 *    notice, this list of conditions and the following disclaimer.
 * 2. Redistributions in binary form must reproduce the above copyright
 *    notice, this list of conditions and the following disclaimer in the
 *    documentation and/or other materials provided with the distribution.
 *
 * THIS SOFTWARE IS PROVIDED BY THE AUTHOR AND CONTRIBUTORS ``AS IS'' AND
 * ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
 * IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE
 * ARE DISCLAIMED.  IN NO EVENT SHALL THE AUTHOR OR CONTRIBUTORS BE LIABLE
 * FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
 * DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS
 * OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION)
 * HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT
 * LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY
 * OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF
 * SUCH DAMAGE.
 */
```

### N3.30 — musl src/math/exp_data.c

原文 SHA256：`f6b5b71ae6dc49255b8982a8de10bb97d66035b0b6ad91843da71fcde64cf45f`。

```text
/*
 * Shared data between exp, exp2 and pow.
 *
 * Copyright (c) 2018, Arm Limited.
 * SPDX-License-Identifier: MIT
 */
```

### N3.31 — musl src/math/exp_data.h, src/math/log2_data.h, src/math/log_data.h, src/math/pow_data.h

原文 SHA256：`6ac2dcacfaa533da778dce54dac87709a1285f08d070d9d95790b50199f71486`。

```text
/*
 * Copyright (c) 2018, Arm Limited.
 * SPDX-License-Identifier: MIT
 */
```

### N3.32 — musl src/math/expf.c

原文 SHA256：`722fce9131e08cd408ec1029bb4fc7ad15c899b6e62ebacb911b298ad069728d`。

```text
/*
 * Single-precision e^x function.
 *
 * Copyright (c) 2017-2018, Arm Limited.
 * SPDX-License-Identifier: MIT
 */
```

### N3.33 — musl src/math/fmaf.c, src/math/fmal.c

原文 SHA256：`c9479241e4e7e711c5cf43e95b86a79af7209b5ea23b8c55f8b95f1788eb5a82`。

```text
/*-
 * Copyright (c) 2005-2011 David Schultz <das@FreeBSD.ORG>
 * All rights reserved.
 *
 * Redistribution and use in source and binary forms, with or without
 * modification, are permitted provided that the following conditions
 * are met:
 * 1. Redistributions of source code must retain the above copyright
 *    notice, this list of conditions and the following disclaimer.
 * 2. Redistributions in binary form must reproduce the above copyright
 *    notice, this list of conditions and the following disclaimer in the
 *    documentation and/or other materials provided with the distribution.
 *
 * THIS SOFTWARE IS PROVIDED BY THE AUTHOR AND CONTRIBUTORS ``AS IS'' AND
 * ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
 * IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE
 * ARE DISCLAIMED.  IN NO EVENT SHALL THE AUTHOR OR CONTRIBUTORS BE LIABLE
 * FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
 * DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS
 * OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION)
 * HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT
 * LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY
 * OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF
 * SUCH DAMAGE.
 */
```

### N3.34 — musl src/math/log.c

原文 SHA256：`7d44f8a20ce2b9ab074ce883201b78815c98e669108b1dbdf3a699098444c326`。

```text
/*
 * Double-precision log(x) function.
 *
 * Copyright (c) 2018, Arm Limited.
 * SPDX-License-Identifier: MIT
 */
```

### N3.35 — musl src/math/log2.c

原文 SHA256：`c359dfaf15768892478461e9cb40da11d380123e47c50f29b3ede8207c04486d`。

```text
/*
 * Double-precision log2(x) function.
 *
 * Copyright (c) 2018, Arm Limited.
 * SPDX-License-Identifier: MIT
 */
```

### N3.36 — musl src/math/log2_data.c

原文 SHA256：`74ae9464d17802e1e8394431844a842eb25efad729748a359a518d26685beedf`。

```text
/*
 * Data for log2.
 *
 * Copyright (c) 2018, Arm Limited.
 * SPDX-License-Identifier: MIT
 */
```

### N3.37 — musl src/math/log2f.c

原文 SHA256：`dfbd4e6eba80a4f25005d42aef337397554ca46598f64439e166633baa58cb3f`。

```text
/*
 * Single-precision log2 function.
 *
 * Copyright (c) 2017-2018, Arm Limited.
 * SPDX-License-Identifier: MIT
 */
```

### N3.38 — musl src/math/log2f_data.c

原文 SHA256：`719c803a410da68f48b70270cd181806b44d0c87ccec10bcba36973c8382a2a9`。

```text
/*
 * Data definition for log2f.
 *
 * Copyright (c) 2017-2018, Arm Limited.
 * SPDX-License-Identifier: MIT
 */
```

### N3.39 — musl src/math/log_data.c

原文 SHA256：`d22e34751c18a13e5cb7e974c1b94c2cc89caad9d2b4f2ef1c85994d32292805`。

```text
/*
 * Data for log.
 *
 * Copyright (c) 2018, Arm Limited.
 * SPDX-License-Identifier: MIT
 */
```

### N3.40 — musl src/math/logf.c

原文 SHA256：`ffd4560b424a532b17b1f8af4405f11b4ceeaf9880ca33ad43f896b620bfb52c`。

```text
/*
 * Single-precision log function.
 *
 * Copyright (c) 2017-2018, Arm Limited.
 * SPDX-License-Identifier: MIT
 */
```

### N3.41 — musl src/math/logf_data.c

原文 SHA256：`7b69dd50886f7e59e969f2d7c91f6d694a42989bde841e57bed58f71a6d34fde`。

```text
/*
 * Data definition for logf.
 *
 * Copyright (c) 2017-2018, Arm Limited.
 * SPDX-License-Identifier: MIT
 */
```

### N3.42 — musl src/math/pow.c

原文 SHA256：`5fa165f5ae7e7b676b5ea1e2ab501f258932355ebad0ffaa76865149b47cc00a`。

```text
/*
 * Double-precision x^y function.
 *
 * Copyright (c) 2018, Arm Limited.
 * SPDX-License-Identifier: MIT
 */
```

### N3.43 — musl src/math/pow_data.c

原文 SHA256：`8867dc852d8c09b25648ac08bb5e7d7effdbff03e10614f13f8e681774ec1414`。

```text
/*
 * Data for the log part of pow.
 *
 * Copyright (c) 2018, Arm Limited.
 * SPDX-License-Identifier: MIT
 */
```

### N3.44 — musl src/math/powf_data.c

原文 SHA256：`0bf37177bf53722a813bbd05dce155746ceb21a1aeee3c7b13a3927554fe53d6`。

```text
/*
 * Data definition for powf.
 *
 * Copyright (c) 2017-2018, Arm Limited.
 * SPDX-License-Identifier: MIT
 */
```

### N3.45 — musl src/regex/regcomp.c

原文 SHA256：`8313e732fdeef03ee534e30bdb0b2df8fea4c9e6618aa5abda363f873c5c961a`。

```text
/*
  regcomp.c - TRE POSIX compatible regex compilation functions.

  Copyright (c) 2001-2009 Ville Laurikari <vl@iki.fi>
  All rights reserved.

  Redistribution and use in source and binary forms, with or without
  modification, are permitted provided that the following conditions
  are met:

    1. Redistributions of source code must retain the above copyright
       notice, this list of conditions and the following disclaimer.

    2. Redistributions in binary form must reproduce the above copyright
       notice, this list of conditions and the following disclaimer in the
       documentation and/or other materials provided with the distribution.

  THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDER AND CONTRIBUTORS
  ``AS IS'' AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
  LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
  A PARTICULAR PURPOSE ARE DISCLAIMED.  IN NO EVENT SHALL THE COPYRIGHT
  HOLDER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
  SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
  LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
  DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
  THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
  (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
  OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

*/
```

### N3.46 — musl src/regex/regexec.c

原文 SHA256：`6b0193762d619afafa7f796e10097fbf7c255a8e96dbfa25f75ab072620a87c5`。

```text
/*
  regexec.c - TRE POSIX compatible matching functions (and more).

  Copyright (c) 2001-2009 Ville Laurikari <vl@iki.fi>
  All rights reserved.

  Redistribution and use in source and binary forms, with or without
  modification, are permitted provided that the following conditions
  are met:

    1. Redistributions of source code must retain the above copyright
       notice, this list of conditions and the following disclaimer.

    2. Redistributions in binary form must reproduce the above copyright
       notice, this list of conditions and the following disclaimer in the
       documentation and/or other materials provided with the distribution.

  THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDER AND CONTRIBUTORS
  ``AS IS'' AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
  LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
  A PARTICULAR PURPOSE ARE DISCLAIMED.  IN NO EVENT SHALL THE COPYRIGHT
  HOLDER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
  SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
  LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
  DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
  THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
  (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
  OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

*/
```

### N3.47 — musl src/regex/tre-mem.c

原文 SHA256：`da041cb2f902d426453521b2aa036ff79e9ed03236519518d2aa0290bddf88ac`。

```text
/*
  tre-mem.c - TRE memory allocator

  Copyright (c) 2001-2009 Ville Laurikari <vl@iki.fi>
  All rights reserved.

  Redistribution and use in source and binary forms, with or without
  modification, are permitted provided that the following conditions
  are met:

    1. Redistributions of source code must retain the above copyright
       notice, this list of conditions and the following disclaimer.

    2. Redistributions in binary form must reproduce the above copyright
       notice, this list of conditions and the following disclaimer in the
       documentation and/or other materials provided with the distribution.

  THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDER AND CONTRIBUTORS
  ``AS IS'' AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
  LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
  A PARTICULAR PURPOSE ARE DISCLAIMED.  IN NO EVENT SHALL THE COPYRIGHT
  HOLDER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
  SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
  LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
  DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
  THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
  (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
  OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

*/
```

### N3.48 — musl src/regex/tre.h

原文 SHA256：`920d6efc69e820c76c990f76db6c6da46c6a8f8ebd306b2004918896a26e4521`。

```text
/*
  tre-internal.h - TRE internal definitions

  Copyright (c) 2001-2009 Ville Laurikari <vl@iki.fi>
  All rights reserved.

  Redistribution and use in source and binary forms, with or without
  modification, are permitted provided that the following conditions
  are met:

    1. Redistributions of source code must retain the above copyright
       notice, this list of conditions and the following disclaimer.

    2. Redistributions in binary form must reproduce the above copyright
       notice, this list of conditions and the following disclaimer in the
       documentation and/or other materials provided with the distribution.

  THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDER AND CONTRIBUTORS
  ``AS IS'' AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
  LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
  A PARTICULAR PURPOSE ARE DISCLAIMED.  IN NO EVENT SHALL THE COPYRIGHT
  HOLDER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
  SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
  LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
  DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
  THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
  (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
  OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

*/
```

### N3.49 — musl src/stdlib/qsort.c

原文 SHA256：`87da201b9c63a4bbc969e88cc99c75b630749ffb45d9abc5b90c58f16e40e016`。

```text
/* Copyright (C) 2011 by Valentin Ochs
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to
 * deal in the Software without restriction, including without limitation the
 * rights to use, copy, modify, merge, publish, distribute, sublicense, and/or
 * sell copies of the Software, and to permit persons to whom the Software is
 * furnished to do so, subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in
 * all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
 * FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
 * AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
 * LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING
 * FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS
 * IN THE SOFTWARE.
 */
```

### N3.50 — musl src/string/aarch64/memcpy.S

原文 SHA256：`6eca170c32acaced38b3e559fda9908b2f16717fc7b4cde36ced5fb407f4f114`。

```text
/*
 * memcpy - copy memory area
 *
 * Copyright (c) 2012-2020, Arm Limited.
 * SPDX-License-Identifier: MIT
 */
```

### N3.51 — musl src/string/aarch64/memset.S

原文 SHA256：`b538ed96d0c4574442e0e3d9be99ca108335ab88db8ef8ca96a08bc9e8d74fa8`。

```text
/*
 * memset - fill memory with a constant byte
 *
 * Copyright (c) 2012-2020, Arm Limited.
 * SPDX-License-Identifier: MIT
 */
```

### N3.52 — musl src/string/arm/memcpy.S

原文 SHA256：`695731bda1c73c7f88c0d04d76ae94efa1eb1cc820b9e97fc384d274571a20d1`。

```text
/*
 * Copyright (C) 2008 The Android Open Source Project
 * All rights reserved.
 *
 * Redistribution and use in source and binary forms, with or without
 * modification, are permitted provided that the following conditions
 * are met:
 *  * Redistributions of source code must retain the above copyright
 *    notice, this list of conditions and the following disclaimer.
 *  * Redistributions in binary form must reproduce the above copyright
 *    notice, this list of conditions and the following disclaimer in
 *    the documentation and/or other materials provided with the
 *    distribution.
 *
 * THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
 * "AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
 * LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS
 * FOR A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE
 * COPYRIGHT OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT,
 * INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING,
 * BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS
 * OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED
 * AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY,
 * OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT
 * OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF
 * SUCH DAMAGE.
 */
```

### N4 — libc UUID 生成代码中的来源声明

以下摘录实际模块中的授权注释（保持注释符号），作为来源记录保留。
交叉评审已确认相关文件仅含常量和类型定义，没有函数；二进制核查及 LGPL-2.1 §5
的适用结论见 Q3，该项已解决。

### N4.1 — uuid/uuid/uuid_linux_amd64.go, uuid/uuid/uuid_linux_arm64.go

原文 SHA256：`a6d0c0641d14897864b1cd47a4c2f0a615eeaa290aafd88eaac373969e1b570e`。

```text
// Public include file for the UUID library
//
// Copyright (C) 1996, 1997, 1998 Theodore Ts'o.
//
// %Begin-Header%
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions
// are met:
// 1. Redistributions of source code must retain the above copyright
//    notice, and the entire permission notice in its entirety,
//    including the disclaimer of warranties.
// 2. Redistributions in binary form must reproduce the above copyright
//    notice, this list of conditions and the following disclaimer in the
//    documentation and/or other materials provided with the distribution.
// 3. The name of the author may not be used to endorse or promote
//    products derived from this software without specific prior
//    written permission.
//
// THIS SOFTWARE IS PROVIDED ``AS IS'' AND ANY EXPRESS OR IMPLIED
// WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED WARRANTIES
// OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE, ALL OF
// WHICH ARE HEREBY DISCLAIMED.  IN NO EVENT SHALL THE AUTHOR BE
// LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR
// CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT
// OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR
// BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF
// LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
// (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE
// USE OF THIS SOFTWARE, EVEN IF NOT ADVISED OF THE POSSIBILITY OF SUCH
// DAMAGE.
// %End-Header%
```

### N4.2 — uuid/uuid/uuid_linux_amd64.go, uuid/uuid/uuid_linux_arm64.go（共 16 处）

原文 SHA256：`6d7b2382d1222242c347048172abf0613f3280937d9d2bc4c717b3a5940109ff`。

```text
// Copyright (C) 1991-2020 Free Software Foundation, Inc.
//    This file is part of the GNU C Library.
//
//    The GNU C Library is free software; you can redistribute it and/or
//    modify it under the terms of the GNU Lesser General Public
//    License as published by the Free Software Foundation; either
//    version 2.1 of the License, or (at your option) any later version.
//
//    The GNU C Library is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
//    Lesser General Public License for more details.
//
//    You should have received a copy of the GNU Lesser General Public
//    License along with the GNU C Library; if not, see
//    <https://www.gnu.org/licenses/>.
```

### N4.3 — uuid/uuid/uuid_linux_amd64.go, uuid/uuid/uuid_linux_arm64.go

原文 SHA256：`ccedbd2bd9d59bf11b6ee42a0ec8f9fdeb119907ce37869541e957a8d4ae7f37`。

```text
// Get definitions of __STDC_* predefined macros, if the compiler has
//    not preincluded this header automatically.
// Copyright (C) 1991-2020 Free Software Foundation, Inc.
//    This file is part of the GNU C Library.
//
//    The GNU C Library is free software; you can redistribute it and/or
//    modify it under the terms of the GNU Lesser General Public
//    License as published by the Free Software Foundation; either
//    version 2.1 of the License, or (at your option) any later version.
//
//    The GNU C Library is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
//    Lesser General Public License for more details.
//
//    You should have received a copy of the GNU Lesser General Public
//    License along with the GNU C Library; if not, see
//    <https://www.gnu.org/licenses/>.
```

### N4.4 — uuid/uuid/uuid_linux_amd64.go, uuid/uuid/uuid_linux_arm64.go

原文 SHA256：`250f7770c101d26506f559d8308d69767a27f952333558c51488e5238a7a8936`。

```text
// This is here only because every header file already includes this one.
// Copyright (C) 1992-2020 Free Software Foundation, Inc.
//    This file is part of the GNU C Library.
//
//    The GNU C Library is free software; you can redistribute it and/or
//    modify it under the terms of the GNU Lesser General Public
//    License as published by the Free Software Foundation; either
//    version 2.1 of the License, or (at your option) any later version.
//
//    The GNU C Library is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
//    Lesser General Public License for more details.
//
//    You should have received a copy of the GNU Lesser General Public
//    License along with the GNU C Library; if not, see
//    <https://www.gnu.org/licenses/>.
```

### N4.5 — uuid/uuid/uuid_linux_amd64.go

原文 SHA256：`5ca46e2a8be7705dd2a4345db01dc24dd68952c41ebb89e0977e3755fa630692`。

```text
// Both x86-64 and x32 use the 64-bit system call interface.
// Properties of long double type.  ldbl-96 version.
//    Copyright (C) 2016-2020 Free Software Foundation, Inc.
//    This file is part of the GNU C Library.
//
//    The GNU C Library is free software; you can redistribute it and/or
//    modify it under the terms of the GNU Lesser General Public
//    License  published by the Free Software Foundation; either
//    version 2.1 of the License, or (at your option) any later version.
//
//    The GNU C Library is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
//    Lesser General Public License for more details.
//
//    You should have received a copy of the GNU Lesser General Public
//    License along with the GNU C Library; if not, see
//    <https://www.gnu.org/licenses/>.
```

### N4.6 — uuid/uuid/uuid_linux_amd64.go, uuid/uuid/uuid_linux_arm64.go（共 24 处）

原文 SHA256：`222ec99391aaae6c531887cd42888b42d59689a8434b1de7fb5fa18bbb90b7f7`。

```text
// bits/types.h -- definitions of __*_t types underlying *_t types.
//    Copyright (C) 2002-2020 Free Software Foundation, Inc.
//    This file is part of the GNU C Library.
//
//    The GNU C Library is free software; you can redistribute it and/or
//    modify it under the terms of the GNU Lesser General Public
//    License as published by the Free Software Foundation; either
//    version 2.1 of the License, or (at your option) any later version.
//
//    The GNU C Library is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
//    Lesser General Public License for more details.
//
//    You should have received a copy of the GNU Lesser General Public
//    License along with the GNU C Library; if not, see
//    <https://www.gnu.org/licenses/>.
```

### N4.7 — uuid/uuid/uuid_linux_amd64.go

原文 SHA256：`1e0731290fc214b6565ac8475d1705a375a069d474c21eef773a8bf842ba5820`。

```text
// Both x86-64 and x32 use the 64-bit system call interface.
// Bit size of the time_t type at glibc build time, x86-64 and x32 case.
//    Copyright (C) 2018-2020 Free Software Foundation, Inc.
//    This file is part of the GNU C Library.
//
//    The GNU C Library is free software; you can redistribute it and/or
//    modify it under the terms of the GNU Lesser General Public
//    License as published by the Free Software Foundation; either
//    version 2.1 of the License, or (at your option) any later version.
//
//    The GNU C Library is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
//    Lesser General Public License for more details.
//
//    You should have received a copy of the GNU Lesser General Public
//    License along with the GNU C Library; if not, see
//    <https://www.gnu.org/licenses/>.
```

### N4.8 — uuid/uuid/uuid_linux_amd64.go

原文 SHA256：`13d2ff35c56fdc1b505c7db6b5a9a848e414787e1d4bc0911802b8f2ebadf60e`。

```text
// No need to mark the typedef with __extension__.
// bits/typesizes.h -- underlying types for *_t.  Linux/x86-64 version.
//    Copyright (C) 2012-2020 Free Software Foundation, Inc.
//    This file is part of the GNU C Library.
//
//    The GNU C Library is free software; you can redistribute it and/or
//    modify it under the terms of the GNU Lesser General Public
//    License as published by the Free Software Foundation; either
//    version 2.1 of the License, or (at your option) any later version.
//
//    The GNU C Library is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
//    Lesser General Public License for more details.
//
//    You should have received a copy of the GNU Lesser General Public
//    License along with the GNU C Library; if not, see
//    <https://www.gnu.org/licenses/>.
```

### N4.9 — uuid/uuid/uuid_linux_amd64.go, uuid/uuid/uuid_linux_arm64.go

原文 SHA256：`4db16c3cc31f506382c02c8a14f2301d292212ff464e316c8f3779f93c225ccd`。

```text
// bits/time64.h -- underlying types for __time64_t.  Generic version.
//    Copyright (C) 2018-2020 Free Software Foundation, Inc.
//    This file is part of the GNU C Library.
//
//    The GNU C Library is free software; you can redistribute it and/or
//    modify it under the terms of the GNU Lesser General Public
//    License as published by the Free Software Foundation; either
//    version 2.1 of the License, or (at your option) any later version.
//
//    The GNU C Library is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
//    Lesser General Public License for more details.
//
//    You should have received a copy of the GNU Lesser General Public
//    License along with the GNU C Library; if not, see
//    <https://www.gnu.org/licenses/>.
```

### N4.10 — uuid/uuid/uuid_linux_amd64.go, uuid/uuid/uuid_linux_arm64.go

原文 SHA256：`b801a57b2955888701c892a37beb308a706145e7856bbc60202389734edcb5e0`。

```text
// Define intN_t types.
//    Copyright (C) 2017-2020 Free Software Foundation, Inc.
//    This file is part of the GNU C Library.
//
//    The GNU C Library is free software; you can redistribute it and/or
//    modify it under the terms of the GNU Lesser General Public
//    License as published by the Free Software Foundation; either
//    version 2.1 of the License, or (at your option) any later version.
//
//    The GNU C Library is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
//    Lesser General Public License for more details.
//
//    You should have received a copy of the GNU Lesser General Public
//    License along with the GNU C Library; if not, see
//    <https://www.gnu.org/licenses/>.
```

### N4.11 — uuid/uuid/uuid_linux_amd64.go, uuid/uuid/uuid_linux_arm64.go

原文 SHA256：`ae418e9cbfbdf48d25162fc1e68ab51048c0be3b60546b54c6b6710b7001a9eb`。

```text
// It also defines `fd_set' and the FD_* macros for `select'.
// `fd_set' type and related macros, and `select'/`pselect' declarations.
//    Copyright (C) 1996-2020 Free Software Foundation, Inc.
//    This file is part of the GNU C Library.
//
//    The GNU C Library is free software; you can redistribute it and/or
//    modify it under the terms of the GNU Lesser General Public
//    License as published by the Free Software Foundation; either
//    version 2.1 of the License, or (at your option) any later version.
//
//    The GNU C Library is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
//    Lesser General Public License for more details.
//
//    You should have received a copy of the GNU Lesser General Public
//    License along with the GNU C Library; if not, see
//    <https://www.gnu.org/licenses/>.
```

### N4.12 — uuid/uuid/uuid_linux_amd64.go, uuid/uuid/uuid_linux_arm64.go

原文 SHA256：`326e89bd7d56319b16fb2bd0483bc05e43876582ce3f0851bf1130d9e9ad700a`。

```text
// Get definition of needed basic types.
// bits/types.h -- definitions of __*_t types underlying *_t types.
//    Copyright (C) 2002-2020 Free Software Foundation, Inc.
//    This file is part of the GNU C Library.
//
//    The GNU C Library is free software; you can redistribute it and/or
//    modify it under the terms of the GNU Lesser General Public
//    License as published by the Free Software Foundation; either
//    version 2.1 of the License, or (at your option) any later version.
//
//    The GNU C Library is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
//    Lesser General Public License for more details.
//
//    You should have received a copy of the GNU Lesser General Public
//    License along with the GNU C Library; if not, see
//    <https://www.gnu.org/licenses/>.
```

### N4.13 — uuid/uuid/uuid_linux_amd64.go, uuid/uuid/uuid_linux_arm64.go

原文 SHA256：`8f99987097cb3c2b9413aaaca5c002f6591111e95563d4e429727eeab8024307`。

```text
// Get __FD_* definitions.
// Copyright (C) 1997-2020 Free Software Foundation, Inc.
//    This file is part of the GNU C Library.
//
//    The GNU C Library is free software; you can redistribute it and/or
//    modify it under the terms of the GNU Lesser General Public
//    License as published by the Free Software Foundation; either
//    version 2.1 of the License, or (at your option) any later version.
//
//    The GNU C Library is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
//    Lesser General Public License for more details.
//
//    You should have received a copy of the GNU Lesser General Public
//    License along with the GNU C Library; if not, see
//    <https://www.gnu.org/licenses/>.
```

### N4.14 — uuid/uuid/uuid_linux_amd64.go, uuid/uuid/uuid_linux_arm64.go, uuid/uuid/uuid_linux_arm64.go, uuid/uuid/uuid_linux_arm64.go

原文 SHA256：`16697f18081a60c71a7f61f387c7cb5fe2fd48de68b1b2ed9aee4da8a20882a8`。

```text
// Endian macros for string.h functions
//    Copyright (C) 1992-2020 Free Software Foundation, Inc.
//    This file is part of the GNU C Library.
//
//    The GNU C Library is free software; you can redistribute it and/or
//    modify it under the terms of the GNU Lesser General Public
//    License as published by the Free Software Foundation; either
//    version 2.1 of the License, or (at your option) any later version.
//
//    The GNU C Library is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
//    Lesser General Public License for more details.
//
//    You should have received a copy of the GNU Lesser General Public
//    License along with the GNU C Library; if not, see
//    <http://www.gnu.org/licenses/>.
```

### N4.15 — uuid/uuid/uuid_linux_amd64.go, uuid/uuid/uuid_linux_amd64.go, uuid/uuid/uuid_linux_arm64.go, uuid/uuid/uuid_linux_arm64.go

原文 SHA256：`d904d9714bfe192dd880605c6d0cf0ab81025c091b0fa29641b9faf3bbe2d63c`。

```text
// Now add the thread types.
// Declaration of common pthread types for all architectures.
//    Copyright (C) 2017-2020 Free Software Foundation, Inc.
//    This file is part of the GNU C Library.
//
//    The GNU C Library is free software; you can redistribute it and/or
//    modify it under the terms of the GNU Lesser General Public
//    License as published by the Free Software Foundation; either
//    version 2.1 of the License, or (at your option) any later version.
//
//    The GNU C Library is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
//    Lesser General Public License for more details.
//
//    You should have received a copy of the GNU Lesser General Public
//    License along with the GNU C Library; if not, see
//    <https://www.gnu.org/licenses/>.
```

### N4.16 — uuid/uuid/uuid_linux_amd64.go, uuid/uuid/uuid_linux_amd64.go, uuid/uuid/uuid_linux_arm64.go, uuid/uuid/uuid_linux_arm64.go

原文 SHA256：`863e5c8835f15f3f703ff15bfd776f716a6434bab4191745a14befaf07cc8326`。

```text
// For internal mutex and condition variable definitions.
// Common threading primitives definitions for both POSIX and C11.
//    Copyright (C) 2017-2020 Free Software Foundation, Inc.
//    This file is part of the GNU C Library.
//
//    The GNU C Library is free software; you can redistribute it and/or
//    modify it under the terms of the GNU Lesser General Public
//    License as published by the Free Software Foundation; either
//    version 2.1 of the License, or (at your option) any later version.
//
//    The GNU C Library is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
//    Lesser General Public License for more details.
//
//    You should have received a copy of the GNU Lesser General Public
//    License along with the GNU C Library; if not, see
//    <https://www.gnu.org/licenses/>.
```

### N4.17 — uuid/uuid/uuid_linux_amd64.go, uuid/uuid/uuid_linux_amd64.go

原文 SHA256：`335e6f6e980d11474630e873246728bb0aa8d944615e989b3bc255293b99c815`。

```text
// Copyright (C) 2002-2020 Free Software Foundation, Inc.
//    This file is part of the GNU C Library.
//
//    The GNU C Library is free software; you can redistribute it and/or
//    modify it under the terms of the GNU Lesser General Public
//    License as published by the Free Software Foundation; either
//    version 2.1 of the License, or (at your option) any later version.
//
//    The GNU C Library is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
//    Lesser General Public License for more details.
//
//    You should have received a copy of the GNU Lesser General Public
//    License along with the GNU C Library; if not, see
//    <https://www.gnu.org/licenses/>.
```

### N4.18 — uuid/uuid/uuid_linux_amd64.go

原文 SHA256：`c06296b8cf3c2afd413889d3e180f90c3b383feb76c079fdf7a97d920cb6ef80`。

```text
// x86 internal mutex struct definitions.
//    Copyright (C) 2019-2020 Free Software Foundation, Inc.
//    This file is part of the GNU C Library.
//
//    The GNU C Library is free software; you can redistribute it and/or
//    modify it under the terms of the GNU Lesser General Public
//    License as published by the Free Software Foundation; either
//    version 2.1 of the License, or (at your option) any later version.
//
//    The GNU C Library is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
//    Lesser General Public License for more details.
//
//    You should have received a copy of the GNU Lesser General Public
//    License along with the GNU C Library; if not, see
//    <http://www.gnu.org/licenses/>.
```

### N4.19 — uuid/uuid/uuid_linux_amd64.go

原文 SHA256：`fcedda0dd602290bf163ae93745a09784c1b45cf27a92f6b1e423a47e1df9a39`。

```text
// x86 internal rwlock struct definitions.
//    Copyright (C) 2019-2020 Free Software Foundation, Inc.
//
//    This file is part of the GNU C Library.
//
//    The GNU C Library is free software; you can redistribute it and/or
//    modify it under the terms of the GNU Lesser General Public
//    License as published by the Free Software Foundation; either
//    version 2.1 of the License, or (at your option) any later version.
//
//    The GNU C Library is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
//    Lesser General Public License for more details.
//
//    You should have received a copy of the GNU Lesser General Public
//    License along with the GNU C Library; if not, see
//    <http://www.gnu.org/licenses/>.
```

### N4.20 — uuid/uuid/uuid_linux_amd64.go, uuid/uuid/uuid_linux_arm64.go

原文 SHA256：`9a33ab2847249e8db4dde5beaa1b458fba06943bebf28a832552eb964f761917`。

```text
// `fd_set' type and related macros, and `select'/`pselect' declarations.
//    Copyright (C) 1996-2020 Free Software Foundation, Inc.
//    This file is part of the GNU C Library.
//
//    The GNU C Library is free software; you can redistribute it and/or
//    modify it under the terms of the GNU Lesser General Public
//    License as published by the Free Software Foundation; either
//    version 2.1 of the License, or (at your option) any later version.
//
//    The GNU C Library is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
//    Lesser General Public License for more details.
//
//    You should have received a copy of the GNU Lesser General Public
//    License along with the GNU C Library; if not, see
//    <https://www.gnu.org/licenses/>.
```

### N4.21 — uuid/uuid/uuid_linux_amd64.go, uuid/uuid/uuid_linux_arm64.go

原文 SHA256：`ed41f03e91cdf0d43f783025060a98e5c56db5f3e1b3f98ff7ce8c8ca7cb1ac3`。

```text
// Copyright (C) 1989-2020 Free Software Foundation, Inc.
//
// This file is part of GCC.
//
// GCC is free software; you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation; either version 3, or (at your option)
// any later version.
//
// GCC is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// Under Section 7 of GPL version 3, you are granted additional
// permissions described in the GCC Runtime Library Exception, version
// 3.1, as published by the Free Software Foundation.
//
// You should have received a copy of the GNU General Public License and
// a copy of the GCC Runtime Library Exception along with this program;
// see the files COPYING3 and COPYING.RUNTIME respectively.  If not, see
// <http://www.gnu.org/licenses/>.
```

### N4.22 — uuid/uuid/uuid_linux_amd64.go, uuid/uuid/uuid_linux_arm64.go

原文 SHA256：`569a0d4157b10dd6a63521479a98a4654c21416d0ff1722bd30e5a01a3207707`。

```text
// This defines CLOCKS_PER_SEC, which is the number of processor clock
//    ticks per second, and possibly a number of other constants.
// System-dependent timing definitions.  Linux version.
//    Copyright (C) 1996-2020 Free Software Foundation, Inc.
//    This file is part of the GNU C Library.
//
//    The GNU C Library is free software; you can redistribute it and/or
//    modify it under the terms of the GNU Lesser General Public
//    License as published by the Free Software Foundation; either
//    version 2.1 of the License, or (at your option) any later version.
//
//    The GNU C Library is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
//    Lesser General Public License for more details.
//
//    You should have received a copy of the GNU Lesser General Public
//    License along with the GNU C Library; if not, see
//    <https://www.gnu.org/licenses/>.
```

### N4.23 — uuid/uuid/uuid_linux_amd64.go, uuid/uuid/uuid_linux_arm64.go

原文 SHA256：`1b34950b7630a445092471e0c418c1a0bfb1130a923525b23c2baebef73a4e6e`。

```text
// Definition of locale_t.
//    Copyright (C) 2017-2020 Free Software Foundation, Inc.
//    This file is part of the GNU C Library.
//
//    The GNU C Library is free software; you can redistribute it and/or
//    modify it under the terms of the GNU Lesser General Public
//    License as published by the Free Software Foundation; either
//    version 2.1 of the License, or (at your option) any later version.
//
//    The GNU C Library is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
//    Lesser General Public License for more details.
//
//    You should have received a copy of the GNU Lesser General Public
//    License along with the GNU C Library; if not, see
//    <https://www.gnu.org/licenses/>.
```

### N4.24 — uuid/uuid/uuid_linux_amd64.go, uuid/uuid/uuid_linux_arm64.go

原文 SHA256：`ac4183bee862334384a24ff59d3cfa3dadf0b16a1f39316db134a9a8966df398`。

```text
// Definition of struct __locale_struct and __locale_t.
//    Copyright (C) 1997-2020 Free Software Foundation, Inc.
//    This file is part of the GNU C Library.
//    Contributed by Ulrich Drepper <drepper@cygnus.com>, 1997.
//
//    The GNU C Library is free software; you can redistribute it and/or
//    modify it under the terms of the GNU Lesser General Public
//    License as published by the Free Software Foundation; either
//    version 2.1 of the License, or (at your option) any later version.
//
//    The GNU C Library is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
//    Lesser General Public License for more details.
//
//    You should have received a copy of the GNU Lesser General Public
//    License along with the GNU C Library; if not, see
//    <https://www.gnu.org/licenses/>.
```

### N4.25 — uuid/uuid/uuid_linux_arm64.go, uuid/uuid/uuid_linux_arm64.go, uuid/uuid/uuid_linux_arm64.go, uuid/uuid/uuid_linux_arm64.go

原文 SHA256：`c2b6aea90bd3d074d102cfdf131d39d601b181de2343d3ad44fbc40de9055dd5`。

```text
// Determine the wordsize from the preprocessor defines.
//
//    Copyright (C) 2016-2020 Free Software Foundation, Inc.
//    This file is part of the GNU C Library.
//
//    The GNU C Library is free software; you can redistribute it and/or
//    modify it under the terms of the GNU Lesser General Public
//    License as published by the Free Software Foundation; either
//    version 2.1 of the License, or (at your option) any later version.
//
//    The GNU C Library is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
//    Lesser General Public License for more details.
//
//    You should have received a copy of the GNU Lesser General Public
//    License along with the GNU C Library; if not, see
//    <https://www.gnu.org/licenses/>.
```

### N4.26 — uuid/uuid/uuid_linux_arm64.go

原文 SHA256：`2a6033d95b20625bef5dd8db6b83ad7e93c52a179662bc1a8ca79f1e6ce2a552`。

```text
// Properties of long double type.  ldbl-128 version.
//    Copyright (C) 2016-2020 Free Software Foundation, Inc.
//    This file is part of the GNU C Library.
//
//    The GNU C Library is free software; you can redistribute it and/or
//    modify it under the terms of the GNU Lesser General Public
//    License  published by the Free Software Foundation; either
//    version 2.1 of the License, or (at your option) any later version.
//
//    The GNU C Library is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
//    Lesser General Public License for more details.
//
//    You should have received a copy of the GNU Lesser General Public
//    License along with the GNU C Library; if not, see
//    <https://www.gnu.org/licenses/>.
```

### N4.27 — uuid/uuid/uuid_linux_arm64.go

原文 SHA256：`b1caaf21c615cdccd050f09a700ec66592e9324e4221cef5219b3cb514c0889d`。

```text
// Bit size of the time_t type at glibc build time, general case.
//    Copyright (C) 2018-2020 Free Software Foundation, Inc.
//    This file is part of the GNU C Library.
//
//    The GNU C Library is free software; you can redistribute it and/or
//    modify it under the terms of the GNU Lesser General Public
//    License as published by the Free Software Foundation; either
//    version 2.1 of the License, or (at your option) any later version.
//
//    The GNU C Library is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
//    Lesser General Public License for more details.
//
//    You should have received a copy of the GNU Lesser General Public
//    License along with the GNU C Library; if not, see
//    <https://www.gnu.org/licenses/>.
```

### N4.28 — uuid/uuid/uuid_linux_arm64.go

原文 SHA256：`dc80abd51805c16490a44ff93d8b47f2a861839f9b58253050799f575f7f0300`。

```text
// No need to mark the typedef with __extension__.
// bits/typesizes.h -- underlying types for *_t.  For the generic Linux ABI.
//    Copyright (C) 2011-2020 Free Software Foundation, Inc.
//    This file is part of the GNU C Library.
//    Contributed by Chris Metcalf <cmetcalf@tilera.com>, 2011.
//
//    The GNU C Library is free software; you can redistribute it and/or
//    modify it under the terms of the GNU Lesser General Public
//    License as published by the Free Software Foundation; either
//    version 2.1 of the License, or (at your option) any later version.
//
//    The GNU C Library is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
//    Lesser General Public License for more details.
//
//    You should have received a copy of the GNU Lesser General Public
//    License along with the GNU C Library.  If not, see
//    <https://www.gnu.org/licenses/>.
```

### N4.29 — uuid/uuid/uuid_linux_arm64.go, uuid/uuid/uuid_linux_arm64.go

原文 SHA256：`3619111d767a59f7b38c4d907f94d33733a5d4e670f584652ced8fb6b4a37b60`。

```text
// Copyright (C) 2002-2020 Free Software Foundation, Inc.
//
//    This file is part of the GNU C Library.
//
//    The GNU C Library is free software; you can redistribute it and/or
//    modify it under the terms of the GNU Lesser General Public
//    License as published by the Free Software Foundation; either
//    version 2.1 of the License, or (at your option) any later version.
//
//    The GNU C Library is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
//    Lesser General Public License for more details.
//
//    You should have received a copy of the GNU Lesser General Public
//    License along with the GNU C Library; if not, see
//    <https://www.gnu.org/licenses/>.
```

### N4.30 — uuid/uuid/uuid_linux_arm64.go

原文 SHA256：`9561332f7a4a918bcd1fbcf5c61096f28e5fa0be4f1715a8d1f28e107789fda0`。

```text
// Default mutex implementation struct definitions.
//    Copyright (C) 2019-2020 Free Software Foundation, Inc.
//    This file is part of the GNU C Library.
//
//    The GNU C Library is free software; you can redistribute it and/or
//    modify it under the terms of the GNU Lesser General Public
//    License as published by the Free Software Foundation; either
//    version 2.1 of the License, or (at your option) any later version.
//
//    The GNU C Library is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
//    Lesser General Public License for more details.
//
//    You should have received a copy of the GNU Lesser General Public
//    License along with the GNU C Library; if not, see
//    <http://www.gnu.org/licenses/>.
```

### N4.31 — uuid/uuid/uuid_linux_arm64.go

原文 SHA256：`2f6c94e20543f40353454762ef81b1b7df4b1271cdb0c860ff8dab821b68ab01`。

```text
// AArch64 internal rwlock struct definitions.
//    Copyright (C) 2019-2020 Free Software Foundation, Inc.
//
//    This file is part of the GNU C Library.
//
//    The GNU C Library is free software; you can redistribute it and/or
//    modify it under the terms of the GNU Lesser General Public
//    License as published by the Free Software Foundation; either
//    version 2.1 of the License, or (at your option) any later version.
//
//    The GNU C Library is distributed in the hope that it will be useful,
//    but WITHOUT ANY WARRANTY; without even the implied warranty of
//    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
//    Lesser General Public License for more details.
//
//    You should have received a copy of the GNU Lesser General Public
//    License along with the GNU C Library; if not, see
//    <http://www.gnu.org/licenses/>.
```

### N4 — GNU LGPL 2.1 全文；https://www.gnu.org/licenses/old-licenses/lgpl-2.1.txt

原文 SHA256：`20e50fe7aae3e56378ebf0417d9de904f55a0e61e4df315333e632a4d3555d95`。

```text
                  GNU LESSER GENERAL PUBLIC LICENSE
                       Version 2.1, February 1999

 Copyright (C) 1991, 1999 Free Software Foundation, Inc.
 <https://fsf.org/>
 Everyone is permitted to copy and distribute verbatim copies
 of this license document, but changing it is not allowed.

[This is the first released version of the Lesser GPL.  It also counts
 as the successor of the GNU Library Public License, version 2, hence
 the version number 2.1.]

                            Preamble

  The licenses for most software are designed to take away your
freedom to share and change it.  By contrast, the GNU General Public
Licenses are intended to guarantee your freedom to share and change
free software--to make sure the software is free for all its users.

  This license, the Lesser General Public License, applies to some
specially designated software packages--typically libraries--of the
Free Software Foundation and other authors who decide to use it.  You
can use it too, but we suggest you first think carefully about whether
this license or the ordinary General Public License is the better
strategy to use in any particular case, based on the explanations below.

  When we speak of free software, we are referring to freedom of use,
not price.  Our General Public Licenses are designed to make sure that
you have the freedom to distribute copies of free software (and charge
for this service if you wish); that you receive source code or can get
it if you want it; that you can change the software and use pieces of
it in new free programs; and that you are informed that you can do
these things.

  To protect your rights, we need to make restrictions that forbid
distributors to deny you these rights or to ask you to surrender these
rights.  These restrictions translate to certain responsibilities for
you if you distribute copies of the library or if you modify it.

  For example, if you distribute copies of the library, whether gratis
or for a fee, you must give the recipients all the rights that we gave
you.  You must make sure that they, too, receive or can get the source
code.  If you link other code with the library, you must provide
complete object files to the recipients, so that they can relink them
with the library after making changes to the library and recompiling
it.  And you must show them these terms so they know their rights.

  We protect your rights with a two-step method: (1) we copyright the
library, and (2) we offer you this license, which gives you legal
permission to copy, distribute and/or modify the library.

  To protect each distributor, we want to make it very clear that
there is no warranty for the free library.  Also, if the library is
modified by someone else and passed on, the recipients should know
that what they have is not the original version, so that the original
author's reputation will not be affected by problems that might be
introduced by others.


  Finally, software patents pose a constant threat to the existence of
any free program.  We wish to make sure that a company cannot
effectively restrict the users of a free program by obtaining a
restrictive license from a patent holder.  Therefore, we insist that
any patent license obtained for a version of the library must be
consistent with the full freedom of use specified in this license.

  Most GNU software, including some libraries, is covered by the
ordinary GNU General Public License.  This license, the GNU Lesser
General Public License, applies to certain designated libraries, and
is quite different from the ordinary General Public License.  We use
this license for certain libraries in order to permit linking those
libraries into non-free programs.

  When a program is linked with a library, whether statically or using
a shared library, the combination of the two is legally speaking a
combined work, a derivative of the original library.  The ordinary
General Public License therefore permits such linking only if the
entire combination fits its criteria of freedom.  The Lesser General
Public License permits more lax criteria for linking other code with
the library.

  We call this license the "Lesser" General Public License because it
does Less to protect the user's freedom than the ordinary General
Public License.  It also provides other free software developers Less
of an advantage over competing non-free programs.  These disadvantages
are the reason we use the ordinary General Public License for many
libraries.  However, the Lesser license provides advantages in certain
special circumstances.

  For example, on rare occasions, there may be a special need to
encourage the widest possible use of a certain library, so that it becomes
a de-facto standard.  To achieve this, non-free programs must be
allowed to use the library.  A more frequent case is that a free
library does the same job as widely used non-free libraries.  In this
case, there is little to gain by limiting the free library to free
software only, so we use the Lesser General Public License.

  In other cases, permission to use a particular library in non-free
programs enables a greater number of people to use a large body of
free software.  For example, permission to use the GNU C Library in
non-free programs enables many more people to use the whole GNU
operating system, as well as its variant, the GNU/Linux operating
system.

  Although the Lesser General Public License is Less protective of the
users' freedom, it does ensure that the user of a program that is
linked with the Library has the freedom and the wherewithal to run
that program using a modified version of the Library.

  The precise terms and conditions for copying, distribution and
modification follow.  Pay close attention to the difference between a
"work based on the library" and a "work that uses the library".  The
former contains code derived from the library, whereas the latter must
be combined with the library in order to run.


                  GNU LESSER GENERAL PUBLIC LICENSE
   TERMS AND CONDITIONS FOR COPYING, DISTRIBUTION AND MODIFICATION

  0. This License Agreement applies to any software library or other
program which contains a notice placed by the copyright holder or
other authorized party saying it may be distributed under the terms of
this Lesser General Public License (also called "this License").
Each licensee is addressed as "you".

  A "library" means a collection of software functions and/or data
prepared so as to be conveniently linked with application programs
(which use some of those functions and data) to form executables.

  The "Library", below, refers to any such software library or work
which has been distributed under these terms.  A "work based on the
Library" means either the Library or any derivative work under
copyright law: that is to say, a work containing the Library or a
portion of it, either verbatim or with modifications and/or translated
straightforwardly into another language.  (Hereinafter, translation is
included without limitation in the term "modification".)

  "Source code" for a work means the preferred form of the work for
making modifications to it.  For a library, complete source code means
all the source code for all modules it contains, plus any associated
interface definition files, plus the scripts used to control compilation
and installation of the library.

  Activities other than copying, distribution and modification are not
covered by this License; they are outside its scope.  The act of
running a program using the Library is not restricted, and output from
such a program is covered only if its contents constitute a work based
on the Library (independent of the use of the Library in a tool for
writing it).  Whether that is true depends on what the Library does
and what the program that uses the Library does.

  1. You may copy and distribute verbatim copies of the Library's
complete source code as you receive it, in any medium, provided that
you conspicuously and appropriately publish on each copy an
appropriate copyright notice and disclaimer of warranty; keep intact
all the notices that refer to this License and to the absence of any
warranty; and distribute a copy of this License along with the
Library.

  You may charge a fee for the physical act of transferring a copy,
and you may at your option offer warranty protection in exchange for a
fee.


  2. You may modify your copy or copies of the Library or any portion
of it, thus forming a work based on the Library, and copy and
distribute such modifications or work under the terms of Section 1
above, provided that you also meet all of these conditions:

    a) The modified work must itself be a software library.

    b) You must cause the files modified to carry prominent notices
    stating that you changed the files and the date of any change.

    c) You must cause the whole of the work to be licensed at no
    charge to all third parties under the terms of this License.

    d) If a facility in the modified Library refers to a function or a
    table of data to be supplied by an application program that uses
    the facility, other than as an argument passed when the facility
    is invoked, then you must make a good faith effort to ensure that,
    in the event an application does not supply such function or
    table, the facility still operates, and performs whatever part of
    its purpose remains meaningful.

    (For example, a function in a library to compute square roots has
    a purpose that is entirely well-defined independent of the
    application.  Therefore, Subsection 2d requires that any
    application-supplied function or table used by this function must
    be optional: if the application does not supply it, the square
    root function must still compute square roots.)

These requirements apply to the modified work as a whole.  If
identifiable sections of that work are not derived from the Library,
and can be reasonably considered independent and separate works in
themselves, then this License, and its terms, do not apply to those
sections when you distribute them as separate works.  But when you
distribute the same sections as part of a whole which is a work based
on the Library, the distribution of the whole must be on the terms of
this License, whose permissions for other licensees extend to the
entire whole, and thus to each and every part regardless of who wrote
it.

Thus, it is not the intent of this section to claim rights or contest
your rights to work written entirely by you; rather, the intent is to
exercise the right to control the distribution of derivative or
collective works based on the Library.

In addition, mere aggregation of another work not based on the Library
with the Library (or with a work based on the Library) on a volume of
a storage or distribution medium does not bring the other work under
the scope of this License.

  3. You may opt to apply the terms of the ordinary GNU General Public
License instead of this License to a given copy of the Library.  To do
this, you must alter all the notices that refer to this License, so
that they refer to the ordinary GNU General Public License, version 2,
instead of to this License.  (If a newer version than version 2 of the
ordinary GNU General Public License has appeared, then you can specify
that version instead if you wish.)  Do not make any other change in
these notices.


  Once this change is made in a given copy, it is irreversible for
that copy, so the ordinary GNU General Public License applies to all
subsequent copies and derivative works made from that copy.

  This option is useful when you wish to copy part of the code of
the Library into a program that is not a library.

  4. You may copy and distribute the Library (or a portion or
derivative of it, under Section 2) in object code or executable form
under the terms of Sections 1 and 2 above provided that you accompany
it with the complete corresponding machine-readable source code, which
must be distributed under the terms of Sections 1 and 2 above on a
medium customarily used for software interchange.

  If distribution of object code is made by offering access to copy
from a designated place, then offering equivalent access to copy the
source code from the same place satisfies the requirement to
distribute the source code, even though third parties are not
compelled to copy the source along with the object code.

  5. A program that contains no derivative of any portion of the
Library, but is designed to work with the Library by being compiled or
linked with it, is called a "work that uses the Library".  Such a
work, in isolation, is not a derivative work of the Library, and
therefore falls outside the scope of this License.

  However, linking a "work that uses the Library" with the Library
creates an executable that is a derivative of the Library (because it
contains portions of the Library), rather than a "work that uses the
library".  The executable is therefore covered by this License.
Section 6 states terms for distribution of such executables.

  When a "work that uses the Library" uses material from a header file
that is part of the Library, the object code for the work may be a
derivative work of the Library even though the source code is not.
Whether this is true is especially significant if the work can be
linked without the Library, or if the work is itself a library.  The
threshold for this to be true is not precisely defined by law.

  If such an object file uses only numerical parameters, data
structure layouts and accessors, and small macros and small inline
functions (ten lines or less in length), then the use of the object
file is unrestricted, regardless of whether it is legally a derivative
work.  (Executables containing this object code plus portions of the
Library will still fall under Section 6.)

  Otherwise, if the work is a derivative of the Library, you may
distribute the object code for the work under the terms of Section 6.
Any executables containing that work also fall under Section 6,
whether or not they are linked directly with the Library itself.


  6. As an exception to the Sections above, you may also combine or
link a "work that uses the Library" with the Library to produce a
work containing portions of the Library, and distribute that work
under terms of your choice, provided that the terms permit
modification of the work for the customer's own use and reverse
engineering for debugging such modifications.

  You must give prominent notice with each copy of the work that the
Library is used in it and that the Library and its use are covered by
this License.  You must supply a copy of this License.  If the work
during execution displays copyright notices, you must include the
copyright notice for the Library among them, as well as a reference
directing the user to the copy of this License.  Also, you must do one
of these things:

    a) Accompany the work with the complete corresponding
    machine-readable source code for the Library including whatever
    changes were used in the work (which must be distributed under
    Sections 1 and 2 above); and, if the work is an executable linked
    with the Library, with the complete machine-readable "work that
    uses the Library", as object code and/or source code, so that the
    user can modify the Library and then relink to produce a modified
    executable containing the modified Library.  (It is understood
    that the user who changes the contents of definitions files in the
    Library will not necessarily be able to recompile the application
    to use the modified definitions.)

    b) Use a suitable shared library mechanism for linking with the
    Library.  A suitable mechanism is one that (1) uses at run time a
    copy of the library already present on the user's computer system,
    rather than copying library functions into the executable, and (2)
    will operate properly with a modified version of the library, if
    the user installs one, as long as the modified version is
    interface-compatible with the version that the work was made with.

    c) Accompany the work with a written offer, valid for at
    least three years, to give the same user the materials
    specified in Subsection 6a, above, for a charge no more
    than the cost of performing this distribution.

    d) If distribution of the work is made by offering access to copy
    from a designated place, offer equivalent access to copy the above
    specified materials from the same place.

    e) Verify that the user has already received a copy of these
    materials or that you have already sent this user a copy.

  For an executable, the required form of the "work that uses the
Library" must include any data and utility programs needed for
reproducing the executable from it.  However, as a special exception,
the materials to be distributed need not include anything that is
normally distributed (in either source or binary form) with the major
components (compiler, kernel, and so on) of the operating system on
which the executable runs, unless that component itself accompanies
the executable.

  It may happen that this requirement contradicts the license
restrictions of other proprietary libraries that do not normally
accompany the operating system.  Such a contradiction means you cannot
use both them and the Library together in an executable that you
distribute.


  7. You may place library facilities that are a work based on the
Library side-by-side in a single library together with other library
facilities not covered by this License, and distribute such a combined
library, provided that the separate distribution of the work based on
the Library and of the other library facilities is otherwise
permitted, and provided that you do these two things:

    a) Accompany the combined library with a copy of the same work
    based on the Library, uncombined with any other library
    facilities.  This must be distributed under the terms of the
    Sections above.

    b) Give prominent notice with the combined library of the fact
    that part of it is a work based on the Library, and explaining
    where to find the accompanying uncombined form of the same work.

  8. You may not copy, modify, sublicense, link with, or distribute
the Library except as expressly provided under this License.  Any
attempt otherwise to copy, modify, sublicense, link with, or
distribute the Library is void, and will automatically terminate your
rights under this License.  However, parties who have received copies,
or rights, from you under this License will not have their licenses
terminated so long as such parties remain in full compliance.

  9. You are not required to accept this License, since you have not
signed it.  However, nothing else grants you permission to modify or
distribute the Library or its derivative works.  These actions are
prohibited by law if you do not accept this License.  Therefore, by
modifying or distributing the Library (or any work based on the
Library), you indicate your acceptance of this License to do so, and
all its terms and conditions for copying, distributing or modifying
the Library or works based on it.

  10. Each time you redistribute the Library (or any work based on the
Library), the recipient automatically receives a license from the
original licensor to copy, distribute, link with or modify the Library
subject to these terms and conditions.  You may not impose any further
restrictions on the recipients' exercise of the rights granted herein.
You are not responsible for enforcing compliance by third parties with
this License.


  11. If, as a consequence of a court judgment or allegation of patent
infringement or for any other reason (not limited to patent issues),
conditions are imposed on you (whether by court order, agreement or
otherwise) that contradict the conditions of this License, they do not
excuse you from the conditions of this License.  If you cannot
distribute so as to satisfy simultaneously your obligations under this
License and any other pertinent obligations, then as a consequence you
may not distribute the Library at all.  For example, if a patent
license would not permit royalty-free redistribution of the Library by
all those who receive copies directly or indirectly through you, then
the only way you could satisfy both it and this License would be to
refrain entirely from distribution of the Library.

If any portion of this section is held invalid or unenforceable under any
particular circumstance, the balance of the section is intended to apply,
and the section as a whole is intended to apply in other circumstances.

It is not the purpose of this section to induce you to infringe any
patents or other property right claims or to contest validity of any
such claims; this section has the sole purpose of protecting the
integrity of the free software distribution system which is
implemented by public license practices.  Many people have made
generous contributions to the wide range of software distributed
through that system in reliance on consistent application of that
system; it is up to the author/donor to decide if he or she is willing
to distribute software through any other system and a licensee cannot
impose that choice.

This section is intended to make thoroughly clear what is believed to
be a consequence of the rest of this License.

  12. If the distribution and/or use of the Library is restricted in
certain countries either by patents or by copyrighted interfaces, the
original copyright holder who places the Library under this License may add
an explicit geographical distribution limitation excluding those countries,
so that distribution is permitted only in or among countries not thus
excluded.  In such case, this License incorporates the limitation as if
written in the body of this License.

  13. The Free Software Foundation may publish revised and/or new
versions of the Lesser General Public License from time to time.
Such new versions will be similar in spirit to the present version,
but may differ in detail to address new problems or concerns.

Each version is given a distinguishing version number.  If the Library
specifies a version number of this License which applies to it and
"any later version", you have the option of following the terms and
conditions either of that version or of any later version published by
the Free Software Foundation.  If the Library does not specify a
license version number, you may choose any version ever published by
the Free Software Foundation.


  14. If you wish to incorporate parts of the Library into other free
programs whose distribution conditions are incompatible with these,
write to the author to ask for permission.  For software which is
copyrighted by the Free Software Foundation, write to the Free
Software Foundation; we sometimes make exceptions for this.  Our
decision will be guided by the two goals of preserving the free status
of all derivatives of our free software and of promoting the sharing
and reuse of software generally.

                            NO WARRANTY

  15. BECAUSE THE LIBRARY IS LICENSED FREE OF CHARGE, THERE IS NO
WARRANTY FOR THE LIBRARY, TO THE EXTENT PERMITTED BY APPLICABLE LAW.
EXCEPT WHEN OTHERWISE STATED IN WRITING THE COPYRIGHT HOLDERS AND/OR
OTHER PARTIES PROVIDE THE LIBRARY "AS IS" WITHOUT WARRANTY OF ANY
KIND, EITHER EXPRESSED OR IMPLIED, INCLUDING, BUT NOT LIMITED TO, THE
IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR
PURPOSE.  THE ENTIRE RISK AS TO THE QUALITY AND PERFORMANCE OF THE
LIBRARY IS WITH YOU.  SHOULD THE LIBRARY PROVE DEFECTIVE, YOU ASSUME
THE COST OF ALL NECESSARY SERVICING, REPAIR OR CORRECTION.

  16. IN NO EVENT UNLESS REQUIRED BY APPLICABLE LAW OR AGREED TO IN
WRITING WILL ANY COPYRIGHT HOLDER, OR ANY OTHER PARTY WHO MAY MODIFY
AND/OR REDISTRIBUTE THE LIBRARY AS PERMITTED ABOVE, BE LIABLE TO YOU
FOR DAMAGES, INCLUDING ANY GENERAL, SPECIAL, INCIDENTAL OR
CONSEQUENTIAL DAMAGES ARISING OUT OF THE USE OR INABILITY TO USE THE
LIBRARY (INCLUDING BUT NOT LIMITED TO LOSS OF DATA OR DATA BEING
RENDERED INACCURATE OR LOSSES SUSTAINED BY YOU OR THIRD PARTIES OR A
FAILURE OF THE LIBRARY TO OPERATE WITH ANY OTHER SOFTWARE), EVEN IF
SUCH HOLDER OR OTHER PARTY HAS BEEN ADVISED OF THE POSSIBILITY OF SUCH
DAMAGES.

                     END OF TERMS AND CONDITIONS


           How to Apply These Terms to Your New Libraries

  If you develop a new library, and you want it to be of the greatest
possible use to the public, we recommend making it free software that
everyone can redistribute and change.  You can do so by permitting
redistribution under these terms (or, alternatively, under the terms of the
ordinary General Public License).

  To apply these terms, attach the following notices to the library.  It is
safest to attach them to the start of each source file to most effectively
convey the exclusion of warranty; and each file should have at least the
"copyright" line and a pointer to where the full notice is found.

    <one line to give the library's name and a brief idea of what it does.>
    Copyright (C) <year>  <name of author>

    This library is free software; you can redistribute it and/or
    modify it under the terms of the GNU Lesser General Public
    License as published by the Free Software Foundation; either
    version 2.1 of the License, or (at your option) any later version.

    This library is distributed in the hope that it will be useful,
    but WITHOUT ANY WARRANTY; without even the implied warranty of
    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
    Lesser General Public License for more details.

    You should have received a copy of the GNU Lesser General Public
    License along with this library; if not, see <https://www.gnu.org/licenses/>.

Also add information on how to contact you by electronic and paper mail.

You should also get your employer (if you work as a programmer) or your
school, if any, to sign a "copyright disclaimer" for the library, if
necessary.  Here is a sample; alter the names:

  Yoyodyne, Inc., hereby disclaims all copyright interest in the
  library `Frob' (a library for tweaking knobs) written by James Random Hacker.

  <signature of Moe Ghoul>, 1 April 1990
  Moe Ghoul, President of Vice

That's all there is to it!
```

### N4 — GNU GPL 3.0 全文；https://www.gnu.org/licenses/gpl-3.0.txt

原文 SHA256：`3972dc9744f6499f0f9b2dbf76696f2ae7ad8af9b23dde66d6af86c9dfb36986`。

```text
                    GNU GENERAL PUBLIC LICENSE
                       Version 3, 29 June 2007

 Copyright (C) 2007 Free Software Foundation, Inc. <https://fsf.org/>
 Everyone is permitted to copy and distribute verbatim copies
 of this license document, but changing it is not allowed.

                            Preamble

  The GNU General Public License is a free, copyleft license for
software and other kinds of works.

  The licenses for most software and other practical works are designed
to take away your freedom to share and change the works.  By contrast,
the GNU General Public License is intended to guarantee your freedom to
share and change all versions of a program--to make sure it remains free
software for all its users.  We, the Free Software Foundation, use the
GNU General Public License for most of our software; it applies also to
any other work released this way by its authors.  You can apply it to
your programs, too.

  When we speak of free software, we are referring to freedom, not
price.  Our General Public Licenses are designed to make sure that you
have the freedom to distribute copies of free software (and charge for
them if you wish), that you receive source code or can get it if you
want it, that you can change the software or use pieces of it in new
free programs, and that you know you can do these things.

  To protect your rights, we need to prevent others from denying you
these rights or asking you to surrender the rights.  Therefore, you have
certain responsibilities if you distribute copies of the software, or if
you modify it: responsibilities to respect the freedom of others.

  For example, if you distribute copies of such a program, whether
gratis or for a fee, you must pass on to the recipients the same
freedoms that you received.  You must make sure that they, too, receive
or can get the source code.  And you must show them these terms so they
know their rights.

  Developers that use the GNU GPL protect your rights with two steps:
(1) assert copyright on the software, and (2) offer you this License
giving you legal permission to copy, distribute and/or modify it.

  For the developers' and authors' protection, the GPL clearly explains
that there is no warranty for this free software.  For both users' and
authors' sake, the GPL requires that modified versions be marked as
changed, so that their problems will not be attributed erroneously to
authors of previous versions.

  Some devices are designed to deny users access to install or run
modified versions of the software inside them, although the manufacturer
can do so.  This is fundamentally incompatible with the aim of
protecting users' freedom to change the software.  The systematic
pattern of such abuse occurs in the area of products for individuals to
use, which is precisely where it is most unacceptable.  Therefore, we
have designed this version of the GPL to prohibit the practice for those
products.  If such problems arise substantially in other domains, we
stand ready to extend this provision to those domains in future versions
of the GPL, as needed to protect the freedom of users.

  Finally, every program is threatened constantly by software patents.
States should not allow patents to restrict development and use of
software on general-purpose computers, but in those that do, we wish to
avoid the special danger that patents applied to a free program could
make it effectively proprietary.  To prevent this, the GPL assures that
patents cannot be used to render the program non-free.

  The precise terms and conditions for copying, distribution and
modification follow.

                       TERMS AND CONDITIONS

  0. Definitions.

  "This License" refers to version 3 of the GNU General Public License.

  "Copyright" also means copyright-like laws that apply to other kinds of
works, such as semiconductor masks.

  "The Program" refers to any copyrightable work licensed under this
License.  Each licensee is addressed as "you".  "Licensees" and
"recipients" may be individuals or organizations.

  To "modify" a work means to copy from or adapt all or part of the work
in a fashion requiring copyright permission, other than the making of an
exact copy.  The resulting work is called a "modified version" of the
earlier work or a work "based on" the earlier work.

  A "covered work" means either the unmodified Program or a work based
on the Program.

  To "propagate" a work means to do anything with it that, without
permission, would make you directly or secondarily liable for
infringement under applicable copyright law, except executing it on a
computer or modifying a private copy.  Propagation includes copying,
distribution (with or without modification), making available to the
public, and in some countries other activities as well.

  To "convey" a work means any kind of propagation that enables other
parties to make or receive copies.  Mere interaction with a user through
a computer network, with no transfer of a copy, is not conveying.

  An interactive user interface displays "Appropriate Legal Notices"
to the extent that it includes a convenient and prominently visible
feature that (1) displays an appropriate copyright notice, and (2)
tells the user that there is no warranty for the work (except to the
extent that warranties are provided), that licensees may convey the
work under this License, and how to view a copy of this License.  If
the interface presents a list of user commands or options, such as a
menu, a prominent item in the list meets this criterion.

  1. Source Code.

  The "source code" for a work means the preferred form of the work
for making modifications to it.  "Object code" means any non-source
form of a work.

  A "Standard Interface" means an interface that either is an official
standard defined by a recognized standards body, or, in the case of
interfaces specified for a particular programming language, one that
is widely used among developers working in that language.

  The "System Libraries" of an executable work include anything, other
than the work as a whole, that (a) is included in the normal form of
packaging a Major Component, but which is not part of that Major
Component, and (b) serves only to enable use of the work with that
Major Component, or to implement a Standard Interface for which an
implementation is available to the public in source code form.  A
"Major Component", in this context, means a major essential component
(kernel, window system, and so on) of the specific operating system
(if any) on which the executable work runs, or a compiler used to
produce the work, or an object code interpreter used to run it.

  The "Corresponding Source" for a work in object code form means all
the source code needed to generate, install, and (for an executable
work) run the object code and to modify the work, including scripts to
control those activities.  However, it does not include the work's
System Libraries, or general-purpose tools or generally available free
programs which are used unmodified in performing those activities but
which are not part of the work.  For example, Corresponding Source
includes interface definition files associated with source files for
the work, and the source code for shared libraries and dynamically
linked subprograms that the work is specifically designed to require,
such as by intimate data communication or control flow between those
subprograms and other parts of the work.

  The Corresponding Source need not include anything that users
can regenerate automatically from other parts of the Corresponding
Source.

  The Corresponding Source for a work in source code form is that
same work.

  2. Basic Permissions.

  All rights granted under this License are granted for the term of
copyright on the Program, and are irrevocable provided the stated
conditions are met.  This License explicitly affirms your unlimited
permission to run the unmodified Program.  The output from running a
covered work is covered by this License only if the output, given its
content, constitutes a covered work.  This License acknowledges your
rights of fair use or other equivalent, as provided by copyright law.

  You may make, run and propagate covered works that you do not
convey, without conditions so long as your license otherwise remains
in force.  You may convey covered works to others for the sole purpose
of having them make modifications exclusively for you, or provide you
with facilities for running those works, provided that you comply with
the terms of this License in conveying all material for which you do
not control copyright.  Those thus making or running the covered works
for you must do so exclusively on your behalf, under your direction
and control, on terms that prohibit them from making any copies of
your copyrighted material outside their relationship with you.

  Conveying under any other circumstances is permitted solely under
the conditions stated below.  Sublicensing is not allowed; section 10
makes it unnecessary.

  3. Protecting Users' Legal Rights From Anti-Circumvention Law.

  No covered work shall be deemed part of an effective technological
measure under any applicable law fulfilling obligations under article
11 of the WIPO copyright treaty adopted on 20 December 1996, or
similar laws prohibiting or restricting circumvention of such
measures.

  When you convey a covered work, you waive any legal power to forbid
circumvention of technological measures to the extent such circumvention
is effected by exercising rights under this License with respect to
the covered work, and you disclaim any intention to limit operation or
modification of the work as a means of enforcing, against the work's
users, your or third parties' legal rights to forbid circumvention of
technological measures.

  4. Conveying Verbatim Copies.

  You may convey verbatim copies of the Program's source code as you
receive it, in any medium, provided that you conspicuously and
appropriately publish on each copy an appropriate copyright notice;
keep intact all notices stating that this License and any
non-permissive terms added in accord with section 7 apply to the code;
keep intact all notices of the absence of any warranty; and give all
recipients a copy of this License along with the Program.

  You may charge any price or no price for each copy that you convey,
and you may offer support or warranty protection for a fee.

  5. Conveying Modified Source Versions.

  You may convey a work based on the Program, or the modifications to
produce it from the Program, in the form of source code under the
terms of section 4, provided that you also meet all of these conditions:

    a) The work must carry prominent notices stating that you modified
    it, and giving a relevant date.

    b) The work must carry prominent notices stating that it is
    released under this License and any conditions added under section
    7.  This requirement modifies the requirement in section 4 to
    "keep intact all notices".

    c) You must license the entire work, as a whole, under this
    License to anyone who comes into possession of a copy.  This
    License will therefore apply, along with any applicable section 7
    additional terms, to the whole of the work, and all its parts,
    regardless of how they are packaged.  This License gives no
    permission to license the work in any other way, but it does not
    invalidate such permission if you have separately received it.

    d) If the work has interactive user interfaces, each must display
    Appropriate Legal Notices; however, if the Program has interactive
    interfaces that do not display Appropriate Legal Notices, your
    work need not make them do so.

  A compilation of a covered work with other separate and independent
works, which are not by their nature extensions of the covered work,
and which are not combined with it such as to form a larger program,
in or on a volume of a storage or distribution medium, is called an
"aggregate" if the compilation and its resulting copyright are not
used to limit the access or legal rights of the compilation's users
beyond what the individual works permit.  Inclusion of a covered work
in an aggregate does not cause this License to apply to the other
parts of the aggregate.

  6. Conveying Non-Source Forms.

  You may convey a covered work in object code form under the terms
of sections 4 and 5, provided that you also convey the
machine-readable Corresponding Source under the terms of this License,
in one of these ways:

    a) Convey the object code in, or embodied in, a physical product
    (including a physical distribution medium), accompanied by the
    Corresponding Source fixed on a durable physical medium
    customarily used for software interchange.

    b) Convey the object code in, or embodied in, a physical product
    (including a physical distribution medium), accompanied by a
    written offer, valid for at least three years and valid for as
    long as you offer spare parts or customer support for that product
    model, to give anyone who possesses the object code either (1) a
    copy of the Corresponding Source for all the software in the
    product that is covered by this License, on a durable physical
    medium customarily used for software interchange, for a price no
    more than your reasonable cost of physically performing this
    conveying of source, or (2) access to copy the
    Corresponding Source from a network server at no charge.

    c) Convey individual copies of the object code with a copy of the
    written offer to provide the Corresponding Source.  This
    alternative is allowed only occasionally and noncommercially, and
    only if you received the object code with such an offer, in accord
    with subsection 6b.

    d) Convey the object code by offering access from a designated
    place (gratis or for a charge), and offer equivalent access to the
    Corresponding Source in the same way through the same place at no
    further charge.  You need not require recipients to copy the
    Corresponding Source along with the object code.  If the place to
    copy the object code is a network server, the Corresponding Source
    may be on a different server (operated by you or a third party)
    that supports equivalent copying facilities, provided you maintain
    clear directions next to the object code saying where to find the
    Corresponding Source.  Regardless of what server hosts the
    Corresponding Source, you remain obligated to ensure that it is
    available for as long as needed to satisfy these requirements.

    e) Convey the object code using peer-to-peer transmission, provided
    you inform other peers where the object code and Corresponding
    Source of the work are being offered to the general public at no
    charge under subsection 6d.

  A separable portion of the object code, whose source code is excluded
from the Corresponding Source as a System Library, need not be
included in conveying the object code work.

  A "User Product" is either (1) a "consumer product", which means any
tangible personal property which is normally used for personal, family,
or household purposes, or (2) anything designed or sold for incorporation
into a dwelling.  In determining whether a product is a consumer product,
doubtful cases shall be resolved in favor of coverage.  For a particular
product received by a particular user, "normally used" refers to a
typical or common use of that class of product, regardless of the status
of the particular user or of the way in which the particular user
actually uses, or expects or is expected to use, the product.  A product
is a consumer product regardless of whether the product has substantial
commercial, industrial or non-consumer uses, unless such uses represent
the only significant mode of use of the product.

  "Installation Information" for a User Product means any methods,
procedures, authorization keys, or other information required to install
and execute modified versions of a covered work in that User Product from
a modified version of its Corresponding Source.  The information must
suffice to ensure that the continued functioning of the modified object
code is in no case prevented or interfered with solely because
modification has been made.

  If you convey an object code work under this section in, or with, or
specifically for use in, a User Product, and the conveying occurs as
part of a transaction in which the right of possession and use of the
User Product is transferred to the recipient in perpetuity or for a
fixed term (regardless of how the transaction is characterized), the
Corresponding Source conveyed under this section must be accompanied
by the Installation Information.  But this requirement does not apply
if neither you nor any third party retains the ability to install
modified object code on the User Product (for example, the work has
been installed in ROM).

  The requirement to provide Installation Information does not include a
requirement to continue to provide support service, warranty, or updates
for a work that has been modified or installed by the recipient, or for
the User Product in which it has been modified or installed.  Access to a
network may be denied when the modification itself materially and
adversely affects the operation of the network or violates the rules and
protocols for communication across the network.

  Corresponding Source conveyed, and Installation Information provided,
in accord with this section must be in a format that is publicly
documented (and with an implementation available to the public in
source code form), and must require no special password or key for
unpacking, reading or copying.

  7. Additional Terms.

  "Additional permissions" are terms that supplement the terms of this
License by making exceptions from one or more of its conditions.
Additional permissions that are applicable to the entire Program shall
be treated as though they were included in this License, to the extent
that they are valid under applicable law.  If additional permissions
apply only to part of the Program, that part may be used separately
under those permissions, but the entire Program remains governed by
this License without regard to the additional permissions.

  When you convey a copy of a covered work, you may at your option
remove any additional permissions from that copy, or from any part of
it.  (Additional permissions may be written to require their own
removal in certain cases when you modify the work.)  You may place
additional permissions on material, added by you to a covered work,
for which you have or can give appropriate copyright permission.

  Notwithstanding any other provision of this License, for material you
add to a covered work, you may (if authorized by the copyright holders of
that material) supplement the terms of this License with terms:

    a) Disclaiming warranty or limiting liability differently from the
    terms of sections 15 and 16 of this License; or

    b) Requiring preservation of specified reasonable legal notices or
    author attributions in that material or in the Appropriate Legal
    Notices displayed by works containing it; or

    c) Prohibiting misrepresentation of the origin of that material, or
    requiring that modified versions of such material be marked in
    reasonable ways as different from the original version; or

    d) Limiting the use for publicity purposes of names of licensors or
    authors of the material; or

    e) Declining to grant rights under trademark law for use of some
    trade names, trademarks, or service marks; or

    f) Requiring indemnification of licensors and authors of that
    material by anyone who conveys the material (or modified versions of
    it) with contractual assumptions of liability to the recipient, for
    any liability that these contractual assumptions directly impose on
    those licensors and authors.

  All other non-permissive additional terms are considered "further
restrictions" within the meaning of section 10.  If the Program as you
received it, or any part of it, contains a notice stating that it is
governed by this License along with a term that is a further
restriction, you may remove that term.  If a license document contains
a further restriction but permits relicensing or conveying under this
License, you may add to a covered work material governed by the terms
of that license document, provided that the further restriction does
not survive such relicensing or conveying.

  If you add terms to a covered work in accord with this section, you
must place, in the relevant source files, a statement of the
additional terms that apply to those files, or a notice indicating
where to find the applicable terms.

  Additional terms, permissive or non-permissive, may be stated in the
form of a separately written license, or stated as exceptions;
the above requirements apply either way.

  8. Termination.

  You may not propagate or modify a covered work except as expressly
provided under this License.  Any attempt otherwise to propagate or
modify it is void, and will automatically terminate your rights under
this License (including any patent licenses granted under the third
paragraph of section 11).

  However, if you cease all violation of this License, then your
license from a particular copyright holder is reinstated (a)
provisionally, unless and until the copyright holder explicitly and
finally terminates your license, and (b) permanently, if the copyright
holder fails to notify you of the violation by some reasonable means
prior to 60 days after the cessation.

  Moreover, your license from a particular copyright holder is
reinstated permanently if the copyright holder notifies you of the
violation by some reasonable means, this is the first time you have
received notice of violation of this License (for any work) from that
copyright holder, and you cure the violation prior to 30 days after
your receipt of the notice.

  Termination of your rights under this section does not terminate the
licenses of parties who have received copies or rights from you under
this License.  If your rights have been terminated and not permanently
reinstated, you do not qualify to receive new licenses for the same
material under section 10.

  9. Acceptance Not Required for Having Copies.

  You are not required to accept this License in order to receive or
run a copy of the Program.  Ancillary propagation of a covered work
occurring solely as a consequence of using peer-to-peer transmission
to receive a copy likewise does not require acceptance.  However,
nothing other than this License grants you permission to propagate or
modify any covered work.  These actions infringe copyright if you do
not accept this License.  Therefore, by modifying or propagating a
covered work, you indicate your acceptance of this License to do so.

  10. Automatic Licensing of Downstream Recipients.

  Each time you convey a covered work, the recipient automatically
receives a license from the original licensors, to run, modify and
propagate that work, subject to this License.  You are not responsible
for enforcing compliance by third parties with this License.

  An "entity transaction" is a transaction transferring control of an
organization, or substantially all assets of one, or subdividing an
organization, or merging organizations.  If propagation of a covered
work results from an entity transaction, each party to that
transaction who receives a copy of the work also receives whatever
licenses to the work the party's predecessor in interest had or could
give under the previous paragraph, plus a right to possession of the
Corresponding Source of the work from the predecessor in interest, if
the predecessor has it or can get it with reasonable efforts.

  You may not impose any further restrictions on the exercise of the
rights granted or affirmed under this License.  For example, you may
not impose a license fee, royalty, or other charge for exercise of
rights granted under this License, and you may not initiate litigation
(including a cross-claim or counterclaim in a lawsuit) alleging that
any patent claim is infringed by making, using, selling, offering for
sale, or importing the Program or any portion of it.

  11. Patents.

  A "contributor" is a copyright holder who authorizes use under this
License of the Program or a work on which the Program is based.  The
work thus licensed is called the contributor's "contributor version".

  A contributor's "essential patent claims" are all patent claims
owned or controlled by the contributor, whether already acquired or
hereafter acquired, that would be infringed by some manner, permitted
by this License, of making, using, or selling its contributor version,
but do not include claims that would be infringed only as a
consequence of further modification of the contributor version.  For
purposes of this definition, "control" includes the right to grant
patent sublicenses in a manner consistent with the requirements of
this License.

  Each contributor grants you a non-exclusive, worldwide, royalty-free
patent license under the contributor's essential patent claims, to
make, use, sell, offer for sale, import and otherwise run, modify and
propagate the contents of its contributor version.

  In the following three paragraphs, a "patent license" is any express
agreement or commitment, however denominated, not to enforce a patent
(such as an express permission to practice a patent or covenant not to
sue for patent infringement).  To "grant" such a patent license to a
party means to make such an agreement or commitment not to enforce a
patent against the party.

  If you convey a covered work, knowingly relying on a patent license,
and the Corresponding Source of the work is not available for anyone
to copy, free of charge and under the terms of this License, through a
publicly available network server or other readily accessible means,
then you must either (1) cause the Corresponding Source to be so
available, or (2) arrange to deprive yourself of the benefit of the
patent license for this particular work, or (3) arrange, in a manner
consistent with the requirements of this License, to extend the patent
license to downstream recipients.  "Knowingly relying" means you have
actual knowledge that, but for the patent license, your conveying the
covered work in a country, or your recipient's use of the covered work
in a country, would infringe one or more identifiable patents in that
country that you have reason to believe are valid.

  If, pursuant to or in connection with a single transaction or
arrangement, you convey, or propagate by procuring conveyance of, a
covered work, and grant a patent license to some of the parties
receiving the covered work authorizing them to use, propagate, modify
or convey a specific copy of the covered work, then the patent license
you grant is automatically extended to all recipients of the covered
work and works based on it.

  A patent license is "discriminatory" if it does not include within
the scope of its coverage, prohibits the exercise of, or is
conditioned on the non-exercise of one or more of the rights that are
specifically granted under this License.  You may not convey a covered
work if you are a party to an arrangement with a third party that is
in the business of distributing software, under which you make payment
to the third party based on the extent of your activity of conveying
the work, and under which the third party grants, to any of the
parties who would receive the covered work from you, a discriminatory
patent license (a) in connection with copies of the covered work
conveyed by you (or copies made from those copies), or (b) primarily
for and in connection with specific products or compilations that
contain the covered work, unless you entered into that arrangement,
or that patent license was granted, prior to 28 March 2007.

  Nothing in this License shall be construed as excluding or limiting
any implied license or other defenses to infringement that may
otherwise be available to you under applicable patent law.

  12. No Surrender of Others' Freedom.

  If conditions are imposed on you (whether by court order, agreement or
otherwise) that contradict the conditions of this License, they do not
excuse you from the conditions of this License.  If you cannot convey a
covered work so as to satisfy simultaneously your obligations under this
License and any other pertinent obligations, then as a consequence you may
not convey it at all.  For example, if you agree to terms that obligate you
to collect a royalty for further conveying from those to whom you convey
the Program, the only way you could satisfy both those terms and this
License would be to refrain entirely from conveying the Program.

  13. Use with the GNU Affero General Public License.

  Notwithstanding any other provision of this License, you have
permission to link or combine any covered work with a work licensed
under version 3 of the GNU Affero General Public License into a single
combined work, and to convey the resulting work.  The terms of this
License will continue to apply to the part which is the covered work,
but the special requirements of the GNU Affero General Public License,
section 13, concerning interaction through a network will apply to the
combination as such.

  14. Revised Versions of this License.

  The Free Software Foundation may publish revised and/or new versions of
the GNU General Public License from time to time.  Such new versions will
be similar in spirit to the present version, but may differ in detail to
address new problems or concerns.

  Each version is given a distinguishing version number.  If the
Program specifies that a certain numbered version of the GNU General
Public License "or any later version" applies to it, you have the
option of following the terms and conditions either of that numbered
version or of any later version published by the Free Software
Foundation.  If the Program does not specify a version number of the
GNU General Public License, you may choose any version ever published
by the Free Software Foundation.

  If the Program specifies that a proxy can decide which future
versions of the GNU General Public License can be used, that proxy's
public statement of acceptance of a version permanently authorizes you
to choose that version for the Program.

  Later license versions may give you additional or different
permissions.  However, no additional obligations are imposed on any
author or copyright holder as a result of your choosing to follow a
later version.

  15. Disclaimer of Warranty.

  THERE IS NO WARRANTY FOR THE PROGRAM, TO THE EXTENT PERMITTED BY
APPLICABLE LAW.  EXCEPT WHEN OTHERWISE STATED IN WRITING THE COPYRIGHT
HOLDERS AND/OR OTHER PARTIES PROVIDE THE PROGRAM "AS IS" WITHOUT WARRANTY
OF ANY KIND, EITHER EXPRESSED OR IMPLIED, INCLUDING, BUT NOT LIMITED TO,
THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR
PURPOSE.  THE ENTIRE RISK AS TO THE QUALITY AND PERFORMANCE OF THE PROGRAM
IS WITH YOU.  SHOULD THE PROGRAM PROVE DEFECTIVE, YOU ASSUME THE COST OF
ALL NECESSARY SERVICING, REPAIR OR CORRECTION.

  16. Limitation of Liability.

  IN NO EVENT UNLESS REQUIRED BY APPLICABLE LAW OR AGREED TO IN WRITING
WILL ANY COPYRIGHT HOLDER, OR ANY OTHER PARTY WHO MODIFIES AND/OR CONVEYS
THE PROGRAM AS PERMITTED ABOVE, BE LIABLE TO YOU FOR DAMAGES, INCLUDING ANY
GENERAL, SPECIAL, INCIDENTAL OR CONSEQUENTIAL DAMAGES ARISING OUT OF THE
USE OR INABILITY TO USE THE PROGRAM (INCLUDING BUT NOT LIMITED TO LOSS OF
DATA OR DATA BEING RENDERED INACCURATE OR LOSSES SUSTAINED BY YOU OR THIRD
PARTIES OR A FAILURE OF THE PROGRAM TO OPERATE WITH ANY OTHER PROGRAMS),
EVEN IF SUCH HOLDER OR OTHER PARTY HAS BEEN ADVISED OF THE POSSIBILITY OF
SUCH DAMAGES.

  17. Interpretation of Sections 15 and 16.

  If the disclaimer of warranty and limitation of liability provided
above cannot be given local legal effect according to their terms,
reviewing courts shall apply local law that most closely approximates
an absolute waiver of all civil liability in connection with the
Program, unless a warranty or assumption of liability accompanies a
copy of the Program in return for a fee.

                     END OF TERMS AND CONDITIONS

            How to Apply These Terms to Your New Programs

  If you develop a new program, and you want it to be of the greatest
possible use to the public, the best way to achieve this is to make it
free software which everyone can redistribute and change under these terms.

  To do so, attach the following notices to the program.  It is safest
to attach them to the start of each source file to most effectively
state the exclusion of warranty; and each file should have at least
the "copyright" line and a pointer to where the full notice is found.

    <one line to give the program's name and a brief idea of what it does.>
    Copyright (C) <year>  <name of author>

    This program is free software: you can redistribute it and/or modify
    it under the terms of the GNU General Public License as published by
    the Free Software Foundation, either version 3 of the License, or
    (at your option) any later version.

    This program is distributed in the hope that it will be useful,
    but WITHOUT ANY WARRANTY; without even the implied warranty of
    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
    GNU General Public License for more details.

    You should have received a copy of the GNU General Public License
    along with this program.  If not, see <https://www.gnu.org/licenses/>.

Also add information on how to contact you by electronic and paper mail.

  If the program does terminal interaction, make it output a short
notice like this when it starts in an interactive mode:

    <program>  Copyright (C) <year>  <name of author>
    This program comes with ABSOLUTELY NO WARRANTY; for details type `show w'.
    This is free software, and you are welcome to redistribute it
    under certain conditions; type `show c' for details.

The hypothetical commands `show w' and `show c' should show the appropriate
parts of the General Public License.  Of course, your program's commands
might be different; for a GUI interface, you would use an "about box".

  You should also get your employer (if you work as a programmer) or school,
if any, to sign a "copyright disclaimer" for the program, if necessary.
For more information on this, and how to apply and follow the GNU GPL, see
<https://www.gnu.org/licenses/>.

  The GNU General Public License does not permit incorporating your program
into proprietary programs.  If your program is a subroutine library, you
may consider it more useful to permit linking proprietary applications with
the library.  If this is what you want to do, use the GNU Lesser General
Public License instead of this License.  But first, please read
<https://www.gnu.org/licenses/why-not-lgpl.html>.
```

### N4 — GCC Runtime Library Exception 3.1；GCC 官方镜像 releases/gcc-10.2.0/COPYING.RUNTIME

原文 SHA256：`9d6b43ce4d8de0c878bf16b54d8e7a10d9bd42b75178153e3af6a815bdc90f74`。

```text
GCC RUNTIME LIBRARY EXCEPTION

Version 3.1, 31 March 2009

Copyright (C) 2009 Free Software Foundation, Inc. <http://fsf.org/>

Everyone is permitted to copy and distribute verbatim copies of this
license document, but changing it is not allowed.

This GCC Runtime Library Exception ("Exception") is an additional
permission under section 7 of the GNU General Public License, version
3 ("GPLv3"). It applies to a given file (the "Runtime Library") that
bears a notice placed by the copyright holder of the file stating that
the file is governed by GPLv3 along with this Exception.

When you use GCC to compile a program, GCC may combine portions of
certain GCC header files and runtime libraries with the compiled
program. The purpose of this Exception is to allow compilation of
non-GPL (including proprietary) programs to use, in this way, the
header files and runtime libraries covered by this Exception.

0. Definitions.

A file is an "Independent Module" if it either requires the Runtime
Library for execution after a Compilation Process, or makes use of an
interface provided by the Runtime Library, but is not otherwise based
on the Runtime Library.

"GCC" means a version of the GNU Compiler Collection, with or without
modifications, governed by version 3 (or a specified later version) of
the GNU General Public License (GPL) with the option of using any
subsequent versions published by the FSF.

"GPL-compatible Software" is software whose conditions of propagation,
modification and use would permit combination with GCC in accord with
the license of GCC.

"Target Code" refers to output from any compiler for a real or virtual
target processor architecture, in executable form or suitable for
input to an assembler, loader, linker and/or execution
phase. Notwithstanding that, Target Code does not include data in any
format that is used as a compiler intermediate representation, or used
for producing a compiler intermediate representation.

The "Compilation Process" transforms code entirely represented in
non-intermediate languages designed for human-written code, and/or in
Java Virtual Machine byte code, into Target Code. Thus, for example,
use of source code generators and preprocessors need not be considered
part of the Compilation Process, since the Compilation Process can be
understood as starting with the output of the generators or
preprocessors.

A Compilation Process is "Eligible" if it is done using GCC, alone or
with other GPL-compatible software, or if it is done without using any
work based on GCC. For example, using non-GPL-compatible Software to
optimize any GCC intermediate representations would not qualify as an
Eligible Compilation Process.

1. Grant of Additional Permission.

You have permission to propagate a work of Target Code formed by
combining the Runtime Library with Independent Modules, even if such
propagation would otherwise violate the terms of GPLv3, provided that
all Target Code was generated by Eligible Compilation Processes. You
may then convey such a combination under terms of your choice,
consistent with the licensing of the Independent Modules.

2. No Weakening of GCC Copyleft.

The availability of this Exception does not imply any general
presumption that third-party software is unaffected by the copyleft
requirements of the license of GCC.
```

### N5 — PSL 固定数据头；publicsuffix/list@2c960dac3d39ba521eb5db9da192968f5be0aded

原文 SHA256：`2f2171a1b36f6f6a772ac6995dced4b9a6e16aa4938e72c46af1d6a4282db337`。

```text
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Please pull this list from, and only from https://publicsuffix.org/list/public_suffix_list.dat,
// rather than any other VCS sites. Pulling from any other URL is not guaranteed to be supported.

// Instructions on pulling and using this list can be found at https://publicsuffix.org/list/.
```

### N5 — MPL 2.0 全文；https://www.mozilla.org/MPL/2.0/

原文 SHA256：`fab3dd6bdab226f1c08630b1dd917e11fcb4ec5e1e020e2c16f83a0a13863e85`。

```text
Mozilla Public License Version 2.0
==================================

1. Definitions
--------------

1.1. "Contributor"
    means each individual or legal entity that creates, contributes to
    the creation of, or owns Covered Software.

1.2. "Contributor Version"
    means the combination of the Contributions of others (if any) used
    by a Contributor and that particular Contributor's Contribution.

1.3. "Contribution"
    means Covered Software of a particular Contributor.

1.4. "Covered Software"
    means Source Code Form to which the initial Contributor has attached
    the notice in Exhibit A, the Executable Form of such Source Code
    Form, and Modifications of such Source Code Form, in each case
    including portions thereof.

1.5. "Incompatible With Secondary Licenses"
    means

    (a) that the initial Contributor has attached the notice described
        in Exhibit B to the Covered Software; or

    (b) that the Covered Software was made available under the terms of
        version 1.1 or earlier of the License, but not also under the
        terms of a Secondary License.

1.6. "Executable Form"
    means any form of the work other than Source Code Form.

1.7. "Larger Work"
    means a work that combines Covered Software with other material, in
    a separate file or files, that is not Covered Software.

1.8. "License"
    means this document.

1.9. "Licensable"
    means having the right to grant, to the maximum extent possible,
    whether at the time of the initial grant or subsequently, any and
    all of the rights conveyed by this License.

1.10. "Modifications"
    means any of the following:

    (a) any file in Source Code Form that results from an addition to,
        deletion from, or modification of the contents of Covered
        Software; or

    (b) any new file in Source Code Form that contains any Covered
        Software.

1.11. "Patent Claims" of a Contributor
    means any patent claim(s), including without limitation, method,
    process, and apparatus claims, in any patent Licensable by such
    Contributor that would be infringed, but for the grant of the
    License, by the making, using, selling, offering for sale, having
    made, import, or transfer of either its Contributions or its
    Contributor Version.

1.12. "Secondary License"
    means either the GNU General Public License, Version 2.0, the GNU
    Lesser General Public License, Version 2.1, the GNU Affero General
    Public License, Version 3.0, or any later versions of those
    licenses.

1.13. "Source Code Form"
    means the form of the work preferred for making modifications.

1.14. "You" (or "Your")
    means an individual or a legal entity exercising rights under this
    License. For legal entities, "You" includes any entity that
    controls, is controlled by, or is under common control with You. For
    purposes of this definition, "control" means (a) the power, direct
    or indirect, to cause the direction or management of such entity,
    whether by contract or otherwise, or (b) ownership of more than
    fifty percent (50%) of the outstanding shares or beneficial
    ownership of such entity.

2. License Grants and Conditions
--------------------------------

2.1. Grants

Each Contributor hereby grants You a world-wide, royalty-free,
non-exclusive license:

(a) under intellectual property rights (other than patent or trademark)
    Licensable by such Contributor to use, reproduce, make available,
    modify, display, perform, distribute, and otherwise exploit its
    Contributions, either on an unmodified basis, with Modifications, or
    as part of a Larger Work; and

(b) under Patent Claims of such Contributor to make, use, sell, offer
    for sale, have made, import, and otherwise transfer either its
    Contributions or its Contributor Version.

2.2. Effective Date

The licenses granted in Section 2.1 with respect to any Contribution
become effective for each Contribution on the date the Contributor first
distributes such Contribution.

2.3. Limitations on Grant Scope

The licenses granted in this Section 2 are the only rights granted under
this License. No additional rights or licenses will be implied from the
distribution or licensing of Covered Software under this License.
Notwithstanding Section 2.1(b) above, no patent license is granted by a
Contributor:

(a) for any code that a Contributor has removed from Covered Software;
    or

(b) for infringements caused by: (i) Your and any other third party's
    modifications of Covered Software, or (ii) the combination of its
    Contributions with other software (except as part of its Contributor
    Version); or

(c) under Patent Claims infringed by Covered Software in the absence of
    its Contributions.

This License does not grant any rights in the trademarks, service marks,
or logos of any Contributor (except as may be necessary to comply with
the notice requirements in Section 3.4).

2.4. Subsequent Licenses

No Contributor makes additional grants as a result of Your choice to
distribute the Covered Software under a subsequent version of this
License (see Section 10.2) or under the terms of a Secondary License (if
permitted under the terms of Section 3.3).

2.5. Representation

Each Contributor represents that the Contributor believes its
Contributions are its original creation(s) or it has sufficient rights
to grant the rights to its Contributions conveyed by this License.

2.6. Fair Use

This License is not intended to limit any rights You have under
applicable copyright doctrines of fair use, fair dealing, or other
equivalents.

2.7. Conditions

Sections 3.1, 3.2, 3.3, and 3.4 are conditions of the licenses granted
in Section 2.1.

3. Responsibilities
-------------------

3.1. Distribution of Source Form

All distribution of Covered Software in Source Code Form, including any
Modifications that You create or to which You contribute, must be under
the terms of this License. You must inform recipients that the Source
Code Form of the Covered Software is governed by the terms of this
License, and how they can obtain a copy of this License. You may not
attempt to alter or restrict the recipients' rights in the Source Code
Form.

3.2. Distribution of Executable Form

If You distribute Covered Software in Executable Form then:

(a) such Covered Software must also be made available in Source Code
    Form, as described in Section 3.1, and You must inform recipients of
    the Executable Form how they can obtain a copy of such Source Code
    Form by reasonable means in a timely manner, at a charge no more
    than the cost of distribution to the recipient; and

(b) You may distribute such Executable Form under the terms of this
    License, or sublicense it under different terms, provided that the
    license for the Executable Form does not attempt to limit or alter
    the recipients' rights in the Source Code Form under this License.

3.3. Distribution of a Larger Work

You may create and distribute a Larger Work under terms of Your choice,
provided that You also comply with the requirements of this License for
the Covered Software. If the Larger Work is a combination of Covered
Software with a work governed by one or more Secondary Licenses, and the
Covered Software is not Incompatible With Secondary Licenses, this
License permits You to additionally distribute such Covered Software
under the terms of such Secondary License(s), so that the recipient of
the Larger Work may, at their option, further distribute the Covered
Software under the terms of either this License or such Secondary
License(s).

3.4. Notices

You may not remove or alter the substance of any license notices
(including copyright notices, patent notices, disclaimers of warranty,
or limitations of liability) contained within the Source Code Form of
the Covered Software, except that You may alter any license notices to
the extent required to remedy known factual inaccuracies.

3.5. Application of Additional Terms

You may choose to offer, and to charge a fee for, warranty, support,
indemnity or liability obligations to one or more recipients of Covered
Software. However, You may do so only on Your own behalf, and not on
behalf of any Contributor. You must make it absolutely clear that any
such warranty, support, indemnity, or liability obligation is offered by
You alone, and You hereby agree to indemnify every Contributor for any
liability incurred by such Contributor as a result of warranty, support,
indemnity or liability terms You offer. You may include additional
disclaimers of warranty and limitations of liability specific to any
jurisdiction.

4. Inability to Comply Due to Statute or Regulation
---------------------------------------------------

If it is impossible for You to comply with any of the terms of this
License with respect to some or all of the Covered Software due to
statute, judicial order, or regulation then You must: (a) comply with
the terms of this License to the maximum extent possible; and (b)
describe the limitations and the code they affect. Such description must
be placed in a text file included with all distributions of the Covered
Software under this License. Except to the extent prohibited by statute
or regulation, such description must be sufficiently detailed for a
recipient of ordinary skill to be able to understand it.

5. Termination
--------------

5.1. The rights granted under this License will terminate automatically
if You fail to comply with any of its terms. However, if You become
compliant, then the rights granted under this License from a particular
Contributor are reinstated (a) provisionally, unless and until such
Contributor explicitly and finally terminates Your grants, and (b) on an
ongoing basis, if such Contributor fails to notify You of the
non-compliance by some reasonable means prior to 60 days after You have
come back into compliance. Moreover, Your grants from a particular
Contributor are reinstated on an ongoing basis if such Contributor
notifies You of the non-compliance by some reasonable means, this is the
first time You have received notice of non-compliance with this License
from such Contributor, and You become compliant prior to 30 days after
Your receipt of the notice.

5.2. If You initiate litigation against any entity by asserting a patent
infringement claim (excluding declaratory judgment actions,
counter-claims, and cross-claims) alleging that a Contributor Version
directly or indirectly infringes any patent, then the rights granted to
You by any and all Contributors for the Covered Software under Section
2.1 of this License shall terminate.

5.3. In the event of termination under Sections 5.1 or 5.2 above, all
end user license agreements (excluding distributors and resellers) which
have been validly granted by You or Your distributors under this License
prior to termination shall survive termination.

************************************************************************
*                                                                      *
*  6. Disclaimer of Warranty                                           *
*  -------------------------                                           *
*                                                                      *
*  Covered Software is provided under this License on an "as is"       *
*  basis, without warranty of any kind, either expressed, implied, or  *
*  statutory, including, without limitation, warranties that the       *
*  Covered Software is free of defects, merchantable, fit for a        *
*  particular purpose or non-infringing. The entire risk as to the     *
*  quality and performance of the Covered Software is with You.        *
*  Should any Covered Software prove defective in any respect, You     *
*  (not any Contributor) assume the cost of any necessary servicing,   *
*  repair, or correction. This disclaimer of warranty constitutes an   *
*  essential part of this License. No use of any Covered Software is   *
*  authorized under this License except under this disclaimer.         *
*                                                                      *
************************************************************************

************************************************************************
*                                                                      *
*  7. Limitation of Liability                                          *
*  --------------------------                                          *
*                                                                      *
*  Under no circumstances and under no legal theory, whether tort      *
*  (including negligence), contract, or otherwise, shall any           *
*  Contributor, or anyone who distributes Covered Software as          *
*  permitted above, be liable to You for any direct, indirect,         *
*  special, incidental, or consequential damages of any character      *
*  including, without limitation, damages for lost profits, loss of    *
*  goodwill, work stoppage, computer failure or malfunction, or any    *
*  and all other commercial damages or losses, even if such party      *
*  shall have been informed of the possibility of such damages. This   *
*  limitation of liability shall not apply to liability for death or   *
*  personal injury resulting from such party's negligence to the       *
*  extent applicable law prohibits such limitation. Some               *
*  jurisdictions do not allow the exclusion or limitation of           *
*  incidental or consequential damages, so this exclusion and          *
*  limitation may not apply to You.                                    *
*                                                                      *
************************************************************************

8. Litigation
-------------

Any litigation relating to this License may be brought only in the
courts of a jurisdiction where the defendant maintains its principal
place of business and such litigation shall be governed by laws of that
jurisdiction, without reference to its conflict-of-law provisions.
Nothing in this Section shall prevent a party's ability to bring
cross-claims or counter-claims.

9. Miscellaneous
----------------

This License represents the complete agreement concerning the subject
matter hereof. If any provision of this License is held to be
unenforceable, such provision shall be reformed only to the extent
necessary to make it enforceable. Any law or regulation which provides
that the language of a contract shall be construed against the drafter
shall not be used to construe this License against a Contributor.

10. Versions of the License
---------------------------

10.1. New Versions

Mozilla Foundation is the license steward. Except as provided in Section
10.3, no one other than the license steward has the right to modify or
publish new versions of this License. Each version will be given a
distinguishing version number.

10.2. Effect of New Versions

You may distribute the Covered Software under the terms of the version
of the License under which You originally received the Covered Software,
or under the terms of any subsequent version published by the license
steward.

10.3. Modified Versions

If you create software not governed by this License, and you want to
create a new license for such software, you may create and use a
modified version of this License if you rename the license and remove
any references to the name of the license steward (except to note that
such modified license differs from this License).

10.4. Distributing Source Code Form that is Incompatible With Secondary
Licenses

If You choose to distribute Source Code Form that is Incompatible With
Secondary Licenses under the terms of this version of the License, the
notice described in Exhibit B of this License must be attached.

Exhibit A - Source Code Form License Notice
-------------------------------------------

  This Source Code Form is subject to the terms of the Mozilla Public
  License, v. 2.0. If a copy of the MPL was not distributed with this
  file, You can obtain one at http://mozilla.org/MPL/2.0/.

If it is not possible or desirable to put the notice in a particular
file, then You may include the notice in a location (such as a LICENSE
file in a relevant directory) where a recipient would be likely to look
for such a notice.

You may add additional accurate notices of copyright ownership.

Exhibit B - "Incompatible With Secondary Licenses" Notice
---------------------------------------------------------

  This Source Code Form is "Incompatible With Secondary Licenses", as
  defined by the Mozilla Public License, v. 2.0.
```

### N6 — ntgo v0.8.0 / LICENSE

原文 SHA256：`fa92e73eadb684fd08a7b6d7cfa995129acbb42b0f286a134f0f4c607a9db002`。

```text
The MIT License (MIT)

Copyright (c) 2018 Miguel Angel Rivera Notararigo

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

## 附录 G：Go 工具链声明原文

### G1 — Go 1.26.8 / LICENSE

原文 SHA256：`911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad`。

```text
Copyright 2009 The Go Authors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google LLC nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

### G1 — Go 1.26.8 / PATENTS

原文 SHA256：`96f408bfae65bf137fc2525d3ecb030271c50c1e90799f87abf8846d8dd505cc`。

```text
Additional IP Rights Grant (Patents)

"This implementation" means the copyrightable works distributed by
Google as part of the Go project.

Google hereby grants to You a perpetual, worldwide, non-exclusive,
no-charge, royalty-free, irrevocable (except as stated in this section)
patent license to make, have made, use, offer to sell, sell, import,
transfer and otherwise run, modify and propagate the contents of this
implementation of Go, where such license applies only to those patent
claims, both currently owned or controlled by Google and acquired in
the future, licensable by Google that are necessarily infringed by this
implementation of Go.  This grant does not include claims that would be
infringed only as a consequence of further modification of this
implementation.  If you or your agent or exclusive licensee institute or
order or agree to the institution of patent litigation against any
entity (including a cross-claim or counterclaim in a lawsuit) alleging
that this implementation of Go or any code incorporated within this
implementation of Go constitutes direct or contributory patent
infringement, or inducement of patent infringement, then any patent
rights granted to you under this License for this implementation of Go
shall terminate as of the date such litigation is filed.
```

### G2 — Go 1.26.8 / src/vendor/golang.org/x/crypto/LICENSE

原文 SHA256：`911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad`。

```text
Copyright 2009 The Go Authors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google LLC nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

### G2 — Go 1.26.8 / src/vendor/golang.org/x/crypto/PATENTS

原文 SHA256：`96f408bfae65bf137fc2525d3ecb030271c50c1e90799f87abf8846d8dd505cc`。

```text
Additional IP Rights Grant (Patents)

"This implementation" means the copyrightable works distributed by
Google as part of the Go project.

Google hereby grants to You a perpetual, worldwide, non-exclusive,
no-charge, royalty-free, irrevocable (except as stated in this section)
patent license to make, have made, use, offer to sell, sell, import,
transfer and otherwise run, modify and propagate the contents of this
implementation of Go, where such license applies only to those patent
claims, both currently owned or controlled by Google and acquired in
the future, licensable by Google that are necessarily infringed by this
implementation of Go.  This grant does not include claims that would be
infringed only as a consequence of further modification of this
implementation.  If you or your agent or exclusive licensee institute or
order or agree to the institution of patent litigation against any
entity (including a cross-claim or counterclaim in a lawsuit) alleging
that this implementation of Go or any code incorporated within this
implementation of Go constitutes direct or contributory patent
infringement, or inducement of patent infringement, then any patent
rights granted to you under this License for this implementation of Go
shall terminate as of the date such litigation is filed.
```

### G2 — Go 1.26.8 / src/vendor/golang.org/x/net/LICENSE

原文 SHA256：`911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad`。

```text
Copyright 2009 The Go Authors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google LLC nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

### G2 — Go 1.26.8 / src/vendor/golang.org/x/net/PATENTS

原文 SHA256：`96f408bfae65bf137fc2525d3ecb030271c50c1e90799f87abf8846d8dd505cc`。

```text
Additional IP Rights Grant (Patents)

"This implementation" means the copyrightable works distributed by
Google as part of the Go project.

Google hereby grants to You a perpetual, worldwide, non-exclusive,
no-charge, royalty-free, irrevocable (except as stated in this section)
patent license to make, have made, use, offer to sell, sell, import,
transfer and otherwise run, modify and propagate the contents of this
implementation of Go, where such license applies only to those patent
claims, both currently owned or controlled by Google and acquired in
the future, licensable by Google that are necessarily infringed by this
implementation of Go.  This grant does not include claims that would be
infringed only as a consequence of further modification of this
implementation.  If you or your agent or exclusive licensee institute or
order or agree to the institution of patent litigation against any
entity (including a cross-claim or counterclaim in a lawsuit) alleging
that this implementation of Go or any code incorporated within this
implementation of Go constitutes direct or contributory patent
infringement, or inducement of patent infringement, then any patent
rights granted to you under this License for this implementation of Go
shall terminate as of the date such litigation is filed.
```

### G2 — Go 1.26.8 / src/vendor/golang.org/x/sys/LICENSE

原文 SHA256：`911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad`。

```text
Copyright 2009 The Go Authors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google LLC nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

### G2 — Go 1.26.8 / src/vendor/golang.org/x/sys/PATENTS

原文 SHA256：`96f408bfae65bf137fc2525d3ecb030271c50c1e90799f87abf8846d8dd505cc`。

```text
Additional IP Rights Grant (Patents)

"This implementation" means the copyrightable works distributed by
Google as part of the Go project.

Google hereby grants to You a perpetual, worldwide, non-exclusive,
no-charge, royalty-free, irrevocable (except as stated in this section)
patent license to make, have made, use, offer to sell, sell, import,
transfer and otherwise run, modify and propagate the contents of this
implementation of Go, where such license applies only to those patent
claims, both currently owned or controlled by Google and acquired in
the future, licensable by Google that are necessarily infringed by this
implementation of Go.  This grant does not include claims that would be
infringed only as a consequence of further modification of this
implementation.  If you or your agent or exclusive licensee institute or
order or agree to the institution of patent litigation against any
entity (including a cross-claim or counterclaim in a lawsuit) alleging
that this implementation of Go or any code incorporated within this
implementation of Go constitutes direct or contributory patent
infringement, or inducement of patent infringement, then any patent
rights granted to you under this License for this implementation of Go
shall terminate as of the date such litigation is filed.
```

### G2 — Go 1.26.8 / src/vendor/golang.org/x/text/LICENSE

原文 SHA256：`911f8f5782931320f5b8d1160a76365b83aea6447ee6c04fa6d5591467db9dad`。

```text
Copyright 2009 The Go Authors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google LLC nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

### G2 — Go 1.26.8 / src/vendor/golang.org/x/text/PATENTS

原文 SHA256：`96f408bfae65bf137fc2525d3ecb030271c50c1e90799f87abf8846d8dd505cc`。

```text
Additional IP Rights Grant (Patents)

"This implementation" means the copyrightable works distributed by
Google as part of the Go project.

Google hereby grants to You a perpetual, worldwide, non-exclusive,
no-charge, royalty-free, irrevocable (except as stated in this section)
patent license to make, have made, use, offer to sell, sell, import,
transfer and otherwise run, modify and propagate the contents of this
implementation of Go, where such license applies only to those patent
claims, both currently owned or controlled by Google and acquired in
the future, licensable by Google that are necessarily infringed by this
implementation of Go.  This grant does not include claims that would be
infringed only as a consequence of further modification of this
implementation.  If you or your agent or exclusive licensee institute or
order or agree to the institution of patent litigation against any
entity (including a cross-claim or counterclaim in a lawsuit) alleging
that this implementation of Go or any code incorporated within this
implementation of Go constitutes direct or contributory patent
infringement, or inducement of patent infringement, then any patent
rights granted to you under this License for this implementation of Go
shall terminate as of the date such litigation is filed.
```

### G3 — Inferno / MIT；Go 1.26.8 / src/runtime/memmove_amd64.s

原文 SHA256：`115b35d4c33d49499560fed05811e006e92db5c1416c6ac0f417753b416d5449`。

```text
// Derived from Inferno's libkern/memmove-386.s (adapted for amd64)
// https://bitbucket.org/inferno-os/inferno-os/src/master/libkern/memmove-386.s
//
//         Copyright © 1994-1999 Lucent Technologies Inc. All rights reserved.
//         Revisions Copyright © 2000-2007 Vita Nuova Holdings Limited (www.vitanuova.com).  All rights reserved.
//         Portions Copyright 2009 The Go Authors. All rights reserved.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.  IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.
```

### G3 — Sun Microsystems 1993；Go 1.26.8 / src/math/acosh.go

原文 SHA256：`3920a5752fad075a9a1e44c69fa0204918bfc2c3ad6c3ed0a285273befd879e0`。

```text
// The original C code, the long comment, and the constants
// below are from FreeBSD's /usr/src/lib/msun/src/e_acosh.c
// and came with this notice. The go code is a simplified
// version of the original C.
//
// ====================================================
// Copyright (C) 1993 by Sun Microsystems, Inc. All rights reserved.
//
// Developed at SunPro, a Sun Microsystems, Inc. business.
// Permission to use, copy, modify, and distribute this
// software is freely granted, provided that this notice
// is preserved.
// ====================================================
```

### G3 — Sun Microsystems 2004；Go 1.26.8 / src/math/exp.go

原文 SHA256：`739ce2959b1f2e8c345f4a18c50f6f23fb2809f7a6681cf0d5a87aa679c2e26f`。

```text
// The original C code, the long comment, and the constants
// below are from FreeBSD's /usr/src/lib/msun/src/e_exp.c
// and came with this notice. The go code is a simplified
// version of the original C.
//
// ====================================================
// Copyright (C) 2004 by Sun Microsystems, Inc. All rights reserved.
//
// Permission to use, copy, modify, and distribute this
// software is freely granted, provided that this notice
// is preserved.
// ====================================================
//
//
```

### G3 — Cephes / Stephen L. Moshier；Go 1.26.8 / src/math/atan.go

原文 SHA256：`e687d1a401030d236cb2770e8d9f10ddd85cfaf50dc0cd6f53d9e4b93aa2173b`。

```text
//    DEC       -10, 10   50000     2.4e-17  8.3e-18
//    IEEE      -10, 10   10^6      1.8e-16  5.0e-17
//
// Cephes Math Library Release 2.8:  June, 2000
// Copyright 1984, 1987, 1989, 1992, 2000 by Stephen L. Moshier
//
// The readme file at http://netlib.sandia.gov/cephes/ says:
//    Some software in this archive may be from the book _Methods and
// Programs for Mathematical Functions_ (Prentice-Hall or Simon & Schuster
// International, 1989) or from the Cephes Mathematical Library, a
// commercial product. In either event, it is copyrighted by the author.
// What you see here may be used freely but it comes with no support or
// guarantee.
```

### G3 — Naoki Shibata 早期实现；Go 1.26.8 / src/math/exp_amd64.s

原文 SHA256：`39560d76c96a55aa9694eb537a24eccb1d2d8f6b338c5498d5a09bd06e8fba8b`。

```text
// The method is based on a paper by Naoki Shibata: "Efficient evaluation
// methods of elementary functions suitable for SIMD computation", Proc.
// of International Supercomputing Conference 2010 (ISC'10), pp. 25 -- 32
// (May 2010). The paper is available at
// https://link.springer.com/article/10.1007/s00450-010-0108-2
//
// The original code and the constants below are from the author's
// implementation available at http://freshmeat.net/projects/sleef.
// The README file says, "The software is in public domain.
// You can use the software without any obligation."
//
// This code is a simplified version of the original.
```

### G3 — FreeBSD ELF / BSD-2-Clause；Go 1.26.8 / src/debug/elf/elf.go

原文 SHA256：`05f5cf488584cf7c3df139be6bea283691166366c511d29940602f295975448f`。

```text
/*
 * ELF constants and data structures
 *
 * Derived from:
 * $FreeBSD: src/sys/sys/elf32.h,v 1.8.14.1 2005/12/30 22:13:58 marcel Exp $
 * $FreeBSD: src/sys/sys/elf64.h,v 1.10.14.1 2005/12/30 22:13:58 marcel Exp $
 * $FreeBSD: src/sys/sys/elf_common.h,v 1.15.8.1 2005/12/30 22:13:58 marcel Exp $
 * $FreeBSD: src/sys/alpha/include/elf.h,v 1.14 2003/09/25 01:10:22 peter Exp $
 * $FreeBSD: src/sys/amd64/include/elf.h,v 1.18 2004/08/03 08:21:48 dfr Exp $
 * $FreeBSD: src/sys/arm/include/elf.h,v 1.5.2.1 2006/06/30 21:42:52 cognet Exp $
 * $FreeBSD: src/sys/i386/include/elf.h,v 1.16 2004/08/02 19:12:17 dfr Exp $
 * $FreeBSD: src/sys/powerpc/include/elf.h,v 1.7 2004/11/02 09:47:01 ssouhlal Exp $
 * $FreeBSD: src/sys/sparc64/include/elf.h,v 1.12 2003/09/25 01:10:26 peter Exp $
 * "System V ABI" (http://www.sco.com/developers/gabi/latest/ch4.eheader.html)
 * "ELF for the ARM® 64-bit Architecture (AArch64)" (ARM IHI 0056B)
 * "RISC-V ELF psABI specification" (https://github.com/riscv-non-isa/riscv-elf-psabi-doc/blob/master/riscv-elf.adoc)
 * llvm/BinaryFormat/ELF.h - ELF constants and structures
 *
 * Copyright (c) 1996-1998 John D. Polstra.  All rights reserved.
 * Copyright (c) 2001 David E. O'Brien
 * Portions Copyright 2009 The Go Authors. All rights reserved.
 *
 * Redistribution and use in source and binary forms, with or without
 * modification, are permitted provided that the following conditions
 * are met:
 * 1. Redistributions of source code must retain the above copyright
 *    notice, this list of conditions and the following disclaimer.
 * 2. Redistributions in binary form must reproduce the above copyright
 *    notice, this list of conditions and the following disclaimer in the
 *    documentation and/or other materials provided with the distribution.
 *
 * THIS SOFTWARE IS PROVIDED BY THE AUTHOR AND CONTRIBUTORS ``AS IS'' AND
 * ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
 * IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE
 * ARE DISCLAIMED.  IN NO EVENT SHALL THE AUTHOR OR CONTRIBUTORS BE LIABLE
 * FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
 * DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS
 * OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION)
 * HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT
 * LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY
 * OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF
 * SUCH DAMAGE.
 */
```

### G3 — fiat-crypto / BSD-1-Clause；Go 1.26.8 / src/crypto/internal/fips140/edwards25519/scalar.go

原文 SHA256：`d63eafda50d8ef034c574babbd3348a4f8bb6995d692a56b2fee6fca0f15f10b`。

```text
// The field implementation in scalar_fiat.go is generated by the fiat-crypto
// project (https://github.com/mit-plv/fiat-crypto) at version v0.0.9 (23d2dbc)
// from a formally verified model.
//
// fiat-crypto code comes under the following license.
//
//     Copyright (c) 2015-2020 The fiat-crypto Authors. All rights reserved.
//
//     Redistribution and use in source and binary forms, with or without
//     modification, are permitted provided that the following conditions are
//     met:
//
//         1. Redistributions of source code must retain the above copyright
//         notice, this list of conditions and the following disclaimer.
//
//     THIS SOFTWARE IS PROVIDED BY the fiat-crypto authors "AS IS"
//     AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO,
//     THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR
//     PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL Berkeley Software Design,
//     Inc. BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL,
//     EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO,
//     PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR
//     PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF
//     LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING
//     NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS
//     SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
//
```

### G3 — Rijndael 3.0 / 公有领域；Go 1.26.8 / src/crypto/internal/fips140/aes/aes_generic.go

原文 SHA256：`ccbab5251f8bbb2e3b33ca98089e0314c018138e48855f2d9345e518fd63e610`。

```text
// This Go implementation is derived in part from the reference
// ANSI C implementation, which carries the following notice:
//
//	rijndael-alg-fst.c
//
//	@version 3.0 (December 2000)
//
//	Optimised ANSI C code for the Rijndael cipher (now AES)
//
//	@author Vincent Rijmen <vincent.rijmen@esat.kuleuven.ac.be>
//	@author Antoon Bosselaers <antoon.bosselaers@esat.kuleuven.ac.be>
//	@author Paulo Barreto <paulo.barreto@terra.com.br>
//
//	This code is hereby placed in the public domain.
//
//	THIS SOFTWARE IS PROVIDED BY THE AUTHORS ''AS IS'' AND ANY EXPRESS
//	OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED
//	WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE
//	ARE DISCLAIMED.  IN NO EVENT SHALL THE AUTHORS OR CONTRIBUTORS BE
//	LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR
//	CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF
//	SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR
//	BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY,
//	WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE
//	OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE,
//	EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
//
// See FIPS 197 for specification, and see Daemen and Rijmen's Rijndael submission
// for implementation details.
//	https://csrc.nist.gov/csrc/media/publications/fips/197/final/documents/fips-197.pdf
//	https://csrc.nist.gov/archive/aes/rijndael/Rijndael-ammended.pdf

package aes
```

### G4 — Unicode 官方数据许可资料

数据版本从工具链源码核实为 15.0.0。以下分别为固定版本 ReadMe 和本次取得的现行官方 license.txt；不将现行文档的年份当成 15.0.0 的发布日期。历史授权链仍列 Q7。

### G4 — https://www.unicode.org/Public/15.0.0/ucd/ReadMe.txt

原文 SHA256：`53672c0d0b5185e3cf04c8e970d544c3af81ae7c8eeba0b9cf6d355aa954ae1f`。

```text
# Unicode Character Database
# Date: 2022-09-02
# © 2022 Unicode®, Inc.
# Unicode and the Unicode Logo are registered trademarks of Unicode, Inc. in the U.S. and other countries.
# For terms of use, see https://www.unicode.org/terms_of_use.html
#
# For documentation, see the following:
# NamesList.html
# UAX #38, "Unicode Han Database (Unihan)"
# UAX #44, "Unicode Character Database"
# UTS #51, "Unicode Emoji"
#
# The UAXes and UTS #51 can be accessed at https://www.unicode.org/versions/Unicode15.0.0/

This directory contains the final data files
for the Unicode Character Database, for Version 15.0.0 of the Unicode Standard.
```

### G4 — https://www.unicode.org/license.txt

原文 SHA256：`e7a93b009565cfce55919a381437ac4db883e9da2126fa28b91d12732bc53d96`。

```text
UNICODE LICENSE V3

COPYRIGHT AND PERMISSION NOTICE

Copyright © 1991-2026 Unicode, Inc.

NOTICE TO USER: Carefully read the following legal agreement. BY
DOWNLOADING, INSTALLING, COPYING OR OTHERWISE USING DATA FILES, AND/OR
SOFTWARE, YOU UNEQUIVOCALLY ACCEPT, AND AGREE TO BE BOUND BY, ALL OF THE
TERMS AND CONDITIONS OF THIS AGREEMENT. IF YOU DO NOT AGREE, DO NOT
DOWNLOAD, INSTALL, COPY, DISTRIBUTE OR USE THE DATA FILES OR SOFTWARE.

Permission is hereby granted, free of charge, to any person obtaining a
copy of data files and any associated documentation (the "Data Files") or
software and any associated documentation (the "Software") to deal in the
Data Files or Software without restriction, including without limitation
the rights to use, copy, modify, merge, publish, distribute, and/or sell
copies of the Data Files or Software, and to permit persons to whom the
Data Files or Software are furnished to do so, provided that either (a)
this copyright and permission notice appear with all copies of the Data
Files or Software, or (b) this copyright and permission notice appear in
associated Documentation.

THE DATA FILES AND SOFTWARE ARE PROVIDED "AS IS", WITHOUT WARRANTY OF ANY
KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT OF
THIRD PARTY RIGHTS.

IN NO EVENT SHALL THE COPYRIGHT HOLDER OR HOLDERS INCLUDED IN THIS NOTICE
BE LIABLE FOR ANY CLAIM, OR ANY SPECIAL INDIRECT OR CONSEQUENTIAL DAMAGES,
OR ANY DAMAGES WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS,
WHETHER IN AN ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION,
ARISING OUT OF OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THE DATA
FILES OR SOFTWARE.

Except as contained in this notice, the name of a copyright holder shall
not be used in advertising or otherwise to promote the sale, use or other
dealings in these Data Files or Software without prior written
authorization of the copyright holder.
```

## 附录 O：OpenRC 许可证原文

### O1 — OpenRC aec5dedb1ce30f8188b8ef1042e518651cc0fe33 / LICENSE

原文 SHA256：`6ac2bf77510808fbec7dc86cbfbca98dfc4c6b47dc99e4ca77b5370e097d8e70`。

```text
Copyright (c) 2007-2008, Roy Marples <roy@marples.name>
Copyright (c) 2007-2023, the OpenRC authors
All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

1. Redistributions of source code must retain the above copyright notice,
   this list of conditions and the following disclaimer.
2. Redistributions in binary form must reproduce the above copyright
   notice, this list of conditions and the following disclaimer in the
   documentation and/or other materials provided with the distribution.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE
ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT OWNER OR CONTRIBUTORS BE
LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR
CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF
SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS
INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN
CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE)
ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE
POSSIBILITY OF SUCH DAMAGE.
```

## 附录 E：上游核验摘要

### E1 — 实际取得的固定来源及网页证据

此处摘要用于定位审计证据；不把当前网页版本替代历史版本。动态网页原始响应不进入仓库。

| 来源 | SHA256 / 标识 |
| --- | --- |
| flag-icons v7.5.0 上游归档 | `5502d1bb0bda9f258d726d3c084a2d57a07cfdfa6d2ed18cbb5a1ee11b307778` |
| Heroicons 固定提交归档 | `0b66712da6b739a7c0c2ad6534e1351fb36659c3153b8d1995dd0957589eaf4b` |
| Devicon Debian SVG | `09b14c9813a322b58b11aa5390fb1a2b4e9bfd02868321482f1a19534a66ec54` |
| Devicon LICENSE | `121194741d4a915b9f5890fdd6dd95121f9b1f816517c792358d72d7c838d664` |
| Simple Icons Alpine SVG | `d37d0a7873c65174308d63929f09ab93c3aaa7f5c5df79cdec4be532e015fc98` |
| Simple Icons LICENSE.md | `e43ad2b5f43c4798f7ceb92c259f4b854d59eb9dee3f4321ea0185758d7dea7b` |
| musl 固定提交归档 | `b65b3c7ca604a839ecb5abcdb7454e2e7d11db2b8e08df668c7fee45e8e50fbf` |
| PSL 固定提交原始数据 | `67567d9849ba4ff4f3ca65fb54e978ae310f8f9228c2d5c5ef23a2dabcbc7feb` |
| ntgo v0.8.0 原始辅助函数 | `defc9139cb34f372fefa736bf534614059bbb050c06d62029364392102bff038` |
| gg-status 固定提交文件 | `ad874f952cb72d65bf5beb52e3b5c3d625fc1437e2df05af24ec114228b972ec` |
| Debian 官方 logo 授权页 | `45908a83b5247cc2560206cf9cb405859299eb2ac30b6e3ec45ec41f71841f7a` |

gg-status 的 Git blob SHA1 为 `50c5afa7f6fae163e48040c4624e0638c6250840`；首次审计时与仓库里的来源注释一致，该注释已随 R22 删除，这里只作为历史审计证据保留。

### E2 — 核验命令与限制

- `git fetch origin`：成功；`git rev-parse origin/roadmap` 返回本文件开头记录的指定提交；已用 `git show origin/roadmap:ROADMAP.md` 重读。
- 固定上游下载与比较：Python urllib、tarfile、hashlib；vendor 285/285 字节一致。
- `go mod verify`：`all modules verified`。模块版权原文读取自实际版本缓存；本文原文摘要可用于复核。
- `GOTOOLCHAIN=go1.26.8 go vet ./...`、`GOTOOLCHAIN=go1.26.8 go build ./...`：Windows 本地通过。构建缓存改放可写临时目录；不是 Linux 上实际运行测试结果。
- Linux amd64/arm64 的 `go list -deps` 检查使用 `CGO_ENABLED=0`，没有运行 Linux 二进制。
- 首次下载与默认缓存访问曾受沙箱权限限制；经允许下载和切换可写构建缓存后完成。依赖下载补写的 go.sum 校验项已恢复到审计基准，未作为本任务改动。
- 不需要测试 Server；未做部署、发布产物构建、真机验证或全套回归测试。本任务验收要求是 vet/build 和单文件来源审计。
- 发布前复核（2026-10-22）：`go.mod`、`go.sum` 自首次审计后没有变化（21 个模块版本与第 3 节、附录 M1–M22 逐项一致）；`web/static/vendor` 自首次审计后没有变化（uPlot JS、CSS、LICENSE 的 SHA256 与上文一致）；用 Go 1.26.8、`CGO_ENABLED=0`、`GOOS=linux`、`GOARCH=amd64/arm64`、`go list -deps` 重新得到的 Server 模块集合（11 个）和 Agent 模块集合（5 个）与第 3 节“发布包依赖归属”一致，没有新增或消失的模块；仓库里已没有 zstatic 目录、gg-status 拷贝或对应代码。没有重新下载上游，附录原文和 E1 摘要保持首次审计的内容。
