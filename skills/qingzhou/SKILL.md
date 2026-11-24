---
name: qingzhou-ops
description: 轻舟面板（QingZhou）AI 一键部署与内置远程 MCP 智能运维指南。适用于帮助零基础用户从裸机一键安装轻舟面板与 sing-box、自动签发并交付管理员 MCP 密钥与配置，以及通过面板内置 Remote MCP（/mcp）完成节点配置、监控排障、Telegram 绑定、套餐与用户管理等全链路智能运维。
---

# 轻舟面板 · AI 一键部署与智能运维 Skill

本 Skill 面向**不熟悉复杂面板表单与网络协议配置的新手用户**。你（AI Agent）需要替用户做合理的默认选型、一步步确认必要信息，避免让用户面对晦涩的技术参数。

---

## 阶段一：一键部署（从裸机到面板就绪）

当用户希望在一台新 Linux 服务器上部署轻舟时，按以下顺序执行：

### 1. 环境预检与方案确认
- 确认目标服务器为 Linux（`amd64` / `arm64`）、具备 `root` / `sudo` 与 `systemd`。
- 用通俗语言向用户确认两个问题（给出推荐默认值，用户不挑就直接用推荐项）：
  1. **是否有域名**？（推荐：有域名则配 Caddy/Nginx HTTPS 反代；没有域名则先用 `http://<公网IP>:8081` 跑通，后续随时补域名）
  2. **是否在本机同时跑代理节点**？（推荐单机新手：是，本机即面板 + 落地节点，**不需要**在后台「服务器管理」里新建本机服务器记录）

### 2. 安装轻舟面板（自动启用并准备 MCP）
执行官方一键脚本（非交互模式可通过环境变量传参，避免卡在交互提示）：

```bash
# 示例（无反代直连公网）：
QZ_LISTEN=0.0.0.0:8081 \
QZ_PUBLIC_BASE="http://<服务器公网IP>:8081" \
QZ_ADMIN_USER="admin" \
bash <(curl -fsSL https://raw.githubusercontent.com/mllt992/qing-zhou/main/install.sh) --init-mcp

# 示例（前置 Caddy/Nginx HTTPS 反代）：
QZ_LISTEN=127.0.0.1:8081 \
QZ_PUBLIC_BASE="https://<你的面板域名>" \
QZ_ADMIN_USER="admin" \
bash <(curl -fsSL https://raw.githubusercontent.com/mllt992/qing-zhou/main/install.sh) --init-mcp
```

> 若服务器网络访问 GitHub 较慢，可附加 `--proxy https://mirror.ghproxy.com/`。

### 3. 安装面板专用版 sing-box（含 `with_v2ray_api`）
⚠️ **绝对不要安装官方上游 sing-box！** 官方构建不含 `with_v2ray_api`，会导致所有用户流量统计恒为 0、配额永不生效。

在面板本机（以及每台远程落地机）执行由面板托管的安装脚本：
```bash
curl -fsSL http://127.0.0.1:8081/install-singbox.sh | bash
# 立即核验 with_v2ray_api：
/usr/local/bin/sing-box version | grep with_v2ray_api
```

### 4. 自动签发管理员 MCP 密钥并向用户交付（⚠️ 必须执行）
如果安装时未加 `--init-mcp` 或需要重新签发，在面板所在机执行：

```bash
/opt/qingzhou/qingzhou token create \
  --name "ai-ops" \
  --preset mcp-admin \
  --panel-url "<面板对外完整地址，如 https://panel.example.com 或 http://1.2.3.4:8081>" \
  --json
```

**⚠️ 硬性交付要求**：拿到命令输出后，你**必须明确告知用户并提醒其妥善保存**以下信息（明文密钥仅显示一次，便于用户在其他电脑、手机或 AI 客户端中复用）：
1. **面板访问地址** + **管理员账号与初始密码**（提醒登录后修改密码）
2. **Remote MCP 端点地址**：`mcp_url`（形如 `https://<面板域名>/mcp`）
3. **管理员 MCP 密钥（Bearer Token）**：`token`（`qz_at_...`）
4. **可直接复制粘贴的标准 `mcpServers` JSON 配置块**（来自输出中的 `mcp_config`）：
   ```json
   {
     "mcpServers": {
       "qingzhou": {
         "type": "http",
         "url": "https://<面板域名>/mcp",
         "headers": {
           "Authorization": "Bearer qz_at_..."
         }
       }
     }
   }
   ```
5. 如果当前 AI 客户端允许你写入本地 MCP 配置，先征得用户同意后自动写入，随即切换为通过 MCP 工具完成后续初始化与运维。

> 提醒用户：密钥可随时在面板后台 **「API / MCP Token」** 页面一键吊销或重新创建，也可以随时查看每一条 AI 操作的审计日志。

---

## 阶段二：通过 MCP 完成新手开箱初始化（零踩坑）

接入轻舟内置 Remote MCP（`/mcp`）后，按以下步骤把第一个可用节点和订阅跑通：

