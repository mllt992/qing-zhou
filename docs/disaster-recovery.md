# 灾备恢复

## 范围与开关

`系统设置 → 数据备份` 默认上传一个 SQLite 一致性快照。服务器运维人员设置
`QZ_BACKUP_MANIFEST=/absolute/path/recovery.json` 后，手动和定时远端备份会改为上传
`.tar.gz` 灾备恢复包。

恢复包包含：

- `database/qingzhou.db`：通过 SQLite `VACUUM INTO` 生成的已提交数据快照；
- `files/`：由服务器清单显式允许的环境文件、systemd unit、sing-box/Tunnel 配置或私钥；
- `manifest.json`：归档时间、版本、平台、原始路径、权限、大小、SHA-256 和缺失的可选项；
- `RESTORE.md`：隔离恢复步骤。

源代码和面板二进制不放入恢复包；应由受保护的 Git 仓库与 `manifest.json` 中记录的版本重新构建。
清单由服务器文件控制，管理员网页不能指定任意路径。必需文件缺失、符号链接、特殊文件、在线
SQLite 文件、超过 256 MiB 原始数据或 10,000 个文件都会使该次备份失败，不会上传不完整的包。

示例（仅按实际运行路径调整，切勿提交生产内容）：

```json
{
  "repository": "https://github.com/your-org/qing-zhou",
  "sing_box_version": "1.x with required features",
  "sources": [
    { "path": "/opt/qingzhou/qingzhou.env" },
    { "path": "/etc/systemd/system/qingzhou.service" },
    { "path": "/etc/systemd/system/qingzhou-sing-box.service" },
    { "path": "/etc/qingzhou-sing-box" },
    { "path": "/etc/cloudflared/config.yml", "optional": true },
    { "path": "/etc/cloudflared/tunnel-credentials.json", "optional": true }
  ]
}
```

## 未额外加密的风险

本实现不对归档再做客户端加密。数据库中的敏感设置仍由 `QZ_SECRET_KEY` 加密，但恢复包可能包含
`QZ_SECRET_KEY`、SSH 私钥、Tunnel 凭据或证书；因此它必须只存入私有桶。不要公开 bucket、设置公开
下载、提交下载链接、或将归档放进 Git。对象存储访问密钥、Cloudflare MFA 恢复方式及 Git 恢复权限必须
在服务器之外独立保管。

R2 使用账户 S3 Endpoint，不附加 Bucket 路径；完整灾备包只允许 HTTPS Endpoint。保留策略删除的是
已记录的远端对象；在启用自动删除前确认保留天数与份数适合恢复目标。

## 恢复演练

1. 下载包到隔离目录，核对面板记录中的整包 SHA-256，再核对 `manifest.json` 的每个文件哈希、大小和路径。
2. 用清单记录的仓库和版本构建匹配面板，安装相同功能集的 sing-box；不要恢复未知二进制。
3. 停止目标面板、sing-box 和 Tunnel 服务；保留目标机现有数据，禁止覆盖运行中的 SQLite。
4. 人工确认 `destination` 后恢复数据库和配置，按清单恢复权限、所有者和 `QZ_SECRET_KEY`。缺失原密钥时，已有敏感设置无法解密。
5. 根据环境重新核对 DNS、Tunnel、反向代理、防火墙与安全组；它们可能不在包中。
6. 对恢复的数据库执行 `PRAGMA integrity_check`，执行 `systemctl daemon-reload`，在隔离环境验证登录、订阅、节点、流量与健康接口后再切换生产。

每次变更服务定义、外部证书/凭据路径、环境变量或节点配置后，更新恢复清单并执行一次非破坏性恢复演练。

## 在线更新快照与二进制回滚

**二进制回滚不等于数据库降级。** 旧程序不保证兼容新 schema、约束或数据；此功能不执行自动向下迁移，
也不在线覆盖数据库。安装指定版本（包括降级）、安装最新版本和离线二进制回滚都会先创建当前数据库快照，
成功后才允许替换程序。下载、SHA-256 和发布签名检查通过后调用 `Store.BackupTo`，由 SQLite
`VACUUM INTO` 生成包含已提交 WAL 数据的单文件一致性快照；失败会中止操作，当前程序和原回滚程序不被替换。

