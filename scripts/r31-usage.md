# R31 合成大库与升级演练工具

scripts/r31-rehearse.sh 只能在 `CLAUDE.local.md` 登记的测试 Server 上运行，并由维护者安排独占测试机时段。数据库全部是 example 合成数据；不复制真实数据库或凭据。发布演练的安装提示、临时密码及两次升级确认仍由维护者在交互终端回答，工具不记录安装终端输出。

## 版本登记与正式产物

`scripts/r31-versions.json` 登记已正式发布的起始版本：版本、GitHub tag 的完整提交、schema、相对上一版的表增删。当前登记 v1.0.6/schema34、v1.0.7/schema35（删除 tcp_mini_high、tcp_mini_state）、v1.0.8/schema35（无表增删）。未登记的起始版本一律拒绝。

每次正式发布后，在下一版准备工作中将该版补入登记表，并核对正式 tag、schema 和表增删；不要把开发分支提交或未发布候选写进去。

每个产物目录使用对应正式版的七件文件：linux amd64/arm64 的 Server/Agent、install.sh、RELEASE-METADATA.json、SHA256SUMS。发布任务先将它们与 GitHub release 的 SHA256SUMS 核对；工具运行时离线复核六个载荷的哈希，并核对元数据版本/提交、元数据中四个二进制哈希、实际 Server `version --json` 的版本/提交及 dirty=false。登记版本还必须匹配登记提交。缺文件、重复清单项、不一致或符号链接都会拒绝。

目标必须是规范且更高的正式版本号。已登记目标采用登记 schema，也可以显式提供并核对；未登记目标（例如即将发布的新版本）必须显式提供 expected_schema，不能猜测。工具不会自行下载产物，也不使用当前分支的迁移代码冒充旧版。

## 生成数据库

v1.0.6 生成主体保持为 schema34 底座，仍由正式 v1.0.6 Server 的 agent list/storage.Open 创建 schema，不手写 DDL。其他起始版本先生成相同底座，再由**所选起始版本的正式 Server**执行 database migrate-protected 和 database verify；例如 v1.0.8 直接由正式 v1.0.8 将底座迁到 schema35，不需要仓库外适配器。

生成一份 v1.0.8 起始的 snapshot 的单行示例（路径替换为准备好的本地目录）：

```bash
python3 -B scripts/r31-generate.py --start-version v1.0.8 --server /local/assets-v1.0.8/404-probe-server-linux-amd64 --base-assets /local/assets-v1.0.6 --scale snapshot --out /var/lib/404-probe-rehearsal/fixtures/snapshot.db
```

- `--scale` 为 tiny、snapshot、full、compact。非 tiny 成品必须至少 500,000,000 字节，否则拒绝；不通过填充表或任意 blob 凑大小。输出 DB/WAL/SHM、`.json`、`.schema34.json` 都必须是新路径。
- JSON 记录底座版本和生成记录、起始版本、每一步官方迁移/verify 的结果及耗时、行数、schema 指纹、页数、大小和总耗时。字节除以1048576换算 MiB。
- 种子默认31，窗口末端默认固定2026-10-23 UTC；用 `--seed`、`--anchor-ms` 改变输入。schema34 底座在相同 SQLite/Server 下字节可复现；后续官方迁移自己的时间戳不承诺跨次字节相同。
- 生成后核对 integrity、外键、quality_totals、每 Agent 的 quality_usage、套餐用量和方向计数器；schema 必须等于登记值。行数只允许 schema_migrations 按 schema 差增加，以及登记的表增删；任何其他变化都拒绝。
- minute_metrics、receipts、三档 buckets、security、jobs 延续原 R29/R31 数据形状、窗口和 v1 直方图编码。snapshot 在正式分钟表中插入/删除合成行，形成页内碎片与空闲页；full 填满保留窗口，compact 使用同样逻辑行数再 VACUUM。它们不复制真实库每页的布局。
- 不关闭外键或触发器，不启动探测；地址只用文档保留地址，token_hash 是合成摘要。

## 发布时的维护者一行入口

