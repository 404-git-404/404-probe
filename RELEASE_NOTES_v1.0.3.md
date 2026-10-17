# 404-probe v1.0.3 Stable

本版在 v1.0.2 基础上整合已验收的 M05-TCP-01B/02B、M10、M11、M12-B 成果。此前的 v1.0.3-rc.1 候选未单独发布。M14–M19、评分与大图新方案不在本次范围。

## 安装与升级

Debian 12/13 systemd，root SSH 固定版本入口：

```bash
curl -fsSL https://github.com/404-git-404/404-probe/releases/download/v1.0.3/install.sh | PROBE_404_VERSION=v1.0.3 bash
```

这是无参数入口。新主机选择 Server 后按提示输入 HTTPS 地址及管理员密码；已有受支持 Server（含 v1.0.2）进入目标版本确认，输入 `y` 才升级。不要追加 `server` 参数。现有 Agent 不会被此命令自动升级。安装器验证官方资产、校验清单、平台、版本/Commit、数据库与磁盘空间，保留配置、凭据、Agent 绑定、监听地址及服务状态。

先升级 Server，再通过 Web 逐台升级具备正常 Updater 的 v1.0.2/v1.0.1 Agent，确认目标 v1.0.3 Stable。默认只显示 Stable；预发布需在单台 Agent 的操作中显式确认。旧 Agent 不理解新增字段时显示未知/不支持，不会冒充新测量。

旧 beta.1/beta.2 Agent，以及缺失 Updater 的 v0.9.3/v1.0.0 Agent，仍使用已发布的固定 **v1.0.1** 历史桥接：

```bash
curl -fsSL https://github.com/404-git-404/404-probe/releases/download/v1.0.1/install.sh | PROBE_404_VERSION=v1.0.1 bash -s -- upgrade-agent
```

桥接要求受支持 Debian/systemd、root 与预先安装的 Python3，保留原 ID、凭据、epoch、配置和状态；完成真实健康报告后再从 Web 升到 v1.0.3。不要用 v1.0.3 替换旧桥接验证器、runner 或恢复标记。中断后按错误提示重跑原固定入口，保留事务证据；不删除配置重装。

## 已完成范围

- M05-TCP-01B：展示真实有效 RTT，部分分钟用空心点表示；失败或缺测不转为零，也不跨缺口连线。
- M05-TCP-02B：IPv4 TCP Mini 小图使用真实 Log10 刻度（基础 5–2000ms，2/5/10 扩展）。任一运营商触发的分钟 P95 高值告警会持久化，同一自然日保持，次日出现新的严格低值才解除。量化为 0、低于 5ms 和历史超界各有独立标记；覆盖不足会明确提示，不当作确认正常。ICMP 语义不变。自然日由 Server 按浏览器 IANA 时区计算，含 23/25 小时的夏令时日，不支持的日期规则返回不可判定。
- M10：Agent 实际路径的公网 IPv4/IPv6 出口观测，以及状态与过期展示。仅在 Server 声明能力时上报；旧 Server 返回 400 时 Agent 自动去掉该字段重试。Web API 不暴露上报序号。
- M11：YouTube 页面地区证据，以及未知、过期、失败、离线的分别表达与原因文案；卡片与历史页共用同一展示逻辑。不代表出口归属，也不验证视频播放或 Premium 解锁。
- YouTube 地区识别修正：v1.0.2 以页面中出现 `www.google.cn` 字样判断为 CN，页面里一个无关的广告链接就可能使 Agent 误报 CN。本版改为只读取页面配置中实际声明的地区（`INNERTUBE_CONTEXT_GL` 等），并仅采信可解析的字面量配置；不再扫描任意文本，也不执行脚本。因此升级后部分设备的 YouTube 地区会从 CN 变为真实地区（例如 US），这是修正而非退化。页面结构有歧义或无法解析时仍显示未知，不猜测。
- M12-B：Server 升级时，离线 helper 先于候选 Server 启动。恢复阶段核验完整备份 SHA256，先用旧程序读取数据库，再检查实际 PID/可执行文件/HTTP 响应并保持至少 5 秒稳定；失败时保留 pending 状态，避免 SQLITE_BUSY 下重复进入迁移，也不再只凭 start 返回 0 判断恢复成功。
- 版本排序为同一基础版本 `beta.N < rc.N < Stable`，数字序号按数值比较。默认入口与 latest 只选择 Stable；预发布确认界面按实际版本显示 Beta 或 RC。
- Agent 升级：认领协议只取决于本地 Updater 的 v2 能力应答。v1.0.2 的 Updater 不识别新增的 RC 能力探测，此前这会让 v1.0.3 Agent 退回协议 1 而无法完成健康确认；现已修复。RC 证明仅继续用于对 Server 上报的 RC 感知标记。
- 既有套餐、单位和统计口径保持不变。

