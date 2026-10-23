# R31 大数据库升级测试工具

仅在维护者安排的测试机独占时段运行。Python 3 标准库，无新 Go 依赖。

## 本机文件位置

工作目录为 `D:\Claude-Github\404-probe-codex`。所有生成的数据库、JSON、日志和本机 v1.0.6 校验程序统一放在仓库外 `D:\Claude-Github\r31-fixtures\`；保留的三份数据库为 `r31-complete-{snapshot,full,compact}.db`，校验程序为 `r31-server-v1.0.6.exe`。输出目录不得位于任何 Git 仓库内，以免未跟踪文件使发布构建目录变脏。

不要在主仓库 `D:\Claude-Github\404-probe` 下创建缓存、fixture、日志或临时工作目录；实现文件只写入自己的 worktree，生成物和临时文件放在仓库外。

## 生成数据库

```bash
python3 scripts/r31-generate.py --server /local/v1.0.6/404-probe-server-linux-amd64 \
  --scale snapshot --out /local/fixtures/snapshot.db
```

`--scale` 支持 `tiny`、`snapshot`、`full`、`compact`。输出路径及同名 WAL/SHM 必须不存在。输出数据库和 `<数据库>.json`；JSON 记录版本身份、schema 指纹、行数、页数、校验结果和包含校验在内的总耗时。大小口径为字节；换算 MiB 除以 1048576，与 R29 报告一致。

- 固定种子 31、固定 UTC 窗口末端（2026-10-23），同一 SQLite/Server 版本下字节可复现；可用 `--seed`、`--anchor-ms` 明确改变输入。
- 通过所给 **v1.0.6 正式 Server 的 `agent list --db`** 调用该发布版本的 `storage.Open` 创建空库；没有手写 DDL。要求版本为 v1.0.6、dirty=false、schema=34。用其 `agent list` 再打开成品，并运行 `database verify`。
- 从官方 schema 填充合成数据；不关外键，不删/关触发器。核对 `integrity_check`、`foreign_key_check`、全部 `quality_totals` 和各 Agent 的 `quality_usage`。填充前后比较所有表、索引、触发器的 schema 指纹。
- minute_metrics、receipts、minute、三档 buckets、security、jobs 遵循 R29 的时间窗口；full 为 31 Agent × 30 天分钟指标、108 序列 × 30 秒完整质量窗口。二进制 buckets 使用 v1.0.6 的 v1 直方图编码。原始质量样本为 0，与快照一致；小表保留外键遍历量，并包含 31 份套餐供 R30 回填使用。
- snapshot 在同一正式表中插入/删除合成分钟行，形成页内碎片和空闲页；无额外填充表。compact 保留同样最终逻辑行数并 VACUUM；full 填满保留窗口并 VACUUM。它们近似 R29 的形状，不能复制真实库每页的布局。
- 所有内容均为 example 合成值，地址仅用文档保留地址。token_hash 为合成摘要，没有可使用的真实凭据。工具不会启动网络探测。
- 后续 schema 变化：**保留本适配器和 v1.0.6 产物**，仍可生成该版数据库。为下一次“上一个正式版本”增加独立适配器，调用该版本正式 Server 创建空库，更新填充字段/编码及测试；不要把当前分支迁移代码冒充旧版 schema。此适配器拒绝其他版本/schema。

## Linux 测试机实跑

需要 root、systemd、既有 Server（404-probe 用户/组已存在）、Python 3，以及安装器依赖（bash、curl、sha256sum、runuser 等）；不安装系统软件。只支持 Linux amd64/arm64。两个本地产物目录各包含正式构建的七个文件，运行前核对其 SHA256、LF、Server 版本和 dirty=false。只接受规范 `vX.Y.Z`，目标必须更高。不能有 Agent 混装、未完成升级、候选文件或服务 drop-in。

```bash
sudo python3 scripts/r31-upgrade.py v1.0.5 v1.0.6 \
  /local/assets-v1.0.5 /local/assets-v1.0.6 snapshot \
  --out /local/r31-results
