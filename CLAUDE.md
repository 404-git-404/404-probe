# 404-probe 项目约定

Go 编写的监控探针：`cmd/server`（Server）、`cmd/agent`（Agent）、`cmd/release-metadata`（发布元数据），业务代码在 `internal/*`，Web 页面在 `web/static`（嵌入二进制），安装器是根目录的 `install.sh`。工具链按 `go.mod` 固定为 go1.26.8，仓库内的拷贝在 `.tools/go`（被 `.gitignore` 忽略）。

## 日常命令

施工方跑测试前、评审方评审测试前，先执行 `go version`，确认等于 `go.mod` 的 `toolchain` 版本（目前 go1.26.8）。本机装有其他 Go（例如更新的 go1.27.0）时，go 不会自动降到项目指定版本，这样的测试结果不能代表项目工具链。Windows 用仓库 `.tools/go` 引导（例如 `& ./.tools/go/bin/go.exe version`，后续命令也使用这个入口）；保持 `GOTOOLCHAIN` 默认，让它自动切到 `go.mod` 指定版本，确认版本一致后再测试。

```bash
go vet ./...
go build ./...
go test ./... -count=1
```

- **施工方交付前、评审方评审时**都必须跑全仓 `go test ./... -count=1` 和相关 Node 测试，不能只跑改动的包。相关测试指加载或测试了本次改动文件的 `scripts/test-*.cjs` 和 `scripts/test-*.sh`；先用 `git diff --name-only <基准提交>` 列出改动文件，再按完整路径、文件名或模块名搜索，例如 `rg -n 'selector\.js|test-m02-real-chain' scripts -g 'test-*.cjs' -g 'test-*.sh'`，并沿公共测试 harness 的加载关系确认需要执行的脚本；改动的测试脚本本身也要执行，在报告中列出选取依据。
- **重复测试超时：** `-count=20` 必须显式带 `-timeout=60m`，发布验证同样适用；首次结果和复测分别记录，不覆盖首次日志。
- **真实安装主机上的 root 测试限制：** 跑前先检查 `/run/404-probe`、`/etc/404-probe`、`/var/lib/404-probe*`；有真实安装的主机不得以 root 直接运行 `go test ./...`。root 仅允许通过 `scripts/installer-fixture.py` 入口运行已接入的 harness，前提是先通过金丝雀、跑前跑后做真实路径/服务快照比对（设备节点还需 mode/uid/gid/ctime 比对），且日志中没有 `INSTALLER-FIXTURE-SKIPPED:`；否则视为未隔离。日常全仓测试一律非 root。“安装器动态 harness 实际执行”是独立的发布前验证项，报告必须逐个列出 harness 的 PASS/SKIP 数量，不得把 `ok` 写成已执行。在第二台 Linux 环境复现通过后，再评估是否进一步放宽；复现通过本身不自动放宽此规则。v1.3.0 起 harness 清单含 `test-install-web-password-reset`：它需要把候选 Server 二进制作为参数传进夹具（v1.3.0 发布时用 `harness2.py` 变体，只对这个入口改参数，驱动脚本在 `D:\Claude-Github\release-records\v1.3.0\` 的工作目录里，未入库）。附加的 `updater-root` 有 2 个顶层用例因夹具内任意 UID `chown` 返回 `invalid argument` 失败、`agent-root-selector` 因夹具里没有 `nobody` 跳过 1 项，v1.2.0、v1.2.1、v1.3.0 记录一致，属夹具限制；报告里照实单列，不写成零失败或零跳过。
- **Windows 全仓测试一律在沙箱外跑。** 沙箱内可能出现临时文件 rename/读写被拒绝、本机 HTTP fixture 不可访问等环境失败；如实保留首次结果，不把沙箱外重跑通过写成首次通过。
- Windows 的 `TestR31Tools` 需要设置 `R31_PYTHON` 指向真正的 Python（本机 `python3` 是 Microsoft Store 假别名）；实际查找方法是 PowerShell `Get-Command python | Select-Object -ExpandProperty Source`，再执行找到的解释器 `--version` 确认。Git Bash 不在 PATH 时还要设置 `R31_BASH`：先用 `Get-Command bash -ErrorAction SilentlyContinue` 查找；找不到时用 `Get-Command git | Select-Object -ExpandProperty Source` 确认 Git 安装位置，再用 `Test-Path 'C:/Program Files/Git/bin/bash.exe'` 核实已有 Git Bash，设置 `$env:R31_BASH='C:/Program Files/Git/bin/bash.exe'`（其他安装位置用实际路径）。直接运行 `python scripts/test-r31.py` 时，Git Bash 不在 PATH 里也要设置 `R31_BASH`。用 `$env:R31_PYTHON=(Get-Command python).Source` 设置真实解释器，不使用带本机用户名的固定路径；未提供 `R31_PREVIOUS_SERVER` 导致的可选集成用例跳过应照实报告。
- **已知清单处理**：下面已列出的 Windows / Linux 偶发和旧 Node 套件基线失败，首次结果（包括失败）照实报告，不阻塞；复测单独记录，不能覆盖首次结果。清单以外的失败一律算问题，要么在授权范围内修复，要么停下来问，不自行归入基线。
- Windows worktree 的构建信息已知限制：`internal/buildinfo` 的 `TestInspectLinkedLinuxReleaseNeverExecutesAndBindsVersion`（`amd64`、`arm64` 子用例）和 `internal/updater` 的 `TestInspectCandidateBuildReadsGoExecutable` 可能因构建信息不全 / vcs 信息缺失而失败（和 Go 版本、vcs 信息有关）；出现这类失败时，必须在主仓库或干净克隆里对照验证，不能仅凭 worktree 失败判定回归。R37 已用相同干净提交对照：worktree 失败，主仓库两包通过；仍需跑全仓并报告该环境失败及独立对照结果。
- 发布前的测试结果以 Linux 为准。下面用例已有偶发记录，按已知清单处理：`internal/agent` 的 `TestRCHelperNegotiationDoesNotTrustOldV2`（Windows 实测失败率约 25%，负载下超时，v1.0.2 上同样如此）和 `internal/storage` 的 `TestCanceledSQLiteQueryReleasesRowsAndAllowsCheckpoint`（Windows、Linux 都出现过偶发；Linux 见 v1.0.9 R37 测试 Server 全仓记录，不推断 Linux 失败率）。遇到时单独重跑该用例，不要把重跑通过写成“首次全量通过”，要如实记下首次的失败。
- Linux 测试的大文件（源码、工具链、Go 缓存、大库）不放 `/tmp`，放 `/var/lib` 下专属目录，用完删除；有些机器的 `/tmp` 是很小的内存盘。Go 测试的临时目录（`TMPDIR` / `GOTMPDIR`）必须用短路径（例如默认 `/tmp`），因为有的用例会创建 Unix socket，路径超过约 108 字节就会失败；Go 的 `testing.TempDir` 直接读取 `GOTMPDIR`。先检查 `/tmp` 挂载和空间，非小内存盘且空闲至少 2 GB 时，不设这两个变量，直接使用默认 `/tmp`；若 `/tmp` 是小内存盘或空闲不足 2 GB，则把 `TMPDIR` 和 `GOTMPDIR` 都设为 `/var/lib` 专属目录下的短路径（例如 `/var/lib/<短名>/t`，总长远小于 108 字节，为用例追加目录名和 socket 文件名留足余量），其余大文件仍放专属目录。测试结束清理本轮留下的临时 fixture 及专属目录。
- Linux 全仓测试必须在 IPv6 回环（`::1`）可用的机器上跑；禁用了 IPv6 的机器上，`internal/agent`、`internal/egress` 的 IPv6 用例会因为绑定不上 `[::1]` 而失败，这是环境限制，不是回归，要换机验证，并如实保留原机器的首次失败。
- Linux 上的既有偶发失败：`internal/egress` 的 `TestM10HTTPSFailureClassification/body_timeout_after_headers` 可能把预期的 `timeout` 判为 `invalid_response`。v1.0.7 发布前首次全量曾失败；同机定向 50 轮对照中 v1.0.6 失败 1 次、v1.0.7 为 0 次，随后 v1.0.7 包级和全量复测通过。维护者据此按既有偶发处理，不视为本次回归；50 轮不能证明两版失败率完全相同。遇到时如实保留首次失败及复测结果，不把复测通过写成首次通过。
- `cmd/server` 的 `TestDatabaseCommandUsesTheTimeoutFlag`（1 ns 超时，属于时间竞争）有偶发失败，登记为既有偶发：v1.1.0 修复轮在干净克隆里对基准提交 421fd12 与当时的修复提交各跑 100 次：基准 98 过 2 败、当前 100 过 0 败；此前各 20 次的一轮是基准 20 过 0 败、当前 19 过 1 败；该测试涉及的代码（`cmd/server`、整个 `internal/storage`、`go.mod`/`go.sum`）在对照范围内无改动，不视为回归。遇到时如实保留首次失败，单独重跑该用例，不能把重跑通过写成首次通过；100 次不能证明两边失败率完全相同。
- `internal/updater` 的 `TestEngineHealthTimeoutRollsBack`（v1.2.0 起的测试时序问题：先写 rolled_back 状态、后删 candidate）有偶发失败，登记为既有偶发；遇到时单独重跑一次并如实记录首次失败，不把重跑通过写成首次通过。
- `-race` 全仓的耗时很长：`internal/storage` 约 34 分钟、`internal/server` 约 21 分钟（测试 Server 上），必须显式带 `-timeout=60m` 以上，并预留 40 分钟以上。
- `scripts/test-p1-agent-state.cjs`（24 过 51 败）、`scripts/test-p2c-google-ui.cjs`（3 过 2 败）、`scripts/test-n3-network-quality.cjs`（10 过 4 败）是旧测试套件的基线失败（测试沙箱没有加载 `latency-mini.js` 等），v1.2.1 发布时对照确认，v1.3.0 发布准备时数字完全相同（`p1-agent-state` 需 `--test-force-exit`，断言完成后进程不退出），不是回归。`scripts/test-p4b2-traffic-chart.cjs` 曾是基线失败（8 过 7 败，v1.0.3 上相同），v1.3.0 发布准备时为 6 过 0 败，不再失败；`scripts/test-p5a2-device-details.cjs` 已不再是基线失败（v1.2.1、v1.3.0 均为 13 过 0 败）。
- `scripts/test-m02-real-chain.cjs`：R37 将旧 radio 操作改为 `selectOption` 后，受控真实链路实测由改前 0 过 6 败变为 5 过 1 败；切换、刷新/重启持久化、目标拒绝和请求校验均通过。剩余 R33 的 30 轮滚动/焦点检查在改前也失败（关闭后 `Control+Home` 等待 150ms 时滚动位置未稳定为 0）；本轮 `pageerror` 为空，不归因为 `latency-mini.js` 未加载。保留为旧套件基线失败，首次结果和改前/改后计数照实报告；v1.2.1、v1.3.0 发布时均为 6 过 1 败（仍是同一项 R33，`errors` 为空）。
- v1.3.0 发布记录（A 段，评审 B 段后补证）：`-race` 要覆盖本版所有改动的 Go 包，包括只改了测试的包（`git diff --name-only <上一个正式 tag> <发布提交> -- '*.go'` 取目录）；v1.3.0 A 段第一轮漏了 `cmd/server`、`internal/auth`、`internal/updater`、`web`，B 段指出后补跑通过（均首次通过，没有数据竞争）。本版没有新登记的偶发。内置浏览器窗格的真机截图输出只有 800 px 宽，满足不了 2560×1440 的原规格；v1.3.0 由维护者接受该偏差，界面细节以施工阶段的原尺寸合成数据截图为准。
- v1.3.1 发布记录（三条操作教训）：（1）Windows 上构建时，产物目录必须用 ASCII 正斜杠路径；用反斜杠路径时 `sha256sum` 会在摘要前加 `\`，构建脚本报 `malformed installer digest`（`write-installer-checksum.sh` 对反斜杠路径的处理留作后续事项）。（2）给 root 隔离夹具传环境变量时，不能把宿主的路径变量（`GOTMPDIR`、`TMPDIR` 等）带进去，否则测试还没跑到检查就退出。（3）真机升级前的审计脚本必须显式关闭 SQLite 连接，否则安装器的“数据库不得有打开句柄”检查会正确拒绝备份并自动恢复。
- `scripts/test-agent-upgrade-systemd.sh` 可以使用（R04 已参数化）：`sudo bash scripts/test-agent-upgrade-systemd.sh <起始版本> <目标版本> <起始资产目录> <目标资产目录> <源码 git 目录> <go 路径>`，在隔离的 systemd 环境里演练一次 Agent 升级（官方起始 Agent → 目标 Server → 受控 updater → 官方目标 Agent）。**只接受正式版本号**（`vX.Y.Z`），不支持 beta/rc、同版本覆盖和降级；两个资产目录各需 `build-release-assets.sh` 输出的 7 个文件；只在 Linux amd64/arm64 且有 root 的机器上运行。它不覆盖官方 updater 本身、Web 操作入口和 GitHub release 的可用性发现，这些由 Web 升级真机验证补上。v1.2.1 起的新用法和提醒：演练用同 uid / gid 与不同身份对照 Agent 健康上报（同身份应报 healthy，只同组应 unreachable，身份不符应 identity_mismatch）；资产目录里的文件要有可执行权限，所有路径写绝对路径且不含空格；演练被 SIGTERM 打断后，清理约 80 秒才完成，别在清理中途再起一轮。
- 文档、说明和提交信息用中文或英文均可，提交信息采用 `type(scope): subject`，作者邮箱用 `305518817+404-git-404@users.noreply.github.com`。

## 换行（务必保持 LF）

- 仓库内文件一律 LF，由 `.gitattributes` 强制；本地设置 `core.autocrlf=false`。`install.sh`、`SHA256SUMS`、`RELEASE-METADATA.json` 含回车符会导致发布校验失败，构建脚本会拒绝含 CR 的 `install.sh`。
- 仓库内所有文本文件一律 LF，没有例外（`README.md` 和 `web/static/jobs.js` 已在 v1.0.4 统一为 LF）。
- 在 Windows 上不要用默认配置的 `git archive` 打包去做测试，会转成 CRLF；用 `git -c core.autocrlf=false archive`。
- 检查换行请按字节（例如 Python 统计 `b'\r'`），不要依赖 Git Bash 的 `grep -c $'\r'`，它会给出假数字。

## 发布流程（v1.0.2、v1.0.3 的做法）

**升级支持策略**：每个版本只支持从前 1–2 个正式版升级；v1.3.1 支持从 v1.3.0、v1.2.1 升级（`minimumUpgradeSchemaVersion` 为 37；本版没有新迁移，schema 仍是 38：v1.3.0 起点保持 38，v1.2.1 起点执行 v1.3.0 引入的 37 → 38 迁移）。发布说明的“升级前必读”一节必须有“支持从 vA、vB 升级”一行，列出该版支持的正式起点。版本窗口只由安装器保证；手动替换二进制不保证（数据库层另有 schema 门槛：schema 36 及更早的库会在打开时被拒绝，但这不等于窗口内的测试保证），发布说明也必须写明这一限制。

### 发布分档（v1.2.0 起，维护者 2026-11-11 确认）

版本号：加功能的版本升中间位（v1.2.0 → v1.3.0），只修 bug 的版本升末位（v1.2.1）。

发布流程按“本版相对上一个正式版的全部改动”分三档，就高不就低，拿不准按高一档。这里的档只管发布流程；施工和评审的风险档仍按“开发方式”第 6 条。下面第 1–7 步写的是“发布-全套”的做法，另外两档只能减免本小节写明可以不做的项目。

| 档 | 判定 | 流程 |
|---|---|---|
| 发布-轻量 | 只改 `web/static/`、文档、测试脚本；Go 产品代码、`install.sh` 的逻辑、schema 都没变 | 施工 → 评审 → 一个发布会话（准备和发布合并，不设独立评审 B 段） |
| 发布-中 | 改了 Go 产品代码或 `install.sh` 的逻辑；没有新的数据库迁移，`minimumUpgradeSchemaVersion` 数值不变 | A 准备 → C 发布；改动涉及 `install.sh`、`internal/updater`、Agent 升级路径、认证 / 会话 / token、网络连接与地址安全时，加独立评审 B 段 |
| 发布-全套 | 有新的数据库迁移，或 `minimumUpgradeSchemaVersion` 数值变化，或升级门槛的判断逻辑变化 | A 准备 → B 独立评审 → C 发布 |

发布准备提交里例行的版本号和版本窗口更新（`DEFAULT_VERSION`、`require_supported_server_version` 的版本、三处提示文字及对应测试）不影响分档。

三档都必须做：

- Linux 全仓 `go vet ./...`、`go test ./... -count=1`，以及相关 Node / 浏览器测试；
- 在主仓库或干净克隆里构建 7 个文件，逐个核对 `vcs.revision`、`vcs.modified=false`、SHA256 和 LF；
- 测试 Server 用本地产物从上一个正式版升级；
- 推送、打 tag、建 release（先 `--latest=false`），把 7 个文件下载回来核对，并核对 `SHA256SUMS` 摘要；
- 至少一台 Debian 测试 Agent 做面板升级，升级前后做 R05 快照对比；
- 成功后标 latest，失败改回草稿；然后文档提交、`main` 快进、roadmap 整理。

可以减免的项目：

| 项目 | 发布-轻量 | 发布-中 | 发布-全套 |
|---|---|---|---|
| 独立评审 B 段 | 不做 | 涉及上表所列范围时做 | 做 |
| R31 大库升级演练 | 不做 | 不做；`install.sh` 的升级事务、备份或回滚逻辑有改动时做 | 做 |
| root 安装器 harness（经 `scripts/installer-fixture.py`） | 不做 | 做 | 做 |
| 相关用例 `-count=20 -timeout=60m` | 不做 | 做 | 做 |
| Agent 升级 systemd 演练 | 不做 | 改了 Agent、Updater 或 `install.sh` 时做 | 做 |
| 面板升级的测试 Agent | 一台 Debian | 两台 Debian | 两台 Debian |
| 200 台性能 | 不做（改了首页渲染或 SSE 路径时，施工阶段已测） | 改了首页数据或渲染路径时做 | 做 |
| 界面截图 | 只截本版改动处 | 只截本版改动处 | 全套 |

定档与复核：每个版本开工前由策划定档，写进第一份任务书。发布会话开始时先执行 `git diff --name-only <上一个正式 tag> <待发布提交>`，对照本档的判定；发现超出本档范围的文件就停下报告，改按高一档做。

理由：Agent 以 root 运行，版本不能降级，安装器出错可能连修复版都装不上，所以涉及 Go、安装器、schema 的版本不能靠“发出去有问题再修”。

1. **发布前不改代码。** 版本号、安装器默认版本、README、`RELEASE_NOTES_vX.Y.Z.md` 以及硬编码版本的测试（`install.sh` 的 `DEFAULT_VERSION`、`cmd/server/installer_contract_test.go`、`cmd/release-metadata/main_test.go`、`scripts/test-install-banner.sh`、`scripts/test-release-build-gates.sh`、`scripts/build-release-assets.sh` 的默认版本、`cmd/server/installer_version_digits_test.go`）都放在**一个**发布准备提交里；`installer_version_digits_test.go` 的升级、降级、同版本三种情况要跟着窗口往后挪（v1.3.1 发布时漏了它，到 root harness 才失败）。发布准备提交还必须“按升级支持策略更新版本窗口”：`internal/storage` 的 `minimumUpgradeSchemaVersion`、`install.sh` 的 `require_supported_server_version` 版本、三处提示（`database_schema_too_old`、`server_version_too_old`、`server_version_unrecognized`）里的版本号、`internal/storage/upgrade_floor_test.go`、`scripts/test-upgrade-floor.sh`。README 的升级说明、迁移链描述和 v1.0.1 桥接节在发布 A 段统一改写。准备提交之后不再有代码提交；任何改动都意味着重建产物并重做真机验证。
2. **发布前做独立 High 评审**（发布-轻量不设，发布-中按‘发布分档’），并在 Linux 上通过 `go vet ./...`、`go test ./... -count=1`，以及相关用例的 `-count=20 -timeout=60m`。
3. **从精确的、干净的发布提交构建产物，输出目录放在仓库外：**

   ```bash
   PATH="$PWD/.tools/go/bin:$PATH" GOPROXY=off \
   PROBE_404_RELEASE_COMMIT=<完整提交哈希> \
   PROBE_404_RELEASE_OUTPUT=<仓库外目录> \
   bash scripts/build-release-assets.sh vX.Y.Z
   ```

   **构建必须在主仓库，或 `core.autocrlf=false` 的干净克隆中进行，不能在 git worktree 里构建**（在 Windows 上用 go1.26.8 实测：worktree 的 `.git` 是一个文件，构建出的产物的 `vcs.revision`/`vcs.modified` 会缺失或错误；Linux 上没有实测。所以“发布构建一律用主仓库或干净克隆”是操作约束，不对其他环境作推断）。克隆用 `git -c core.autocrlf=false clone --no-hardlinks`，检出 detached 的发布提交，`git status` 必须干净。

   共 7 个文件：4 个 ELF（linux amd64/arm64 × server/agent）、`install.sh`、`RELEASE-METADATA.json`、`SHA256SUMS`。逐个核对 `vcs.revision`、`vcs.modified=false` 和 SHA256，并按字节确认三个文本文件都是 LF；另记录 `SHA256SUMS` 文件自身的 SHA-256。

   **不要设置 `GOTOOLCHAIN=local`。** `.tools/go` 自带的是 go1.26.5，只用来引导；保持 `GOTOOLCHAIN` 默认（auto），go 会自动切到 `go.mod` 里 `toolchain` 行指定的版本（目前 go1.26.8，已缓存在本机的模块缓存里，`GOPROXY=off` 也能用）。换了电脑、模块缓存里没有这个工具链时，第一次构建需要联网让 go 下载它，之后才能用 `GOPROXY=off`。构建脚本会强制检查：构建前 `go version` 必须等于 `go.mod` 的 toolchain 版本，每个 ELF 构建后 `go version -m` 的第一行必须是同一个版本、`vcs.revision` 必须等于发布提交、`vcs.modified` 必须是 `false`，任何一项不符就报错并清掉这次的输出。
4. **真机验证用本地产物**，不从 GitHub 下载：`PROBE_404_LOCAL_ASSET_DIRECTORY=<目录> PROBE_404_VERSION=vX.Y.Z bash install.sh`。安装器拒绝用同版本不同提交覆盖；要重测就先装上一版再升级。
   - 发布-全套（以及‘发布分档’写明需要做 R31 的情况）：发布前真机验证必须包含接近生产规模的合成库升级测试，分别记录升级前检查、迁移、回滚恢复的耗时和结果，写入评审记录。发布门槛使用至少 500,000,000 字节的 snapshot 或 full，演练矩阵覆盖本版支持的前两个正式版。v1.3.0 的起点是 v1.2.0、v1.2.1，目标 schema 38。**v1.3.0 的做法（维护者 2026-11-19 决定）：v1.2.1 起点做真机演练，v1.2.0 起点做离线迁移核对**（在专属目录用生成器生成起点库，用候选 Server 在副本上执行 `database migrate-protected`、`database verify` 并做行数审计）；理由是两个起点都是 schema 37、表结构完全一样，v1.2.1 没有新迁移，而 v1.2.0 起点要真机演练就得把测试 Server 重装回去。`scripts/r31-versions.json` 当前登记这两版。正式 v1.2.0 Server 创建 schema 37 底座，生成器填入合成数据，v1.2.1 起点再由正式 v1.2.1 Server 迁移和校验。维护者一行入口为 `scripts/r31-rehearse.sh`，冻结证据入口为 `scripts/r31-run-with-evidence.py`。用法和提示清单见 [scripts/r31-usage.md](scripts/r31-usage.md)，发布时对照目标版 `install.sh` 复核清单。
   - 每个版本正式发布后，在下一版准备工作里把该版补入 R31 登记表：版本、正式 tag 提交、schema、相对上一版的表增删。
5. **推送顺序：** 推送和 gh 操作需要维护者授权，可以在任务书里一次性授权。任务书必须写明哪些情况一定要停下来问，至少包括：结果与预期不符、升级失败、需要维护者操作面板或输入密码。
   1. 把发布分支 `release/vX.Y.Z` 推到远端。每个版本使用独立的发布分支，从 `dev/vX.Y.Z` 上已验收的提交拉出（`dev` 本身从 `main` 拉出，包含该版本的全部开发提交；`main` 只包含已正式发布的版本，所以不能直接从 `main` 拉，否则会漏掉开发提交）；tag 打在该分支的发布提交上。旧分支 `codex/v1.0.1-m01` 不再使用，暂时保留，因为 v1.0.1–v1.0.3 的 tag 在它上面。
   2. 对发布提交打**带注释**的 tag `vX.Y.Z`，只推这个 tag。
   - **推送遇到传输层失败（例如“连接被重置”）时**：先用 `git ls-remote` 确认远端没有任何变化，没有变化就可以把同一条已授权的命令原样重试一次，并把首次失败和重试写进报告；远端状态不明（查询失败、分支或 tag 指向与预期不符）就停下报告，不重试、不强推。
   3. `gh release create vX.Y.Z --verify-tag --title "vX.Y.Z" --notes-file <说明文件> --latest=false`，附上这 7 个文件。说明文件只有 3 行（v1.2.1 起的做法）：“支持从 vA、vB 升级。”；“完整说明：”加 tag 里 `RELEASE_NOTES_vX.Y.Z.md` 的完整 URL（`https://github.com/404-git-404/404-probe/blob/vX.Y.Z/RELEASE_NOTES_vX.Y.Z.md`）；“SHA256SUMS 的 SHA-256：<摘要>”（从最终产物计算，不手抄；摘要不能提前写进发布准备提交里的文件，因为 `SHA256SUMS` 来自这个提交的构建）。完整说明留在仓库的 `RELEASE_NOTES_vX.Y.Z.md`，tag 里的该文件保持原样，main 上的文件在发布后文档提交里补上摘要。不要用 `--prerelease`、`--draft`、`--generate-notes`，也不要让 `gh` 自己建 tag。
   4. **核对清单：GitHub release 说明里公布的 `SHA256SUMS` 摘要 = 最终上传的 `SHA256SUMS` 文件的 SHA-256**：把 7 件资产下载回来逐个比对 SHA256，并用下载回来的 `SHA256SUMS` 重新计算摘要，与 release 正文第 3 行公布的值逐字符比对（用 `gh release view … --json body` 取 `body` 字符串后做 ordinal 比较，并核对正文与 `--notes-file` 文件一致）；不符就按失败处理（见本条第 6 点）。
   5. 在运行同一提交的测试 Server 上，对旧版本的测试 Agent 做 Web 升级。**升级前后各记录一次状态快照并对比（R05）**：升级前 `python3 -I scripts/agent-state-snapshot.py collect --label before --out <仓库外目录> <测试 Agent 的 SSH 别名…>`，升级后 `--label after`，再 `python3 -I scripts/agent-state-snapshot.py compare <目录> before after`。快照只读采集版本、二进制哈希、`agent.env` 的各项设置（密钥只记“已设置/为空”）、每个 systemd 单元（含 drop-in）或 OpenRC 脚本的哈希、实际生效的沙箱选项、服务状态；版本、二进制哈希、进程是升级预期会变的，其他任何差异（单元文件、沙箱、设置、权限、服务不再运行）都要查清，命令退出码为 1。快照放在仓库外（例如 `release-records` 的版本目录），不提交。
   6. 成功后 `gh release edit vX.Y.Z --latest`；失败则改回草稿（`--draft=true`），不删除 release 或 tag，也不重新上传不同字节。
   7. 说明文件的后续修改在 tag 之后单独提交并推送（包括把 `SHA256SUMS` 摘要补进 `RELEASE_NOTES_vX.Y.Z.md`；只改 main，不影响 tag）。
   8. **最后一步**才把 `main` 快进到该发布分支（发布完成后 `main` 才前进）。
