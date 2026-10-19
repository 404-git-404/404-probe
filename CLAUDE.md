# 404-probe 项目约定

Go 编写的监控探针：`cmd/server`（Server）、`cmd/agent`（Agent）、`cmd/release-metadata`（发布元数据），业务代码在 `internal/*`，Web 页面在 `web/static`（嵌入二进制），安装器是根目录的 `install.sh`。工具链按 `go.mod` 固定为 go1.26.8，仓库内的拷贝在 `.tools/go`（被 `.gitignore` 忽略）。

## 日常命令

```bash
go vet ./...
go build ./...
go test ./... -count=1
```

- 发布前的测试结果以 Linux 为准。`internal/agent` 的个别用例（如 `TestRCHelperNegotiationDoesNotTrustOldV2`）在 Windows 负载下会偶发超时，v1.0.2 上同样如此。
- `scripts/test-p1-agent-state.cjs`、`scripts/test-p2c-google-ui.cjs`、`scripts/test-m02-real-chain.cjs`、`scripts/test-n3-network-quality.cjs`（19 过 4 败，v1.0.3 上相同）是旧测试套件的基线失败（测试沙箱没有加载 `latency-mini.js` 等），v1.0.2 上相同，不是回归。`scripts/test-p4b2-traffic-chart.cjs`（8 过 7 败）和 `scripts/test-p5a2-device-details.cjs`（3 败）同样是基线失败，v1.0.3 上相同。
- `scripts/test-agent-upgrade-systemd.sh` 是为 v0.9.3→v1.0.0 迁移写的（Server、Agent 和资产版本都固定为 v1.0.0），目前跑不了。v1.0.4 用正式发布后的 Web 升级真机验证代替；脚本参数化放到 v1.0.5。
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

   共 7 个文件：4 个 ELF（linux amd64/arm64 × server/agent）、`install.sh`、`RELEASE-METADATA.json`、`SHA256SUMS`。逐个核对 `vcs.revision`、`vcs.modified=false` 和 SHA256，并按字节确认三个文本文件都是 LF。

   **不要设置 `GOTOOLCHAIN=local`。** `.tools/go` 自带的是 go1.26.5，只用来引导；保持 `GOTOOLCHAIN` 默认（auto），go 会自动切到 `go.mod` 里 `toolchain` 行指定的版本（目前 go1.26.8，已缓存在本机的模块缓存里，`GOPROXY=off` 也能用）。换了电脑、模块缓存里没有这个工具链时，第一次构建需要联网让 go 下载它，之后才能用 `GOPROXY=off`。构建脚本会强制检查：构建前 `go version` 必须等于 `go.mod` 的 toolchain 版本，每个 ELF 构建后 `go version -m` 的第一行必须是同一个版本、`vcs.revision` 必须等于发布提交、`vcs.modified` 必须是 `false`，任何一项不符就报错并清掉这次的输出。
4. **真机验证用本地产物**，不从 GitHub 下载：`PROBE_404_LOCAL_ASSET_DIRECTORY=<目录> PROBE_404_VERSION=vX.Y.Z bash install.sh`。安装器拒绝用同版本不同提交覆盖；要重测就先装上一版再升级。
5. **推送顺序（每一步推送都先让维护者确认）：**
   1. 把发布分支 `release/vX.Y.Z` 推到远端。每个版本使用独立的发布分支，从 `main` 拉出（`main` 只包含已正式发布的版本）；tag 打在该分支的发布提交上。旧分支 `codex/v1.0.1-m01` 不再使用，暂时保留，因为 v1.0.1–v1.0.3 的 tag 在它上面。
   2. 对发布提交打**带注释**的 tag `vX.Y.Z`，只推这个 tag。
   3. `gh release create vX.Y.Z --verify-tag --title "404-probe vX.Y.Z Stable" --notes-file RELEASE_NOTES_vX.Y.Z.md --latest=false`，附上这 7 个文件。不要用 `--prerelease`、`--draft`、`--generate-notes`，也不要让 `gh` 自己建 tag。
   4. 在运行同一提交的测试 Server 上，对旧版本的测试 Agent 做 Web 升级。
   5. 成功后 `gh release edit vX.Y.Z --latest`；失败则改回草稿（`--draft=true`），不删除 release 或 tag，也不重新上传不同字节。
   6. 说明文件的后续修改在 tag 之后单独提交并推送。
   7. **最后一步**才把 `main` 快进到该发布分支（发布完成后 `main` 才前进）。
