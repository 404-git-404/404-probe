# 404-probe v1.0.1

## 安装与升级

固定版本入口（Debian 12/13 systemd，新装 Server/Agent 或升级已有受支持 Server）：

```bash
curl -fsSL https://github.com/404-git-404/404-probe/releases/download/v1.0.1/install.sh | sudo PROBE_404_VERSION=v1.0.1 bash
```

已有 v0.9.3/v1.0.0 Server 先本地升级，再通过 Server Web 对正常具备 Updater 的受支持 Agent 逐台升级到 v1.0.1。保留配置、凭据、Agent 身份、epoch 和业务数据；候选校验及实际健康报告失败时按事务恢复。Web 升级只替换 Agent 二进制，不安装新增 root helper。

数据库当前 schema 为 **25**，包含升级任务的持久授权及兼容信息。迁移后不能直接换回旧 Server 二进制；恢复旧版本必须同时恢复匹配的一致性数据库备份。

已有 v1.0.1-beta.1/beta.2 Agent，以及缺失 Updater 的 v0.9.3/v1.0.0 Agent，使用固定的本地迁移入口：

```bash
curl -fsSL https://github.com/404-git-404/404-probe/releases/download/v1.0.1/install.sh | sudo PROBE_404_VERSION=v1.0.1 bash -s -- upgrade-agent
```

此入口仅适用于受支持的 Debian systemd 安装，要求 root 和预先安装的 Python3。安装器先静态校验迁移程序及发布身份，缺 Python3 时提前拒绝且不自动补包。保留原 ID、凭据、epoch、配置和业务状态，无需重新 enrollment；缺失 Updater 仅在迁移实际健康成功后补装。失败或中断保留恢复标记和事务，按提示重跑同一入口。已有正常 Stable Updater 的 Agent 继续使用 Web。

## 本版改进

- 完成 M01–M04 范围的设备卡片、详情、布局及实时交互改进；保留设备状态、详情焦点/滚动和 Selector 保存后的实际刷新行为。内存、磁盘、Swap 使用二进制单位，网络流量使用十进制单位；不显示虚构的网络质量评分。
- Agent 升级默认 Stable。Beta 需要在当前单台 Agent 的操作中显式选择；每次升级保留其授权和目标身份，拒绝未授权 Beta、重复/降级、错误候选及同设备并发操作。新旧 Server/Agent 保持能力协商，旧组件不能被授予不支持的升级任务。
- 下载候选按固定官方发布来源验证校验清单、平台、模块及实际版本/commit；未经验证的下载程序不能用于试运行。v1.0.1 保持原 schema 1 发布 metadata 和七资产结构。
- 修复普通 Updater 交接时运行目录生命周期和过早认定 Agent 启动成功的问题；以实际运行程序确认恢复，失败不能只凭 systemctl 返回成功认定回滚完成。
- 本地迁移准备受保护的固定 socket 父目录，保留已有安全目录；迁移重启避免通过 Agent 依赖启动第二 daemon，消除同一事务/socket 的竞争。失败恢复和中断继续依赖真实事务与健康确认。

## 验收范围与限制

功能验收在隔离 Debian 12/13 使用受控源码资产，覆盖官方旧版本迁移、失败回滚、中断恢复、缺失 Updater 和实际浏览器/Selector 业务链路。未来三段升级使用私有夹具，不代表这些未来版本已经发布。正式发布资产须从最终 clean commit/tag 重新构建并完成最终验收，不能复用受控夹具。

SQLite 原 scalar-only 取消测试在 Debian12 有一次未命中实际中断，保留为该测试未通过；独立持续 SQL 取消、连接复用及 WAL 验证在双 Debian 共 60 轮通过。此结论不等于所有测试全绿或长期负载稳定性保证，不修改原断言，也不删除运行中的 WAL/SHM。

Alpine 3.24.x OpenRC 仍仅支持 Agent 基础功能，不支持 Server。须预先具备完整 OpenRC 启动环境和 Bash、GNU coreutils/tar、util-linux、shadow、curl、CA。Alpine 自动升级/回滚、安全观察和远程永久删除，以及真实网络质量评分，延期至 v1.1。跨架构构建和目录保护测试不等于完整 ARM 程序运行验收。

M05–M13 未在本版范围内施工。历史发布说明保留原版本的验收边界，不能将本次结果追溯套用到旧版本。
