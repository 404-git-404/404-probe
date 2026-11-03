# 首页读取与更新契约（v1.0.10）

## v1.1.0 延迟槽只读扩展

全量和 `?ids=` 响应的每个设备行新增固定三项 `network_slots`，顺序及 `slot` 数字为 1/2/3（telecom/unicom/mobile）。每项为 `{slot, target_name, latest_ms, sampled_at, state}`；`latest_ms` 单位毫秒、缺失为 null，`sampled_at` 为 Unix 毫秒、缺失为 0。显示名取当前槽设置的地区/ISP 标签（内置 ISP 翻译成电信/联通/移动），未标注时为“卡槽 N（未标注）”。不返回目标地址。

口径固定为 IPv4、TCP 连接延迟、当前目标最近 1 小时内最近一个成功有效样本（含恰好 1 小时，排除未来时间）；更近的失败、IPv6 和 ICMP 不会覆盖它。读现有 `quality_facts.rtt/sample_time`（上传流程已校验、标记 `eligible=1` 的 TCP 成功样本），由当前 `quality_slots` 的 IPv4/TCP target_id 关联；不使用分钟均值代替单次样本，不新增写入或 schema。样本事实按现有保留/容量规则清理后，无可用成功事实时返回 `no_data`。这不是长期历史接口。1 小时内没有有效样本返回 no_data，5 分钟到 1 小时仍为 stale 并保留数值。sample_time 来自 Agent 时钟，Agent 时钟偏差会影响 ok/stale 判断，与现有网络质量历史一致。

状态优先级：平台不支持/被排除 → `unsupported`；总开关关闭、槽未启用、未配置地址或未启用 IPv4 TCP → `no_target`；有目标但无有效样本 → `no_data`；样本距 Server 当前时间严格超过 300000ms → `stale`；否则 `ok`（恰好五分钟仍为 ok）。`stale` 保留延迟和采样时刻；其余无数据状态返回 null/0。设备在线、暂停/撤销和遥测新鲜度仍由既有字段表达，不改变原语义。

新增槽数据在原 dashboard 读事务内最多用两次批量 SQL 读取：一次设置/能力，缓存未命中时再一次取所有目标的最新有效样本，设备数增加不会增加 SQL 调用次数；内部每目标最新行搜索使用现有 `quality_fact_neighbor` 索引。样本（含无样本结果）缓存 5 秒，随后重读；配置、显示名、能力、启用状态以及五分钟/一小时边界每次即时计算。正常显示的新样本最多附加 5 秒缓存滞后，不重置既有 dashboard 节流和校准规则。全量、15 秒校准及合批 partial 读取都携带此字段。SSE 不新增槽负载或专用事件：遥测仍仅使对应 ID 标脏，配置变更沿现有即时合批读取路径，不能因延迟槽触发全量绘制或新建逐设备请求。

第 4/5 段增加前端消费者时，须把 `network_slots` 登记到 AgentState 的 `summary` 资源组（与 `network` 相同），沿 dashboard-data 的现有屏障合并；本后端段不修改 JavaScript。渲染只更新脏行，不因延迟值变化重新排序。前端应使用 Server 的 `state` 判定新鲜度，也可用 `server_now`/`sampled_at` 在等待下一次校准时将超过五分钟的值变灰。

## v1.1.0 版本检查缓存

状态文件的 `checked_at` 若超过 Server 当前时间五分钟，会清为 0 并保留上次已验证的 latest，记录一次警告并重新安排检查；0 也可能表示成功缓存的时间不可信。时钟回拨不会触发“尚未满 12 小时”的负差值门槛，启动剩余等待最多 12 小时。写盘与缓存读取使用不同锁，磁盘持久化挂起不阻塞 `/version` 的 Snapshot；设置保存失败保留内存原值；成功取到版本但缓存落盘失败时，仍更新内存 latest / checked_at，不改变 enabled，并保持 12 小时成功请求间隔。主动关闭引发的请求取消不算检查失败。

默认外联、状态文件和卸载保留规则见 [新版本检查运维说明](update-check-operations.md)。

未启用 Web 认证（`webAuth == nil`）的 Server 不启动版本检查器，不读取更新检查设置，Snapshot 固定为关闭状态且不发外网请求；安装器正常创建的带 Web 认证 Server 仍按开关设置运行。

`GET /api/v1/web/version` 保留原字段，新增 `update:{enabled,latest,newer,checked_at}`。`latest` 缺失为 ""，`checked_at` 为最近成功查询的 Unix 毫秒、缺失为 0；仅 `newer=true` 时提示。关闭、开发/dirty/RC 本机构建均不提示。失败保留上一次成功值，页面不显示网络错误。读取此接口不访问外网、不唤醒检查器。