6. 默认分支是 `main`，发布不通过合并进 `main` 完成，而是先在 `release/vX.Y.Z` 上完成，最后快进 `main`；tag 指向的提交必须在远端某个分支上可见。
7. 评审记录统一保存在 `D:\Claude-Github\release-records\<版本>\`。

## Web 升级的约束（决定了上面的顺序）

- 目标按精确 tag 解析（`releases/tags/<版本>`、`releases/download/<版本>/`），不看 latest。
- 版本号为正式版格式时，对应的 GitHub release 必须是非预发布、非草稿，否则目标显示为不可用。
- Server 会校验自身的提交与 release 的提交一致，所以必须让测试 Server 运行发布提交。
- 一旦 release 公开，线上旧版本 Server 的 Agent 升级对话框就会把它列为默认选项（仍需手动点击）。

## 技术与安全决策

遇到需要二选一的技术或安全决策（例如是否阻塞发布、测试白名单还是不暴露字段），先调用子代理 `release-decider`（`.claude/agents/release-decider.md`），按它的结论执行；它自己认为必须由人决定的事项，再交给维护者。拿不准时选更安全、更保守的方案。

Codex 无法调用 `release-decider`，遇到这类决策时把问题和可选方案交给维护者。

## 测试机

登记 3 台测试机（Alpine 2 台已于 2026-11-14 退役；东京 512M 不再使用；DE、FR、NL 三台 Podman 已退役，不再登记），具体 SSH 别名和对应角色见本地的 `CLAUDE.local.md`（该文件被 `.gitignore` 忽略，不提交；换了电脑需要重新创建）。**它们都是纯测试机，可以随意卸载、删数据、重装**：

- 只测 Debian 13（正式环境也是 Debian 13 + amd64）：1 台 Server + 2 台 Agent，其中一台只有 IPv4，另一台 IPv4 + IPv6。
- 不再测试 Debian 12 和 Alpine（安装器不改，仍能装；只是不再测试）；arm64 照常发布，但未经真机测试。`sandbox_probe` 只由 `scripts/test-install-sandbox.sh` 覆盖。

使用规则：
- 只做 404-probe 的安装和验证，不改动与本项目无关的系统配置和服务；改防火墙、重启机器要先问。**测试必需的工具（例如 bubblewrap、strace）可以直接在登记的测试机上安装：只限登记的测试机，只用系统官方软件源，装了什么写进报告；正式环境一律不装。**（维护者 2026-11-13 决定；其他系统软件仍要先问。）
- 安装器的角色选择和管理员密码从 `/dev/tty` 读取。管理员密码由维护者自己输入，不要代填。交互安装 Agent 时粘贴的 enrollment value（`404-probe-install enroll <名字>` 生成，是 Agent 的永久凭据）同样由维护者自己输入，不在对话中明文显示。
- 测试机随机管理员密码（维护者 2026-11-21 决定）：测试 Server 全新安装时可以用安装器自动生成的随机管理员密码；测试面板需要登录时，可以用 `404-probe-install web-password reset` 设随机密码。密码都存在测试 Server 上 root 才能读的文件里（`0600`，例如 `/root/404-probe-test-web-password`），不写进日志、报告或聊天。**只限测试机，正式环境照旧由维护者亲手输入**；重置后维护者浏览器里保存的旧测试面板密码会失效。
- 注册 token：在 CLAUDE.local.md 列出的测试 Server 上，Claude Code 可以自己用 `404-probe-install oneclick-token <名称>` 生成，用于一键安装和注册验证；绝不用于任何正式环境。
  - 它生成与面板相同的一次性 token（30 分钟、绑定名称、计入"同时最多 10 条未使用"的上限），不能用 `enroll`：`enroll` 给的是 Agent 的永久凭据，不能用于一键安装（`PROBE_404_ENROLL_TOKEN` 要求 43 个字符的注册 token）。
  - token 只存在权限 600 的文件里：命令把它写到 `/root/oneclick-token-<名称>.token`（`--output <绝对路径>` 可改），终端只输出文件路径、名称和过期时间，不打印 token。在 Server 上生成后，用 `scp` 拷到目标测试机（同样 600），通过 `PROBE_404_ENROLL_TOKEN_FILE` 交给安装器。
  - 不在对话、提交、文档或日志里明文出现。
  - 安装结束后（无论成功失败），删除 Server 和目标机上的 token 文件。
- Server 只监听 `127.0.0.1:8080`；Agent 用 HTTP 回环地址接入时，通过 `ssh -R 18080:127.0.0.1:8080` 反向隧道（Web 页面用 `ssh -L 8080:127.0.0.1:8080` 本地转发），Agent 的 `--server` 填 `http://127.0.0.1:18080`。这只是测试用法，线上应使用 HTTPS 地址。验证结束后要关闭隧道。
- 不要把主机名之外的连接信息（IP、密钥）、密码、token 写进仓库、说明、文档或提交。第三方页面的原始抓取（例如 YouTube 页面）只保留脱敏后的最小样本，使用后删除原始文件。
- Alpine 只支持 Agent，不提供 Server 和自动升级，且不再测试；若要手动验证，新装前先确认 OpenRC 已完整启动，工具为 GNU 版本；不要把产物放在 `/tmp`（tmpfs 占内存）。