Linux amd64/arm64，root、systemd、既有 Server 用户/组，以及已安装的 Python3、bash、curl、sha256sum、runuser、flock 等依赖。**Linux 源码、工具链、Go 缓存和大库放在 /var/lib 下专属目录，不放 /tmp**；一些机器的 /tmp 是小型 tmpfs。Go 测试的 socket fixture 则需要短临时路径，见下文。至少留8 GiB空闲，发布任务自行确认独占时段及其他发布门禁。

发布任务先准备：

1. 干净源码中的 scripts/r31-rehearse.sh、r31-preflight.py、r31-run-with-evidence.py、r31-upgrade.py、r31-generate.py、r31lib.py、r31-versions.json 保持同一套版本；直接引用该源码目录，或将这几件文件一起复制到仓库外工具目录。
2. 正式 v1.0.6 底座、所选起始版、目标版各自的七件产物目录；起始版已安装，二进制必须与其产物完全相同。
3. 仓库外的 root 私有0700工作目录及全新的空 `evidence/`、`results/`（均0700）；没有 run-started、operator-exit.txt、operator-summary.json 或上次恢复残留。
4. 创建配置文件。下面是 **v1.0.8→v1.0.9、目标 schema35 的示例**；实际目标 schema 由发布任务确认。路径都是参数，不在工具里写死机器、提交或目录：

```json
{
  "from_version": "v1.0.8",
  "to_version": "v1.0.9",
  "from_assets": "/var/lib/404-probe-rehearsal/assets-v1.0.8",
  "to_assets": "/var/lib/404-probe-rehearsal/assets-v1.0.9",
  "base_assets": "/var/lib/404-probe-rehearsal/assets-v1.0.6",
  "work_dir": "/var/lib/404-probe-rehearsal/run",
  "scale": "snapshot",
  "expected_schema": 35
}
```

先只读预检（可以没有TTY；不会创建锁、标记或结果文件）：

```bash
sudo bash /path/to/404-probe/scripts/r31-rehearse.sh --check /var/lib/404-probe-rehearsal/config.json
```

在维护者的交互 root SSH 终端执行这一行启动：

```bash
sudo bash /path/to/404-probe/scripts/r31-rehearse.sh --config /var/lib/404-probe-rehearsal/config.json
```

入口要求 root、交互TTY和 /dev/tty；先用 flock 加锁再预检，用 noclobber 原子创建 run-started，只接受一次启动。普通启动预检失败时，工作目录也会留下 `rehearsal.lock`；该文件无害，不影响重跑。`--check` 仍是只读预检，不创建这个文件。预检检查起始版身份/哈希、数据库 schema/integrity、服务 active/enabled、没有 pending/candidate/drop-in/混装 Agent/恢复工作区，检查至少8 GiB空间及全新0700证据/结果目录。`--confirm-test-host` 由入口自动填本机主机名，不能防止在另一台装着同版本的正式机上误跑；防误跑由发布任务的门禁负责。Linux验收完成记录等发布专用门禁由发布任务自行检查，不写死在工具里。

### 维护者逐条回答的提示

1. `Select a role [1-2]`：输入 `1`，选择 Server。
2. `Web public origin`：回车，采用测试回环默认值。
3. `Allowed root domain suffixes`：回车，留空。
4. 管理员密码方式 `Choice [1]`：输入 `2`。
5. 自己输入至少16字符的**临时测试密码**并确认；不要使用正式密码，也不要把密码发送到对话。
6. 第一次 `Proceed with this Server upgrade?`：核对配置中的起始版→目标版，输入 `y`。
7. 等待工具在迁移、二进制替换后故意 `exit 87`，安装器自动回滚；这是预期步骤，不要中断恢复。
8. 第二次升级确认：再次核对版本，输入 `y`，等待成功路径检查及原安装恢复。

snapshot 生成和官方旧版迁移可能需要一段无提示时间。演练生成采用当前 UTC 分钟作为 anchor，避免历史过期；JSON记录该anchor。不要把 tiny 流程检查当成大库验收。

入口自动写 work_dir 下的 `operator-exit.txt` 和 `operator-summary.json`；正常应 tool_exit_code=0、status=passed、restored=true、frozen_equal=true、schema与配置相符、integrity=ok、verified=true。任意非零或不一致都停止，不重复启动，不删除唯一恢复备份；只回传脱敏摘要，不发送密码、token或安装终端内容。

