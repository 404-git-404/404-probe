# 404-probe v1.0.0

## 安装与升级

固定版本安装入口（Debian 12/13 systemd，新装 Server/Agent 或升级已有受支持 Server）：

```bash
curl -fsSL https://github.com/404-git-404/404-probe/releases/download/v1.0.0/install.sh | sudo PROBE_404_VERSION=v1.0.0 bash
```

v0.9.3 → v1.0.0：先在 Server 主机运行此命令并确认升级，再通过 Server Web 对具备 updater 能力的 Debian Agent 逐台升级。Server 安装器验证官方版本、commit、资产校验、数据库与磁盘空间，维护一致性备份及恢复事务；Agent updater 验证官方资产并等待健康版本上报，失败回滚。配置、凭据、Agent 身份及既有数据保留；候选或开发构建不能冒充正式版本。

数据库当前 schema 为 24。不要在迁移后直接换回旧 Server 二进制；恢复旧版本必须恢复匹配的一致性数据库备份。Web Agent 升级只替换二进制，不安装新增 root helper。缺少本地安全采集组件时，在完成 Agent 升级后用同版本 installer 的 `setup-security` 本地入口，见 README。

## 本版功能

- 更新设备卡片、详情、历史和管理操作；网络流量统一十进制 B/KB/MB/GB，内存、磁盘和 Swap 保持二进制单位。套餐日期、流量周期、续期与流量重置保留明确的时区、覆盖和幂等语义。
- 网络质量探测及有界历史/聚合，区分未知、间断与不完整数据；不把缺失数据补成成功。TCP 连接耗时包含解析，TCP 连接失败率不是物理丢包率；ICMP 的丢包率按探测结果统计，两者不混称。本版不发布真实网络质量评分，评分延期至 v1.1。
- 当前 Google 状态检测仅请求并展示 YouTube；不再主动检测 Gemini/Search/Signin。旧协议兼容槽及历史数据保留。升级前的旧 Agent 仍可能执行旧检测，不能把新 Server 的展示变化当成旧 Agent 已停止请求。
- 支持手动、单次、有界的国家/地区重取；失败不覆盖最近成功值，旧能力不足的 Agent 显示需升级。
- Debian 支持完整本地卸载和重复卸载，以及经过确认、具有受限 helper 能力且显式 opt-in 的远程永久删除流程。Web 二进制升级不会自动安装删除 helper 或开启能力。未知/失败/未核实不冒充删除成功；本地卸载与 Server 业务数据删除是不同操作。远程删除不是通用远程命令。
- 安全观察继续是固定目的、最小权限的本地 sing-box REALITY 日志聚合，不做防火墙封禁，也不上传原始日志。

## 修复与验收边界

修复 Agent 认证失效的后台请求生命周期：普通认证 401 会终止该次运行的后台工作并阻止新请求，等待人工修正配置和重启；不会自行反复注册或重置 epoch。明确 revoked 信号保持原清洁退出语义，外部目标/Clash 的 401 不是 Server 凭据失效。

改进 SQLite 查询取消、reader/Rows 生命周期与有界读取，回归覆盖真实取消命中、WAL 边界、checkpoint 进展和关闭后的清理。发布前最终 Linux 选择验收八组普通/race 通过，另有浏览器、安装/卸载及隔离升级/删除验收；这不等于全仓库每个平台全量重测或数日稳定性观察，也不能保证任意负载下 WAL 永不增长。不要删除运行中的 WAL/SHM。

## 平台限制

Debian 安装入口支持 12/13 systemd；本轮实机验收为 Debian 12，不宣称已在 Debian 13 做同样实机矩阵。

Alpine v1.0 仅支持 3.24.x、已真实启动的 OpenRC 环境和 Agent 基础功能，不支持 Server。需预先准备 Bash、GNU/coreutils、util-linux、shadow、curl、CA 和 GNU tar；安装器不替你升级系统或补包。仅有命令或容器文件不能代替完整 OpenRC 启动状态。Alpine 的自动升级/回滚、安全观察、远程永久删除均延期至 v1.1，不能通过环境变量绕过平台门禁。amd64/arm64 提供官方资产，但不同平台的构建通过不等于都做了相同运行时验收。

域名后缀策略默认关闭，迁移保留 exact-origin；管理员可通过 `sudo 404-probe-install domains` 本地菜单及 `domains add/list/remove/disable` 管理。修改实际策略会使既有 Web 会话失效；host-only cookie、同源请求和 CSRF 约束不变。
