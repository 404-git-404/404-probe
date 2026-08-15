# 404-probe V0.1

404-probe 是一个从零实现的轻量 Linux 主机探针。Agent 只主动向 Server 上报，不监听端口；Server 校验每台机器的独立 token，把最新状态和 1 分钟历史聚合写入 SQLite，并提供内嵌 Web Dashboard。

V0.1 的正式生产部署支持范围是 Linux Agent 与 Linux Server。Windows 可用于开发和编译检查，但不承诺 Agent epoch 状态文件具备与 Linux `fsync` + directory sync 相同的掉电持久化保证。

```text
Linux Agent --HTTPS JSON POST--> Go Server --> SQLite (WAL)
                                      |
                                      +--> Embedded HTML/CSS/JS + SSE
```

V0.1 以单 Server、少量到中等数量 VPS 的可靠基础监控为目标。产品代码不会依赖或修改 `research/` 中的参考仓库。

## 构建

需要 Go 1.23 或更新版本：

```bash
go test ./...
go build -o 404-probe-agent ./cmd/agent
go build -o 404-probe-server ./cmd/server

mkdir -p build/linux-amd64 build/linux-arm64
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o build/linux-amd64/404-probe-agent ./cmd/agent
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o build/linux-amd64/404-probe-server ./cmd/server
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o build/linux-arm64/404-probe-agent ./cmd/agent
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o build/linux-arm64/404-probe-server ./cmd/server
```

SQLite 驱动是纯 Go 实现，因此不需要 CGO。

## 启动 Server

```bash
./404-probe-server serve \
  --listen :8080 \
  --db /var/lib/404-probe/404-probe.db \
  --offline-timeout 30s
```

Server 的 HTTP 监听适合放在 Caddy、nginx 或其他 TLS 反向代理后面。生产环境应对外只暴露 HTTPS；默认 Agent 会拒绝明文 HTTP。Dashboard 位于 `/`，上报端点为 `/api/v1/report`。

Server 配置项：

| Flag | 默认值 | 说明 |
|---|---:|---|
| `--listen` | `:8080` | HTTP 监听地址 |
| `--db` | `404-probe.db` | SQLite 文件路径 |
| `--offline-timeout` | `30s` | 有效上报超过该时间未到达即显示离线 |

数据库目录在自动创建时使用 `0700`，数据库主文件收紧为 `0600`。仍建议使用专用的非 root 系统用户运行 Server。

## 创建、查看与撤销 Agent

管理命令直接操作同一个 SQLite 文件：

```bash
./404-probe-server agent add singapore-01 --db /var/lib/404-probe/404-probe.db
./404-probe-server agent list --db /var/lib/404-probe/404-probe.db
./404-probe-server agent revoke <agent-id> --db /var/lib/404-probe/404-probe.db
```

`agent add` 会输出 Agent ID 和随机 256-bit token。token 只显示这一次；Server 仅保存 SHA-256 hash。每台 Agent 必须使用自己的 token，撤销一台不会影响其他机器。

## 启动 Agent

建议把凭据放进权限为 `0600` 的环境文件，避免 token 出现在进程命令行或 shell history：

```bash
sudo install -m 600 /dev/null /etc/404-probe-agent.env
sudo sh -c 'cat > /etc/404-probe-agent.env' <<'EOF'
PROBE_404_SERVER=https://probe.example.com
PROBE_404_AGENT_ID=<agent-id>
PROBE_404_TOKEN=<one-time-token>
PROBE_404_STATE=/var/lib/404-probe-agent/epoch
EOF

set -a
. /etc/404-probe-agent.env
set +a
./404-probe-agent --interval 10s --timeout 8s
```

Agent 配置可通过以下 flags 提供，URL、ID、token 也可分别使用 `PROBE_404_SERVER`、`PROBE_404_AGENT_ID`、`PROBE_404_TOKEN` 环境变量：

