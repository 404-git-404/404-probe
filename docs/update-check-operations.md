# 新版本检查：外联、状态文件与卸载说明

## 外联与关闭

Server 默认访问 `api.github.com`，只读取固定仓库最新正式版的版本号。启用 Web 登录且没有近期成功检查缓存时，Server 启动后随机等待 1–10 分钟首次检查，成功后每 12 小时检查一次；重启或重新开启仍遵守最近成功检查的间隔。失败会退避重试，页面刷新只读缓存，不触发访问。请求使用固定 User-Agent: 404-probe-update-check 和通用 HTTP 请求头，不携带 Server 版本、地址、实例标识、Cookie 或凭据；GitHub 能看到出口 IP。

面板 ⚙ 的“检查新版本”开关可以关闭后续请求和版本提示。未启用 Web 认证的 Server 不启动检查器。

## 状态文件

`update-check.json` 由 Server 在实际数据库同目录创建；默认安装位置为 `/var/lib/404-probe/update-check.json`，由 Server 用户所有，权限为 0600。文件保存开关、最近成功版本号和查询时间，不含敏感凭据。文件不存在时默认开启；损坏、不可读或权限 / 属主不对时按关闭处理并记录警告。升级保留该文件；它不属于数据库备份 / 回滚副本。

## 卸载时的保留与手动清理

v1.1.0 的安装器卸载只删除明确登记的托管文件，会将 `update-check.json` 当作“未识别文件”保留。在隔离的安装器夹具里实测（用 `uninstall server --confirm-delete-data`）：

- 第一次卸载：服务、二进制、配置、数据库（含 WAL / SHM）和已安装的 `404-probe-install` 辅助脚本都被删除；`/var/lib/404-probe` 目录因为还有 `update-check.json` 而保留，服务账户 `404-probe` 也因此保留。
- 只要该文件还在，再运行卸载不会改变上述结果。
- 手动删除这个文件后重新运行官方卸载，状态目录和服务账户才会被清理。

完全清理的步骤（Server 已卸载后）：

```bash
sudo rm /var/lib/404-probe/update-check.json
curl -fsSL https://github.com/404-git-404/404-probe/releases/download/v1.1.0/install.sh | sudo PROBE_404_VERSION=v1.1.0 bash -s -- uninstall server --confirm-delete-data
```

第一次卸载已经删除了本机的 `404-probe-install`，所以第二步要用上面这条官方固定版本入口。交互运行时也可以不带 `--confirm-delete-data`，按提示输入 `DELETE 404-probe server`；没有终端的自动化场景必须带这个参数。数据库不在默认位置时，文件在实际数据库所在目录。其他未识别文件也必须先由你确认处理；不要为删除更新检查缓存而删除数据库或整个状态目录。仅删除 JSON 文件本身不会自动删除目录或账户。

下一版计划把该文件加入卸载清单，届时需要安装器夹具测试。本版继续沿用现有安装器规则。
