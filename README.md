# 轻舟 (QingZhou)

> 面向终端用户的**轻量级多用户代理订阅管理面板** —— 单文件部署，自管原生 sing-box，把「人 / 钱 / 节点 / 订阅 / 统计」一站式管起来。

![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)
![Vue 3](https://img.shields.io/badge/Vue-3-4FC08D?logo=vue.js&logoColor=white)
![SQLite](https://img.shields.io/badge/SQLite-WAL-003B57?logo=sqlite&logoColor=white)
![Single Binary](https://img.shields.io/badge/deploy-single%20binary-success)
![Platform](https://img.shields.io/badge/platform-Linux%20amd64%20%7C%20arm64-lightgrey)

轻舟是一个**开箱即用的代理业务前台**：管理员在可视化后台管理节点、套餐和用户，普通用户注册后用积分自助购买流量包 / 订阅套餐，拿到一条**多端自适应**的订阅链接。真正承载流量的是 **sing-box**（作为独立进程运行），轻舟负责生成并下发它的配置、采集每用户流量、并把整套业务（注册登录、计费、订阅、统计、运营）包好。

**它适合谁**：想给一小群人（家庭 / 朋友 / 小团队）自建、自管代理订阅服务，又不想被臃肿面板和一堆依赖折腾的人。**一台 1H1G 的小机器**就能把面板和落地节点一起跑起来。

---

## ✨ 核心优势

- **🪶 单文件部署** —— 一个 Go 二进制内嵌了 Vue 前端和 SQLite 驱动，无需 Node 构建、无需 CGO、无需外部数据库。丢到服务器上配个 systemd 就能跑。
- **🔌 不依赖任何外部面板** —— 轻舟**自管原生 sing-box**：面板直接生成 `config.json`、下发、reload，并通过 sing-box 官方 `v2ray_api` 读回每用户流量。不套壳、不二次代理别的面板。
- **🎛️ 9 协议纯可视化** —— 下拉、开关、零手写 JSON 即可管理 TLS / Reality 和入站，覆盖 **vless / vmess / trojan / tuic / hysteria2 / shadowsocks / anytls / hysteria**，外加 **mixed（HTTP/SOCKS5 代理账号）**；含传输层（ws/grpc/httpupgrade）与 uTLS / ALPN / Mux / Brutal。
- **📜 证书中心** —— 面板内一键 **ACME 签发真实证书**（Let's Encrypt · Cloudflare DNS-01，只填一个 CF Token），或粘贴已有证书 / 一键自签；证书全局复用、被入站引用、**到期自动续期并推送到各落地机**。
- **🌉 第三方静态 IP 出口** —— 入站可指定一个购买来的 **SOCKS5 / HTTP 代理**作为出口，流量经它出网、出口 IP 即代理 IP；密码库内加密、失效则「fail-closed」不回落直连。
- **📱 订阅多端自适应** —— 同一条链接按客户端 `User-Agent` 自动返回 **Clash / sing-box / Surge**（含官方 SFA/SFI/SFM/SFT 客户端），其余回退 base64；内置 **URLTest 智能优选**（全局选最快 / 锁定 IP 只切协议）与**防泄漏分流模板**（局域网直连 / 广告拦截 / 公网代理），开箱即用。用浏览器打开订阅链接会看到一张**可读的信息页**（剩余流量 / 到期 / 节点数 + 一键复制），而不是一坨 base64。
- **⚡ 面向网速/延迟的调优下发** —— **Brutal / Mux / TCP Fast Open / MPTCP、tuic 0-RTT、ws early-data** 从入站自动镜像到客户端出站——这些两端对称才生效的项，以前配了却在订阅里丢掉，现在端到端对齐（vless vision flow 时自动避让 mux）。
- **🔀 中转 / 落地链路（多级链式）** —— 入站可设「落地 / 中转」：选一个落地入站，本入站即变**线路机**，流量转发到**落地机**再出网（`客户端 → 线路机 → 落地机 → 互联网`）；支持**多级串联**（A→B→C→…）与末端接第三方代理出口；复用已有落地入站、自动派生并注入中转凭据，无需手工配隧道，**链路拓扑图**可视化。
- **🧾 多套餐独立计费（桶模型）** —— 一个用户可同时持有多个套餐，每个套餐是独立的「桶」（各自流量 / 到期 / 节点 / 计量身份）；某套餐到期只下线它名下的节点，互不影响。重复购买**同一套餐自动排队**（不再叠加：一次只跑一份，用完/到期后下一份自动顶上），退款按**剩余流量/时间比例**结算（详见[计费与退款业务说明](docs/计费与退款业务说明.md)）。
- **🌐 多落地服务器** —— 面板在中心机，通过 SSH 把配置下发到多台落地 sing-box，统一管理。
- **🔢 节点 sing-box 版本可见可升** —— 「服务器管理」页长期显示每台机器实际在跑的 sing-box 版本（数据来自面板本来就在做的能力探测，不额外连机器），低于面板要求的最低版本会标红 —— 那种情况下节点的 `sing-box check` 会失败、面板**停止向它下发任何配置**而表面看不出来；装成官方版（不含 `v2ray_api`）导致**流量统计不到**也会当场点破。可**逐台一键重装**为面板发布的版本（安装脚本经 SSH 推过去执行，节点无需能访问面板）。
- **📊 服务器监控** —— 内置探针，多机 CPU / 内存 / 磁盘 / 负载 / 流量实时采集，可用性热力图 + 每机趋势卡片。
- **🔒 安全内建** —— 敏感配置（SMTP 密码 / Reality 私钥等）库内 **AES-256-GCM 加密**；登录 / 注册 / 找回按 IP 限流；JWT 绑 `jti` 支持会话吊销；生成的节点配置**默认阻断私网出口**（内网段 / `127.0.0.1` / 云元数据 `169.254.169.254`），用 IP 或域名访问都拦得住（先解析再判断，否则一个指向内网的 A 记录就能绕过），用户拿不到落地机的内网身份。
- **⏮️ 版本可回退** —— 「在线更新」页除了升级，还能**一键回滚到上一个版本**（用的是升级时留在本机的旧二进制，**不联网、不重新下载**，新版本起不来时这是唯一还能走的路径；回滚后当前版本成为新的回滚目标，点错还能回来），或从版本列表里**指定安装任意历史版本**。降级带明确提示：库结构只增不减，旧版本会忽略多出来的列，但仍建议先导出备份。
- **💾 在线备份** —— 面板一键导出整库一致性快照（`VACUUM INTO`，单文件、无 `-wal` 附件）。WAL 模式下直接拷贝 `.db` 拿到的是残缺副本，这里由 SQLite 自己在读事务上生成，导出期间面板照常读写。
- **🚀 为小机器而生** —— WAL 模式 SQLite、纯 Go 无 CGO、内存占用低，1H1G 可同时跑面板 + 落地。

---

## 📸 界面预览

<table>
<tr>
<td width="50%"><img src="docs/images/monitor.png" alt="服务器监控" /></td>
<td width="50%"><img src="docs/images/topology.png" alt="链路拓扑" /></td>
</tr>
<tr>
<td align="center"><b>服务器监控</b> · 多机 CPU / 内存 / 磁盘 / 流量实时采集 + 可用性热力图</td>
<td align="center"><b>链路拓扑</b> · 客户端 → 入站 →（中转 → 落地）→ 互联网</td>
</tr>
<tr>
<td width="50%"><img src="docs/images/nodes.png" alt="节点管理" /></td>
<td width="50%"><img src="docs/images/online-update.png" alt="在线更新" /></td>
</tr>
<tr>
<td align="center"><b>节点管理</b> · 按分组卡片聚合节点</td>
<td align="center"><b>在线更新</b> · 读取 GitHub release，一键校验并升级</td>
</tr>
</table>

> 前端为单页应用，移动端自适应（侧栏转顶部、宽表横向滚动）。

---

## 🧩 功能一览

<table>
<tr><th>用户端</th><th>管理端</th></tr>
<tr valign="top"><td>

- 注册 / 登录 / 邮箱验证 / 找回密码
- 控制台：按**套餐维度**呈现各桶的流量 / 到期 / 节点，积分、趋势图、公告
- **订阅管理**：聚合链接 + 二维码，多端格式自适应；节点列表按套餐分组并显示链路拓扑
- 智能优选：`♻️ 自动选择` / `⚡ 锁定服务器` / `✈️ 节点选择`
- 订阅安全：「更换订阅地址」与「重置节点凭据」是两个独立操作，可只换链接不动凭据
- **节点管理**：筛选、测速、条件启用、批量开关（仅对自己生效）
- mixed 代理账号一键复制为 URL，直接喂给浏览器插件 / 终端
- 积分商城：积分自助购买流量包 / 订阅套餐，订单与积分流水（含可视化）
- 个人中心：改邮箱 / 改密码 / 登录设备管理（远程注销）

</td><td>

- 数据总览：时间范围 + 套餐 / 用户两个维度，Top 榜、分布、趋势
- **用量分析**：多选用户 × 任意时间段 × 套餐维度看流量消耗，图表可点击反向筛选
- 用户管理：按**套餐维度**呈现，新建 / 充值 / 改额度·到期·封禁 / 重置密码 / 移除套餐 / 删除
- **用户组**：把用户分组，套餐可勾选「限定哪些组可购买」（不勾 = 公开）；注册码可携带用户组，用该码注册自动入组
- 套餐运营：直接开通（赠送）、**按剩余比例退款**（退款前预览 + 撤销权益）、上下架、消费总览
- 商品：流量包 / 订阅套餐 CRUD，套餐绑定节点分组
- **原生 sing-box 管理**：可视化管 TLS/Reality + 9 协议入站；**证书中心**（ACME/粘贴/自签、自动续期）；**代理出口**（第三方静态 IP）
- **链路拓扑**：客户端→入站→(多级中转→落地→代理出口)→互联网，一键串联落地 / 解除中转
- 节点 & 分组：按分组卡片聚合、可手动排序；自建 + 外部机场源抓取（base64 / Clash YAML）
- **服务器监控**：多机资源 / 流量 / 负载 / 可用性热力图；阈值告警按「事件」聚合，不刷重复条目
- **在线更新**：升级 / 回滚上一版 / 指定任意历史版本；一键导出整库快照备份
- 注册码、公告、帮助文档（Markdown 实时预览）、系统设置

</td></tr>
</table>

---

## 🏗️ 架构

```
                          用户浏览器 / 代理客户端
                                   │  HTTPS
                          ┌────────▼─────────┐
                          │   Nginx / Caddy  │  443 + 证书
                          └────────┬─────────┘
                                   │  反向代理
                       ┌───────────▼────────────┐
                       │   轻舟面板 (单 Go 二进制) │   ← 内嵌 Vue 前端 + SQLite
                       │   业务 / 订阅 / 统计 / 管理 │
                       └───┬───────────────┬─────┘
              生成配置+SSH下发 │               │ v2ray_api gRPC 读流量
                       ┌───▼───┐        ┌───▼───┐
                       │sing-box│  ....  │sing-box│   ← 一台或多台落地
                       │ 落地#1 │        │ 落地#N │
                       └────────┘        └────────┘
```

- **面板与 sing-box 可同机**（最简单）**，也可分离**：面板在中心机，落地 sing-box 在多台机器，面板用 SSH 下发。
- 轻舟生成 sing-box 的 `config.json` → `sing-box check` 校验 → 原子替换 → reload；再按 `v2ray_api` 采集每用户上下行流量。
- **中转链路（可选）**：某个入站可指定「落地入站」，其流量经出站转发到另一台机器的落地入站再出网 —— `客户端 → 线路机入站 → 落地机入站 → 互联网`；可**多级串联**，末端还能接第三方代理出口，per-user 计量在入口侧完成。

---

## 🚀 快速开始

### 环境要求

- **Go 1.25+**（仅编译时需要；用 release 二进制或 Docker 则不需要）
- 一台 **Linux amd64 / arm64** 服务器（落地节点用）；面板本身跨平台，但**在线更新与一键安装脚本仅支持 Linux**
- 可选：域名 + HTTPS 证书（生产强烈建议）、SMTP（邮箱验证 / 找回密码用）
- **客户端 sing-box ≥ 1.12**（仅当用户使用 sing-box 格式订阅时）。下发的 sing-box 模板用 1.12 引入的
  DNS 服务器写法（`{"type":"https","server":"1.1.1.1"}`），因为 **1.14 已移除旧的 `address` 写法**——
  继续用旧写法的订阅在 1.14 上不是告警而是直接加载失败。两种写法无法同时满足 1.11 和 1.14，因此底线定在 1.12。
  Clash / Surge / base64 订阅不受影响。管理员自定义的 sing-box 模板会在下发时自动改写成新写法，无需手工迁移。

### 〇、脚本一键安装 / 更新（Linux 裸机推荐）

自动识别架构（amd64/arm64）、下载 GitHub 最新 release、SHA-256 校验、交互式引导配置（监听地址 / 访问地址 / 管理员账号，密钥自动生成）、装好 systemd 并启动；**已安装则原地升级**（配置与数据库不动，二进制原子替换）：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/mllt992/qing-zhou/main/install.sh)
```

常用选项：`--version vX.Y.Z` 装指定版本；`--force` 同版本强制重装；`--proxy https://mirror.ghproxy.com/` 国内下载加速。装完后升级可重跑脚本，或直接用面板内「在线更新」。

安装时会问「面板打算怎么访问」：**直接用 IP:端口打开选 1**（监听 `0.0.0.0:8081`，默认项）；前面有 nginx/caddy 反代才选 2（监听 `127.0.0.1:8081`）。选错了不用重装，改配置重启即可：

```bash
sed -i 's|^QZ_LISTEN=.*|QZ_LISTEN=0.0.0.0:8081|' /opt/qingzhou/qingzhou.env && systemctl restart qingzhou
```

**卸载**（脚本安装时已把自己存了一份到 `/opt/qingzhou/install.sh`）：

```bash
bash /opt/qingzhou/install.sh uninstall
```

先停服务、删 systemd 与二进制，再单独确认是否连数据库和配置一起删（输 `yes` 才删 `/opt/qingzhou`）。老版本装的没有这份副本，直接 `bash <(curl -fsSL https://raw.githubusercontent.com/mllt992/qing-zhou/main/install.sh) uninstall`。

### 一、Docker 一键部署（最省事）

面板是中心机、SSH 管远程落地，容器不需要跑 sing-box；镜像内置两架构探针，支持 amd64/arm64。

```bash
git clone https://github.com/mllt992/qing-zhou.git && cd qing-zhou
# 改 docker-compose.yml 里的 QZ_PUBLIC_BASE 与 QZ_SECRET_KEY(openssl rand -hex 32)
docker compose up -d
docker compose logs -f qingzhou     # 首启打印随机管理员密码（未设 QZ_ADMIN_PASS 时）
```

或直接用镜像：`docker run -d -p 8081:8081 -e QZ_SECRET_KEY=$(openssl rand -hex 32) -v qingzhou-data:/data ghcr.io/mllt992/qing-zhou:latest`。**Docker 用「拉新镜像 + 重建容器」升级**，详见 [Wiki · Docker 部署](https://github.com/mllt992/qing-zhou/wiki/Docker-部署)。

通过当前发布工作流新构建的 GHCR 镜像会携带构建来源证明（provenance）与 SBOM，并标记
对应的源码 revision。可用 Buildx 查看镜像清单和证明；把示例版本换成实际安装的 tag：

```bash
docker buildx imagetools inspect ghcr.io/mllt992/qing-zhou:vX.Y.Z
docker buildx imagetools inspect ghcr.io/mllt992/qing-zhou:vX.Y.Z --format '{{json .Provenance}}'
docker buildx imagetools inspect ghcr.io/mllt992/qing-zhou:vX.Y.Z --format '{{json .SBOM}}'
```

### 二、本地开发运行

前端产物不入库（仓库里 `frontend/dist` 只有一个占位文件），所以**先构建一次前端**，否则面板是白页：

```bash
cd frontend && npm install && npx vite build && cd ..
QZ_LISTEN=127.0.0.1:8081 go run .
```

Windows PowerShell 可直接用 `./start.ps1`（已设好上述环境变量）。

改前端时有两种方式：

| 方式 | 做法 | 适用 |
| --- | --- | --- |
| **热更新**（推荐） | 另开终端 `cd frontend && npm run dev`，访问 <http://127.0.0.1:5173> | 改前端，vite 把 `/api`、`/sub` 代理到 8081 的后端 |
| **直读磁盘** | `npx vite build` 后设 `QZ_WEB_DIR=frontend/dist` 启动 | 想在 8081 一个端口上看完整效果，改完重新 build 刷新即可，无需重编 Go |

> 用 `npx vite build` 而不是 `npm run build`：后者会先跑 `vue-tsc`，而仓库目前有一批与业务无关的既有类型错误，CI（`release.yml`）用的也是 `npx vite build`。

浏览器访问 <http://127.0.0.1:8081>。**首次启动**会自动初始化数据库并创建管理员账号：

- 用户名默认 `mllt992`（可用 `QZ_ADMIN_USER` 指定）
- 未设 `QZ_ADMIN_PASS` 时，会**随机生成密码并打印到启动日志**（请到终端查看并首登后立即改密）

### 三、编译生产二进制（单文件，内嵌前端）

```bash
cd frontend && npx vite build && cd ..     # 前端产物会被编译进二进制，必须先构建
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -ldflags "-s -w" -o qingzhou .
```

不设 `QZ_WEB_DIR` 时使用内嵌前端资源，**单文件即可部署**。注意 `frontend/dist` 是在 `go build` 时被内嵌的——改了前端只重编 Go 是没用的，要先 `vite build`。

自己编译出来的二进制不校验发布签名（见下），无需任何额外配置。

### 四、最小可用部署（单机把面板 + 落地一起跑，手动版）

```bash
# 1) 在落地服务器上一键安装 sing-box（含 v2ray_api 的构建 + systemd + 内核调优）
#    脚本由面板托管，把域名换成你自己的面板地址（也可在「系统设置 → 面板访问地址」一键复制）：
curl -fsSL https://<你的面板域名>/install-singbox.sh | bash
#    ⚠️ 装的是本项目发布页的 sing-box —— 与上游同版本，但额外编入了 with_v2ray_api。
#    官方 release 不带这个插件，而面板正是靠它读每用户流量：装官方版的节点流量恒为 0、
#    配额永不生效，且界面上看不出异常。脚本下载失败时会回退官方版并明确告警。

# 2) 放置二进制并写配置
install -Dm755 qingzhou /opt/qingzhou/qingzhou
cat >/opt/qingzhou/qingzhou.env <<'EOF'
QZ_LISTEN=127.0.0.1:8081
QZ_DB=/opt/qingzhou/qingzhou.db
QZ_SECRET_KEY=<openssl rand -hex 32 生成>
QZ_PUBLIC_BASE=https://<你的面板域名>
QZ_SINGBOX_BIN=/usr/local/bin/sing-box
QZ_SINGBOX_CONFIG=/etc/sing-box/config.json
QZ_SINGBOX_UNIT=sing-box
QZ_SINGBOX_V2RAY=127.0.0.1:18080
EOF
chmod 600 /opt/qingzhou/qingzhou.env

# 3) 安装 systemd 服务
cp deploy/qingzhou.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now qingzhou

# 4) 用 Nginx/Caddy 反代到 127.0.0.1:8081，配好 443 证书（转发 Host 与 X-Forwarded-*）
```

> 完整部署、多落地、协议选型、排错见 **[部署与配置手册](docs/部署与配置手册.md)**。

---

## 🗺️ 部署后怎么用

### 管理员（首次配置顺序）

登录后台后，按此顺序一次配好即可对外服务：

1. **服务器** —— 单机落地用默认值即可；多落地则「新增」，填一键脚本输出的 host / SSH / 路径。
2. **TLS / Reality** —— 一键生成 Reality 密钥对 + short_id，填个借用域名（如 `www.microsoft.com`），抗封锁且无需自有证书。需要真实证书（套 CDN 的 WS-TLS 等）时，去 **证书管理** 一键 ACME 签发（Let's Encrypt，填 Cloudflare Token 走 DNS-01）或粘贴已有证书，再在入站里「引用证书」。
3. **入站（节点）** —— 选协议建入站并绑定上一步的 TLS；同机可建多协议入站，订阅会自动聚成「锁定 IP·切协议」优选组。需要中转时，在入站的「落地 / 中转」里选一个落地入站即可（或在 sing-box→链路拓扑里一键「串联落地」，支持多级）；想让某入站从**购买的静态 IP** 出网，则在「代理出口」页录入该 SOCKS5/HTTP 代理后，在入站上选它作出口。
4. **分组** —— 把节点归类（免费 / 高级…），订阅与套餐都按分组授权；在「设置」里可指定**免费分组**（所有用户可见）。
5. **套餐（商品）** —— 建流量包 / 订阅套餐并绑定可访问的分组。
6. **机场订阅源（可选）** —— 粘贴外部机场订阅链接，定时抓取聚合进来。

### 普通用户

注册 → （用积分购买套餐，或由管理员开通）→ 打开「**订阅管理**」复制链接或扫码 → 导入客户端。同一条链接通用所有客户端，自动返回对应格式。

> 客户端导入、智能优选、分流说明见 **[订阅使用指南](docs/订阅使用指南.md)**。

---

## ⚙️ 配置项

环境变量在启动时读取，优先级高于面板内「系统设置」。**SMTP 相关改后即时生效**，其余环境变量改动需重启服务。

| 变量 | 默认值 | 说明 |
|---|---|---|
| `QZ_LISTEN` | `0.0.0.0:8081` | 监听地址，默认公网可达（明文 HTTP，建议尽快套反代 + 证书）。**前置 nginx/Caddy 时改成 `127.0.0.1:8081`**，面板就只有本机连得上。升级时若配置里没有这一项，一键脚本会按原行为写入回环，不会替你扩大监听范围 |
| `QZ_DB` | `qingzhou.db` | SQLite 数据库文件路径（相对路径基于进程工作目录） |
| `QZ_PUBLIC_BASE` | 设置页/请求推断 | 面板对外地址（订阅链接、探针安装、邮件链接、sing-box 安装命令），如 `https://node.example.com`。也可在「系统设置 → 面板访问地址」填；本变量优先，且设了之后设置页对应字段变只读 |
| `QZ_TRUSTED_PROXIES` | 空（仅信回环） | 受信任反代的 IP / CIDR，逗号分隔。**反代不在本机时必须设**，否则转发头被忽略，限流与链接生成会拿到反代的 IP |
| `QZ_PROBE_DIR` | `cmd/probe/dist` | 探针二进制目录，面板据此提供下载与「一键安装」。二进制部署须设为绝对目录（放入 `probe-linux-amd64/arm64`），否则探针安装 404。`install.sh`、面板「在线更新」、以及新版本启动时都会把该目录对齐到当前 release，避免只升面板、一键安装仍下发旧探针 |
| `QZ_WEB_DIR` | 空 | 设为 `frontend/dist` 从磁盘读前端；生产留空用内嵌资源 |
| `QZ_ADMIN_USER` | `mllt992` | 初始管理员用户名（仅首次 seed 生效）。**一键脚本安装时会问，默认写入 `admin`** |
| `QZ_ADMIN_PASS` | 随机生成 | 初始管理员密码；留空则随机生成并打印到日志 |
| `QZ_SECRET_KEY` | 回退 jwt_secret | **加密库内敏感配置的主密钥**，建议 `openssl rand -hex 32`，置环境变量不入库。**一旦使用勿再更换**，否则已加密内容无法解密 |
| `QZ_UPDATE_REPO` | `mllt992/qing-zhou` | 「在线更新」检查的 GitHub 仓库；fork 后想发自己的版本就改成你的仓库 |
| `QZ_UPDATE_GITHUB_TOKEN` | 空 | 仅用于提升 GitHub API 匿名速率上限（60/时），公开仓库无需任何权限 |
| `QZ_SINGBOX_BIN` | 自动探测 | sing-box 可执行路径。顺序：本变量 → 常见安装路径 → `PATH` |
| `QZ_SINGBOX_CONFIG` | `/etc/sing-box/config.json` | 面板下发的配置路径 |
| `QZ_SINGBOX_UNIT` | `sing-box` | sing-box 的 systemd 服务名 |
| `QZ_SINGBOX_V2RAY` | `127.0.0.1:18080` | v2ray_api gRPC 监听地址（统计用，须与配置一致） |
| `QZ_MONITOR_PROBE_INTERVAL` | `60s` | 节点探针与面板本机的指标采集周期；可在「系统设置 → 采集与同步」在线修改，本变量设置后界面只读 |
| `QZ_SINGBOX_STATS_INTERVAL` | `10m` | 采集用量并生成期望配置（剔除超额/到期用户）的周期；可在设置页在线修改，本变量优先 |
| `QZ_SINGBOX_RECONCILE_INTERVAL` | `60m` | 绕过摘要缓存，核对远端配置文件与 sing-box 运行状态的周期；可在设置页在线修改，本变量优先 |
| `QZ_SMTP_HOST` / `PORT` / `USER` / `PASS` / `FROM` / `FROM_NAME` / `SECURITY` | — | SMTP 配置（也可在面板「设置」页填，密码加密存储） |

> 完整示例见 [`deploy/qingzhou.env.example`](deploy/qingzhou.env.example)。

---

## 🛠️ 技术栈

| 层 | 选型 |
|---|---|
| 后端 | Go 1.25，路由 `go-chi/v5`，鉴权 `golang-jwt/v5` |
| 数据库 | SQLite（`modernc.org/sqlite`，纯 Go 免 CGO，WAL 模式） |
| 前端 | Vue 3 + Vite 单页应用，构建产物由 `embed.FS` 编进二进制（**改前端必须先 `vite build`**），手绘 SVG 图表 + `qrcode-generator` |
| sing-box 对接 | `internal/singbox`（配置生成 / 分享链接 / Reality 密钥）、`sbproc`（写→check→原子替换→reload）、`sbstats`（手写 gRPC-over-h2c 读 v2ray 统计）、`sbctl`（编排）、`sshctl`（多机下发）、`acmesh`（ACME 证书签发 / 续期） |
| 订阅转换 | `internal/subconv`：vless/vmess/ss/trojan/hysteria2/tuic ↔ base64 / Clash / sing-box / Surge，注入优选组 + 防泄漏分流模板 |

---

## 🔌 API 概览

鉴权：`Authorization: Bearer <JWT 或 API Token>` 或 `qz_token` Cookie。

机器用 **API Token**（`qz_at_…`，管理页 `/admin/api-tokens`）：可 scope / 过期 / 吊销；首批 scope 为 `stats:read`、`users:read`、`nodes:read`、`servers:read`、`backup:write`。Token **不能**调用 `update/*`；管理 Token 本身只能用管理员登录会话。明文只在创建时返回一次，库内仅存哈希。

- **公开**：`GET /api/health`、`GET /api/config`、`POST /api/auth/{login,register,forgot,reset}`、`GET /api/auth/verify`、`GET /sub/{token}`（聚合订阅，UA 自适应或 `?format=clash|singbox|surge`）、`GET /install-singbox.sh`、`GET /api/monitor/{public,heatmap,install.sh,agent/{arch}}`
- **用户**：`/api/user/{dashboard,plans,subscription,reset-sub,reset-node-creds,packages,purchase,orders,points,announcements,sessions,nodes,proxies,stats/traffic,password,email}`、`GET /api/auth/me`
- **管理**：
  - 业务 `/api/admin/{users,user-groups,packages,orders,reg-codes,announcements,help,settings,tokens}`（含 `orders/{id}/refund-preview`、`packages/{id}/retire`；`tokens` 为 API Token 创建/列表/吊销）
  - 节点 `/api/admin/{nodes,node-groups,node-sources,servers}`（含 `nodes/singbox` 版本探测与 `nodes/{id}/singbox/upgrade` 一键重装）
  - 原生 sing-box `/api/admin/sb/*`（TLS、入站、代理出口 `sb/egresses`、`sb/preview`、`sb/port-check`、`sb/sni-test`、`sb/import-remote/*`）
  - 证书中心 `/api/admin/certs/*`
  - 统计 `/api/admin/stats/{overview,traffic,top,distribution,packages,users,usage,usage/users,usage/packages}`
  - 监控 `/api/admin/monitor/{dashboard,servers,heatmap,alerts}`
  - 运维 `/api/admin/{backup,rebuild,update/*}`（`update` 含 `check`/`apply`/`releases`/`rollback`/`status`）

---

## 📖 文档

- **[部署与配置手册](docs/部署与配置手册.md)** —— 从零把面板 + sing-box 跑起来、多落地、协议选型、运维排错。
- **[订阅使用指南](docs/订阅使用指南.md)** —— 面向普通用户：各客户端导入、智能优选、分流、常见问题。
- **[计费与退款业务说明](docs/计费与退款业务说明.md)** —— 面向运营者：桶模型、重复购买排队生效、多套餐并存、按剩余比例退款与后台操作。

---

## 🤝 贡献

欢迎 Issue 与 PR。提交前请确保：

```bash
go build ./...   # 编译通过
go vet ./...     # 静态检查通过
```

数据库为自动建表 + 幂等迁移，改动 schema 请在 `internal/store/migrate.go` 中以幂等方式追加。

### 发布签名（只有发版的人需要看）

面板的「在线更新」会用编进二进制的公钥校验新版本的签名。GitHub 给出的 sha256
只能证明下载没损坏，不能证明是本项目发的——有了签名，即使发布渠道被攻破也推不动
恶意版本。

**普通使用者和贡献者不用管这一节**：`go build` 出来的二进制没有公钥，就不校验签名，
和以前完全一样。

自己 fork 后要发版，一次性配置：

```bash
go run ./tools/sign -genkey     # 在自己机器上生成，私钥不要进 CI
```

把私钥存为仓库 Actions secret `RELEASE_SIGNING_KEY`，公钥存为 `RELEASE_PUBLIC_KEY`。
`.github/workflows/release.yml` 会自动把公钥编进二进制、给产物签名并上传 `.sig`。
两个 secret 都不配时 workflow 照常发布，只是产物不带签名。

---

## 📄 许可证

本项目采用 **[CC BY-NC-SA 4.0](LICENSE)**（署名-非商业性使用-相同方式共享）授权：

- ✅ 可自由复制、修改、再分发、个人自用
- 🚫 **禁止商业 / 盈利用途**
- 🔁 任何包含本代码的衍生作品**必须同样以 CC BY-NC-SA 4.0 公开**
- 🖊️ 必须**保留作者署名**（萌狼蓝天 / xrilang）与本许可声明
- ⚖️ 本项目不授予任何专利权；按「现状」提供，作者不承担任何担保与法律责任

> 因禁止商业使用，按 OSI 定义本项目属「源码公开（Source-Available）」，商业授权请联系作者。

版权所有 © 2026 萌狼蓝天 (xrilang)

---

## 🔗 友情链接

- [Linux.do](https://linux.do/) —— 新的理想型社区
