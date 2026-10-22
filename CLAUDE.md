# 404-probe 项目约定

Go 编写的监控探针：`cmd/server`（Server）、`cmd/agent`（Agent）、`cmd/release-metadata`（发布元数据），业务代码在 `internal/*`，Web 页面在 `web/static`（嵌入二进制），安装器是根目录的 `install.sh`。工具链按 `go.mod` 固定为 go1.26.8，仓库内的拷贝在 `.tools/go`（被 `.gitignore` 忽略）。

## 日常命令

```bash
go vet ./...
go build ./...
go test ./... -count=1
```

- 发布前的测试结果以 Linux 为准。下面两个用例在 Windows 上会偶发失败，不是回归：`internal/agent` 的 `TestRCHelperNegotiationDoesNotTrustOldV2`（Windows 实测失败率约 25%，负载下超时，v1.0.2 上同样如此）和 `internal/storage` 的 `TestCanceledSQLiteQueryReleasesRowsAndAllowsCheckpoint`（Windows 约 5%）。遇到时单独重跑该用例，不要把重跑通过写成“首次全量通过”，要如实记下首次的失败。
- `scripts/test-p1-agent-state.cjs`、`scripts/test-p2c-google-ui.cjs`、`scripts/test-m02-real-chain.cjs`、`scripts/test-n3-network-quality.cjs`（19 过 4 败，v1.0.3 上相同）是旧测试套件的基线失败（测试沙箱没有加载 `latency-mini.js` 等），v1.0.2 上相同，不是回归。`scripts/test-p4b2-traffic-chart.cjs`（8 过 7 败）和 `scripts/test-p5a2-device-details.cjs`（3 败）同样是基线失败，v1.0.3 上相同。
- `scripts/test-agent-upgrade-systemd.sh` 可以使用（R04 已参数化）：`sudo bash scripts/test-agent-upgrade-systemd.sh <起始版本> <目标版本> <起始资产目录> <目标资产目录> <源码 git 目录> <go 路径>`，在隔离的 systemd 环境里演练一次 Agent 升级（官方起始 Agent → 目标 Server → 受控 updater → 官方目标 Agent）。**只接受正式版本号**（`vX.Y.Z`），不支持 beta/rc、同版本覆盖和降级；两个资产目录各需 `build-release-assets.sh` 输出的 7 个文件；只在 Linux amd64/arm64 且有 root 的机器上运行。它不覆盖官方 updater 本身、Web 操作入口和 GitHub release 的可用性发现，这些由 Web 升级真机验证补上。
- 文档、说明和提交信息用中文或英文均可，提交信息采用 `type(scope): subject`，作者邮箱用 `305518817+404-git-404@users.noreply.github.com`。

## 换行（务必保持 LF）

- 仓库内文件一律 LF，由 `.gitattributes` 强制；本地设置 `core.autocrlf=false`。`install.sh`、`SHA256SUMS`、`RELEASE-METADATA.json` 含回车符会导致发布校验失败，构建脚本会拒绝含 CR 的 `install.sh`。
- 仓库内所有文本文件一律 LF，没有例外（`README.md` 和 `web/static/jobs.js` 已在 v1.0.4 统一为 LF）。
- 在 Windows 上不要用默认配置的 `git archive` 打包去做测试，会转成 CRLF；用 `git -c core.autocrlf=false archive`。
- 检查换行请按字节（例如 Python 统计 `b'\r'`），不要依赖 Git Bash 的 `grep -c $'\r'`，它会给出假数字。

## 发布流程（v1.0.2、v1.0.3 的做法）