### 位置、命名与权限

快照存放在实际数据库文件同目录的 `upgrade-snapshots/`，不使用可能为 tmpfs 的系统临时目录。
每份快照使用 `snapshot-<UTC 时间（纳秒）>-<16 位随机十六进制>` 作为目录名，版本号不会成为路径。
目录内有 `database.db` 和 `metadata.json`；元数据记录源版本、目标版本、创建完成时间、源 Git revision、
字节数和 SHA-256。Git revision 来自 Go build info；未包含 VCS 信息的构建明确记录 `unknown`，不推测 revision。

根目录和单份目录为 `0700`，数据库及元数据为 `0600`。先写私有 `.pending-*` 目录，同步数据库、元数据和目录，
再在同文件系统原子重命名发布。未完成的目录不出现在下载列表，下一次操作会清理遗留的未发布目录。
错误权限、符号链接或损坏元数据会导致失败，需要服务器管理员检查；不会通过自动放宽权限继续升级。

最多保留 **5 份已发布快照**：始终保护 `.prev.meta` 记录的当前回滚程序对应快照，其余按最新优先保留。
创建前先清理超过 4 份的旧快照以腾出一个名额；因此一次失败尝试可能清理旧历史，但不会删除当前回滚关联快照。
这是一项份数限制，并非字节配额：预留至少一个完整数据库快照及待下载程序的可用磁盘空间。
磁盘空间不足（包括写入或同步时报错）、权限错误、数据库备份错误或元数据持久化错误均中止升级，禁止跳过快照。

管理员在「在线更新」可查看版本、revision、时间、校验值并下载快照；回滚区域显示与 `.prev` 关联的快照。
`GET /api/admin/update/snapshots` 与 `GET /api/admin/update/snapshots/{id}/download` 都要求已登录管理员，
沿用管理 API 的令牌作用域限制。下载验证 SHA-256，响应禁止缓存。旧版本留下的 `.prev` 可能没有匹配快照，
页面会明确提示。数据库包含敏感信息，快照仅保存原加密列，**不包含恢复这些列所需的 `QZ_SECRET_KEY`**。

### 人工恢复步骤

1. 在管理员页面下载对应快照，记录其源版本、时间、revision 和 SHA-256；也可由服务器管理员读取磁盘上的目录。
   使用 `sha256sum` 核对下载文件与记录。恢复会丢失该快照之后的写入，先决定是否需要导出、补录或保留这些数据。
2. 停止轻舟面板和其他数据库写入者；另行保存当前数据库及同名 `-wal`、`-shm` 文件作为回退资料。
   不要复制或覆盖正在运行的 SQLite 文件，不要把旧 WAL 与恢复快照混用。
3. 在隔离环境使用快照的**源版本**程序与原 `QZ_SECRET_KEY` 演练恢复。将快照放回实际 `QZ_DB`；
   旧 `-wal`、`-shm` 应已移入备份目录。设置数据库权限为 `0600`，所有者为运行服务的用户。
4. 启动前用 SQLite 执行 `PRAGMA integrity_check;`，结果应为 `ok`。验证登录、订阅、节点、流量和敏感配置解密。
   确认恢复点与业务影响后，再在正式环境重复停服恢复步骤并启动服务。
5. 如果新版本完全不能启动，网页回滚功能也不可用。由服务器管理员检查服务日志，在停服状态下核对
   `.prev.meta` 与快照元数据，恢复匹配的数据库和二进制；不要只覆盖 `.prev` 就假设完成了数据库降级。

回滚操作本身也会快照当前数据库，以便保留新版本下产生的数据供人工恢复；它不会自动加载较早快照。
回滚后 `.prev` 与对应快照会转为本次离开的版本，仍可在需要时离线回到该版本。
