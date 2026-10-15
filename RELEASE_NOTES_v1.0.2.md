# 404-probe v1.0.2 Stable

本版整合已验收的 M01–M09 范围成果及 M05-DV-UI02-R1 最终外观。M10–M13、网络质量详情三网大图、虚构评分不在本次范围；详情继续使用现有单目标 320px 历史图。

## 安装与升级

Debian 12/13 systemd，root SSH 固定版本入口：

```bash
curl -fsSL https://github.com/404-git-404/404-probe/releases/download/v1.0.2/install.sh | PROBE_404_VERSION=v1.0.2 bash
```

这是无参数入口。新主机选择 Server 后按提示输入 HTTPS 地址及管理员密码；已有受支持 Server（含 v1.0.1）进入目标版本确认，输入 `y` 才升级。不要追加 `server` 参数。现有 Agent 不会被此命令自动升级。安装器验证官方资产、校验清单、平台、版本/Commit、数据库与磁盘空间，保留配置、凭据、Agent 绑定、监听地址及服务状态。

先升级 Server，再通过 Web 逐台升级具备正常 Updater 的 Agent，确认目标 v1.0.2 Stable。旧 Agent 没有 staged_probe_v2 能力时显示未知/不支持，不将旧 DNS-inclusive 结果冒充新测量。显式单 Agent Beta 选择仍兼容，但本次没有创建新的 Beta Release。

旧 beta.1/beta.2 Agent，以及缺失 Updater 的 v0.9.3/v1.0.0 Agent，仍使用已发布的固定 **v1.0.1** 历史桥接：

```bash
curl -fsSL https://github.com/404-git-404/404-probe/releases/download/v1.0.1/install.sh | PROBE_404_VERSION=v1.0.1 bash -s -- upgrade-agent
```

桥接要求受支持 Debian/systemd、root 与预先安装的 Python3，保留原 ID、凭据、epoch、配置和状态；完成真实健康报告后再从 Web 升到 v1.0.2。不要用 v1.0.2 替换旧桥接验证器、runner 或恢复标记。中断后按错误提示重跑原固定入口，保留事务证据；不删除配置重装。

## 已完成范围

- M05 卡片 A/B/C 独立及整组双视图；B 使用真实近期 Server 接收观测，保留 5m/15m、零速率与缺口。C 为固定 IPv4 三网 TCP/ICMP 一分钟 RTT P95，保留 1–24h、历史平移与现在。完整“数字＋单位”Y刻度右对齐、两图实际绘图区协调，整组按钮固定在详情印章左侧，保留卡片尺寸与底栏。
- M06 当前状态过期/离线与已完成历史分开表达；SSE/轮询保留 DOM、控件和交互状态。
- M07 暖色界面与控件整理，保留原设备详情、YouTube、出站选择、套餐与续费功能。
- M08 staged_probe_v2 TCP 纯 Connect（不含 DNS）及 ICMP 实际 Echo RTT/三 Echo 完整窗口分母；失败、未执行、无 RTT、丢包分别表达。0.25ms 直方图的百分位最大量化误差 ≤0.125ms，mean/Jitter保留原值统计规则。
- M08 紧凑数据存储、三级历史汇总、Raw 8h/receipt 48h 保留与安全清理；schema27 有界恢复回填及真实资源预算。热请求不进行全库 dbstat 扫描，磁盘不足时可能保守节流，不能将缺测算作失败。
- M09 三网历史方块，协议、地区、IPv4/IPv6 配置与旧详情入口保留；不混合不同配置/槽位修订的统计。

## 数据库与恢复

数据库目标 schema 为 **27**。升级事务先停止 Server，确认无数据库打开句柄，保存一致的 DB/WAL/SHM、旧二进制、配置和服务文件，生成受保护的 SHA256 备份清单，再迁移、检查完整性、验证实际新服务。失败按已记录事务恢复旧程序及匹配的一致性数据库；未确认恢复时保留失败日志与状态。

不能仅将旧 Server 二进制换回去读取新 schema。保留 `/var/lib/404-probe-upgrade/backup` 和同目录事务状态，按安装器恢复提示操作；若升级已经成功且需要人工回退，先停服务并使用同一次完整备份恢复程序、数据库与配置，不合并新旧 DB/WAL。不要删除正在使用的 WAL/SHM。安装器为数据库及 WAL/SHM 预留三倍文件空间另加 128MiB，实际可用空间不足时在升级前拒绝。

检查命令：

```bash
/usr/local/bin/404-probe-server version --json
systemctl --no-pager --full status 404-probe-server.service
journalctl -u 404-probe-server.service -n 100 --no-pager
```

正式四个 Linux 二进制必须对应同一个真实干净 Commit（dirty=false）。本版使用既有 schema2 Release metadata，最低 Server v1.0.1、升级协议2；SHA256SUMS包含四二进制、metadata及安装器六条。SHA256SUMS自身另行核对，不含自身哈希。七项资产和最终发布身份以 High 正式发布验证报告为准。

## 支持与验证边界

支持 Debian 12/13 systemd Server/Agent 和 Linux amd64/arm64 资产。最终正式发布验收使用隔离环境；编译成功不等于所有架构安装、系统服务、网络或长期运行验证通过，实际平台验证结果与失败说明见正式 Release 的最终验收摘要。

Alpine 3.24.x OpenRC 仅支持 Agent 基础功能，新装须预先具备完整 OpenRC、Bash、GNU coreutils/tar、util-linux、shadow、curl、CA。Alpine Server、已有 Alpine Agent 自动升级/回滚、安全观察及远程永久删除仍不支持；Podman 环境只有满足运行、权限和网络前提时适用，不等于完整主机 OpenRC/systemd 生命周期。

此前容量测试为30天等效物化及实际增量写入/清理，不是现实连续30天。公网大陆三网/IPv6、原生 ARM Debian/systemd 安装升级、完整 Alpine 生命周期、128MiB 长期驻留等不得从受控测试或跨编译推定通过。保留旧 Node 套件的逐项基线失败和全部返修证据；不宣称全仓测试无失败或无限容量。旧版本说明与其验收边界保持原文。
