# 404-probe v1.0.3-rc.1 候选说明

本文件随本地候选源码交付；不是公开发布或升级通知。Stable仍为v1.0.2。仅在用户确认完整资产清单、独立High完成并批准发布后，才创建tag、推送或发布预发布Release。

## 集成范围

以当前已验收源码冻结为准，保留M01–M09既有设备管理、详情、卡片布局、离线语义、暖色主题、真实TCP/ICMP测量、历史汇总及三网展示；加入已验收待发成果：

- M05-TCP-01B：真实有效RTT和known_gap部分分钟空心点；失败/缺测不转零，不跨缺口连线。
- M05-TCP-02B：IPv4 TCP Mini真实Log10，基础5–2000ms，2/5/10扩展；VPS级任意ISP触发的分钟P95告警持久化，同自然日保持，次日新的严格低值方可解除。量化0、低于5ms和历史超界有独立标记。覆盖不足明确提示，不当作确认正常。ICMP保持原语义。
- M10：Agent实际路径的公网IPv4/IPv6出口观测和对应状态/过期展示，追加数据库Schema28。
- M11：YouTube页面地区证据、未知/过期/失败边界和可靠性；不承诺出口归属、视频播放或Premium解锁。
- M12-B：升级时离线helper先于候选启动；恢复核验完整备份SHA、旧程序读库、实际PID/exe/HTTP及至少5秒稳定期，失败保留pending，避免SQLITE_BUSY重入迁移和仅凭start返回0误判恢复。
- 既有套餐、单位和统计口径保持；不借发布准备改变核查范围或新增统计算法。M01–M13验收依据由独立验收报告索引提供，不以模块编号推导新增功能。

不纳入M14–M19、评分、大图新方案或其他未授权模块。

## 安装、升级与兼容

候选资产为Linux amd64/arm64 Server及Agent四个ELF，加install.sh、RELEASE-METADATA.json和SHA256SUMS，共七项。四个ELF必须来自同一个真实干净Commit；metadata为既有schema2，数据库目标为Schema29，二者不是同一版本号。

RC安装/Server升级必须显式指定`PROBE_404_VERSION=v1.0.3-rc.1`，并使用确认后的同版本资产。无显式版本的安装器默认仍为v1.0.2，latest解析只接受Stable；不会自动选择RC。未发布前不要在业务主机运行候选安装命令。

Debian12/13 systemd的新装、已有Server升级、Schema27→28→29和28→29、升级中断恢复及Server/Agent兼容性须以本候选真实资产进行独立High。跨编译不是原生arm64生命周期验收。Windows本地测试不能代替Debian/systemd验收。

版本排序为同一基础版本`beta.N < rc.N < Stable`，数字序号按数值比较；保持旧Beta和旧v1.0.1桥接语义。RC metadata声明最低Server为v1.0.3-rc.1及升级协议2，旧Server不会把RC误认成兼容Stable。

预发布显式升级沿用原`beta`线协议通道和`confirm_beta`字段，以保留既有数据库/协议兼容；界面按实际版本显示Beta或RC，确认文字覆盖两者。不会新增数据库Schema30或新自动升级通道。RC Server的空请求默认升级被拒绝，只有显式选择及确认可进入预发布路径。

旧Stable/Beta Agent可继续向RC Server上报；这不等于其已安装Updater理解RC。旧Agent/Updater不能从Web直接进入RC；需要RC-aware Agent和本地Updater能力探测共同通过，才允许后续显式RC操作。新的`capabilities_rc`探测不改变旧capabilities响应格式；未知动作的旧Updater安全拒绝。旧v1.0.1-beta.1/beta.2和缺失Updater的历史桥接继续使用已发布固定v1.0.1入口，不以RC程序替代。

Alpine支持边界不扩大：仍仅Agent基础监控，专用质量采集禁用；不支持Alpine Server或借RC开启现有Agent自动升级/回滚。测量上限保持6000ms，7200ms仅为算法/渲染夹具。

## 数据与恢复限制

Schema29新增本模块两张TCP告警表；高事件窗口锚定最新高分钟，最多4322行/VPS，不随无数据时间过期。初始回填分批进行；升级前已清理的分钟/临时尖峰不能追溯补造，覆盖不足使用默认字色并明示。自然日由Server按浏览器IANA时区计算，适用域按实际时区规则检查，含23/25小时DST；不支持的日期规则返回不可判定。

跨Schema回滚必须恢复匹配的旧程序及同次完整DB/WAL/SHM备份，不能仅换旧二进制。恢复点之后的新写入可能丢失，不承诺零损失；pending和失败证据不得自行删除。正式候选的完整main升级/中断恢复闸门仍须独立High，不能用旧unknown身份组件测试代替。

此阶段不自动升级用户设备，不操作生产主机，不发布资产；已发布历史资产及报告保持原样。
