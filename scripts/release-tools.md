# 发布与测试机工具

## 发布准备

在干净的开发分支上用 Python 3.11+、Git、Bash、Node 及 go.mod 指定的 Go 运行。Windows 使用 Git Bash，race 还需要 PATH 中的 64 位 MinGW-w64 GCC。输出路径用仓库外的 ASCII 正斜杠绝对路径，无空格。

```powershell
python -B scripts/release.py all --version v1.4.0 --from-version v1.3.1 --out D:/release-v140 --notes D:/release-notes-v140.md
```

- 在 `--out/candidate` 建立独立克隆和提交。原分支保持干净。正式使用 Claude Code 审定的说明，标题为 `# 404-probe v1.4.0`，包含“支持从 v1.3.1 升级”和“手动替换二进制不保证”。候选预演可以省略 `--notes`，脚本会为版本和升级窗口生成简短说明。
- 升级起点从 R31 注册表读取，校对本地 tag 和 schema。v1.4.0 只支持从 v1.3.1 开始。测试候选可用临时正式格式版本。
- `build` 调用现有构建脚本。 `verify` 校对七个文件、LF、SHA-256、ELF 的 Go 版本和 Git 构建信息。单步重跑，例如 `python -B scripts/release.py verify --out D:/release-v140`。构建重跑把上一批文件保存在相邻目录。
- `go` 运行全仓 vet 和测试，`race` 用上个 tag 到候选提交的 Go 文件选包，`node` 沿脚本引用选取相关套件。浏览器套件沿用本地 Playwright/Chromium 配置。若测试要求候选 Server，请按套件提示设置其运行参数。日志与 `results.md` 保留首次结果及单步重跑。平台跳过、历史基线和可选夹具须在验收记录说明。
- 为 Linux 运行器创建具备 `sudo` 权限的非 root 用户，用该用户运行发布命令，并启用 IPv6 回环。动态安装器步骤还需要 `bubblewrap`、`strace`、Python 和无 404-probe 安装的 Linux 测试运行器。2 核机器上安装器步骤约需 1 小时，中途长时间没有输出是正常的。Windows 原生执行会将 Linux 动态夹具标为跳过。有本地 WSL 2 环境时可给同一命令加 `--wsl <发行版>`，也可在隔离的 Linux 运行器执行。
- 浏览器套件使用 `M02_PLAYWRIGHT` 指向 Playwright 或 Playwright Core 1.55 及以上版本。
- 准备命令只操作本地。发布时从干净候选克隆取准确提交，按 CLAUDE.md 推送、建 tag 和 release。

## 测试机

仅在维护者登记的测试主机执行。把别名、Agent 名称和连接参数设在本机不提交的环境或配置里。用 SSH config 别名及已核对的 host key，确保它们只指向测试机。Debian 测试机使用可非交互登录的 root SSH 别名，需提供 Python 3.11+。以下变量的值均留在本机。

| 本地环境变量 | 用途 |
|---|---|
| `PROBE_404_TEST_SERVER_SSH` | 测试 Server 的 SSH 别名 |
| `PROBE_404_TEST_AGENT_SSH` | 测试 Agent 的 SSH 别名 |
| `PROBE_404_TEST_AGENT_ORIGIN` | 由 Agent 访问测试 Server 的 HTTPS 或 HTTP 回环 origin |
| `PROBE_404_TEST_AGENT_NAME` | 新装 Agent 的注册名，供一次性 token 绑定 |

先离线演练。去掉 `--dry-run` 才会连接测试机。

```powershell
python -B scripts/test-machine-release.py server fresh --assets D:/release-v140/assets --dry-run
python -B scripts/test-machine-release.py server upgrade --from-version v1.3.1 --assets D:/release-v140/assets --dry-run
python -B scripts/test-machine-release.py agent fresh --assets D:/release-v140/assets --dry-run
python -B scripts/test-machine-release.py agent upgrade --from-version v1.3.1 --assets D:/release-v140/assets --dry-run
```

Server 新装使用默认回环地址及安装器随机密码。助手用 `PROBE_404_INSTALL_LOCAL_AGENT=y` 接受候选安装器的本机 Agent 默认选项，v1.3.1 会忽略该变量。脚本把密码写入测试 Server 的 `/root/404-probe-test-web-password`（root、0600），终端不显示。新装遇到现有安装或密码文件会停止。Server 升级检查起点，保留原有凭据和安装器的受保护备份，不自动轮换已有的上一份备份。Agent 新装由测试 Server 生成一次性 token，上传到目标的 0600 临时文件，成功或失败均清理两端。

Agent 升级使用测试 Server 正规 Web 路径，要求其已在运行精确候选提交、已有根专属随机密码文件，且同提交 release 资产可下载。助手按测试 Agent 的本机 ID 发起升级并核对最终版本、提交、二进制 SHA-256 和服务状态。评审时另用 `scripts/agent-state-snapshot.py` 做 R05 前后对照。测试机 SSH 隧道由本地维护，用完关闭。
