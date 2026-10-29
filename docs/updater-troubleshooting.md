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

核对 `/var/lib/404-probe-updater/operation.json` 中的 `status`。`claimed`、`downloading`、`verifying`、`staging`、`installing`、`restarting`、`health_check` 等表示操作未结束；恢复、回滚或移除正在进行时也不能重启。检查状态目录中是否有 pending、candidate、previous、bootstrap、removal 等未处理文件，并检查二进制目录中的候选或旧程序。如果不确定操作是否已结束，保留现场，暂停处理。

检查和分享输出时，不要公开连接地址、主机名、密码、凭据或 token；不要读取或分享 `agent.env` 内容。`operation.json` 只核对状态等诊断字段，不修改或删除它。需要协助时提交脱敏的状态、路径元数据及相关服务日志。

应急检查第 3 步：以下命令**无输出才继续**；有输出时先确认对应事务已结束，不确定就保留现场。

```sh
ls /var/lib/404-probe-updater/ | grep -E 'pending|candidate|previous|bootstrap|remov'
```

## 连接文件缺失（updater_socket_missing）

运行目录还在，但 `agent-updater.sock` 丢失。新版服务在目录安全且没有升级、恢复、移除时，会尝试重建连接文件。稍后刷新面板。

若没有恢复，先完成上面的检查，确认没有进行中的操作及待处理文件，再**只执行一次**：

```sh
sudo systemctl restart 404-probe-agent-updater
```

随后重新检查服务状态和 socket，并刷新面板。socket 应为 root 属主、404-probe 属组、`srw-rw----`（0660）。Agent 身份、配置、历史及单元的沙箱设置不应改变。仍缺失或仍不可升级时停止，不反复重启，保存脱敏诊断信息。

## 运行目录丢失（updater_directory_missing）

整个 `/run/404-probe` 丢失时，本版不自动重建运行目录，由 systemd 在重启升级服务时重建。不要放宽 `/run` 权限、修改 systemd 沙箱或手动创建宽松权限的目录。

确认没有进行中的升级、恢复、移除或待处理文件后，按上一节**只重启一次**升级服务，由 systemd 的 RuntimeDirectory 生命周期重建目录。随后核对 socket、身份和服务状态。仍不可用就停止并保留现场。

## 无法连接或没有正确应答（updater_unreachable）

文件存在不代表服务能应答。先检查服务状态、最近日志和目录/socket 元数据；若有权限异常或路径被替换，改按下一节处理。

确认没有进行中的操作后，可以按“连接文件缺失”的步骤只重启一次。仍连不上或没有正确应答时停止，保留日志，不删除 socket、状态文件或候选程序。

## 路径或身份异常（updater_identity_mismatch）

可能是目录/socket 被符号链接、普通文件或另一个 socket 替换，也可能是属主、属组或权限异常。

**保留现场，不删除、不改权限、不反复重启。** 记录 `ls -ld`、服务状态及日志；不要用 `rm`、`chmod`、`chown` 把未知对象强行改成预期样子。确认对象来源和是否有活动事务后，再制定恢复操作。升级服务会保留这些未知对象，不会抢占另一个 listener。

仍无法确认来源时，把脱敏的目录/文件信息（ls -ld、服务状态、日志）交给维护者。

## 系统不支持（updater_unsupported）

这台机器的系统（如 Alpine/OpenRC）暂不支持从面板升级，升级按钮已停用，监控仍可使用。请保持现状，等待后续版本；不要为了启用按钮修改服务或重装。

## 未知原因（updater_unavailable）

旧 Agent 可能不报告细分健康状态。先按本文通用检查确认服务、路径和操作状态，再选择对应步骤。不要把所有“不可用”都当作需要迁移或重装；较新正式版的连接文件丢失与旧版本缺少升级服务是不同情况。

## 维护者备注

新版修复需要通过可用的旧升级通道或手动安装生效。旧服务已经不可达时，不能依赖面板升级自动修好旧通道；应先按本文确认安全并恢复通道。

启动时已有遗留 socket：升级服务有意拒绝启动并保留原对象（fail-closed）。正常 systemd 单元由 RuntimeDirectory 生命周期清理旧目录；手动启动等绕开该生命周期的方式不能抢占遗留 socket。不要删除来源不明的对象。

移除 worker 启动成功后，即使 worker 后续失败，本进程仍保持 removing，禁止升级和自愈；确认移除状态、保留日志并由维护者制定恢复步骤，重启 Updater 后才清除此内存标记。启动命令失败会回滚标记。