## 开发方式（v1.0.8 起）

1. 施工方由 ROADMAP 指定（Claude Code 或 Codex），评审方为另一方；谁写的代码不能由谁评审。维护者可以把某件事作为新任务交给另一方修改或重做，这时由原施工方评审。换人最好在任务或阶段结束时，中途换人要先写交接清单。Codex 另需遵守 `AGENTS.md`。
2. 分支与目录：每个版本从 `main` 拉出 `dev/vX.Y.Z`；Claude Code 用分支 `claude/vX.Y.Z`，工作目录 `D:\Claude-Github\404-probe-claude`；Codex 有施工任务时用分支 `codex/vX.Y.Z`，工作目录 `D:\Claude-Github\404-probe-codex`。两个施工分支都从 `dev/vX.Y.Z` 拉出，不共用工作目录。验收后合入 `dev/vX.Y.Z`，发布时从 `dev/vX.Y.Z` 准备 `release/vX.Y.Z`。
3. 同一版本内只允许一方新增数据库迁移，由 ROADMAP 指定。
4. 开发期间不改 `RELEASE_NOTES`、`README`、`CLAUDE.md`、`AGENTS.md`（任务书明确授权的除外），发布时统一整理。
5. 测试 Server 同一时间只给一方使用，由维护者安排。
6. 风险分两档：
   - **严格档**：网络连接与地址安全、认证与 token、安装器、数据库迁移、升级、会改变已存数据的逻辑（例如套餐用量）、发布前真机验证。施工方自测，另一方用 high 评审，修复后复核。
   - **轻量档**：界面、文案、显示、测试工具。另一方评审一次，意见一次改完即收，不再复核；只有修复引入了可复现的新缺陷才再看。
