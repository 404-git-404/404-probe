# 404-probe v1.0.5 Stable

本版在 v1.0.4 基础上重做网络质量：移除全部内置目标，每张卡片改为 3 个由用户自己填写的卡槽；同时修正 YouTube 对被 Google 判为中国的 IP 的地区显示，加入 `licenses` 子命令，并带来安装器和界面的若干修复。此前的 v1.0.5 候选版本未单独发布。

## 升级前必读

先读这一节。

- **升级顺序：先升级 Server，再升级 Agent。** Agent 通过 Web 逐台升级；升级前，旧版 Agent 继续可用。

- **网络质量改版：全部内置目标（zstatic）已移除。** 每张卡片改为 3 个用户自己填写的卡槽。**升级后所有网络质量检测都是关闭的**，需要重新填写目标并打开总开关。具体时点：Server 升级只关闭 Server 上保存的设置；Agent 离线或与 Server 断开（包括 Server 停机升级期间）时，仍按断开前的最后一份配置继续检测，直到重新连上、拿到已停用的配置为止（网络质量配置没有租期，见“已知限制”）。所以“所有检测关闭”指 Server 升级完成、Agent 重新连上之后，不是升级的那一刻。旧的内置目标的历史数据会保留，查看入口计划在 v1.0.8 的设备详情页改版中提供。
- **Agent 需要升级。** v1.0.4 及更早的 Agent 在卡片上显示“需升级 Agent”，不能使用自定义目标；资源监控照常。升级 Agent 后会自动协商出新能力。
- **YouTube 检测修正。** v1.0.3 和 v1.0.4 会把被 Google 判为中国的 IP 误报为页面里的其他地区（例如 US）。现在这类 IP 显示红色“地区 CN”，所以部分设备的显示会因此改变，这是预期的结果。同时更正 v1.0.3 说明里“页面中的 google.cn 只是无关广告链接”的说法：对被 Google 判为中国的 IP，它并不是无关链接。
- **数据库迁移 33、34（schema 34）。** 升级前自动备份；回退到旧版本只能恢复这份备份，不能只换回旧二进制（旧二进制遇到 schema 34 会拒绝启动）。详见“数据库与恢复”。

## 安装与升级

Debian 12/13 systemd，root SSH 固定版本入口：

```bash
curl -fsSL https://github.com/404-git-404/404-probe/releases/download/v1.0.5/install.sh | PROBE_404_VERSION=v1.0.5 bash
```

这是无参数入口。新主机选择 Server 后按提示输入 HTTPS 地址并设置管理员密码（默认生成随机密码）；已有受支持 Server（含 v1.0.4）进入目标版本确认，输入 `y` 才升级。安装器验证官方资产、校验清单、平台、版本/Commit、数据库与磁盘空间，保留配置、凭据、Agent 绑定、监听地址及服务状态。现有 Agent 不会被此命令自动升级。

先升级 Server，再通过 Web 逐台升级具备正常 Updater 的 Agent，确认目标 v1.0.5 Stable。默认选中 Stable；预发布需在单台 Agent 的操作中显式确认。旧 Agent 不理解新增字段时显示未知/不支持，不会冒充新测量。

旧 beta.1/beta.2 Agent，以及缺失 Updater 的 v0.9.3/v1.0.0 Agent，仍使用已发布的固定 **v1.0.1** 历史桥接，步骤与 v1.0.3、v1.0.4 相同，不要用 v1.0.5 替换旧桥接验证器、runner 或恢复标记。

## 新功能

- **网络质量设置。** 每张卡片 3 个卡槽，目标由用户自己填写；每个卡槽可选内置或自定义的地区和 ISP 标签，4 个检测子项可分别开启，检测频率 30–300 秒。
  - 总开关立即生效；“保存”只保存设置，不会开启检测。
  - 有未保存的修改，或设置与其他窗口的修改冲突时，页面会给出提示，不会悄悄覆盖。
  - 目标只允许公网地址：私网、环回、链路本地、CGNAT 等地址会在连接前被 Agent 拒绝。
- **各种状态的显示。** 卡片和曲线对“未开启”“需升级 Agent”“无当前 TCP 检测”“Agent 不支持”“环境不可用（例如容器里没有 ICMP 权限）”等状态分别显示，不再互相混淆。
- **`licenses` 子命令。** 运行 `404-probe-server licenses` 或 `404-probe-agent licenses` 可以查看第三方声明。

## 修复与改进

- 安装器：一键安装注册失败时的提示更准确；运行环境检测期间有进度提示（一键安装、接管已有 Agent 和升级时都会出现）；拒绝非法 UTF-8 的密码。
- 公网 IP 检测的冷却提示带日期。
- 历史图的 Y 轴刻度修正（刻度位数不再混乱）。
- 设备详情页不再在刷新数据时弹回顶部。
- 移除 gg-status 的拷贝，以及 Search、Sign-in、Gemini 的残留代码。
- Agent 升级演练脚本（`scripts/test-agent-upgrade-systemd.sh`）可以指定起止版本；新增升级前后的状态快照脚本（`scripts/agent-state-snapshot.py`），用来核对升级只改变了版本、二进制和进程。