6. 默认分支是 `main`，发布不通过合并进 `main` 完成，而是先在 `release/vX.Y.Z` 上完成，最后快进 `main`；tag 指向的提交必须在远端某个分支上可见。

## Web 升级的约束（决定了上面的顺序）

- 目标按精确 tag 解析（`releases/tags/<版本>`、`releases/download/<版本>/`），不看 latest。
- 版本号为正式版格式时，对应的 GitHub release 必须是非预发布、非草稿，否则目标显示为不可用。
- Server 会校验自身的提交与 release 的提交一致，所以必须让测试 Server 运行发布提交。
- 一旦 release 公开，线上旧版本 Server 的 Agent 升级对话框就会把它列为默认选项（仍需手动点击）。

## 技术与安全决策

遇到需要二选一的技术或安全决策（例如是否阻塞发布、测试白名单还是不暴露字段），先调用子代理 `release-decider`（`.claude/agents/release-decider.md`），按它的结论执行；它自己认为必须由人决定的事项，再交给维护者。拿不准时选更安全、更保守的方案。

## 测试机

共 8 台测试机，具体 SSH 别名和对应角色见本地的 `CLAUDE.local.md`（该文件被 `.gitignore` 忽略，不提交；换了电脑需要重新创建）。**它们都是纯测试机，可以随意卸载、删数据、重装**：

- 1 台 Debian 12，作 Server。
- 2 台 Debian Agent（Debian 12 和 Debian 13）。
- 2 台 Alpine 3.24 Agent（OpenRC）：一台 128 MiB 内存（arm64，cgroup 限制，用来验证内存是否稳定），另一台 512 MiB（SSH 经常超时，已决定不再尝试）。
- 3 台 Debian 13（DE、FR、NL），都是 Podman 容器（`systemd-detect-virt` 输出 `podman`），用于欧盟 YouTube 检测和容器环境兼容测试。容器里没有 D-Bus 系统总线（`systemd-run` 不可用），systemd 沙箱选项和 CAP_NET_RAW 也受限，安装器的沙箱探测（`sandbox_probe`）在这里才有意义。

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

## 待办

v1.0.4 的 Issues #3 到 #9 已在 v1.0.4 完成：#3 恢复重试幂等、#4 ipify 外联开关、#5 YouTube 欧盟 consent 与失败原因细分、#6 正常升级流程的 5 秒稳定检查、#7 `install.sh` 非交互安装、#8 工具链改为 go1.26.8 并由构建脚本校验、#9 文本文件统一 LF。

v1.0.5 及以后的待办：

- 设备详情页重新设计。
- 面板修改密码。
- Cloudflare Zero Trust / 双域名。
- Agent 升级演练脚本（`scripts/test-agent-upgrade-systemd.sh`）参数化。
- Server 本机监控（在 Server 上安装 Agent）。
- 已撤销 Agent 记录的彻底删除。
- M14–M19。
- v1.0.4 评审建议：
  - 一键安装在 `oneclick_redeem_token` 子 shell 中失败（响应解析失败或未知状态码）时，报错里加上“token 可能已被使用，请在面板重新生成命令”。
  - 沙箱探测最坏情况会长时间无输出：加开头提示“正在检测运行环境…”，并缩短单次超时。
  - 非法 UTF-8 下 Go 与 bash 的密码长度计数差异（低优先级，Server 端会再校验）。
- 网络质量目标改造：
  - 内置目标只保留 zstaticcdn 的 TCP 检测，去掉内置 ICMP。
  - 支持手动添加目标（只允许公网地址，复用 Agent 端的地址检查）。
  - 检测频率可调，默认 30 秒，不允许快于 30 秒。
  - 目标失效时显示“目标异常”，而不是线路丢包。
- 小的显示问题：
  - 公网 IP 冷却提示只写时间、不写日期（“可在 23:59 后”），改为带日期。
  - 网络质量关闭后，卡片仍显示地区和“暂无有效 RTT”，应显示“已关闭”。
  - 一键安装曾出现过笼统的“生成失败”，原因未查明，现在已有具体错误码，再出现时据此排查。
- 第三方代码审计和 `THIRD_PARTY_NOTICES.md`（Komari 之外，含 `web/static/vendor`）。
- 项目许可证：在公开推广或接受外部贡献之前选定。
- 发布流程改进：Web 升级验证前，先记录各测试 Agent 的状态快照（版本、加固方式、开关、单元文件哈希），升级后逐项对比。
- 评审记录统一保存在 `D:\Claude-Github\release-records\<版本>\`。