7. 复核最多两轮；两轮之后，除安全或数据问题外，剩余意见一律记为后续事项（写进评审记录，由维护者排期）。
8. **评审收尾规则**：只有能复现的缺陷才阻塞验收（评审要附复现步骤或失败的测试）；不能复现的怀疑、风格、更好的写法、范围外的想法，一律作为建议记录（写进评审记录，由维护者决定是否排期），不阻塞，也不要在同一轮里无限追加新的“必须修改”。
9. 修复攒成一批再提交评审，不逐条来回。
10. 只有真正复杂的任务才分阶段授权和验收。
11. 会话命名：每个施工或评审会话都有名字，格式“v<版本无点>-<CC 或 CX>-<用途>”，例如 v107-CC-后端、v107-CX-评审。报告和评审记录保存在 `D:\Claude-Github\release-records\<版本>\`，文件名以会话名开头。
12. （维护者 2026-11-21 决定）发布（A、C 段）、测试机的安装 / 升级 / 真机验证由 **Claude Code** 负责，维护者已全部授权，尽量全自动；Codex 只负责任务明确的施工；发布前独立评审（B 段）由哪一方、哪个会话做，由策划在每个版本的第一份任务书里写明。发布-轻量不设独立评审，见‘发布分档’。
13. 模型强度：Codex（gpt-6.1-sol）施工严格档 high、轻量档 medium；Claude Code（Sonnet 5.5）评审严格档 high、轻量档 medium。

## 对维护者的回复格式

Claude Code 和 Codex 都适用：对话回复不超过 5 行——① 一句话结论；② 需要维护者做什么（没有就写“无”）；③ 报告文件路径。细节一律写进报告文件。给维护者在终端执行的命令必须做成一行就能启动的脚本，不要让他粘贴多行命令。

## 后续任务

未来任务以 `roadmap` 分支的 ROADMAP.md 为准（该分支不合并进 `main` 或任何发布分支）：

- 在线阅读：https://github.com/404-git-404/404-probe/blob/roadmap/ROADMAP.md
- 原始文本：https://raw.githubusercontent.com/404-git-404/404-probe/roadmap/ROADMAP.md