## 已知限制

- TCP Mini 的告警过于敏感，计划在 v1.0.7 重新设计。
- **NAT64 的目标检查不是完整的。** Agent 用 `ipv4only.arpa` 自动发现网络的 NAT64 前缀（启动时查询一次，之后每 10 分钟刷新），对落在这些前缀里的地址再检查其中内嵌的 IPv4 是否为公网地址；知名前缀 `64:ff9b::/96` 不依赖发现，始终按内嵌的 IPv4 检查。下面两种情况下，Agent 识别不出 IPv6 地址里内嵌的私网 IPv4，NAT64 这一层检查不会拦截它（其他公网地址检查照常进行）：
  - 没有 DNS64、查不到 `ipv4only.arpa` 的 NAT64，例如只通过 RA PREF64 通告前缀，或手工配置的转换器；
  - 前缀发现失败，或尚未完成（例如 Agent 刚启动、解析器故障且之前从未成功）。刚启动时自定义目标最多等待 10 秒的首次发现，之后无论是否完成都开始检测；刷新失败时沿用上一次的发现结果。
- NAT64 相关的另外两种情况会拒绝目标，记为 `unsafe_address`：只使用本地用途前缀 `64:ff9b:1::/48` 的 IPv6-only 主机，经它转换的目标一律被拒绝；发现到的前缀过多时（超过 8 个，第 9–64 个前缀内的地址被拒绝；超过 64 个前缀，或解析结果超过 128 个，则拒绝所有 IPv6 目标）。
- 网络质量配置没有租期：Agent 与 Server 断开时，按最后一份配置继续检测。
- Alpine 不提供网络质量检测。
- 旧的内置目标的历史虽然保留，但 v1.0.5 里没有查看入口（计划在 v1.0.8 提供）。
- 登录没有失败次数限制（只限制同时进行的密码哈希数）；已有的短密码（少于 16 位）请尽快更换，步骤见 README。
- 已撤销的 Agent 记录目前不能从面板彻底删除。

## 数据库与恢复

数据库目标 schema 为 **34**（32→33→34；更早的版本依次经过 30、31、32）。迁移 33、34 各自在同一事务内完成，失败不会留下半成品。迁移 34 会关闭所有网络质量检测并结束正在进行的区间，所以升级后、重新配置并开启之前，网络质量的告警不会继续出现。

升级事务先停止 Server，确认无数据库打开句柄，保存一致的 DB/WAL/SHM、旧二进制、配置和服务文件，生成受保护的 SHA256 备份清单，再迁移、检查完整性、验证实际新服务；失败按已记录事务恢复旧程序及匹配的一致性数据库。

**不提供降级迁移。** v1.0.4 及更早的 Server 二进制遇到 schema 34 会拒绝启动。需要回退时，先停服务，使用同一次完整备份恢复程序、数据库与配置（`/var/lib/404-probe-upgrade/backup` 和同目录事务状态），不合并新旧 DB/WAL；恢复点之后的新写入可能丢失，不承诺零损失。不要删除正在使用的 WAL/SHM，也不要自行删除 pending 与失败证据。

Agent 不提供降级路径。

检查命令：

```bash
/usr/local/bin/404-probe-server version --json
runuser -u 404-probe -- /usr/local/bin/404-probe-server database verify --db /var/lib/404-probe/404-probe.db
systemctl --no-pager --full status 404-probe-server.service
journalctl -u 404-probe-server.service -n 100 --no-pager
```

正式四个 Linux 二进制必须对应同一个真实干净 Commit（dirty=false）。SHA256SUMS 包含四个二进制、metadata 及安装器六条，自身另行核对，不含自身哈希。

## 支持与验证边界

支持 Debian 12/13 systemd Server/Agent 和 Linux amd64/arm64 资产，包括 Podman 等容器里的 Agent。编译成功或跨编译不等于所有架构的安装、系统服务、网络或长期运行验证通过；原生 ARM Debian/systemd 生命周期不得由跨编译推定通过。

Alpine 3.24.x OpenRC 仅支持 Agent 基础监控，没有 systemd 沙箱，专用质量采集禁用；Alpine Server、已有 Alpine Agent 自动升级/回滚、安全观察及远程永久删除仍不支持，延期至 v1.1。测量上限保持 6000ms。

旧 Node 测试套件保留逐项基线失败，不宣称全仓测试无失败：`scripts/test-p1-agent-state.cjs`、`scripts/test-p2c-google-ui.cjs`、`scripts/test-m02-real-chain.cjs`、`scripts/test-n3-network-quality.cjs`、`scripts/test-p4b2-traffic-chart.cjs` 和 `scripts/test-p5a2-device-details.cjs` 在 v1.0.3 上失败情况相同，不是本版回归。`internal/agent` 和 `internal/storage` 各有一个 Go 测试在 Windows 负载下偶发失败，发布前以 Linux 全量运行为准。

本版不自动升级用户设备。已发布历史版本的说明及其验收边界保持原文。