| Flag | 默认值 | 说明 |
|---|---:|---|
| `--server` | 环境变量 | Server 基础 URL |
| `--agent-id` | 环境变量 | Server 创建的 Agent ID |
| `--token` | 环境变量 | 独立 Agent token |
| `--interval` | `10s` | 采集/上报间隔 |
| `--timeout` | `8s` | 单次 HTTP timeout |
| `--state` | `404-probe-agent.state` | 持久化单调 epoch 的状态文件；也可用 `PROBE_404_STATE`；相对默认值仅适合开发 |
| `--network-include` | 空 | 逗号分隔的网卡 glob；非空时只统计匹配项 |
| `--network-exclude` | 空 | 逗号分隔的额外排除 glob |
| `--allow-insecure-http` | `false` | 仅本地或开发环境显式允许 HTTP |

默认排除 `lo`、`docker*`、`veth*`、`br-*`、`virbr*`、`tun*`、`tap*`、`wg*`、`tailscale*`、`cni*` 和 `flannel*`。显式 include 可以选中默认排除的接口，显式 exclude 最终优先。

Agent 网络失败时只记录错误并在下一个周期采集新数据，不排队旧报告。连接恢复后会自动继续。每次进程启动时，Agent 会在状态文件中原子递增并同步写盘一个单调 epoch，同时生成新 session ID；进程内 sequence 严格递增。Server 只用 epoch 与 sequence 判断新旧，Agent 系统时间不参与 session 代际判定。生产部署必须通过 `--state` 或 `PROBE_404_STATE` 使用可写、跨重启保留的稳定绝对路径。

## systemd 示例

```ini
[Unit]
Description=404-probe Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
EnvironmentFile=/etc/404-probe-agent.env
StateDirectory=404-probe-agent
ExecStart=/usr/local/bin/404-probe-agent --interval 10s --timeout 8s --state /var/lib/404-probe-agent/epoch
Restart=always
RestartSec=5
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

Agent 为读取常规 `/proc` 与系统统计通常不需要 root，可根据目标发行版改为低权限用户。

## V0.1 指标与数据口径

Agent 采集 hostname、OS、架构、boot ID、uptime、CPU、Load 1/5/15、RAM、Swap、根文件系统 `/` 的磁盘使用量，以及筛选后的网卡原始 RX/TX counter。Linux boot identity 直接使用 `/proc/sys/kernel/random/boot_id`；读取失败时跳过该次样本，不在 UUID 与不稳定 fallback 之间切换。

Server 以收到有效报告的实际时间差计算速率。Agent 首次被 Server 纳管时，当前 raw counter 只建立基线，永久累计的增量为 0；之后正常增长累加差值。已纳管 Agent 的 boot ID 改变时，新 boot 首次报告的 raw counter 作为该 boot 已产生的新增量计入永久累计；counter 回退也按安全复位处理，并把复位点速率置零。流量基线、永久累计、当前 epoch、session 与 sequence 在同一 SQLite 事务中持久化，因此 Agent restart（boot ID 不变）不会重复计数，Server 重启不会清零，旧 epoch、旧 sequence 或其他迟到报告也不能覆盖当前状态。

最新状态保存在 Server 内存，并同步持久化。历史只保存每分钟的平均 CPU、RAM、Swap、Disk、Load 和 RX/TX rate，以及该分钟最后的累计流量；默认每小时清理 30 天以前的数据。SQLite 开启 WAL、外键和 5 秒 busy timeout。

首页显示所有 Agent 卡片、在线状态、系统使用率、实时/累计流量和最后上报时间；详情页使用 Canvas 展示最近 24 小时 CPU、RAM、RX rate、TX rate。实时更新使用 SSE，浏览器断线后会自动重连，同时保留 15 秒 polling 作为状态/离线刷新机制。

## 当前明确不支持

V0.1 不包含 WebShell、文件管理、远程命令、SSH、Docker 管理、DDNS、隧道、多用户/RBAC、告警通知、自动升级、Ping/Loss/Jitter/DNS/UDP/TLS 等网络协议探测、地图、主题系统、Prometheus/Grafana/VictoriaMetrics、Redis/PostgreSQL，也不包含 React/Vue 或 Node 构建链。

当前已知限制：磁盘指标只表示根文件系统 `/`；Server 自身不直接终止 TLS，需要生产反向代理；没有 Web 管理后台；历史是固定 1 分钟聚合；单 SQLite 写路径适用于轻量部署而不是超大规模集群；接口 glob 匹配区分大小写。
