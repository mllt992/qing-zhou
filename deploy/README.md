# 部署 / 运维

## Docker（最省事）

面板是中心机、SSH 管远程落地，容器不需要跑 sing-box，只需持久化 DB。镜像内置两架构探针，支持 amd64/arm64。

```bash
# 按根目录 README 创建或保留 .env 中的 QZ_SECRET_KEY，并设置 QZ_PUBLIC_BASE
docker compose up -d
docker compose logs -f qingzhou     # 首启打印随机管理员密码（未设 QZ_ADMIN_PASS 时）
```

要点：容器内 `QZ_LISTEN=0.0.0.0:8081`（镜像默认）；DB 在 `/data`（挂卷持久化）；生产必设 `QZ_SECRET_KEY`；
以 uid 10001 非 root 运行（命名卷可直接写，bind 挂载需 `chown 10001`）。**升级用「拉新镜像 + 重建容器」，
不要用面板内在线更新。** 详见 Wiki「Docker 部署」。自建镜像 `docker build --build-arg VERSION=<tag> -t qingzhou .`；
发 release 时 `.github/workflows/docker.yml` 会自动构建并推送到 GHCR。

## sing-box（每台落地机）
轻舟自管原生 sing-box。每台落地服务器先跑一键脚本（已装则检测、未装则装到
`/usr/local/bin/sing-box` + systemd + 内核调优，并输出可填入面板「服务器」的信息）：
```bash
curl -fsSL https://<你的面板域名>/install-singbox.sh | bash
```
> ⚠️ **必须用带 `v2ray_api` 的构建。** 脚本装的是**本项目发布页**的 sing-box —— 与上游同版本、
> 同功能，但额外编入了 `with_v2ray_api`（`release.yml` 里从上游源码构建并断言该 tag 存在）。
> **官方 release 不含这个插件**，而面板正是靠它读每用户流量：装官方版的节点流量恒为 0、配额
> 永不生效，界面上还看不出异常。脚本从发布页下载失败时会回退官方版，并明确告警统计不可用。
> 「服务器管理」页会显示各机实际版本，缺 `v2ray_api` 或版本过低都会点破，可逐台一键重装。

面板「**系统设置 → 面板访问地址**」会按你配置的地址生成可一键复制的完整命令。访问地址来源优先级：
`QZ_PUBLIC_BASE` 环境变量 > 设置页「访问地址」> 反代头/请求 Host。

本机落地用脚本输出的默认值即可（`QZ_SINGBOX_*`）；远程落地在面板「服务器」新增并填写。

## 探针监控（可选，每台服务器）
探针 `qingzhou-probe` 上报 CPU/内存/磁盘/负载，安装命令在面板「监控管理」页获取。

> **注意**：探针二进制由面板托管下载（`/api/monitor/agent/linux-<arch>`），从 `QZ_PROBE_DIR`
> 指定的目录读取，默认相对路径 `cmd/probe/dist`。二进制部署时该相对目录通常不存在，探针安装会
> 报「下载失败: HTTP 404」。请从 GitHub Release 下载 `probe-linux-amd64` / `probe-linux-arm64`
> 放到一个目录（文件名不变），并在 env 里设 `QZ_PROBE_DIR=/opt/qingzhou/probe`。

## 目录约定（服务器）
- 程序：`/opt/qingzhou/qingzhou`
- 配置：`/opt/qingzhou/qingzhou.env`（`chmod 600`，见 `qingzhou.env.example`）
- 数据库：`/opt/qingzhou/qingzhou.db`（WAL 模式）
- 备份：`/opt/qingzhou/backups/`

## 安装

### 脚本一键安装（推荐）
仓库根目录的 `install.sh` 会自动识别架构、下载最新 release 并校验 SHA-256、交互式引导写出
`qingzhou.env`（密钥自动生成、可选托管探针二进制）、装好 systemd 并启动；已安装时则升级
（配置不动、二进制原子替换，并顺带刷新 `QZ_PROBE_DIR` 里的探针；面板「在线更新」以及新版本启动时同样会把该目录对齐到当前 release，避免一键安装仍下发旧探针）：
```bash
bash <(curl -fsSL https://raw.githubusercontent.com/mllt992/qing-zhou/main/install.sh)
# 选项：--version vX.Y.Z | --force | --proxy https://mirror.ghproxy.com/
```