1. **发布前不改代码。** 版本号、安装器默认版本、README、`RELEASE_NOTES_vX.Y.Z.md` 以及硬编码版本的测试（`install.sh` 的 `DEFAULT_VERSION`、`cmd/server/installer_contract_test.go`、`cmd/release-metadata/main_test.go`、`scripts/test-install-banner.sh`、`scripts/test-release-build-gates.sh`、`scripts/build-release-assets.sh` 的默认版本）都放在**一个**发布准备提交里。准备提交之后不再有代码提交；任何改动都意味着重建产物并重做真机验证。
2. **发布前做独立 High 评审**，并在 Linux 上通过 `go vet ./...`、`go test ./... -count=1`，以及相关用例的 `-count=20`。
3. **从精确的、干净的发布提交构建产物，输出目录放在仓库外：**

   ```bash
   PATH="$PWD/.tools/go/bin:$PATH" GOPROXY=off \
   PROBE_404_RELEASE_COMMIT=<完整提交哈希> \
   PROBE_404_RELEASE_OUTPUT=<仓库外目录> \
   bash scripts/build-release-assets.sh vX.Y.Z
   ```

   **构建必须在主仓库，或 `core.autocrlf=false` 的干净克隆中进行，不能在 git worktree 里构建**（worktree 的 `.git` 是一个文件，Go 读不到完整的 VCS 信息，产物的 `vcs.revision`/`vcs.modified` 会缺失或错误）。克隆用 `git -c core.autocrlf=false clone --no-hardlinks`，检出 detached 的发布提交，`git status` 必须干净。

   共 7 个文件：4 个 ELF（linux amd64/arm64 × server/agent）、`install.sh`、`RELEASE-METADATA.json`、`SHA256SUMS`。逐个核对 `vcs.revision`、`vcs.modified=false` 和 SHA256，并按字节确认三个文本文件都是 LF。

   **不要设置 `GOTOOLCHAIN=local`。** `.tools/go` 自带的是 go1.26.5，只用来引导；保持 `GOTOOLCHAIN` 默认（auto），go 会自动切到 `go.mod` 里 `toolchain` 行指定的版本（目前 go1.26.8，已缓存在本机的模块缓存里，`GOPROXY=off` 也能用）。换了电脑、模块缓存里没有这个工具链时，第一次构建需要联网让 go 下载它，之后才能用 `GOPROXY=off`。构建脚本会强制检查：构建前 `go version` 必须等于 `go.mod` 的 toolchain 版本，每个 ELF 构建后 `go version -m` 的第一行必须是同一个版本、`vcs.revision` 必须等于发布提交、`vcs.modified` 必须是 `false`，任何一项不符就报错并清掉这次的输出。
4. **真机验证用本地产物**，不从 GitHub 下载：`PROBE_404_LOCAL_ASSET_DIRECTORY=<目录> PROBE_404_VERSION=vX.Y.Z bash install.sh`。安装器拒绝用同版本不同提交覆盖；要重测就先装上一版再升级。
5. **推送顺序（每一步推送都先让维护者确认）：**
   1. 把发布分支 `release/vX.Y.Z` 推到远端。每个版本使用独立的发布分支，从 `main` 拉出（`main` 只包含已正式发布的版本）；tag 打在该分支的发布提交上。旧分支 `codex/v1.0.1-m01` 不再使用，暂时保留，因为 v1.0.1–v1.0.3 的 tag 在它上面。
   2. 对发布提交打**带注释**的 tag `vX.Y.Z`，只推这个 tag。
   3. `gh release create vX.Y.Z --verify-tag --title "404-probe vX.Y.Z Stable" --notes-file RELEASE_NOTES_vX.Y.Z.md --latest=false`，附上这 7 个文件。不要用 `--prerelease`、`--draft`、`--generate-notes`，也不要让 `gh` 自己建 tag。
   4. 在运行同一提交的测试 Server 上，对旧版本的测试 Agent 做 Web 升级。**升级前后各记录一次状态快照并对比（R05）**：升级前 `python3 -I scripts/agent-state-snapshot.py collect --label before --out <仓库外目录> <测试 Agent 的 SSH 别名…>`，升级后 `--label after`，再 `python3 -I scripts/agent-state-snapshot.py compare <目录> before after`。快照只读采集版本、二进制哈希、`agent.env` 的各项设置（密钥只记“已设置/为空”）、每个 systemd 单元（含 drop-in）或 OpenRC 脚本的哈希、实际生效的沙箱选项、服务状态；版本、二进制哈希、进程是升级预期会变的，其他任何差异（单元文件、沙箱、设置、权限、服务不再运行）都要查清，命令退出码为 1。快照放在仓库外（例如 `release-records` 的版本目录），不提交。
   5. 成功后 `gh release edit vX.Y.Z --latest`；失败则改回草稿（`--draft=true`），不删除 release 或 tag，也不重新上传不同字节。
   6. 说明文件的后续修改在 tag 之后单独提交并推送。
   7. **最后一步**才把 `main` 快进到该发布分支（发布完成后 `main` 才前进）。
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

共 5 台测试机（DE、FR、NL 三台 Podman 已退役，不再使用），具体 SSH 别名和对应角色见本地的 `CLAUDE.local.md`（该文件被 `.gitignore` 忽略，不提交；换了电脑需要重新创建）。**它们都是纯测试机，可以随意卸载、删数据、重装**：

- 1 台 Debian 12，作 Server。
- 2 台 Debian Agent（Debian 12 和 Debian 13）。
- 2 台 Alpine 3.24 Agent（OpenRC），**都是容器**（没有 D-Bus 系统总线，`systemd-run` 不可用，CAP_NET_RAW 等能力受限，安装器的沙箱探测 `sandbox_probe` 在这里才有意义）：一台 128 MiB 内存（arm64，cgroup 限制，用来验证内存是否稳定），另一台东京 512 MiB（SSH 经常超时，**不再使用**）。