`PUT /api/v1/web/settings/update-check` 使用现有会话、Origin、`Sec-Fetch-Site: same-origin` 和 `X-CSRF-Token` 校验；JSON 请求 `{"enabled":bool}`，上限 1 KiB，返回上述 update 对象本身。状态文件损坏/不可读时 `enabled=false`，设置弹窗仍可显式开启，成功保存会原子覆盖坏文件。保存失败返回 500，前端应保留原开关状态。关闭保留缓存但使 `newer=false`；重新开启仍受成功请求间隔至少 12 小时约束，其他情况下随机延迟 1–10 分钟后检查。

前端“说明”链接仅在 `newer` 为真时用已验证的 `latest` 拼接 `https://github.com/404-git-404/404-probe/releases/tag/<latest>`，新标签页加 `rel="noopener noreferrer"`。后端不返回 GitHub 的 html_url。长时间打开的页面可低频重读 version 缓存，不能使 Server 新增出站请求。

## v1.0.10 既有更新行为

`GET /api/v1/web/dashboard` 和 `?ids=<最多200个ID>` 继续使用管理员会话、同源检查和 AgentState 的资源/mutation/生命周期屏障。响应按 `Accept-Encoding: gzip` 协商 gzip（支持 q，q=0 不压缩），带 `Vary: Accept-Encoding`，没有 ETag。

`GET /api/v1/web/organization/catalog` 为同权限只读目录快照，不接受查询参数。单一 SQLite 读事务返回 `revision/groups/tags/exchange_rates/items`；items 仅含未撤销设备的 `agent_id/group_id/tag_ids/revision/plan.currency`，用于影响台数和未设置汇率的币种列表。不读取运行态、网络摘要或计算费用。归属、管理、汇率对话框使用它；写入仍使用打开或上次显式读取的修订号，409 不自动重放。

遥测 state 立即经 AgentState 合并并以脏 ID 绘制。相同附带元数据的正常遥测不会立即触发摘要读取；每设备摘要最多每十秒一次，页面合批最多每秒一次。生命周期、配置、套餐、organization 和 dashboard 事件保持下一帧立即合并读取；该路径不受遥测节流限制。后台页暂停十五秒全量校准，恢复可见立即全量读取；EventSource 首次打开时若初始读取已完成则补读，重连始终全量读取。成功读取恢复连接徽标。

这里的十秒/一秒是遥测读取的上限，不是固定频率。全量响应把各设备视为刚刷新，清除已排队遥测，再按设备 ID 的 FNV-1a 散列把下一次资格分散到“响应后10–20秒”；15秒校准通常会先覆盖后半段设备。partial 成功后至少再等10秒，页面实际发出遥测批次的起点至少间隔1秒；元数据仍可绕过此限制。网络/摘要滞后典型约15秒（每台相邻两次刷新间隔中位约14秒，最大约16秒，另有读取耗时），不承诺每台每10秒恰好读取。

网络摘要客户端新鲜期由唯一常量 `NETWORK_SUMMARY_FRESH_MS=30000` 定义：覆盖10秒节流、15秒校准、读取耗时与余量，约两个刷新周期。持续不可达时，网络状态最多额外保留约20秒旧值，超过窗口变未知；没有接收时刻的旧对象仍按 `expires_at` 判断。资源 `max_age_ms` 和不足资料规则不变。

Server 启动的 CleanupLoop 发起一次网络摘要缓存预热：后台最多2个worker、总计30秒期限，跳过暂停/撤销设备；不等待它才开始监听或处理首个请求，不定期续热。Shutdown取消并回收worker，失败不会填入未知缓存。预热与前台使用同一个每设备generation/map重置栅栏；5秒缓存TTL和过期必须重新计算的语义均不改变。多个浏览器页仍各有自己的读取周期。

网络缓存只比较本设备 Generation 与 map 重置计数，其他设备失效不使本次计算变为未知。同源 SSE 域名策略缓存三秒，收紧最长延迟三秒生效；登录会话撤销/到期仍经独立通道即时生效。

`masked_ips/masked_egress` 只提供当前有效、available、资源未陈旧的遮挡地址；后者含 family 对应的遮挡 ip、status、checked_at、valid_until，卡片按有效期判断。过期观测 IP 在存储中可能保留，不能据非空 IP 判为可用。批量与 SSE 卡片采用相同悬停文案；完整 IP 仍仅走原详情路径。

一帧中的卡片更新使用约 8ms 工作预算，余下脏 ID 留给下一帧；初始绘制和批量校准都用同一机制，首次有效内容和全部摘要仍分别按两个 rAF 后测量。历史图表由自身的数据/配置计时器更新，CPU 遥测不重建未变化 SVG。折叠和筛选卸载节点后保留可复用卡片，真实删除时清理，未挂载卡片不发懒加载请求。