## 计时、冻结证据和恢复语义

r31-upgrade.py 保留原先的本地安装、计时、故意失败、自动回滚和原安装恢复流程。起始版从登记表选择；低层CLI增加 --base-assets，目标未登记必须 --expected-schema。维护者应通过上面的加锁单行入口运行。

- 停服后原数据目录（含 WAL/SHM）、配置、二进制、helper、unit、升级目录和运行锁目录移入 /var/lib/404-probe-r31-编号/original（0700）；原 active/enabled 状态及文件 SHA/属主/权限冻结记录不改。
- 包装真实 precheck-verify、migrate-copy、migrate-protected、postcheck-verify，保留参数、重定向和退出码；四个锚点不唯一就拒绝。protected-backup 另计时；rollback-total、rollback-restore-files、rollback-verify-files、health-observation 仍记录，回滚也给出扣除健康观察的耗时。
- 先失败回滚，再成功升级。缓存不主动清空，后一次通常更热；不做冷缓存对照，不执行 drop_caches。
- r31-run-with-evidence.py 只增加原有两个冻结观察点：移动原安装前的 before-frozen、恢复文件后且原服务重启前的 after-restored-frozen，及 frozen-compare。文件0600、证据目录0700且必须为空；不保存配置/凭据原文。`--evidence-dir` 明确指定准备好的目录。
- finally 恢复期间忽略 INT/TERM/HUP，逐项捕获错误，继续安全的恢复步骤。确认原服务未活动才替换文件；原文件核对和 daemon-reload 通过才重启。保留原有 stop=5、部分恢复、enable/reload失败等处理。
- 恢复失败时保留0700私有备份，result.json 给出路径；不继续其他任务。SIGKILL、断电或 Python 崩溃不能靠 finally恢复，需维护者按 PATHS 零基编号人工恢复，先核对原数据/配置/二进制，最后才清理失败现场。私有备份含凭据，不上传或打印。

## 隔离自测（不操作运行中的服务）

Windows 按 CLAUDE.md 设置 R31_PYTHON、R31_BASH，在沙箱外跑全仓；Linux 使用有 ::1 的机器，大文件及 GOCACHE/GOMODCACHE 放 /var/lib 专属目录。先只读检查 /tmp：非小型 tmpfs 且空闲至少2 GB时，Go测试不设置 TMPDIR/GOTMPDIR，使用默认短 /tmp；结束后核查和清理本轮残留。Go的 testing.TempDir 直接读取 GOTMPDIR，不能将它设成长路径，否则 Unix socket 路径超过约108字节会失败。若 /tmp 是小内存盘或空闲不足2 GB，把 TMPDIR 和 GOTMPDIR 都设成 /var/lib 专属目录下的短路径（例如 `/var/lib/<短名>/t`，总长远小于108字节，为用例追加目录名和 socket 文件名留足余量）；源码、工具链、Go 缓存及大库仍放专属目录的其他位置，将大文件与短的测试 fixture 路径分开。测试结束一并清理本轮 fixture 和专属目录。

下面一行以 v1.0.8 为起始做 tiny 集成，底座始终为正式 v1.0.6；分别将 R31_START_VERSION 和 R31_PREVIOUS_SERVER 改为 v1.0.6、v1.0.7、v1.0.8 执行：

```bash
R31_BASE_SERVER=/local/assets-v1.0.6/404-probe-server-linux-amd64 R31_PREVIOUS_SERVER=/local/assets-v1.0.8/404-probe-server-linux-amd64 R31_START_VERSION=v1.0.8 python3 -B scripts/test-r31.py -v
```

未给正式产物时 tiny 集成明确跳过；测试其余部分只使用合成资产、临时根目录、假 systemctl 和PTY，不调用真实安装/升级。Linux root 下还验证入口的非TTY拒绝、锁排他、只运行一次和 --check 不修改文件。已有冻结证据、计时锚点、恢复故障及字节可复现测试继续保留。真实的大库安装/升级/回滚演练仍由后续发布任务安排，不能把这些自测写成真实演练通过。
