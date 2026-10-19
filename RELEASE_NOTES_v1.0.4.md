# 404-probe v1.0.4 Stable

本版在 v1.0.3 基础上加入一键安装 Agent、容器环境支持、可开关的公网 IP 检测、网络质量曲线与目标的重做、YouTube 欧盟同意页识别，以及登录与安全方面的多项收紧。此前的 v1.0.4-rc 候选未单独发布。

## 升级须知

先读这一节。升级本身与 v1.0.3 相同：先升级 Server，再通过 Web 逐台升级 Agent（旧版 Agent 在升级前继续可用）。

- **公网 IP 检测升级后默认关闭。** 需要在设备详情的“设备与管理”页里手动开启（每台设备单独开关），开启后才会访问第三方公网 IP 服务。
  - 旧版（v1.0.3）Agent 在开关关闭时不再检测公网 IP：Server 不再向它声明该能力，它会停止自己的检测。
  - 开关打开时，旧版 Agent 恢复自己的检测（约每 30 分钟一次），但没有“立即检测”按钮；把 Agent 升级到 v1.0.4 之后，才改为由 Server 调度的任务式检测（每 7 天一次，另有手动“立即检测”，24 小时冷却）。
- **“手填地址”已移除。** 网络质量的三条线路只能选目录里的目标；升级后已有的手填线路变为“未设置”并停止检测，历史数据保留，需要重新选择地区。
- **登录不再锁定。** 错误密码不会再锁住任何人（包括在 Cloudflare Tunnel 后所有请求都来自同一个本地地址的部署）；改为同一时间最多 2 个密码哈希校验，名额已满时直接返回 429 busy，不排队。
  - 新设置的管理员密码至少 16 个字符；新安装默认生成 43 个字符的随机密码，只在终端显示一次，也可以选择自己输入。
  - **如果现有管理员密码较短（少于 16 位），强烈建议更换为强密码。** 面板内修改密码的功能计划在后续版本提供，目前请按 README 的“手动更换管理员密码”操作。
- **数据库 schema 升到 32**（29→30→31→32）。30 新增公网 IP 检测控制表，31 把手填线路改为未设置，32 新增一键安装的注册 token 表。升级、恢复与回退的注意事项见下文“数据库与恢复”。

## 安装与升级

Debian 12/13 systemd，root SSH 固定版本入口：

```bash
curl -fsSL https://github.com/404-git-404/404-probe/releases/download/v1.0.4/install.sh | PROBE_404_VERSION=v1.0.4 bash
```

这是无参数入口。新主机选择 Server 后按提示输入 HTTPS 地址并设置管理员密码（默认生成随机密码）；已有受支持 Server（含 v1.0.3）进入目标版本确认，输入 `y` 才升级。安装器验证官方资产、校验清单、平台、版本/Commit、数据库与磁盘空间，保留配置、凭据、Agent 绑定、监听地址及服务状态。现有 Agent 不会被此命令自动升级。

先升级 Server，再通过 Web 逐台升级具备正常 Updater 的 Agent，确认目标 v1.0.4 Stable。默认选中 Stable；预发布需在单台 Agent 的操作中显式确认。旧 Agent 不理解新增字段时显示未知/不支持，不会冒充新测量。

旧 beta.1/beta.2 Agent，以及缺失 Updater 的 v0.9.3/v1.0.0 Agent，仍使用已发布的固定 **v1.0.1** 历史桥接，步骤与 v1.0.3 相同，不要用 v1.0.4 替换旧桥接验证器、runner 或恢复标记。

## 新功能