使用规则：
- 只做 404-probe 的安装和验证，不改动与本项目无关的系统配置和服务；安装系统软件、改防火墙、重启机器要先问。
- 安装器的角色选择和管理员密码从 `/dev/tty` 读取。管理员密码由维护者自己输入，不要代填。交互安装 Agent 时粘贴的 enrollment value（`404-probe-install enroll <名字>` 生成，是 Agent 的永久凭据）同样由维护者自己输入，不在对话中明文显示。
- 注册 token：在 CLAUDE.local.md 列出的测试 Server 上，Claude Code 可以自己用 `404-probe-install oneclick-token <名称>` 生成，用于一键安装和注册验证；绝不用于任何正式环境。
  - 它生成与面板相同的一次性 token（30 分钟、绑定名称、计入"同时最多 10 条未使用"的上限），不能用 `enroll`：`enroll` 给的是 Agent 的永久凭据，不能用于一键安装（`PROBE_404_ENROLL_TOKEN` 要求 43 个字符的注册 token）。
  - token 只存在权限 600 的文件里：命令把它写到 `/root/oneclick-token-<名称>.token`（`--output <绝对路径>` 可改），终端只输出文件路径、名称和过期时间，不打印 token。在 Server 上生成后，用 `scp` 拷到目标测试机（同样 600），通过 `PROBE_404_ENROLL_TOKEN_FILE` 交给安装器。
  - 不在对话、提交、文档或日志里明文出现。
  - 安装结束后（无论成功失败），删除 Server 和目标机上的 token 文件。
- Server 只监听 `127.0.0.1:8080`；Agent 用 HTTP 回环地址接入时，通过 `ssh -R 18080:127.0.0.1:8080` 反向隧道（Web 页面用 `ssh -L 8080:127.0.0.1:8080` 本地转发），Agent 的 `--server` 填 `http://127.0.0.1:18080`。这只是测试用法，线上应使用 HTTPS 地址。验证结束后要关闭隧道。
- 不要把主机名之外的连接信息（IP、密钥）、密码、token 写进仓库、说明、文档或提交。第三方页面的原始抓取（例如 YouTube 页面）只保留脱敏后的最小样本，使用后删除原始文件。
- Alpine 只支持 Agent，不提供 Server 和自动升级；新装前先确认 OpenRC 已完整启动，工具为 GNU 版本；不要把产物放在 `/tmp`（tmpfs 占内存）。

## 双模型开发

v1.0.5 起试运行：Claude Code 和 Codex 各自施工，互相做只读交叉评审。

- 施工方是 Claude Code 或 Codex，由维护者在 ROADMAP.md 中分配；Codex 另需遵守根目录的 `AGENTS.md`。
- 每个施工方使用独立的 git worktree 和分支，不共用工作目录。公共开发分支是 `dev/vX.Y.Z`（从 `main` 拉出）；v1.0.5 的约定：
  - Claude Code：分支 `claude/v1.0.5`，worktree 在 `D:\Claude-Github\404-probe-claude`。
  - Codex：分支 `codex/v1.0.5`，worktree 在 `D:\Claude-Github\404-probe-codex`。
  - 两个分支都从 `dev/v1.0.5` 拉出。
- 同一版本内只允许一方新增数据库迁移，由 ROADMAP 指定（v1.0.5 为 Claude Code）。
- 开发期间双方都不改 `RELEASE_NOTES`、`README`、`CLAUDE.md`、`AGENTS.md`，发布时统一整理。
- `app.js` 等大型公共文件，同一版本内尽量只由一方修改（v1.0.5 为 Claude Code）；另一方的任务如果需要修改，先停下来报告。
- 测试 Server 同一时间只给一方使用，由维护者安排。
- 模块完成后由另一方做只读交叉评审，以此代替一轮模块评审；发布前仍保留一次独立 High 评审。
- **评审分级**：低风险任务由施工方自测，另一方做只读交叉评审，维护者验收；高风险阶段（例如数据库迁移、Server/Agent 协议、授权与安全相关的阶段）和发布前的独立评审，额外由 GPT（high）做独立评审。
- **评审收尾规则**：只有能复现的缺陷才阻塞验收（评审要附复现步骤或失败的测试）；不能复现的怀疑、风格、更好的写法、范围外的想法，一律作为建议记录（写进评审记录，由维护者决定是否排期），不阻塞，也不要在同一轮里无限追加新的“必须修改”。
- 合并顺序：双方分支经验收后合入 `dev/vX.Y.Z`；发布时从 `dev/vX.Y.Z` 准备 `release/vX.Y.Z`。

## 后续任务

未来任务以 `roadmap` 分支的 ROADMAP.md 为准（该分支不合并进 `main` 或任何发布分支）：

- 在线阅读：https://github.com/404-git-404/404-probe/blob/roadmap/ROADMAP.md
- 原始文本：https://raw.githubusercontent.com/404-git-404/404-probe/roadmap/ROADMAP.md
