# Cloudflare R2 加密异地备份

在 Server 面板打开 **设置 → 备份**，填写 R2 账号 ID 或完整的 HTTPS endpoint、桶名，以及具有该桶对象读写权限的 R2 S3 凭据。多个 Server 共用桶时，为每台 Server 配置独立的对象前缀。**测试连接**会上传并删除一个小对象。保存后，两个凭据输入框留空会继续使用当前凭据，更换凭据时请同时重新填写两项。

启用时，浏览器生成 age X25519 钥匙。下载私钥并勾选**我已保存**后保存设置。私钥丢失后，相应备份无法恢复。重新生成钥匙后，旧备份仍需旧私钥。保留策略在每次上传成功后运行，默认保留 30 份，每日 04:00 按 Server 本地时间备份。**立即备份**可以在启用期间手动运行。

每份 `.tar.zst.age` 包含一致的 SQLite 快照以及 Server 使用的 `server.env`、控制凭据、Web 密码哈希和已有的版本检查设置。备份文件写入 R2 前经过压缩和 age 加密。上传采用单次流式 PUT，压缩加密后的文件上限为 5 GiB 减 5 MiB。Server 数据目录需有足够空间暂存快照和密文，备份会预留至少 128 MiB 给运行中的 Server。保留策略仅处理当前前缀下带备份标记的对象。

## 检查备份

将私钥放入仅当前用户可读的文件，例如在 Linux 上执行 `chmod 600 /secure/backup.agekey`。以下命令解密并校验备份，输出到一个尚未存在的目录。

```sh
sudo 404-probe-install database restore-backup \
  --key /secure/backup.agekey --file /secure/backup.tar.zst.age \
  --extract-to /secure/inspection
```

解出的数据库和配置含有敏感数据。检查完成后妥善保管或清理。也可用 `404-probe-server database restore-backup` 执行仅解密检查。

## 恢复到已安装的 Server

先安装支持备份中 schema 版本的 404-probe Server。运行下列其中一条命令。校验通过后，命令会列出目标路径。输入 `RESTORE` 才会停服务并保存原库、SQLite 辅助文件与配置，然后安装备份并启动 Server。

```sh
sudo 404-probe-install database restore-backup \
  --key /secure/backup.agekey --file /secure/backup.tar.zst.age

sudo 404-probe-install database restore-backup \
  --key /secure/backup.agekey --from-r2 'server-a/404-probe-backup-…tar.zst.age' \
  --r2-config /secure/r2.json

sudo 404-probe-install database verify
```

`--from-r2` 接收完整对象名。默认读取目标 Server 数据库中已有的 R2 凭据。刚安装的 Server 可用仅当前用户可读的 `--r2-config` 文件提供 `endpoint`、`bucket`、`access_key_id` 和 `secret_access_key` 四个 JSON 字段。该文件中的 `endpoint` 接受 R2 账号 ID 或完整 endpoint。

恢复后使用来源 Server 的面板凭据及 Web 地址。恢复命令会显示原文件保存位置，备份计划会暂停。在新 Server 上再次启用前，请检查 R2 前缀和私钥。