- **一键安装 Agent。** 面板“添加 Agent”→“一键安装”：输入名称后生成一条安装命令，带复制按钮和 30 分钟倒计时，可在使用前作废。命令里的注册 token 一次性、30 分钟有效、与填写的名称绑定，数据库只存哈希；同一时间最多 10 条未使用的命令。关闭对话框后命令不能再次查看。
  - 命令形如 `curl … | PROBE_404_ROLE=agent PROBE_404_SERVER=… PROBE_404_ENROLL_TOKEN=… PROBE_404_AGENT_NAME=… bash`，变量写在 `bash` 前面，不出现在任何进程的参数里；请以 root 执行（非 root 先 `sudo -i`），并要求该版本已发布到 GitHub。开发版或 dirty 构建的 Server 不提供一键安装。
  - 安装器的非交互模式：`PROBE_404_ROLE=agent` 加上 `PROBE_404_SERVER`、`PROBE_404_ENROLL_TOKEN`（或 `PROBE_404_ENROLL_TOKEN_FILE`）和 `PROBE_404_AGENT_NAME`；只给出一部分变量会报错并列出缺少的变量，Server 角色只能交互安装。原来的手动安装流程（粘贴 enrollment value）保持不变。
- **支持 Podman/LXC 等容器环境。** 安装前先在本机实测 systemd 的沙箱选项，只去掉当前环境不能用的那些（例如 Podman 里的 ProtectKernel*、seccomp 类选项），其余加固保持不变；去掉的选项会在安装输出里列出，结果记录在配置目录的 `sandbox.conf`（仅供参考，安装和升级时都会重新探测）。注册 token 只在探测通过之后才会被使用。
  - 容器里 CAP_NET_RAW 常常不可用：此时 ICMP 检测在面板上显示“不可用（当前环境无 ICMP 权限）”，TCP 不受影响。
  - 本地安全采集在受限环境里如果起不来，Agent 照常安装，安全观察显示“不可用：<原因>”。没有 sing-box 日志时显示中性提示“未发现 sing-box 日志（本机可能未安装或未运行 sing-box）”，不再显示成采集失败。
- **公网 IP 检测：手动开关与立即检测。** 每台设备单独开关，默认关闭；开启后由 Server 调度（每 7 天一次，失败按 1/5/15/30 分钟退避，重连触发的检测至少间隔 6 小时），设备详情里可“立即检测”，24 小时冷却，并显示 V4/V6 结果、上次与下次检测时间。地址默认半隐藏（IPv4 保留前两段，IPv6 保留前两组），可切换显示完整地址并复制；卡片只显示可用地址族的 V4/V6 标签。
- **网络质量曲线。** 迷你曲线改为 5 分钟滚动 P50（每分钟一个点），固定平方根刻度 0–1000ms，超过 1000ms 用顶边虚线表示；没有选择地区时曲线和方块视图都显示“未选择地区”，不再误显示“Agent 不支持”。
- **YouTube。** 地区按 Premium 官方可用名单着色；遇到欧盟隐私同意页时显示绿色“地区 EU”（具体国家未知，欧盟/欧洲经济区、英国、瑞士均在 Premium 可用名单内）。页面里与地区无关的 ytcfg 脚本解析失败不再让整页失效。

## 修复和改进

- Server 升级：正常升级流程也执行 5 秒稳定检查（#6）；恢复阶段在每个阶段都幂等，重试可以继续（#3）。
- 网络质量探测只访问公网地址：目标是私网、环回、链路本地（含云元数据地址）、CGNAT、文档地址段等时，Agent 在连接前拒绝并记为 `unsafe_address`；知名 NAT64 前缀（64:ff9b::/96）按其中嵌入的 IPv4 判断。
- 工具链改为 go1.26.8，构建脚本会校验工具链版本、每个 ELF 的版本与提交、`vcs.modified=false`（#8）；仓库内文本文件统一为 LF，README 与 `jobs.js` 不再例外（#9）。
- 登录与密码：见“升级须知”；安装器和 `web password-hash` 都按字符（Unicode 码点）计数。
- 一键安装在注册 token 已被使用之后的任何失败，都会明确提示“注册 token 已被使用，请在面板重新生成命令”。
- 迁移 31 遇到无法解析的旧配置行时不再让 Server 启动失败，该线路改为未设置并记录日志。

## 已知问题