监听地址问成二选一：`1` = `0.0.0.0:8081` 直连公网（默认），`2` = `127.0.0.1:8081` 走反代。
选错不必重装：改 `QZ_LISTEN` 后 `systemctl restart qingzhou` 即可。`QZ_LISTEN` 环境变量存在时跳过询问。

### 卸载
安装时脚本会把自身存一份到 `/opt/qingzhou/install.sh`（curl 安装则回源下载一份），所以：
```bash
bash /opt/qingzhou/install.sh uninstall
```
先停服务、删 systemd 单元与二进制，再单独确认是否删除 `/opt/qingzhou`（数据库、配置、探针）。
`QZ_SECRET_KEY` 在配置文件里，删掉后备份的加密内容将无法解密，删前想清楚。

### 手动安装
```bash
# 1. 二进制 + 配置
install -m755 qingzhou /opt/qingzhou/qingzhou
install -m600 qingzhou.env /opt/qingzhou/qingzhou.env   # 按 env.example 填好

# 2. systemd
cp deploy/qingzhou.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now qingzhou

# 3. nginx 反代到 127.0.0.1:8081。兼容旧系统客户端的当前建议：
certbot --nginx -d panel.example.com --key-type rsa --rsa-key-size 2048 --preferred-chain "ISRG Root X1"
```

`--preferred-chain` 是兼容性偏好，不应代替签发后的核验。上线前请确认实际
证书域名、有效期、密钥匹配和完整链均正确；若你的用户设备均为现代系统，也可
按 CA 默认链或使用 ECDSA。证书中心的新申请默认使用可演进的「自动兼容」策略，
升级不会给已有证书强行换密钥或换链。

## 更新

### 面板内在线更新（推荐）
管理后台 →「在线更新」页可一键升级：面板读取 GitHub 最新 release，显示变更日志，
点「立即更新」后自动**下载对应架构二进制 → 校验 SHA-256 → 原子替换 → 进程自我重启**
（同 PID，兼容 `Restart=on-failure`，约 1~2 秒短暂中断）。要能生效，二进制必须带内置
版本号（见下方构建说明），且发布产物里有 `qingzhou-linux-<arch>` 资产——`.github/workflows/release.yml`
会在你于 GitHub 上发布 release 时自动构建并上传（版本号自动注入 = release tag）。

> 仅 Linux 部署支持自更新；其他平台请手动替换。可选环境变量：
> `QZ_UPDATE_REPO`（默认 `mllt992/qing-zhou`）、`QZ_UPDATE_GITHUB_TOKEN`（提升 GitHub API 速率上限）。

### 手动更新
```bash
systemctl stop qingzhou        # 或直接覆盖后 restart
install -m755 qingzhou /opt/qingzhou/qingzhou
systemctl restart qingzhou
```

### 从源码构建（必须注入版本号，否则在线更新无法比较版本）
```bash
# 1. 构建内嵌前端
cd frontend && npm ci && npx vite build && cd ..
# 2. 注入版本号（= 目标 release tag）构建面板
go build -ldflags "-s -w -X qingzhou/internal/version.Version=v0.2.38" -o qingzhou .
```
不带 `-X ...version.Version` 时版本为 `dev`，在线更新页会把任意最新 release 视为「可更新」。

## 数据库备份（每日）
```bash
apt-get install -y sqlite3
install -m755 deploy/backup.sh /opt/qingzhou/backup.sh
# cron：每天 04:30，保留 7 天
cat > /etc/cron.d/qingzhou-backup <<'CRON'
30 4 * * * root /opt/qingzhou/backup.sh >> /opt/qingzhou/backups/backup.log 2>&1
CRON
/opt/qingzhou/backup.sh    # 立即跑一次验证
```
`backup.sh` 用 `sqlite3 .backup` 在线热备份 `qingzhou.db`，运行中执行安全。

## 密钥
`QZ_SECRET_KEY`（env 内，DB 外）用于加密 SMTP / Reality 私钥等敏感配置落库。生成：`openssl rand -hex 32`。
一旦设置并写入了加密配置，请勿更换该值，否则已加密内容无法解密。
