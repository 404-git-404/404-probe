# 404-probe 项目约定

Go 编写的监控探针：`cmd/server`（Server）、`cmd/agent`（Agent）、`cmd/release-metadata`（发布元数据），业务代码在 `internal/*`，Web 页面在 `web/static`（嵌入二进制），安装器是根目录的 `install.sh`。工具链按 `go.mod` 固定为 go1.26.8，仓库内的拷贝在 `.tools/go`（被 `.gitignore` 忽略）。

## 日常命令

```bash
go vet ./...
go build ./...
go test ./... -count=1
```

- 发布前的测试结果以 Linux 为准。`internal/agent` 的个别用例（如 `TestRCHelperNegotiationDoesNotTrustOldV2`）在 Windows 负载下会偶发超时，v1.0.2 上同样如此。
- `scripts/test-p1-agent-state.cjs`、`scripts/test-p2c-google-ui.cjs`、`scripts/test-m02-real-chain.cjs`、`scripts/test-n3-network-quality.cjs`（19 过 4 败，v1.0.3 上相同）是旧测试套件的基线失败（测试沙箱没有加载 `latency-mini.js` 等），v1.0.2 上相同，不是回归。
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

   **不要设置 `GOTOOLCHAIN=local`。** `.tools/go` 自带的是 go1.26.5，只有保持默认（auto）时 go 才会切到 `go.mod` 里 `toolchain` 行指定的 go1.26.8（已缓存在本机的模块缓存里，`GOPROXY=off` 也能用）。构建前先在仓库目录里运行 `go version`，构建后用 `go version -m <二进制>` 核对每个 ELF 的第一行是 `go1.26.8`。v1.0.1 是 go1.26.8 编的，v1.0.2 和 v1.0.3 的二进制是 go1.26.5 编的（构建时设了 `GOTOOLCHAIN=local`），下个版本要改回来。
4. **真机验证用本地产物**，不从 GitHub 下载：`PROBE_404_LOCAL_ASSET_DIRECTORY=<目录> PROBE_404_VERSION=vX.Y.Z bash install.sh`。安装器拒绝用同版本不同提交覆盖；要重测就先装上一版再升级。
5. **推送顺序（每一步推送都先让维护者确认）：**
   1. 把发布分支快进推到远端（v1.0.1/v1.0.2/v1.0.3 都是 `codex/v1.0.1-m01`）。
   2. 对发布提交打**带注释**的 tag `vX.Y.Z`，只推这个 tag。
   3. `gh release create vX.Y.Z --verify-tag --title "404-probe vX.Y.Z Stable" --notes-file RELEASE_NOTES_vX.Y.Z.md --latest=false`，附上这 7 个文件。不要用 `--prerelease`、`--draft`、`--generate-notes`，也不要让 `gh` 自己建 tag。
   4. 在运行同一提交的测试 Server 上，对旧版本的测试 Agent 做 Web 升级。
   5. 成功后 `gh release edit vX.Y.Z --latest`；失败则改回草稿（`--draft=true`），不删除 release 或 tag，也不重新上传不同字节。
   6. 说明文件的后续修改在 tag 之后单独提交并推送。
   7. **最后一步**才把 `main` 快进到最新提交（`main` 此前一直落后于发布分支）。
6. 默认分支是 `main`，发布不通过合并进 `main` 完成；tag 指向的提交必须在远端某个分支上可见。

## Web 升级的约束（决定了上面的顺序）

- 目标按精确 tag 解析（`releases/tags/<版本>`、`releases/download/<版本>/`），不看 latest。
- 版本号为正式版格式时，对应的 GitHub release 必须是非预发布、非草稿，否则目标显示为不可用。
- Server 会校验自身的提交与 release 的提交一致，所以必须让测试 Server 运行发布提交。
- 一旦 release 公开，线上旧版本 Server 的 Agent 升级对话框就会把它列为默认选项（仍需手动点击）。

## 技术与安全决策

遇到需要二选一的技术或安全决策（例如是否阻塞发布、测试白名单还是不暴露字段），先调用子代理 `release-decider`（`.claude/agents/release-decider.md`），按它的结论执行；它自己认为必须由人决定的事项，再交给维护者。拿不准时选更安全、更保守的方案。

## 测试机

共 5 台测试机，具体 SSH 别名和对应角色见本地的 `CLAUDE.local.md`（该文件被 `.gitignore` 忽略，不提交；换了电脑需要重新创建）。**它们都是纯测试机，可以随意卸载、删数据、重装**：

- 1 台 Debian 12，作 Server。
- 2 台 Debian Agent（Debian 12 和 Debian 13）。
- 2 台 Alpine 3.24 Agent（OpenRC）：一台 128 MiB 内存（arm64，cgroup 限制，用来验证内存是否稳定），另一台 512 MiB（SSH 经常超时，已决定不再尝试）。

使用规则：
- 只做 404-probe 的安装和验证，不改动与本项目无关的系统配置和服务；安装系统软件、改防火墙、重启机器要先问。
- 安装器的角色选择、管理员密码、注册 token 都从 `/dev/tty` 读取。**密码和 token 由维护者自己输入**，不要代填。注册 token 用 `404-probe-install enroll <名字>` 在 Server 上生成，写到 Server 上权限 600 的文件里，不在对话中明文显示。
- Server 只监听 `127.0.0.1:8080`；Agent 用 HTTP 回环地址接入时，通过 `ssh -R 18080:127.0.0.1:8080` 反向隧道（Web 页面用 `ssh -L 8080:127.0.0.1:8080` 本地转发），Agent 的 `--server` 填 `http://127.0.0.1:18080`。这只是测试用法，线上应使用 HTTPS 地址。验证结束后要关闭隧道。
- 不要把主机名之外的连接信息（IP、密钥）、密码、token 写进仓库、说明、文档或提交。第三方页面的原始抓取（例如 YouTube 页面）只保留脱敏后的最小样本，使用后删除原始文件。
- Alpine 只支持 Agent，不提供 Server 和自动升级；新装前先确认 OpenRC 已完整启动，工具为 GNU 版本；不要把产物放在 `/tmp`（tmpfs 占内存）。

## 待办

v1.0.4 的待办在 GitHub Issues #3 到 #7：M12-B 恢复重试不幂等、ipify 外联开关、YouTube 欧盟 consent 与失败原因细分、正常升级流程的 5 秒稳定检查、`install.sh` 非交互安装。