```

发布时使用 `v1.0.6 <目标正式版>`。当前生成适配器只接受以 v1.0.6 为起始版，或 v1.0.5 → v1.0.6 的流程验证组合。两个 schema 都是 34 的实跑只验证流程和计时，不证明未来新增迁移正确。

流程：

1. 确认独占、至少 8 GiB 空闲空间。停现有 Server；原数据库目录（含 WAL/SHM）、配置、二进制、helper、unit、升级目录和运行锁目录移动到 `/var/lib/404-probe-r31-<编号>/original/`（0700）。保留原启用/活动状态及文件 SHA256、属主、权限快照。
2. 用本地正式起始产物和未改动的 `install.sh` 安装起始版，参数为 `PROBE_404_LOCAL_ASSET_DIRECTORY`、`PROBE_404_VERSION`。安装交互仍在终端：维护者选择 **Server**，选择回环监听，输入管理员密码；不得输入任何真实服务凭据。工具不代填密码，也不保存终端安装录屏。生成的 Control token 仅由安装器写入私有配置文件，结束时删除。
3. 停服换入本工具生成的库，重新启动。实跑生成时采用当前 UTC 分钟作为窗口末端，避免未来执行时历史数据已经过期；JSON 会记录 anchor，重现时传同一个 `--anchor-ms`。
4. 从目标本地产物的 installer 创建一次性计时副本：包装真实 `database verify`、`migrate-copy`、`migrate-protected` 命令，分别记退出码和单调时钟耗时。正式产物不改，依然由安装器校验正式资产；包装保留原命令退出码、参数及重定向。布局锚点变化时拒绝运行。
5. 先在迁移完成及二进制替换后 `exit 87`，调用安装器自身 EXIT trap 自动回滚，单独计时 `restore_server_upgrade`。检查旧二进制哈希、服务活动、pending 已清除。数据库/WAL/SHM 的恢复哈希由安装器自身的备份清单验证，记入结果。然后在恢复的起始版上执行一次成功升级，检查目标二进制哈希及服务活动。两次升级确认由维护者在终端输入 `y`。
6. 无论成功失败，停测试服务、删除生成安装、原路移回原文件；先逐项核对哈希/属主/权限，再恢复原启用/活动状态。输出 `result.json` 和 `summary.md`（不含配置内容/凭据），成功恢复才删除私有备份及全部生成库/临时脚本。资产和结果目录必须在安装路径外，资产不会删除。

结果中的每个 event 都有 `run`（failure/success）、`phase`、`seconds`、`exit_code`。同一次 installer 中第一个 `verify` 是升级前检查，后续 `verify` 是升级后检查；副本迁移和正式迁移分开记录。耗时包括 CLI 进程启动及其正式校验。回滚耗时包含文件恢复、哈希验证及服务健康观测。

恢复异常时 `restored=false`，保留 0700 备份，退出非零，并给出目录；此时不要开始其他任务。SIGKILL、断电、Python 进程自身崩溃不能执行 finally，需要人工恢复：停止 Server，按 `PATHS` 中的零基序号，将 `original/<序号>` 移回对应路径（先保留失败现场，不覆盖唯一备份），daemon-reload，按运行前状态恢复 enable/disable 和 start/stop。先核对原数据/配置/二进制，再删除失败现场。**私有备份含运行前数据和凭据，不上传或打印。**

## 自测

```bash
R31_PREVIOUS_SERVER=/local/v1.0.6/404-probe-server-linux-amd64 \
  python3 scripts/test-r31.py -v
R31_PYTHON=python3 R31_PREVIOUS_SERVER=/local/v1.0.6/404-probe-server-linux-amd64 \
  go test ./scripts/r31-tooltest -count=1 -v
```

`R31_BASH` 可指定 bash 路径；`R31_PYTHON` 可指定 Python 路径。未提供 `R31_PREVIOUS_SERVER` 时跳过 tiny 集成；没有 Python 时 Go 包明确 skip。常规 `go build ./...` 不新增工具产品二进制。测试覆盖跨秒两次 tiny 的字节一致性、拒绝覆盖已有库、损坏计数器拒绝、直方图编码及 installer 注入锚点/语法；真实安装/迁移/回滚/原环境恢复必须在安排好的 Linux 测试机上验证。
