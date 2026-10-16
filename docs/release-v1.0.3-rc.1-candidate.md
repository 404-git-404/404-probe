# v1.0.3-rc.1 本地候选构建契约

1. 精确冻结并核对当前分支中的已验收待发源码与最小RC准备改动；排除.tools、数据库、凭据、日志及M14–M19。
2. 使用现有Git身份在当前分支建立本地集成Commit。不得用旧HEAD或伪造VCS字段表示新源码。提交后工作树必须干净。
3. 显式设置`PROBE_404_RELEASE_COMMIT`为当前完整HEAD，设置仓库外独立输出目录，运行`bash scripts/build-release-assets.sh v1.0.3-rc.1`。既有构建脚本支持无tag的精确干净Commit审阅模式；不创建任何引用。
4. 四个ELF逐项核对linked Version、Commit、真实Go VCS revision/modified、模块路径与GOOS/GOARCH。应为v1.0.3-rc.1、同一完整Commit、modified=false，Linux amd64/arm64、对应cmd/server或cmd/agent。
5. 七资产精确为：404-probe-server-linux-amd64、404-probe-server-linux-arm64、404-probe-agent-linux-amd64、404-probe-agent-linux-arm64、install.sh、RELEASE-METADATA.json、SHA256SUMS。
6. metadata schema2覆盖四个ELF；RC最低Server v1.0.3-rc.1、升级协议2。SHA256SUMS以严格GNU双空格格式认证四个ELF、metadata和installer六项，不循环包含自身；七项各自SHA再记录于外部资产清单。
7. 交独立High：Debian12/13新装、已有Server升级、27→28→29/28→29、完整main中断恢复、SQLITE_BUSY与假恢复成功对照、TCP持久化/跨日以及Agent兼容。使用明确隔离资源，不把跨编译或Windows检查记成Linux生命周期通过。
8. 提交真实资产、精确文件/Commit/版本和兼容清单给用户确认。批准前禁止tag、push、GitHub Release、生产部署及用户设备升级。候选准备本身不等于发布Go。

本文件不嵌入自身Commit哈希；最终真实身份以构建后二进制VCS、metadata及外部不可变交包记录共同核验，避免自引用Commit或事后修改已构建源码。
