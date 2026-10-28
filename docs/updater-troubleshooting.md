# 升级服务排查

本文适用于使用 systemd 的 Agent。升级按钮停用时，先确认面板显示的原因。不要卸载重装、重置凭据或删除升级状态文件；保留原配置和历史。

## 先确认没有进行中的操作

先在面板查看升级状态，再在 Agent 机器上逐项检查（需要管理员权限）：

```sh
sudo systemctl status 404-probe-agent-updater --no-pager
sudo ls -ld /run/404-probe /run/404-probe/agent-updater.sock
sudo ls -la /var/lib/404-probe-updater/
sudo journalctl -u 404-probe-agent-updater -n 80 --no-pager
```

核对 `operation.json` 中的 `status`。`claimed`、`downloading`、`verifying`、`staging`、`installing`、`restarting`、`health_check` 等表示操作未结束；恢复、回滚或移除正在进行时也不能重启。检查状态目录中是否有 pending、candidate、previous、bootstrap、removal 等未处理文件，并检查二进制目录中的候选或旧程序。如果不确定操作是否已结束，保留现场，暂停处理。

检查和分享输出时，不要公开连接地址、主机名、密码、凭据或 token；不要读取或分享 `agent.env` 内容。`operation.json` 只核对状态等诊断字段，不修改或删除它。需要协助时提交脱敏的状态、路径元数据及相关服务日志。

## 连接文件缺失（updater_socket_missing）

运行目录还在，但 `agent-updater.sock` 丢失。新版服务在目录安全且没有升级、恢复、移除时，会尝试重建连接文件。稍后刷新面板。

若没有恢复，先完成上面的检查，确认没有进行中的操作及待处理文件，再**只执行一次**：

```sh
sudo systemctl restart 404-probe-agent-updater
```

随后重新检查服务状态和 socket，并刷新面板。socket 应为 root 属主、404-probe 属组、`srw-rw----`（0660）。Agent 身份、配置、历史及单元的沙箱设置不应改变。仍缺失或仍不可升级时停止，不反复重启，保存脱敏诊断信息。

## 运行目录丢失（updater_directory_missing）

整个 `/run/404-probe` 丢失时，运行中的服务可能受系统保护限制，不能安全重建目录。不要放宽 `/run` 权限、修改 systemd 沙箱或手动创建宽松权限的目录。

确认没有进行中的升级、恢复、移除或待处理文件后，按上一节**只重启一次**升级服务，由 systemd 的 RuntimeDirectory 生命周期重建目录。随后核对 socket、身份和服务状态。仍不可用就停止并保留现场。

## 无法连接或没有正确应答（updater_unreachable）

文件存在不代表服务能应答。先检查服务状态、最近日志和目录/socket 元数据；若有权限异常或路径被替换，改按下一节处理。

确认没有进行中的操作后，可以按“连接文件缺失”的步骤只重启一次。仍连不上或没有正确应答时停止，保留日志，不删除 socket、状态文件或候选程序。

## 路径或身份异常（updater_identity_mismatch）

可能是目录/socket 被符号链接、普通文件或另一个 socket 替换，也可能是属主、属组或权限异常。

**保留现场，不删除、不改权限、不反复重启。** 记录 `ls -ld`、服务状态及日志；不要用 `rm`、`chmod`、`chown` 把未知对象强行改成预期样子。确认对象来源和是否有活动事务后，再制定恢复操作。升级服务会保留这些未知对象，不会抢占另一个 listener。

## 系统不支持（updater_unsupported）

Alpine/OpenRC 等不支持当前 systemd 面板自动升级链。监控仍可使用；需要更新时，按所用系统的手动更新说明操作，先确认配置和历史的保留方式。不要为了启用按钮而修改服务权限或伪造支持状态。

## 未知原因（updater_unavailable）

旧 Agent 可能不报告细分健康状态。先按本文通用检查确认服务、路径和操作状态，再选择对应步骤。不要把所有“不可用”都当作需要迁移或重装；较新正式版的连接文件丢失与旧版本缺少升级服务是不同情况。

新版修复需要通过可用的旧升级通道或手动安装生效。旧服务已经不可达时，不能依赖面板升级自动修好旧通道；应先按本文确认安全并恢复通道。
