# 404-probe 项目约定

Server、Agent 和发布元数据分别在 `cmd/server`、`cmd/agent`、`cmd/release-metadata`。Web 资源在 `web/static`，安装器为 `install.sh`。

## 分工与节奏

- v1.4.0 从 `main` 的 `1bb3ce72` 开出 `dev/v1.4.0`（v1.3.1 tag 为 `cdd4fa3e`，schema 38）。按 ROADMAP 的 v1.4.0 清单完成后一起发布。期间只为安全问题、数据丢失或正式环境不可用发小版本。正式环境出问题时尽快发布修复版。
- Codex（gpt-6.2-sol，施工 high）只施工明确授权的任务。S0 试用，如不顺手可回退到 gpt-6.1-sol。每项使用 `codex/v140-<主题>` 和独立工作目录。
- Claude Code（Sonnet 5.5）负责评审、合并、测试机安装 / 升级 / 真机验证和发布，维护者已授权其执行，尽量自动化。评审通过后合到 `dev/v1.4.0`。中途可从开发分支构建候选给维护者试用。
- 同时最多两个会话，通常一个 Codex 施工和一个 Claude Code 评审或测试。Claude Code 可评审上一批，Codex 可施工下一批。
- 任务书写目标、范围和验收标准。自己的脚本或环境故障自行修复重跑。需要改变产品方向、触及正式环境或可能丢失数据时停下来问。
- 评审只查实际问题。阻塞项附复现步骤或失败测试，最多一轮复审，剩余记录为遗留。补充说明、反向验证及变异测试仅在安全相关改动确有需要时做。
- 报告写结论、改动、测试和遗留，记录本轮联网操作及临时环境变量名称和用途。会话和报告命名示例为 `v140-CX-S0`，报告存于 `D:/Claude-Github/release-records/v1.4.0/`。对维护者的回复最多五行，包含结论、需要其处理的事项及报告路径。

## 日常验证

- 施工和评审先用 `go version` 核对 `go.mod` 的 `toolchain`。Windows 可用 `./.tools/go/bin/go.exe` 引导，保持 `GOTOOLCHAIN` 默认。运行 `go vet ./...`、`go test ./... -count=1` 和相关 Node 测试。重复 20 次带 `-timeout=60m`。
- Windows 全仓测试在沙箱外跑。R31 使用真实 Python 和 Git Bash，必要时设置 `R31_PYTHON` 和 `R31_BASH`。worktree 的 VCS 构建信息失败用独立干净克隆对照。发布的 Linux 测试需要 IPv6 回环可用，Go 临时路径要短，避免 Unix socket 触及路径上限。
- 全仓测试使用非 root。Linux root 安装器测试经 `scripts/installer-fixture.py`，要求金丝雀、宿主路径和服务快照、设备 mode/uid/gid/ctime 比对通过。禁止在真实安装主机上用 root 直接跑全仓测试。明确记录 PASS、SKIP 和首次失败，复测保留独立日志。
- 旧 Node 基线为 `test-p1-agent-state.cjs`（24/51）、`test-p2c-google-ui.cjs`（3/2）、`test-n3-network-quality.cjs`（10/4）以及 `test-m02-real-chain.cjs` 的 R33 焦点检查（6/1）。只有核对为相同基线的失败才归入历史问题。Go 偶发和 root 夹具历史见 v1.3.1 发布报告，先保留首次结果再针对性复测。
- 仓库文本一律 LF，Git 使用 `core.autocrlf=false`。提交用 `type(scope): subject`，作者邮箱为 `305518817+404-git-404@users.noreply.github.com`。同一版本指定一方负责数据库迁移。

## 发布

1. Claude Code 从已验收的 `dev/v1.4.0` 准备候选。打开独立 Claude Code 会话做最终评审。v1.4.0 只在最后走一次完整发布。
2. 执行一条准备和验收命令，路径替换成实际仓库外的 ASCII 正斜杠路径。发布说明先准备好 UTF-8 LF 文件。
   ```powershell
   python -B scripts/release.py all --version v1.4.0 --from-version v1.3.1 --out D:/release-v140 --notes D:/release-notes-v140.md
   ```
   命令在独立克隆产生候选提交、七件产物和 `results.md`。报告失败时按其提示单步重跑。正式发布以 Linux 全仓、相关 `-race` 和动态安装器测试的结果为准。Windows 可本地构建和验收通用步骤，Linux 夹具在 Linux 运行器上完成。详细选项见 `scripts/release.py --help`。
3. 真机只验一次 v1.3.1 → v1.4.0 升级和一次全新安装。用同一批产物，核对测试 Server 的迁移和数据、测试 Agent 的版本和状态，Agent 面板升级按 R05 做前后快照。数据库审计脚本先显式关闭 SQLite 连接。升级窗口由安装器保证，发布说明写明支持起点和手动替换二进制的限制。R31 登记表在下版补入正式 tag 提交、schema 和表增删。
4. 将验收通过的候选提交推到 `release/vX.Y.Z`，注释 tag 指向该提交。创建 release 时先用 `--latest=false`，附七件资产和 `SHA256SUMS` 自身摘要。下载回核对全部资产以及正文摘要。确认测试 Agent 面板升级，再标 latest；失败恢复为草稿。最后更新发布文档、快进 `main` 并整理 ROADMAP。推送传输中断先用 `git ls-remote` 核对状态，状态明确且未变时可原样重试。

Web 升级按精确 tag 解析目标版本。目标 release 必须是已发布的正式版本，草稿和预发布均不可用。测试 Server 必须运行该 release 对应的提交。

## 测试机与凭据

- 先读不提交的 `CLAUDE.local.md`，只操作已登记的测试机。不碰正式环境。仓库、日志、报告、聊天均不得含测试机别名、地址、连接信息、密钥、管理员密码或 token。只操作本项目及所需的测试工具，改防火墙、重启主机或可能丢失数据时停下来问。
- 测试 Server 全新装可选择安装器生成随机管理员密码，保存在测试 Server 的 root 专属 0600 文件 `/root/404-probe-test-web-password`。可在测试 Server 用 `404-probe-install web-password reset` 重新随机生成，重置会使旧密码失效。正式环境管理员密码和手动注册的永久 enrollment value 由维护者亲自在 TTY 输入。
- 测试 Agent 一键注册使用测试 Server 的 `404-probe-install oneclick-token` 生成一次性 43 字符 token。Server 和 Agent 只用 0600 文件存放，以 `PROBE_404_ENROLL_TOKEN_FILE` 交给安装器。安装成功或失败均清理两端文件。验证完关闭隧道，清理本轮临时目录并保留脱敏证据。
- 测试机助手的三个安装路径都使用本机候选产物。Agent 升级走候选 Server 的 Web 路径，需要同提交的 release 资产已可下载。助手用法和本机配置见 [scripts/release-tools.md](scripts/release-tools.md)。S0 只做语法检查与离线 dry-run。