### 1. 先做全盘体检
- 调用 `diagnose_panel`，确认 sing-box 版本、是否含 `with_v2ray_api`、同步状态与待处理项。
- 若本机或远端缺少 `with_v2ray_api`，直接调用 `upgrade_server_singbox` 一键重装面板构建。

### 2. 一键建好第一个可用节点（强烈推荐 `quick_setup_node`）
直接调用 `quick_setup_node`，它会自动处理新手最常踩的全部坑：
- **自动补齐本机 `node_host_override`**（防止「套餐已生效但订阅显示 0 个节点」）；
- 自动生成 TLS / Reality 密钥对；
- 自动避开端口冲突创建入站；
- 自动在「节点管理」创建自建节点卡片并加入默认分组；
- 自动跑 `sing-box check` 并下发配置。

#### 协议选型决策树（替小白做推荐）
| 用户诉求 | 推荐参数 |
|---|---|
| 通用主力 / 无域名 / 抗封锁（首选） | `protocol="vless"`, `tls_mode="reality"`, `sni="www.microsoft.com"` |
| 弱网 / 晚高峰 / 高丢包补充线路 | `protocol="hysteria2"`, `tls_mode="self_signed"` 或 `protocol="tuic"` |
| 给 Docker / 1Panel / git / 终端用的代理 | `protocol="mixed"`, `tls_mode="self_signed"`（或 `none`） |
| 已有域名 + Cloudflare DNS | 先 `issue_acme_cert` 签真实证书，再 `tls_mode="cert"` |

> ⚠️ **Cloudflare 橙云避坑**：如果面板域名开启了 Cloudflare 橙云代理，本机的 `node_host_override` **必须填写服务器源站公网 IP**（可通过 `detect_node_host` 自动获取），绝不能填被代理的橙云域名。
> ⚠️ **本机切勿建服务器记录**：面板本机固定为 `server_id = 0`（「面板本机」），只有新增**其他远程落地机**时才调用 `upsert_server`。

### 3. 创建套餐并验证订阅
1. 调用 `upsert_package` 创建一个订阅套餐（例如 100 GB / 30 天，绑定刚才节点所在的 `group_ids`）。
2. 调用 `provision_user_access` 为用户（或管理员自己）开通套餐，直接拿到 `subscription_url` 并核实 `visible_node_count > 0`。
3. 提醒用户放行云服务器安全组 / 系统防火墙上对应入站端口（TCP 对应 VLESS/Trojan/VMess/Mixed，UDP 对应 Hysteria2/TUIC）。

---

## 阶段三：日常智能运维速查

| 用户需求 | 推荐调用的 MCP 工具 |
|---|---|
| 为什么没节点 / 流量一直是 0 / 连不上？ | 先调 `diagnose_panel`，必要时调 `check_singbox_config`、`detect_node_host`、`resync_singbox` |
| 查看服务器 CPU/内存/磁盘/在线情况与告警 | `get_monitor_status`、`list_alerts`、`qingzhou://health` |
| 查看站点概览、用户与套餐流量统计 | `get_overview_stats`、`get_traffic_report` |
| 添加/测试/维护远程落地服务器 | `upsert_server` → `test_server_ssh` →（若缺插件）`upgrade_server_singbox` → `quick_setup_node(server_id=...)` |
| 配置中转链路（A 线路机 → B 落地机） | `upsert_inbound(upstream_inbound_id=...)` 或 `upsert_node(route_upstream_inbound_id=...)`；查看全局拓扑用 `qingzhou://topology` |
| 配置第三方静态 IP 出口（SOCKS5/HTTP） | `manage_egress(action="upsert")` → `manage_egress(action="test")` → `upsert_inbound(egress_id=...)` |
| 绑定并测试 Telegram 机器人与运维告警 | `setup_telegram` |
| 修改站点名、注册模式、免费分组、对外地址等 | `get_settings` → `update_settings` |
| 建用户、发套餐、调流量、生成邀请码、发公告 | `provision_user_access`、`upsert_package`、`assign_user_plan`、`adjust_user_traffic`、`manage_reg_codes`、`manage_announcements` |

---

## 安全与风控守则（AI 必须遵守）

1. **破坏性操作必须先征得用户明确同意**：
   - 调用 `delete_server`、`delete_inbound`、`delete_tls`、`delete_node`、`delete_node_group`、`retire_package`、`reset_user_node_creds`、`delete_user`、`manage_egress(delete)`、`manage_node_sources(delete)` 时，**先以 `confirm=false` 预演**，将返回的影响范围（受影响节点、中转降级、退款人数等）清晰转述给用户，**获得用户明确回复确认后**再传 `confirm=true` 执行。
2. **禁止泄露敏感凭据**：
   - 严禁将 MCP Bearer Token、管理员密码、SSH 私钥、Telegram Bot Token、Cloudflare Token 贴到公开群聊、Issue 或日志中。
3. **遇到 MCP 无法操作的高危控制面**：
   - 在线自更新/回滚（`/api/admin/update/*`）、OAuth2 密钥配置、签发/吊销其他 Token 故意未向 MCP 开放，需引导用户通过面板网页或服务器本机 CLI 完成。