- 容器适配目前只覆盖 Agent 的 systemd 单元；Server 的 systemd 单元仍使用完整加固，Server 需要安装在 systemd 沙箱选项可用的环境里。
- 登录没有失败次数限制（只限制同时进行的密码哈希数），在线猜解的速度主要由密码强度决定；已有的短密码（少于 16 位）请尽快更换，步骤见 README。名额被持续占满时，合法登录也可能暂时返回 busy。
- 一键安装在注册成功之后、Agent 起来之前失败（例如下载产物失败）时，注册 token 已被使用，面板上会留下一条从未连接的同名 Agent 记录，请先把它撤销，再重新生成命令。
- ICMP 检测目标与 TCP 共用，目标提供方仅建议用于 TCP-Ping，部分节点可能禁 ICMP，此时 ICMP 丢包会误报；计划在后续版本调整。
- 升级恢复过程中如被打断、且旧 Server 已经启动并写入了数据，再次恢复时这部分数据会丢失（恢复总是还原升级前的完整备份）。
- 位于欧盟地区的主机，YouTube 会重定向到同意页，Agent 不会跟随，只显示“地区 EU”，不显示具体国家。
- Agent 升级演练脚本（`scripts/test-agent-upgrade-systemd.sh`）暂不可用，本版本以正式发布后的 Web 升级真机验证代替，脚本参数化放到 v1.0.5。
- 已撤销的 Agent 记录目前不能从面板彻底删除。

## 数据库与恢复

数据库目标 schema 为 **32**（29→30→31→32）。迁移在同一事务内完成，失败不会留下半成品。升级事务先停止 Server，确认无数据库打开句柄，保存一致的 DB/WAL/SHM、旧二进制、配置和服务文件，生成受保护的 SHA256 备份清单，再迁移、检查完整性、验证实际新服务；失败按已记录事务恢复旧程序及匹配的一致性数据库。

不能仅将旧 Server 二进制换回去读取 schema 32。保留 `/var/lib/404-probe-upgrade/backup` 和同目录事务状态，按安装器恢复提示操作；若升级已成功且需要人工回退，先停服务并使用同一次完整备份恢复程序、数据库与配置，不合并新旧 DB/WAL。恢复点之后的新写入可能丢失，不承诺零损失。不要删除正在使用的 WAL/SHM，也不要自行删除 pending 与失败证据。

检查命令：

```bash
/usr/local/bin/404-probe-server version --json
runuser -u 404-probe -- /usr/local/bin/404-probe-server database verify --db /var/lib/404-probe/404-probe.db
systemctl --no-pager --full status 404-probe-server.service
journalctl -u 404-probe-server.service -n 100 --no-pager
```

正式四个 Linux 二进制必须对应同一个真实干净 Commit（dirty=false）。SHA256SUMS 包含四个二进制、metadata 及安装器六条，自身另行核对，不含自身哈希。

## 支持与验证边界

支持 Debian 12/13 systemd Server/Agent 和 Linux amd64/arm64 资产，包括 Podman 等容器里的 Agent（沙箱选项按实测精简，见“新功能”）。编译成功或跨编译不等于所有架构的安装、系统服务、网络或长期运行验证通过；原生 ARM Debian/systemd 生命周期不得由跨编译推定通过。

Alpine 3.24.x OpenRC 仅支持 Agent 基础监控，专用质量采集禁用，没有 systemd 沙箱探测；Alpine Server、已有 Alpine Agent 自动升级/回滚、安全观察及远程永久删除仍不支持，延期至 v1.1。测量上限保持 6000ms。

旧 Node 测试套件保留逐项基线失败，不宣称全仓测试无失败：`scripts/test-p1-agent-state.cjs`、`scripts/test-p2c-google-ui.cjs`、`scripts/test-m02-real-chain.cjs`、`scripts/test-n3-network-quality.cjs`、`scripts/test-p4b2-traffic-chart.cjs` 和 `scripts/test-p5a2-device-details.cjs`（3 项）在 v1.0.3 上失败情况相同，不是本版回归。`internal/agent` 的个别 Go 测试在 Windows 负载下偶发超时，发布前以 Linux 全量运行为准。

本版不自动升级用户设备。已发布历史版本的说明及其验收边界保持原文。
