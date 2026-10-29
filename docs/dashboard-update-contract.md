# 首页读取与更新契约（v1.0.10）

`GET /api/v1/web/dashboard` 和 `?ids=<最多200个ID>` 继续使用管理员会话、同源检查和 AgentState 的资源/mutation/生命周期屏障。响应按 `Accept-Encoding: gzip` 协商 gzip（支持 q，q=0 不压缩），带 `Vary: Accept-Encoding`，没有 ETag。

`GET /api/v1/web/organization/catalog` 为同权限只读目录快照，不接受查询参数。单一 SQLite 读事务返回 `revision/groups/tags/exchange_rates/items`；items 仅含未撤销设备的 `agent_id/group_id/tag_ids/revision/plan.currency`，用于影响台数和未设置汇率的币种列表。不读取运行态、网络摘要或计算费用。归属、管理、汇率对话框使用它；写入仍使用打开或上次显式读取的修订号，409 不自动重放。

遥测 state 立即经 AgentState 合并并以脏 ID 绘制。相同附带元数据的正常遥测不会立即触发摘要读取；每设备摘要最多每十秒一次，页面合批最多每秒一次。生命周期、配置、套餐、organization 和 dashboard 事件保持下一帧立即合并读取；该路径不受遥测节流限制。后台页暂停十五秒全量校准，恢复可见立即全量读取；EventSource 首次打开时若初始读取已完成则补读，重连始终全量读取。成功读取恢复连接徽标。

网络缓存只比较本设备 Generation 与 map 重置计数，其他设备失效不使本次计算变为未知。同源 SSE 域名策略缓存三秒，收紧最长延迟三秒生效；登录会话撤销/到期仍经独立通道即时生效。

`masked_ips/masked_egress` 只提供当前有效、available、资源未陈旧的遮挡地址；后者含 family 对应的遮挡 ip、status、checked_at、valid_until，卡片按有效期判断。过期观测 IP 在存储中可能保留，不能据非空 IP 判为可用。批量与 SSE 卡片采用相同悬停文案；完整 IP 仍仅走原详情路径。

一帧中的卡片更新使用约 8ms 工作预算，余下脏 ID 留给下一帧；初始绘制和批量校准都用同一机制，首次有效内容和全部摘要仍分别按两个 rAF 后测量。历史图表由自身的数据/配置计时器更新，CPU 遥测不重建未变化 SVG。折叠和筛选卸载节点后保留可复用卡片，真实删除时清理，未挂载卡片不发懒加载请求。