## 数据库与恢复

数据库目标 schema 为 **29**（27→28→29）：28 新增 `agent_public_egress`，29 新增 TCP 告警的两张表。高事件窗口锚定最新高分钟，每台 VPS 最多保留 4322 行，不随无数据时间过期。初始回填分批进行；升级前已被清理的分钟和临时尖峰无法追溯补造，覆盖不足时使用默认字色并明示。

升级事务先停止 Server，确认无数据库打开句柄，保存一致的 DB/WAL/SHM、旧二进制、配置和服务文件，生成受保护的 SHA256 备份清单，再迁移、检查完整性、验证实际新服务。失败按已记录事务恢复旧程序及匹配的一致性数据库；未确认恢复时保留失败日志与状态。

不能仅将旧 Server 二进制换回去读取 schema 29。保留 `/var/lib/404-probe-upgrade/backup` 和同目录事务状态，按安装器恢复提示操作；若升级已成功且需要人工回退，先停服务并使用同一次完整备份恢复程序、数据库与配置，不合并新旧 DB/WAL。恢复点之后的新写入可能丢失，不承诺零损失。不要删除正在使用的 WAL/SHM，也不要自行删除 pending 与失败证据。安装器为数据库及 WAL/SHM 预留三倍文件空间另加 128MiB，实际可用空间不足时在升级前拒绝。

检查命令：

```bash
/usr/local/bin/404-probe-server version --json
runuser -u 404-probe -- /usr/local/bin/404-probe-server database verify --db /var/lib/404-probe/404-probe.db
systemctl --no-pager --full status 404-probe-server.service
journalctl -u 404-probe-server.service -n 100 --no-pager
```

正式四个 Linux 二进制必须对应同一个真实干净 Commit（dirty=false）。本版使用既有 schema2 Release metadata，最低 Server v1.0.1、升级协议 2；SHA256SUMS 包含四个二进制、metadata 及安装器六条，自身另行核对，不含自身哈希。

## 支持与验证边界

支持 Debian 12/13 systemd Server/Agent 和 Linux amd64/arm64 资产。编译成功或跨编译不等于所有架构的安装、系统服务、网络或长期运行验证通过；原生 ARM Debian/systemd 生命周期不得由跨编译推定通过。

Alpine 3.24.x OpenRC 仅支持 Agent 基础监控，专用质量采集禁用；新装须预先具备完整 OpenRC、Bash、GNU coreutils/tar、util-linux、shadow、curl、CA。Alpine Server、已有 Alpine Agent 自动升级/回滚、安全观察及远程永久删除仍不支持，延期至 v1.1。测量上限保持 6000ms。

旧 Node 测试套件保留逐项基线失败，不宣称全仓测试无失败：`scripts/test-p1-agent-state.cjs`、`scripts/test-p2c-google-ui.cjs` 的页面沙箱没有加载 `latency-mini.js`，因此引用 `LatencyMini` 的用例报 `ReferenceError`；`scripts/test-m02-real-chain.cjs` 依赖本机路径与预置产物，并要求全新的输出目录。这三者在 v1.0.2 上失败情况完全相同，真实浏览器中页面无控制台错误。补充加载真实的 `latency-mini.js` 后，p2c 全部通过；p1 在 v1.0.3 为 67 通过 5 失败，在 v1.0.2 为 68 通过 4 失败：四项套餐用例两个版本都失败，多出的一项是旧断言过时（5xx 现在提示“提交结果未确认，正在核实；不会自动重发”）。`internal/agent` 的个别 Go 测试在 Windows 负载下偶发超时（含 v1.0.2），发布前以 Linux 全量运行为准。

本版不自动升级用户设备。已发布历史版本的说明及其验收边界保持原文。
