package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"qingzhou/internal/acmesh"
	"qingzhou/internal/certcheck"
	"qingzhou/internal/intervalcfg"
	"qingzhou/internal/sbctl"
	"qingzhou/internal/sbver"
	"qingzhou/internal/singbox"
	"qingzhou/internal/store"
	"qingzhou/internal/subconv"
)

// ---- #138 Input Structs ----

type mcpQuickSetupNodeIn struct {
	ServerID        int64  `json:"server_id,omitempty" jsonschema:"目标机器 ID，0 或留空表示面板本机"`
	Protocol        string `json:"protocol,omitempty" jsonschema:"协议类型：vless（默认，推荐 Reality）| hysteria2 | tuic | trojan | vmess | shadowsocks | anytls | mixed"`
	Name            string `json:"name,omitempty" jsonschema:"节点展示名称（留空自动生成，如 本机-VLESS-Reality）"`
	Tag             string `json:"tag,omitempty" jsonschema:"入站 Tag（不能含空格，留空自动生成）"`
	Port            int    `json:"port,omitempty" jsonschema:"监听端口 1-65535（留空自动挑选未占用端口，如 443 / 8443 / 2053）"`
	TLSMode         string `json:"tls_mode,omitempty" jsonschema:"TLS 模式：reality（vless 默认）| self_signed（hy2/tuic/trojan 默认）| cert（引用 cert_id）| none"`
	CertID          int64  `json:"cert_id,omitempty" jsonschema:"当 tls_mode 为 cert 时引用的托管证书 ID"`
	SNI             string `json:"sni,omitempty" jsonschema:"TLS/Reality 借用或证书 SNI 域名（reality 默认 www.microsoft.com）"`
	NodeHost        string `json:"node_host,omitempty" jsonschema:"可选：当 server_id 为 0 且未设置 node_host_override 时指定本机对外地址（留空自动探测公网 IP）"`
	GroupID         int64  `json:"group_id,omitempty" jsonschema:"要加入的节点分组 ID（留空时自动使用第一个分组，或自动新建『默认分组』）"`
	GroupName       string `json:"group_name,omitempty" jsonschema:"当 group_id 未传且需新建分组时的名称，默认『默认分组』"`
	BindPackageID   int64  `json:"bind_package_id,omitempty" jsonschema:"可选：顺便将该节点分组绑定到指定套餐 ID"`
	SetAsFreeGroup  bool   `json:"set_as_free_group,omitempty" jsonschema:"可选：是否将该节点分组设为免费分组（所有用户可见）"`
	UpstreamInbound int64  `json:"upstream_inbound_id,omitempty" jsonschema:"可选：中转落地入站 ID"`
	EgressID        int64  `json:"egress_id,omitempty" jsonschema:"可选：第三方代理出口 ID"`
}

type mcpSetupTelegramIn struct {
	BotToken         string `json:"bot_token,omitempty" jsonschema:"从 @BotFather 获取的 Bot Token（留空表示保留当前已保存 Token 并仅做测试/更新其他项）"`
	NotifyExpiryDays int    `json:"notify_expiry_days,omitempty" jsonschema:"套餐到期提前提醒天数（1-30）"`
	NotifyTrafficPct int    `json:"notify_traffic_pct,omitempty" jsonschema:"流量不足提醒剩余百分比阈值（1-50）"`
	ExtraOpsChats    string `json:"extra_ops_chats,omitempty" jsonschema:"可选：运维熔断告警额外推送的 Telegram chat_id（逗号分隔）"`
	TestConnection   *bool  `json:"test_connection,omitempty" jsonschema:"是否调用 getMe 测试连通性，默认 true"`
}

type mcpUpdateSettingsIn struct {
	Settings map[string]string `json:"settings" jsonschema:"要更新的键值对，例如 {\"site_name\":\"轻舟\",\"node_host_override\":\"1.2.3.4\",\"public_base\":\"https://panel.example.com\"}。不可修改系统禁区项（update_repo/jwt_secret/oauth2_config 等）"`
}

type mcpUpsertServerIn struct {
	ID            int64  `json:"id,omitempty" jsonschema:"服务器 ID；0 或留空为新增，非 0 为更新"`
	Name          string `json:"name" jsonschema:"服务器名称（必填）"`
	Host          string `json:"host" jsonschema:"SSH/客户端连接地址（IP 或域名，必填）"`
	HostV6        string `json:"host_v6,omitempty" jsonschema:"可选 IPv6 订阅地址"`
	Domain        string `json:"domain,omitempty" jsonschema:"可选客户端连接域名（覆盖 host）"`
	DomainV6      string `json:"domain_v6,omitempty" jsonschema:"可选客户端 IPv6 域名"`
	Port          int    `json:"port,omitempty" jsonschema:"SSH 端口，默认 22"`
	SSHUser       string `json:"ssh_user,omitempty" jsonschema:"SSH 用户名，默认 root"`
	SSHPassword   string `json:"ssh_password,omitempty" jsonschema:"SSH 密码（更新时留空或传 *** 保留原值）"`
	SSHKey        string `json:"ssh_key,omitempty" jsonschema:"SSH 私钥 PEM（更新时留空或传 *** 保留原值）"`
	SSHKeyPath    string `json:"ssh_key_path,omitempty" jsonschema:"面板 ssh-keys 目录中的私钥文件名（推荐）"`
	SSHKeyPass    string `json:"ssh_key_pass,omitempty" jsonschema:"SSH 私钥口令"`
	UseSudo       *bool  `json:"use_sudo,omitempty" jsonschema:"普通用户是否启用 sudo 提权"`
	SudoPassword  string `json:"sudo_password,omitempty" jsonschema:"sudo 密码（免密 sudo 留空）"`
	ConfigPath    string `json:"config_path,omitempty" jsonschema:"sing-box 配置路径，默认 /etc/sing-box/config.json"`
	SystemdUnit   string `json:"systemd_unit,omitempty" jsonschema:"systemd 服务名，默认 sing-box"`
	SingBoxBin    string `json:"sing_box_bin,omitempty" jsonschema:"sing-box 可执行路径，默认 /usr/local/bin/sing-box"`
	V2RayListen   string `json:"v2ray_listen,omitempty" jsonschema:"v2ray_api 监听地址，默认 127.0.0.1:18080"`
	Enabled       *bool  `json:"enabled,omitempty" jsonschema:"是否启用（新增默认 true）"`
	Location      string `json:"location,omitempty" jsonschema:"地区备注，如 日本东京"`
	Provider      string `json:"provider,omitempty" jsonschema:"服务商备注"`
}

type mcpConfirmIDIn struct {
	ID      int64 `json:"id" jsonschema:"目标记录 ID"`
	Confirm bool  `json:"confirm,omitempty" jsonschema:"破坏性操作确认开关：false/省略仅返回影响范围预览，true 才真正执行"`
}

type mcpResyncIn struct {
	ServerID *int64 `json:"server_id,omitempty" jsonschema:"可选：指定只重推某台服务器（0 表示面板本机）；不传则全量重推"`
}

type mcpCreateRealityTLSIn struct {
	Name            string   `json:"name" jsonschema:"TLS 配置名称（必填）"`
	ServerID        int64    `json:"server_id,omitempty" jsonschema:"所属机器 ID，0 为本机"`
	ServerName      string   `json:"server_name" jsonschema:"借用 SNI 域名，如 www.microsoft.com（必填）"`
	HandshakeServer string   `json:"handshake_server,omitempty" jsonschema:"握手目标域名，默认同 server_name"`
	HandshakePort   int      `json:"handshake_port,omitempty" jsonschema:"握手目标端口，默认 443"`
	Fingerprint     string   `json:"fingerprint,omitempty" jsonschema:"uTLS 指纹，默认 chrome"`
	ShortIDs        []string `json:"short_ids,omitempty" jsonschema:"可选自定义 short_id 列表，留空自动生成"`
}

type mcpCreateSelfSignedTLSIn struct {
	Name       string   `json:"name,omitempty" jsonschema:"TLS 配置名称"`
	ServerID   int64    `json:"server_id,omitempty" jsonschema:"所属机器 ID，0 为本机"`
	ServerName string   `json:"server_name,omitempty" jsonschema:"可选 SNI/域名覆盖，留空自动使用机器地址"`
	SANs       []string `json:"sans,omitempty" jsonschema:"可选附加 SAN（域名或 IP）"`
}

type mcpIssueAcmeCertIn struct {
	Name    string `json:"name" jsonschema:"证书名称（必填）"`
	Domain  string `json:"domain" jsonschema:"申请域名，支持泛域名 *.example.com（必填）"`
	Method  string `json:"method,omitempty" jsonschema:"验证方式：dns-cf（默认，需系统设置已填 Cloudflare Token）| http-01 | webroot"`
	Webroot string `json:"webroot,omitempty" jsonschema:"当 method 为 webroot 时的站点目录"`
	Profile string `json:"acme_profile,omitempty" jsonschema:"兼容策略：auto（默认）| compatible | modern"`
}

type mcpUpsertInboundIn struct {
	ID                int64          `json:"id,omitempty" jsonschema:"入站 ID；0 为新建，非 0 为修改"`
	ServerID          int64          `json:"server_id,omitempty" jsonschema:"所属机器 ID，0 为本机"`
	Type              string         `json:"type" jsonschema:"协议：vless | hysteria2 | tuic | trojan | vmess | shadowsocks | anytls | hysteria | mixed"`
	Tag               string         `json:"tag" jsonschema:"唯一入站标识（不能含空格）"`
	Listen            string         `json:"listen,omitempty" jsonschema:"监听地址，默认 ::"`
	ListenPort        int            `json:"listen_port" jsonschema:"监听端口 1-65535"`
	TlsID             int64          `json:"tls_id,omitempty" jsonschema:"绑定的 TLS 配置 ID"`
	Enabled           *bool          `json:"enabled,omitempty" jsonschema:"是否启用（新建默认 true）"`
	UpstreamInboundID int64          `json:"upstream_inbound_id,omitempty" jsonschema:"中转落地入站 ID（与 egress_id 互斥）"`
	EgressID          int64          `json:"egress_id,omitempty" jsonschema:"第三方代理出口 ID（与 upstream_inbound_id 互斥）"`
	Options           map[string]any `json:"options,omitempty" jsonschema:"协议附加选项 JSON 对象（如 flow/transport/method/obfs 等）"`
}

type mcpManageEgressIn struct {
	Action           string `json:"action" jsonschema:"操作：upsert | test | delete"`
	ID               int64  `json:"id,omitempty" jsonschema:"代理出口 ID（更新/测试/删除时使用）"`
	Name             string `json:"name,omitempty" jsonschema:"出口名称"`
	Type             string `json:"type,omitempty" jsonschema:"socks | http"`
	Host             string `json:"host,omitempty" jsonschema:"代理地址"`
	Port             int    `json:"port,omitempty" jsonschema:"代理端口 1-65535"`
	Username         string `json:"username,omitempty" jsonschema:"认证用户名"`
	Password         string `json:"password,omitempty" jsonschema:"认证密码（更新留空或 *** 保留原值）"`
	TLSEnabled       bool   `json:"tls_enabled,omitempty" jsonschema:"仅 http 类型可用：是否启用 HTTPS 代理"`
	SNI              string `json:"sni,omitempty" jsonschema:"TLS SNI"`
	TLSCertID        int64  `json:"tls_cert_id,omitempty" jsonschema:"信任锚证书 ID"`
	TLSInsecure      bool   `json:"tls_insecure,omitempty" jsonschema:"是否跳过证书校验（仅排障）"`
	UDPMode          string `json:"udp_mode,omitempty" jsonschema:"passthrough | block"`
	TestViaServerID  *int64 `json:"test_via_server_id,omitempty" jsonschema:"测试模式从哪台机器发起测试（0 表示本机）"`
	Confirm          bool   `json:"confirm,omitempty" jsonschema:"删除操作时的确认开关"`
}

type mcpUpsertNodeIn struct {
	ID                     int64   `json:"id,omitempty" jsonschema:"节点 ID；0 为新建，非 0 为修改"`
	Type                   string  `json:"type,omitempty" jsonschema:"节点类型：self_built（默认）| external"`
	Name                   string  `json:"name" jsonschema:"节点名称（必填）"`
	Remark                 string  `json:"remark,omitempty" jsonschema:"订阅展示备注（可选，优先于 name）"`
	InboundTag             string  `json:"inbound_tag,omitempty" jsonschema:"self_built 绑定的入站 tag"`
	RouteUpstreamInboundID int64   `json:"route_upstream_inbound_id,omitempty" jsonschema:"可选：共享入口固定落地入站 ID"`
	ShareLink              string  `json:"share_link,omitempty" jsonschema:"external 节点的原始分享链接"`
	Enabled                *bool   `json:"enabled,omitempty" jsonschema:"是否启用（新建默认 true）"`
	OutputV4               *bool   `json:"output_v4,omitempty" jsonschema:"是否输出 IPv4/主订阅项（默认 true）"`
	OutputV6               *bool   `json:"output_v6,omitempty" jsonschema:"是否输出 -v6 订阅项（默认 true）"`
	GroupIDs               []int64 `json:"group_ids,omitempty" jsonschema:"所属节点分组 ID 列表"`
}

type mcpUpsertGroupIn struct {
	ID          int64  `json:"id,omitempty" jsonschema:"分组 ID；0 为新建，非 0 为修改"`
	Name        string `json:"name" jsonschema:"分组名称（必填）"`
	Description string `json:"description,omitempty" jsonschema:"分组说明"`
	IsAI        bool   `json:"is_ai,omitempty" jsonschema:"是否为 AI 专用路由分组"`
}

type mcpManageNodeSourceIn struct {
	Action   string  `json:"action" jsonschema:"操作：upsert | fetch | delete"`
	ID       int64   `json:"id,omitempty" jsonschema:"订阅源 ID"`
	Name     string  `json:"name,omitempty" jsonschema:"订阅源名称"`
	URL      string  `json:"url,omitempty" jsonschema:"机场订阅 URL"`
	Enabled  *bool   `json:"enabled,omitempty" jsonschema:"是否启用自动同步（默认 true）"`
	GroupIDs []int64 `json:"group_ids,omitempty" jsonschema:"抓取后归入的节点分组 ID"`
	Confirm  bool    `json:"confirm,omitempty" jsonschema:"删除操作时的确认开关"`
}

func (a *API) registerMCPInfraTools(srv *mcp.Server) {
	// 1. quick_setup_node (flagship workflow tool)
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "quick_setup_node",
		Description: "【推荐】一键端到端配置可用节点：自动处理本机 node_host_override 探测补齐、生成 TLS/Reality、挑选空闲端口建入站、创建自建节点并归入分组、可选绑定套餐或设为免费分组、校验配置并触发 sing-box 下发。彻底避免漏配『节点对外地址』或漏建节点卡片导致订阅为空。",
	}, "mcp:nodes", func(ctx context.Context, auth mcpAuthInfo, in mcpQuickSetupNodeIn) (any, error) {
		return a.mcpQuickSetupNode(ctx, auth, in)
	})

	// 2. setup_telegram
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "setup_telegram",
		Description: "配置并测试 Telegram 机器人（保存 Bot Token、调用 getMe 验证并缓存机器人用户名、可选设置到期/流量提醒阈值及运维告警 chat_id）。",
	}, "mcp:settings", func(ctx context.Context, _ mcpAuthInfo, in mcpSetupTelegramIn) (any, error) {
		return a.mcpSetupTelegram(ctx, in)
	})

	// 3. update_settings
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "update_settings",
		Description: "更新轻舟系统设置（如 public_base、node_host_override、site_name、register_mode、free_group_id、default_traffic、default_expiry_days 等）。禁止修改系统禁区项（update_repo/jwt_secret/oauth2_config）。",
	}, "mcp:settings", func(_ context.Context, _ mcpAuthInfo, in mcpUpdateSettingsIn) (any, error) {
		return a.mcpUpdateSettings(in.Settings)
	})

	// 4. upsert_server
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "upsert_server",
		Description: "新增或更新远程落地服务器（注意：面板本机 server_id=0 不需要创建服务器记录）。返回结果中所有 SSH 凭据均已脱敏。",
	}, "mcp:servers", func(_ context.Context, _ mcpAuthInfo, in mcpUpsertServerIn) (any, error) {
		return a.mcpUpsertServer(in)
	})

	// 5. delete_server
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "delete_server",
		Description: "删除远程服务器（两段确认：confirm=false 仅返回影响预览，confirm=true 才真正执行）。",
	}, "mcp:servers", func(_ context.Context, _ mcpAuthInfo, in mcpConfirmIDIn) (any, error) {
		sv, err := a.st.GetServer(in.ID)
		if err != nil || sv == nil {
			return nil, errors.New("服务器不存在")
		}
		ibs, _ := a.st.ListSbInboundsByServer(in.ID)
		if !in.Confirm {
			return J{
				"dry_run":        true,
				"server_id":      sv.ID,
				"server_name":    sv.Name,
				"host":           sv.Host,
				"inbound_count":  len(ibs),
				"message":        fmt.Sprintf("即将删除服务器「%s」(%s)，当前其下有 %d 个入站。请确认后传 confirm=true 执行。", sv.Name, sv.Host, len(ibs)),
			}, nil
		}
		if err := a.st.DeleteServer(in.ID); err != nil {
			return nil, err
		}
		a.sbRebuildLog()
		return J{"deleted": true, "server_id": in.ID, "server_name": sv.Name}, nil
	})

	// 6. test_server_ssh
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "test_server_ssh",
		Description: "测试指定远程服务器的 SSH 连通性、sudo 提权和 sing-box 版本（自动记录版本与可执行路径）。",
	}, "mcp:servers", func(ctx context.Context, _ mcpAuthInfo, in mcpServerIDIn) (any, error) {
		if in.ServerID <= 0 {
			return nil, errors.New("server_id 必须为正整数（本机无需 SSH 测试，请用 check_singbox_config）")
		}
		sv, err := a.st.GetServer(in.ServerID)
		if err != nil || sv == nil {
			return nil, errors.New("服务器不存在")
		}
		if !serverHasCredentials(sv) {
			return nil, errors.New("该服务器未配置 SSH 密钥或密码")
		}
		rm := a.newRemoteManager(10 * time.Second)
		cfg := sbctl.SSHConfigFor(sv)
		tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		ver, err := rm.TestConnection(tctx, cfg)
		if err != nil {
			_ = a.st.UpdateServerStatus(in.ServerID, "error")
			return nil, fmt.Errorf("SSH 连接失败: %w", err)
		}
		_ = a.st.UpdateServerStatus(in.ServerID, "online")
		info := sbver.Parse(ver)
		_ = a.st.SetNodeSingbox(sv.ID, info)
		if bin, err := rm.ResolveSingBoxBin(tctx, cfg); err == nil && bin != "" && bin != sv.SingBoxBin {
			_ = a.st.SetServerSingBoxBin(sv.ID, bin)
		}
		return J{
			"status":        "online",
			"server_id":     sv.ID,
			"server_name":   sv.Name,
			"version":       info.Version,
			"has_v2ray_api": info.HasV2RayAPI,
			"too_old":       info.TooOld(),
			"raw":           ver,
		}, nil
	})

	// 7. upgrade_server_singbox
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "upgrade_server_singbox",
		Description: "异步触发指定节点（server_id=0 为本机）重装/升级为轻舟发布的 sing-box 构建（含 with_v2ray_api）。",
	}, "mcp:servers", func(_ context.Context, _ mcpAuthInfo, in mcpServerIDIn) (any, error) {
		id := in.ServerID
		if id == store.LocalNodeID {
			if !runtimeIsLinux() {
				return nil, errLinuxOnly
			}
		} else if err := a.checkRemoteUpgradable(id); err != nil {
			return nil, err
		}
		if !a.claimUpgrade(id) {
			return nil, errors.New("该节点正在安装/升级中，请稍后查询 list_servers 查看进度")
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			var out string
			var err error
			if id == store.LocalNodeID {
				out, err = upgradeLocalSingBox(ctx)
			} else {
				out, err = a.upgradeRemoteSingBox(ctx, id)
			}
			a.finishUpgrade(id, out, err)
			if err == nil && a.sbctl != nil {
				a.sbctl.RefreshVersions()
			}
		}()
		return J{"started": true, "server_id": id, "hint": "升级任务已在后台启动，请稍后调用 list_servers 或 diagnose_panel 查看完成状态。"}, nil
	})

	// 8. resync_singbox
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "resync_singbox",
		Description: "手动触发单台机器或全部机器的 sing-box 配置重新生成并下发（同时清空订阅缓存）。",
	}, "mcp:nodes", func(_ context.Context, _ mcpAuthInfo, in mcpResyncIn) (any, error) {
		a.invalidateAllLinks()
		if a.sbctl == nil {
			return nil, errors.New("sing-box 控制器未启用")
		}
		if in.ServerID == nil {
			a.sbctl.ScheduleRebuild()
			return J{"queued": "all"}, nil
		}
		if *in.ServerID < 0 {
			return nil, errors.New("无效的 server_id")
		}
		a.sbctl.ScheduleRebuildServer(*in.ServerID)
		return J{"queued_server_id": *in.ServerID}, nil
	})

	// 9. create_reality_tls
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "create_reality_tls",
		Description: "自动生成 x25519 密钥对与 short_id 并创建 Reality TLS 配置（私钥加密存库、不回显明文）。",
	}, "mcp:nodes", func(_ context.Context, _ mcpAuthInfo, in mcpCreateRealityTLSIn) (any, error) {
		return a.mcpCreateRealityTLS(in)
	})

	// 10. create_self_signed_tls
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "create_self_signed_tls",
		Description: "按目标机器地址（含 host/host_v6 或本机 node_host）自动签发多 SAN 自签证书并保存为 TLS 配置（适合 Hysteria2 / TUIC / 自建 HTTPS 代理）。",
	}, "mcp:nodes", func(_ context.Context, _ mcpAuthInfo, in mcpCreateSelfSignedTLSIn) (any, error) {
		return a.mcpCreateSelfSignedTLS(in)
	})

	// 11. issue_acme_cert
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "issue_acme_cert",
		Description: "通过面板证书中心使用 acme.sh 签发真实 Let's Encrypt 托管证书（默认 dns-cf，需先配置 Cloudflare API Token）。",
	}, "mcp:nodes", func(ctx context.Context, _ mcpAuthInfo, in mcpIssueAcmeCertIn) (any, error) {
		return a.mcpIssueAcmeCert(ctx, in)
	})

	// 12. delete_tls
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "delete_tls",
		Description: "删除未被任何入站引用的 TLS 配置（两段确认）。",
	}, "mcp:nodes", func(_ context.Context, _ mcpAuthInfo, in mcpConfirmIDIn) (any, error) {
		t, err := a.st.GetSbTls(in.ID)
		if err != nil || t == nil {
			return nil, errors.New("TLS 配置不存在")
		}
		if !in.Confirm {
			return J{"dry_run": true, "tls_id": t.ID, "name": t.Name, "mode": t.Mode, "message": "请确认无误后传 confirm=true 执行删除。"}, nil
		}
		if err := a.st.DeleteSbTls(in.ID); err != nil {
			return nil, err
		}
		a.sbRebuildLog()
		return J{"deleted": true, "tls_id": in.ID}, nil
	})

	// 13. upsert_inbound
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "upsert_inbound",
		Description: "创建或更新 sing-box 入站（自动做端口冲突检测、中转防环校验、出口互斥校验，并异步触发对应机器下发）。",
	}, "mcp:nodes", func(_ context.Context, _ mcpAuthInfo, in mcpUpsertInboundIn) (any, error) {
		return a.mcpUpsertInbound(in)
	})

	// 14. delete_inbound
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "delete_inbound",
		Description: "删除 sing-box 入站（两段确认：会预览受影响的自建节点与引用它的中转链路降级风险）。",
	}, "mcp:nodes", func(_ context.Context, _ mcpAuthInfo, in mcpConfirmIDIn) (any, error) {
		ib, err := a.st.GetSbInbound(in.ID)
		if err != nil || ib == nil {
			return nil, errors.New("入站不存在")
		}
		allIbs, _ := a.st.ListSbInbounds()
		allNodes, _ := a.st.ListNodes()
		relays := []string{}
		for _, other := range allIbs {
			if other.UpstreamInboundID == ib.ID {
				relays = append(relays, other.Tag)
			}
		}
		boundNodes := []string{}
		for _, n := range allNodes {
			if n.Type == "self_built" && (n.InboundTag == ib.Tag || n.RouteUpstreamInboundID == ib.ID) {
				boundNodes = append(boundNodes, fmt.Sprintf("#%d(%s)", n.ID, n.Name))
			}
		}
		if !in.Confirm {
			return J{
				"dry_run":            true,
				"inbound_id":         ib.ID,
				"tag":                ib.Tag,
				"affected_nodes":     boundNodes,
				"affected_relays":    relays,
				"message":            fmt.Sprintf("即将删除入站「%s」：关联节点 %d 个，引用作落地的中转 %d 个（删除后中转将降级直出）。请确认后传 confirm=true 执行。", ib.Tag, len(boundNodes), len(relays)),
			}, nil
		}
		relayServers, err := a.st.DeleteSbInbound(in.ID)
		if err != nil {
			return nil, err
		}
		ids := append([]int64{ib.ServerID}, a.landingServerIDs(ib.UpstreamInboundID)...)
		a.sbScheduleServer(append(ids, relayServers...)...)
		a.invalidateAllLinks()
		return J{"deleted": true, "inbound_id": in.ID, "tag": ib.Tag}, nil
	})

	// 15. manage_egress
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "manage_egress",
		Description: "管理第三方静态 IP 代理出口（SOCKS5/HTTP/HTTPS）：支持 action=upsert（新增/修改）、test（经节点实测出口 IP 与延迟）、delete（两段确认删除）。",
	}, "mcp:nodes", func(ctx context.Context, _ mcpAuthInfo, in mcpManageEgressIn) (any, error) {
		return a.mcpManageEgress(ctx, in)
	})

	// 16. upsert_node
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "upsert_node",
		Description: "创建或修改节点卡片（自建节点绑定 inbound_tag，或外部节点粘贴 share_link），支持设置分组、v4/v6 开关和固定落地路由。",
	}, "mcp:nodes", func(_ context.Context, _ mcpAuthInfo, in mcpUpsertNodeIn) (any, error) {
		return a.mcpUpsertNode(in)
	})

	// 17. delete_node
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "delete_node",
		Description: "删除节点卡片（两段确认）。",
	}, "mcp:nodes", func(_ context.Context, _ mcpAuthInfo, in mcpConfirmIDIn) (any, error) {
		n, err := a.st.GetNode(in.ID)
		if err != nil || n == nil {
			return nil, errors.New("节点不存在")
		}
		if !in.Confirm {
			return J{"dry_run": true, "node_id": n.ID, "name": n.Name, "type": n.Type, "message": fmt.Sprintf("即将删除节点「%s」，请确认后传 confirm=true 执行。", n.Name)}, nil
		}
		if err := a.st.DeleteNode(in.ID); err != nil {
			return nil, err
		}
		a.sbRebuildLog()
		return J{"deleted": true, "node_id": in.ID, "name": n.Name}, nil
	})

	// 18. upsert_node_group
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "upsert_node_group",
		Description: "创建或更新节点分组（分组用于将节点授权给套餐或设为免费分组）。",
	}, "mcp:nodes", func(_ context.Context, _ mcpAuthInfo, in mcpUpsertGroupIn) (any, error) {
		name := strings.TrimSpace(in.Name)
		if name == "" {
			return nil, errors.New("分组名称不能为空")
		}
		g := store.NodeGroup{ID: in.ID, Name: name, Description: strings.TrimSpace(in.Description), IsAI: in.IsAI}
		if in.ID == 0 {
			id, err := a.st.CreateGroup(g)
			if err != nil {
				return nil, fmt.Errorf("创建分组失败: %w", err)
			}
			g.ID = id
		} else {
			if err := a.st.UpdateGroup(g); err != nil {
				return nil, fmt.Errorf("更新分组失败: %w", err)
			}
		}
		a.invalidateAllLinks()
		return g, nil
	})

	// 19. delete_node_group
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "delete_node_group",
		Description: "删除节点分组（两段确认，会解除关联节点与套餐的分组绑定）。",
	}, "mcp:nodes", func(_ context.Context, _ mcpAuthInfo, in mcpConfirmIDIn) (any, error) {
		if !in.Confirm {
			return J{"dry_run": true, "group_id": in.ID, "message": fmt.Sprintf("即将删除节点分组 #%d（会同时移除该分组与节点/套餐的关联），请确认后传 confirm=true 执行。", in.ID)}, nil
		}
		if err := a.st.DeleteGroup(in.ID); err != nil {
			return nil, err
		}
		a.sbRebuildLog()
		return J{"deleted": true, "group_id": in.ID}, nil
	})

	// 20. manage_node_sources
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "manage_node_sources",
		Description: "管理外部机场订阅源：action=upsert（新增/编辑）、fetch（立即抓取并导入节点）、delete（两段确认删除）。",
	}, "mcp:nodes", func(ctx context.Context, _ mcpAuthInfo, in mcpManageNodeSourceIn) (any, error) {
		return a.mcpManageNodeSource(ctx, in)
	})
}

// ---- Implementations ----

func (a *API) mcpQuickSetupNode(ctx context.Context, auth mcpAuthInfo, in mcpQuickSetupNodeIn) (any, error) {
	proto := strings.ToLower(strings.TrimSpace(in.Protocol))
	if proto == "" {
		proto = "vless"
	}
	if !sbInboundTypes[proto] {
		return nil, fmt.Errorf("不支持的协议类型: %s", proto)
	}
	machineLabel := store.LocalNodeName
	if in.ServerID > 0 {
		sv, err := a.st.GetServer(in.ServerID)
		if err != nil || sv == nil {
			return nil, fmt.Errorf("服务器 #%d 不存在", in.ServerID)
		}
		if !sv.Enabled {
			return nil, fmt.Errorf("服务器「%s」已禁用，请先启用", sv.Name)
		}
		machineLabel = sv.Name
	}

	stepsDone := []string{}

	// Step A: Ensure local node_host_override is set if targeting local machine and nodeHost() is empty
	hostAutoSet := ""
	if in.ServerID == store.LocalNodeID && a.nodeHost() == "" {
		chosenHost := strings.TrimSpace(in.NodeHost)
		if chosenHost == "" {
			pctx, cancel := context.WithTimeout(ctx, nodeHostProbeTimeout)
			cands := recommendMeasured(dedupeNodeHostCandidates(detectEgressIPs(pctx)))
			cancel()
			if len(cands) > 0 {
				chosenHost = cands[0].Value
			}
		}
		if chosenHost != "" {
			if store.APITokenHasScope(auth.Scopes, "mcp:settings") {
				if err := a.st.SetSetting("node_host_override", chosenHost); err == nil {
					hostAutoSet = chosenHost
					stepsDone = append(stepsDone, "已自动设置本机节点对外地址 node_host_override="+chosenHost)
				}
			} else {
				stepsDone = append(stepsDone, "警告：本机未设置 node_host_override 且当前 Token 无 mcp:settings 权限，需手动配置 "+chosenHost)
			}
		} else {
			stepsDone = append(stepsDone, "警告：未能自动探测本机公网 IP，请稍后手动设置 node_host_override")
		}
	}

	// Step B: Create/select TLS profile
	tlsMode := strings.ToLower(strings.TrimSpace(in.TLSMode))
	if tlsMode == "" {
		switch proto {
		case "vless":
			tlsMode = "reality"
		case "hysteria2", "tuic", "hysteria", "trojan", "anytls":
			tlsMode = "self_signed"
		default:
			tlsMode = "none"
		}
	}
	var tlsID int64
	if proto != "shadowsocks" && tlsMode != "none" {
		switch tlsMode {
		case "reality":
			sni := strings.TrimSpace(in.SNI)
			if sni == "" {
				sni = "www.microsoft.com"
			}
			tlsName := fmt.Sprintf("Reality-%s-%d", machineLabel, time.Now().Unix()%10000)
			created, err := a.mcpCreateRealityTLS(mcpCreateRealityTLSIn{
				Name:       tlsName,
				ServerID:   in.ServerID,
				ServerName: sni,
			})
			if err != nil {
				return nil, fmt.Errorf("创建 Reality TLS 失败: %w", err)
			}
			tlsID = created["id"].(int64)
			stepsDone = append(stepsDone, fmt.Sprintf("已创建 Reality TLS #%d (SNI=%s)", tlsID, sni))
		case "self_signed":
			created, err := a.mcpCreateSelfSignedTLS(mcpCreateSelfSignedTLSIn{
				Name:       fmt.Sprintf("自签-%s-%d", proto, time.Now().Unix()%10000),
				ServerID:   in.ServerID,
				ServerName: strings.TrimSpace(in.SNI),
			})
			if err != nil {
				return nil, fmt.Errorf("创建自签 TLS 失败: %w", err)
			}
			tlsID = created["id"].(int64)
			stepsDone = append(stepsDone, fmt.Sprintf("已创建自签 TLS #%d", tlsID))
		case "cert":
			if in.CertID <= 0 {
				return nil, errors.New("tls_mode=cert 时必须提供 cert_id")
			}
			cert, err := a.st.GetCert(in.CertID)
			if err != nil || cert == nil {
				return nil, errors.New("指定的托管证书不存在")
			}
			sni := strings.TrimSpace(in.SNI)
			if sni == "" {
				sni = cert.Domain
			}
			server := map[string]any{"enabled": true, "server_name": sni}
			client := map[string]any{
				"insecure": managedCertClientInsecure(cert, false),
				"utls":     map[string]any{"enabled": true, "fingerprint": "chrome"},
			}
			sj, _ := json.Marshal(server)
			cj, _ := json.Marshal(client)
			id, err := a.st.SaveSbTls(&store.SbTls{
				ServerID:   in.ServerID,
				Name:       fmt.Sprintf("证书-%s-%d", cert.Name, time.Now().Unix()%10000),
				Mode:       "tls",
				CertID:     in.CertID,
				ServerJSON: string(sj),
				ClientJSON: string(cj),
			})
			if err != nil {
				return nil, fmt.Errorf("保存证书 TLS 失败: %w", err)
			}
			tlsID = id
			stepsDone = append(stepsDone, fmt.Sprintf("已绑定托管证书 #%d 到 TLS #%d", in.CertID, tlsID))
		default:
			return nil, fmt.Errorf("不支持的 tls_mode: %s", tlsMode)
		}
	}

	// Step C: Choose a non-conflicting port
	port := in.Port
	if port <= 0 {
		preferred := []int{443, 8443, 2053, 2083, 2087, 2096, 10443, 24433, 34433}
		if proto == "hysteria2" || proto == "tuic" {
			preferred = []int{8443, 443, 2083, 2096, 18443, 28443}
		} else if proto == "mixed" {
			preferred = []int{10808, 10809, 18088, 28088}
		}
		for _, p := range preferred {
			conflict, _, _ := a.st.SbInboundPortConflict(&store.SbInbound{
				ServerID: in.ServerID, Type: proto, Listen: "::", ListenPort: p,
			})
			if !conflict {
				port = p
				break
			}
		}
		if port == 0 {
			port = 20000 + int(time.Now().Unix()%20000)
		}
	}

	// Step D: Build unique tag & options
	tag := strings.TrimSpace(in.Tag)
	if tag == "" {
		tag = fmt.Sprintf("%s-%d-%d", proto, in.ServerID, port)
	}
	opts := map[string]any{}
	if proto == "vless" && tlsMode == "reality" {
		opts["flow"] = "xtls-rprx-vision"
	} else if proto == "tuic" {
		opts["congestion_control"] = "bbr"
	} else if proto == "shadowsocks" {
		opts["method"] = "2022-blake3-aes-128-gcm"
	}

	// Step E: Save inbound
	en := true
	ibRes, err := a.mcpUpsertInbound(mcpUpsertInboundIn{
		ServerID:          in.ServerID,
		Type:              proto,
		Tag:               tag,
		Listen:            "::",
		ListenPort:        port,
		TlsID:             tlsID,
		Enabled:           &en,
		UpstreamInboundID: in.UpstreamInbound,
		EgressID:          in.EgressID,
		Options:           opts,
	})
	if err != nil {
		return nil, fmt.Errorf("创建入站失败: %w", err)
	}
	ibObj := ibRes.(*store.SbInbound)
	stepsDone = append(stepsDone, fmt.Sprintf("已创建入站 #%d (%s / %s:%d)", ibObj.ID, ibObj.Tag, ibObj.Type, ibObj.ListenPort))

	// Step F: Resolve or create node group
	groupID := in.GroupID
	if groupID <= 0 {
		groups, _ := a.st.ListGroups()
		if len(groups) > 0 {
			groupID = groups[0].ID
			stepsDone = append(stepsDone, fmt.Sprintf("已复用现有节点分组 #%d「%s」", groupID, groups[0].Name))
		} else {
			gname := strings.TrimSpace(in.GroupName)
			if gname == "" {
				gname = "默认分组"
			}
			gid, err := a.st.CreateGroup(store.NodeGroup{Name: gname, Description: "AI 自动创建的默认节点分组"})
			if err != nil {
				return nil, fmt.Errorf("创建默认分组失败: %w", err)
			}
			groupID = gid
			stepsDone = append(stepsDone, fmt.Sprintf("已自动创建节点分组 #%d「%s」", groupID, gname))
		}
	}

	// Step G: Create self_built node (except mixed which is shown in proxies card; creating a node row still binds it to group!)
	nodeName := strings.TrimSpace(in.Name)
	if nodeName == "" {
		nodeName = fmt.Sprintf("%s-%s", machineLabel, strings.ToUpper(proto))
		if tlsMode == "reality" {
			nodeName += "-Reality"
		}
	}
	nodeObj, err := a.mcpUpsertNode(mcpUpsertNodeIn{
		Type:       "self_built",
		Name:       nodeName,
		InboundTag: ibObj.Tag,
		Enabled:    &en,
		GroupIDs:   []int64{groupID},
	})
	if err != nil {
		return nil, fmt.Errorf("创建自建节点卡片失败: %w", err)
	}
	n := nodeObj.(*store.Node)
	stepsDone = append(stepsDone, fmt.Sprintf("已创建自建节点 #%d「%s」并加入分组 #%d", n.ID, n.Name, groupID))

	// Step H: Optional package binding or free_group_id
	if in.BindPackageID > 0 {
		existing, _ := a.st.PlanGroupIDs(in.BindPackageID)
		found := false
		for _, g := range existing {
			if g == groupID {
				found = true
				break
			}
		}
		if !found {
			existing = append(existing, groupID)
			if err := a.st.SetPlanGroups(in.BindPackageID, existing); err == nil {
				stepsDone = append(stepsDone, fmt.Sprintf("已将分组 #%d 绑定到套餐 #%d", groupID, in.BindPackageID))
			}
		}
	}
	if in.SetAsFreeGroup && store.APITokenHasScope(auth.Scopes, "mcp:settings") {
		if err := a.st.SetSetting("free_group_id", fmt.Sprintf("%d", groupID)); err == nil {
			stepsDone = append(stepsDone, fmt.Sprintf("已将分组 #%d 设为免费节点分组", groupID))
		}
	}

	a.invalidateAllLinks()
	a.sbScheduleServer(in.ServerID)
	checkRes, _ := a.runSingboxConfigCheck(ctx, in.ServerID)

	return J{
		"ok":                  true,
		"steps":               stepsDone,
		"server_id":           in.ServerID,
		"effective_node_host": a.nodeHost(),
		"node_host_auto_set":  hostAutoSet,
		"tls_id":              tlsID,
		"inbound":             ibObj,
		"node":                n,
		"group_id":            groupID,
		"singbox_check":       checkRes,
	}, nil
}

func (a *API) mcpSetupTelegram(ctx context.Context, in mcpSetupTelegramIn) (any, error) {
	tok := strings.TrimSpace(in.BotToken)
	if tok != "" && tok != "***" {
		if err := a.st.SetSetting("telegram_bot_token", tok); err != nil {
			return nil, fmt.Errorf("保存 Bot Token 失败: %w", err)
		}
		_ = a.st.SetSetting("telegram_bot_username", "")
	}
	if in.NotifyExpiryDays >= 1 && in.NotifyExpiryDays <= 30 {
		_ = a.st.SetSetting("tg_notify_expiry_days", fmt.Sprintf("%d", in.NotifyExpiryDays))
	}
	if in.NotifyTrafficPct >= 1 && in.NotifyTrafficPct <= 50 {
		_ = a.st.SetSetting("tg_notify_traffic_pct", fmt.Sprintf("%d", in.NotifyTrafficPct))
	}
	if strings.TrimSpace(in.ExtraOpsChats) != "" {
		_ = a.st.SetSetting("alert_ops_extra_chats", strings.TrimSpace(in.ExtraOpsChats))
	}

	res := J{
		"configured": a.telegramConfigured(),
		"bot_name":   a.telegramUsername(),
	}
	doTest := true
	if in.TestConnection != nil {
		doTest = *in.TestConnection
	}
	if doTest {
		c := a.telegramClient()
		if c == nil {
			return nil, errors.New("未配置 Telegram Bot Token")
		}
		tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		username, err := a.refreshTelegramUsername(tctx, c)
		if err != nil {
			return nil, fmt.Errorf("Telegram 连通性测试失败（请检查 Token 或服务器访问 api.telegram.org 网络）: %w", err)
		}
		res["bot_name"] = username
		res["test_ok"] = true
		res["bind_hint"] = fmt.Sprintf("机器人 @%s 已连通！用户或管理员在面板「账户设置 → Telegram 绑定」点绑定后，即可用机器人查询订阅并接收运维告警。", username)
	}
	return res, nil
}

func (a *API) mcpUpdateSettings(in map[string]string) (any, error) {
	if len(in) == 0 {
		return nil, errors.New("settings 不能为空")
	}
	if raw, ok := in[telegramCustomCommandsSetting]; ok {
		norm, err := normalizeTelegramCustomCommands(raw)
		if err != nil {
			return nil, err
		}
		in[telegramCustomCommandsSetting] = norm
	}
	if err := validateBrandIconSetting(in); err != nil {
		return nil, err
	}
	if err := a.validateHelpDocsSettings(in); err != nil {
		return nil, err
	}
	runtimeChanged, err := a.validateRuntimeIntervals(in)
	if err != nil {
		return nil, err
	}

	updated := []string{}
	skipped := []string{}
	tokenChanged := false
	if submitted, ok := in["telegram_bot_token"]; ok && submitted != "***" {
		cur, _ := a.st.GetSetting("telegram_bot_token")
		tokenChanged = strings.TrimSpace(submitted) != strings.TrimSpace(cur)
	}
	needLinkFlush := false
	needRebuild := false
	for k, v := range in {
		if strings.HasPrefix(k, "_") || immutableSettings[k] {
			skipped = append(skipped, k+"(禁止修改)")
			continue
		}
		if _, locked := intervalcfg.EnvSettingValue(k); locked {
			skipped = append(skipped, k+"(环境变量锁定)")
			continue
		}
		if secretSettings[k] && (v == "***" || (v == "" && !clearableSecrets[k])) {
			skipped = append(skipped, k+"(保持原密钥)")
			continue
		}
		if k == "sub_clash_template" && v == subconv.DefaultClashTemplate {
			v = ""
		}
		if k == "sub_singbox_template" && v == subconv.DefaultSingboxTemplate {
			v = ""
		}
		v = normalizeTGTplSetting(k, v)
		if err := a.st.SetSetting(k, v); err != nil {
			return nil, fmt.Errorf("保存 %s 失败: %w", k, err)
		}
		updated = append(updated, k)
		if k == "node_host_override" || k == "free_group_id" || k == "public_base" {
			needLinkFlush = true
		}
		if k == store.SettingBlockPrivateEgress || k == "free_group_id" {
			needRebuild = true
		}
	}
	if tokenChanged {
		_ = a.st.SetSetting("telegram_bot_username", "")
	}
	if runtimeChanged {
		a.notifyRuntimeIntervalsChanged()
	}
	if needLinkFlush {
		a.invalidateAllLinks()
	}
	if needRebuild {
		a.sbRebuildLog()
	}
	safe, _ := a.buildSafeSettingsMap()
	return J{"updated": updated, "skipped": skipped, "effective": safe}, nil
}

func (a *API) mcpUpsertServer(in mcpUpsertServerIn) (any, error) {
	var sv *store.Server
	if in.ID > 0 {
		var err error
		sv, err = a.st.GetServer(in.ID)
		if err != nil || sv == nil {
			return nil, errors.New("服务器不存在")
		}
	} else {
		sv = &store.Server{
			Port:        22,
			SSHUser:     "root",
			ConfigPath:  "/etc/sing-box/config.json",
			SystemdUnit: "sing-box",
			SingBoxBin:  "/usr/local/bin/sing-box",
			V2rayListen: "127.0.0.1:18080",
			Enabled:     true,
			Status:      "unknown",
		}
	}
	oldHost := sv.Host
	oldHostKey := sv.HostKey
	if strings.TrimSpace(in.Name) != "" {
		sv.Name = strings.TrimSpace(in.Name)
	}
	if strings.TrimSpace(in.Host) != "" {
		sv.Host = strings.TrimSpace(in.Host)
	}
	if in.HostV6 != "" {
		sv.HostV6 = strings.Trim(strings.TrimSpace(in.HostV6), "[]")
	}
	if in.Domain != "" {
		sv.Domain = normalizeDomainField(in.Domain)
	}
	if in.DomainV6 != "" {
		sv.DomainV6 = normalizeDomainField(in.DomainV6)
	}
	if in.Port > 0 {
		sv.Port = in.Port
	}
	if strings.TrimSpace(in.SSHUser) != "" {
		sv.SSHUser = strings.TrimSpace(in.SSHUser)
	}
	if in.SSHPassword != "" && in.SSHPassword != "***" {
		sv.SSHPassword = in.SSHPassword
	}
	if in.SSHKey != "" && in.SSHKey != "***" {
		sv.SSHKey = in.SSHKey
	}
	if in.SSHKeyPath != "" {
		sv.SSHKeyPath = strings.TrimSpace(in.SSHKeyPath)
	}
	if in.SSHKeyPass != "" && in.SSHKeyPass != "***" {
		sv.SSHKeyPass = in.SSHKeyPass
	}
	if in.UseSudo != nil {
		sv.UseSudo = *in.UseSudo
	}
	if in.SudoPassword != "" && in.SudoPassword != "***" {
		sv.SudoPassword = in.SudoPassword
	}
	if strings.TrimSpace(in.ConfigPath) != "" {
		sv.ConfigPath = strings.TrimSpace(in.ConfigPath)
	}
	if strings.TrimSpace(in.SystemdUnit) != "" {
		sv.SystemdUnit = strings.TrimSpace(in.SystemdUnit)
	}
	if strings.TrimSpace(in.SingBoxBin) != "" {
		sv.SingBoxBin = strings.TrimSpace(in.SingBoxBin)
	}
	if strings.TrimSpace(in.V2RayListen) != "" {
		sv.V2rayListen = strings.TrimSpace(in.V2RayListen)
	}
	if in.Enabled != nil {
		sv.Enabled = *in.Enabled
	}
	if in.Location != "" {
		sv.Location = strings.TrimSpace(in.Location)
	}
	if in.Provider != "" {
		sv.Provider = strings.TrimSpace(in.Provider)
	}
	if sv.Name == "" || sv.Host == "" {
		return nil, errors.New("服务器名称和主机地址必填")
	}
	if err := a.checkServerKeyFile(sv); err != nil {
		return nil, err
	}
	if in.ID == 0 {
		id, err := a.st.CreateServer(*sv)
		if err != nil {
			return nil, fmt.Errorf("创建服务器失败: %w", err)
		}
		sv, _ = a.st.GetServer(id)
	} else {
		if err := a.st.UpdateServer(*sv); err != nil {
			return nil, fmt.Errorf("更新服务器失败: %w", err)
		}
		if oldHost != sv.Host && oldHostKey != "" {
			_ = a.st.SetServerHostKey(sv.ID, "")
		}
		a.sbRebuildLog()
		sv, _ = a.st.GetServer(sv.ID)
	}
	if sv != nil {
		maskServerSecrets(sv)
		sv.ProbeToken = ""
	}
	return sv, nil
}

func (a *API) mcpCreateRealityTLS(in mcpCreateRealityTLSIn) (J, error) {
	name := strings.TrimSpace(in.Name)
	sni := strings.TrimSpace(in.ServerName)
	if name == "" || sni == "" {
		return nil, errors.New("名称和 server_name (SNI) 必填")
	}
	hs := strings.TrimSpace(in.HandshakeServer)
	if hs == "" {
		hs = sni
	}
	hp := in.HandshakePort
	if hp <= 0 {
		hp = 443
	}
	priv, pub, err := singbox.GenerateRealityKeypair()
	if err != nil {
		return nil, fmt.Errorf("生成 Reality 密钥对失败: %w", err)
	}
	sids := []string{}
	for _, s := range in.ShortIDs {
		if s = strings.TrimSpace(s); s != "" {
			sids = append(sids, s)
		}
	}
	if len(sids) == 0 {
		sid, _ := singbox.GenerateShortID(8)
		sids = []string{sid}
	}
	server := map[string]any{
		"enabled":     true,
		"server_name": sni,
		"reality": map[string]any{
			"enabled":     true,
			"handshake":   map[string]any{"server": hs, "server_port": hp},
			"private_key": priv,
			"short_id":    sids,
		},
	}
	client := map[string]any{
		"server_name": sni,
		"public_key":  pub,
		"short_id":    sids[0],
		"reality":     map[string]any{"public_key": pub},
		"utls":        map[string]any{"enabled": true, "fingerprint": fpOrDefault(in.Fingerprint)},
	}
	sj, _ := json.Marshal(server)
	cj, _ := json.Marshal(client)
	id, err := a.st.SaveSbTls(&store.SbTls{
		ServerID:   in.ServerID,
		Name:       name,
		Mode:       "reality",
		ServerJSON: string(sj),
		ClientJSON: string(cj),
	})
	if err != nil {
		return nil, err
	}
	return J{
		"id":         id,
		"server_id":  in.ServerID,
		"name":       name,
		"mode":       "reality",
		"sni":        sni,
		"public_key": pub,
		"short_ids":  sids,
	}, nil
}

func (a *API) mcpCreateSelfSignedTLS(in mcpCreateSelfSignedTLSIn) (J, error) {
	names := []string{strings.TrimSpace(in.ServerName)}
	serverHost := ""
	if in.ServerID != 0 {
		if sv, _ := a.st.GetServer(in.ServerID); sv != nil {
			serverHost = sv.ClientHost()
			names = append(names, sv.ClientHost(), sv.ClientHostV6(), sv.Host, sv.HostV6)
		}
	}
	if serverHost == "" {
		names = append(names, a.nodeHost())
	}
	names = append(names, in.SANs...)
	names = quickCertNames(names)
	if len(names) == 0 {
		return nil, errors.New("无法确定自签证书域名/IP：请先配置节点对外地址（node_host_override）或显式提供 server_name")
	}
	sni := names[0]
	certPEM, keyPEM, err := singbox.GenerateSelfSignedCertSANs(names, 3650)
	if err != nil {
		return nil, fmt.Errorf("生成自签证书失败: %w", err)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = "自签-" + sni
	}
	server := map[string]any{
		"enabled":     true,
		"server_name": sni,
		"certificate": certPEM,
		"key":         keyPEM,
	}
	client := map[string]any{
		"insecure": true,
		"utls":     map[string]any{"enabled": true, "fingerprint": "chrome"},
	}
	sj, _ := json.Marshal(server)
	cj, _ := json.Marshal(client)
	id, err := a.st.SaveSbTls(&store.SbTls{
		ServerID:   in.ServerID,
		Name:       name,
		Mode:       "tls",
		ServerJSON: string(sj),
		ClientJSON: string(cj),
	})
	if err != nil {
		return nil, err
	}
	return J{
		"id":        id,
		"server_id": in.ServerID,
		"name":      name,
		"mode":      "tls",
		"sni":       sni,
		"sans":      names,
		"sha256":    singbox.CertFingerprintSHA256(certPEM),
	}, nil
}

func (a *API) mcpIssueAcmeCert(ctx context.Context, in mcpIssueAcmeCertIn) (any, error) {
	name := strings.TrimSpace(in.Name)
	domain := strings.TrimSpace(in.Domain)
	if name == "" || domain == "" {
		return nil, errors.New("名称和域名必填")
	}
	method := acmesh.Method(strings.TrimSpace(in.Method))
	if method == "" {
		method = acmesh.MethodCFDNS
	}
	profile := strings.TrimSpace(in.Profile)
	if profile == "" {
		profile = acmesh.ProfileAuto
	}
	keyLength, preferredChain, err := acmesh.OptionsForProfile(profile)
	if err != nil {
		return nil, errors.New("证书兼容策略无效")
	}
	cfToken, _ := a.st.GetSetting("cf_api_token")
	email, _ := a.st.GetSetting("acme_email")
	if method == acmesh.MethodCFDNS && strings.TrimSpace(cfToken) == "" {
		return nil, errors.New("请先通过 update_settings 配置 cf_api_token（Zone→DNS→Edit 权限）")
	}
	cctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	res, err := acmesh.Issue(cctx, acmesh.LocalRunner{}, acmesh.IssueOpts{
		Domain:         domain,
		Method:         method,
		CFToken:        strings.TrimSpace(cfToken),
		Webroot:        strings.TrimSpace(in.Webroot),
		Email:          strings.TrimSpace(email),
		CertDir:        a.certDir(),
		KeyLength:      keyLength,
		PreferredChain: preferredChain,
	})
	if err != nil {
		return nil, fmt.Errorf("证书申请失败: %w", err)
	}
	certPEM, keyPEM, err := acmesh.ReadPEM(cctx, acmesh.LocalRunner{}, res)
	if err != nil {
		return nil, fmt.Errorf("证书已签发但读取失败: %w", err)
	}
	verified, err := certcheck.Verify(certPEM, keyPEM, domain, time.Now())
	if err != nil {
		return nil, fmt.Errorf("证书核验失败: %w", err)
	}
	id, err := a.st.SaveCert(&store.Cert{
		Name:          name,
		Domain:        domain,
		Source:        "acme",
		AcmeMethod:    string(method),
		AcmeProfile:   profile,
		ActualKeyType: verified.KeyType,
		ActualChain:   verified.Chain,
		LastVerifyAt:  verified.VerifiedAt,
		CertPEM:       certPEM,
		KeyPEM:        keyPEM,
		AutoRenew:     true,
		LastRenewAt:   time.Now().Unix(),
	})
	if err != nil {
		return nil, fmt.Errorf("保存证书失败: %w", err)
	}
	saved, _ := a.st.GetCert(id)
	return certPublic(saved), nil
}

func (a *API) mcpUpsertInbound(in mcpUpsertInboundIn) (any, error) {
	var n store.SbInbound
	var oldUpstream int64
	if in.ID > 0 {
		cur, err := a.st.GetSbInbound(in.ID)
		if err != nil || cur == nil {
			return nil, errors.New("入站不存在")
		}
		n = *cur
		oldUpstream = cur.UpstreamInboundID
	} else {
		n.Enabled = true
		n.Listen = "::"
		n.Options = "{}"
	}
	if strings.TrimSpace(in.Type) != "" {
		n.Type = strings.TrimSpace(in.Type)
	}
	if strings.TrimSpace(in.Tag) != "" {
		n.Tag = strings.TrimSpace(in.Tag)
	}
	if strings.TrimSpace(in.Listen) != "" {
		n.Listen = strings.TrimSpace(in.Listen)
	}
	if in.ListenPort > 0 {
		n.ListenPort = in.ListenPort
	}
	if in.ID == 0 || in.ServerID != 0 {
		n.ServerID = in.ServerID
	}
	if in.ID == 0 || in.TlsID != 0 {
		n.TlsID = in.TlsID
	}
	if in.Enabled != nil {
		n.Enabled = *in.Enabled
	}
	if in.UpstreamInboundID != 0 || in.ID == 0 {
		n.UpstreamInboundID = in.UpstreamInboundID
	}
	if in.EgressID != 0 || in.ID == 0 {
		n.EgressID = in.EgressID
	}
	if in.Options != nil {
		b, err := json.Marshal(in.Options)
		if err != nil {
			return nil, fmt.Errorf("options 序列化失败: %w", err)
		}
		n.Options = string(b)
	}
	if !sbInboundTypes[n.Type] {
		return nil, errors.New("不支持的协议类型")
	}
	if n.Tag == "" || n.ListenPort <= 0 || n.ListenPort > 65535 {
		return nil, errors.New("tag 必填且端口需在 1-65535")
	}
	if strings.ContainsAny(n.Tag, " \t\r\n") {
		return nil, errors.New("tag 不能包含空格")
	}
	if n.Type == "mixed" && n.TlsID != 0 {
		if t, _ := a.st.GetSbTls(n.TlsID); t != nil && strings.Contains(t.ServerJSON, "\"reality\"") {
			return nil, errors.New("mixed 代理不支持 Reality，请选普通 TLS 证书或留空")
		}
	}
	if conflict, existingTag, err := a.st.SbInboundPortConflict(&n); err != nil {
		return nil, err
	} else if conflict {
		return nil, fmt.Errorf("端口 %d 已被同机入站「%s」占用", n.ListenPort, existingTag)
	}
	if n.Type == "shadowsocks" && n.Options != "" {
		var opts map[string]any
		if json.Unmarshal([]byte(n.Options), &opts) == nil {
			method, _ := opts["method"].(string)
			if pw, _ := opts["password"].(string); pw == "" {
				key := make([]byte, singbox.SSKeyLen(method))
				_, _ = rand.Read(key)
				opts["password"] = base64.StdEncoding.EncodeToString(key)
				if b, err := json.Marshal(opts); err == nil {
					n.Options = string(b)
				}
			}
		}
	}
	if n.UpstreamInboundID != 0 && n.EgressID != 0 {
		return nil, errors.New("落地入站与代理出口只能二选一")
	}
	if n.UpstreamInboundID != 0 {
		if n.UpstreamInboundID == n.ID {
			return nil, errors.New("落地入站不能是自己")
		}
		up, err := a.st.GetSbInbound(n.UpstreamInboundID)
		if err != nil || up == nil {
			return nil, errors.New("所选落地入站不存在")
		}
		seen := map[int64]bool{n.ID: true}
		for cur := up; cur != nil; {
			if seen[cur.ID] || len(seen) > 16 {
				return nil, errors.New("落地链路存在环路或层级过深")
			}
			seen[cur.ID] = true
			if cur.UpstreamInboundID == 0 {
				break
			}
			cur, _ = a.st.GetSbInbound(cur.UpstreamInboundID)
		}
	}
	newID, err := a.st.SaveSbInbound(&n)
	if err != nil {
		return nil, err
	}
	a.sbScheduleServer(append([]int64{n.ServerID}, a.landingServerIDs(n.UpstreamInboundID, oldUpstream)...)...)
	a.invalidateAllLinks()
	return a.st.GetSbInbound(newID)
}

func (a *API) mcpManageEgress(ctx context.Context, in mcpManageEgressIn) (any, error) {
	switch strings.ToLower(strings.TrimSpace(in.Action)) {
	case "upsert":
		e := store.SbEgress{
			ID:          in.ID,
			Name:        strings.TrimSpace(in.Name),
			Type:        strings.TrimSpace(in.Type),
			Host:        strings.TrimSpace(in.Host),
			Port:        in.Port,
			Username:    strings.TrimSpace(in.Username),
			Password:    in.Password,
			TLSEnabled:  in.TLSEnabled,
			SNI:         strings.TrimSpace(in.SNI),
			TLSCertID:   in.TLSCertID,
			TLSInsecure: in.TLSInsecure,
			UDPMode:     strings.TrimSpace(in.UDPMode),
		}
		if h, p, err := net.SplitHostPort(e.Host); err == nil && h != "" {
			if n := atoi(p); n > 0 && n <= 65535 {
				e.Host, e.Port = h, int(n)
			}
		}
		if e.Type != "socks" && e.Type != "http" {
			return nil, errors.New("type 仅支持 socks / http")
		}
		if e.Name == "" || e.Host == "" || e.Port <= 0 || e.Port > 65535 {
			return nil, errors.New("名称、地址必填，端口需在 1-65535")
		}
		if e.TLSEnabled && e.Type != "http" {
			return nil, errors.New("仅 http 代理出口支持 TLS")
		}
		if (e.Password == "" || e.Password == "***") && e.ID != 0 {
			if stored, _ := a.st.GetSbEgress(e.ID); stored != nil {
				e.Password = stored.Password
			}
		}
		newID, err := a.st.SaveSbEgress(&e)
		if err != nil {
			return nil, err
		}
		a.sbRebuildLog()
		saved, _ := a.st.GetSbEgress(newID)
		return egressJSON(saved, a.countEgressRefs()[newID]), nil
	case "test":
		eg, err := a.st.GetSbEgress(in.ID)
		if err != nil || eg == nil {
			return nil, errors.New("代理出口不存在")
		}
		if a.sbctl == nil {
			return nil, errors.New("sing-box 控制器未启用")
		}
		var sid int64
		if in.TestViaServerID != nil {
			sid = *in.TestViaServerID
		}
		proxy, extra := egressProxyURL(eg)
		trustPEM := ""
		if eg.TLSEnabled && eg.Type == "http" && eg.TLSCertID != 0 {
			if c, _ := a.st.GetCert(eg.TLSCertID); c != nil && !c.DecryptFailed {
				trustPEM = c.CertPEM
			}
		}
		script := egressCheckPrelude(proxy, extra, trustPEM, false) + egressSingleCheckScript()
		tctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		out, runErr := a.sbctl.RunOnServer(tctx, sid, script)
		out = strings.TrimSpace(out)
		if runErr != nil {
			return J{"ok": false, "via_server_id": sid, "output": truncateRunesAPI(out, 500), "error": runErr.Error()}, nil
		}
		ip, latMs := out, 0
		if nl := strings.LastIndexByte(out, '\n'); nl >= 0 {
			ip = strings.TrimSpace(out[:nl])
			if f := parseFloat(strings.TrimSpace(out[nl+1:])); f > 0 {
				latMs = int(f * 1000)
			}
		}
		return J{"ok": true, "via_server_id": sid, "exit_ip": ip, "latency_ms": latMs}, nil
	case "delete":
		eg, err := a.st.GetSbEgress(in.ID)
		if err != nil || eg == nil {
			return nil, errors.New("代理出口不存在")
		}
		refs := a.countEgressRefs()[in.ID]
		if !in.Confirm {
			return J{"dry_run": true, "egress_id": eg.ID, "name": eg.Name, "inbound_refs": refs, "message": "请确认后传 confirm=true 执行删除。"}, nil
		}
		if err := a.st.DeleteSbEgress(in.ID); err != nil {
			return nil, err
		}
		return J{"deleted": true, "egress_id": in.ID}, nil
	default:
		return nil, errors.New("action 必须为 upsert | test | delete")
	}
}

func (a *API) mcpUpsertNode(in mcpUpsertNodeIn) (any, error) {
	var n store.Node
	if in.ID > 0 {
		cur, err := a.st.GetNode(in.ID)
		if err != nil || cur == nil {
			return nil, errors.New("节点不存在")
		}
		n = *cur
	} else {
		n.Type = "self_built"
		n.Enabled = true
		n.SetOutputFamilies(true, true)
	}
	if strings.TrimSpace(in.Type) != "" {
		n.Type = strings.TrimSpace(in.Type)
	}
	if strings.TrimSpace(in.Name) != "" {
		n.Name = strings.TrimSpace(in.Name)
	}
	if in.Remark != "" {
		n.Remark = strings.TrimSpace(in.Remark)
	}
	if in.InboundTag != "" {
		n.InboundTag = strings.TrimSpace(in.InboundTag)
	}
	if in.RouteUpstreamInboundID != 0 {
		n.RouteUpstreamInboundID = in.RouteUpstreamInboundID
	}
	if in.ShareLink != "" {
		n.ShareLink = strings.TrimSpace(in.ShareLink)
		if n.Type == "external" {
			if p, err := subconv.ParseLink(n.ShareLink); err == nil {
				if n.Protocol == "" {
					n.Protocol = p.Protocol
				}
				if n.Name == "" {
					n.Name = p.Name
				}
			}
		}
	}
	if in.Enabled != nil {
		n.Enabled = *in.Enabled
	}
	if in.OutputV4 != nil || in.OutputV6 != nil {
		v4, v6 := n.OutputV4, n.OutputV6
		if in.OutputV4 != nil {
			v4 = *in.OutputV4
		}
		if in.OutputV6 != nil {
			v6 = *in.OutputV6
		}
		n.SetOutputFamilies(v4, v6)
	}
	if in.GroupIDs != nil {
		n.GroupIDs = in.GroupIDs
	}
	if n.Type != "self_built" && n.Type != "external" {
		return nil, errors.New("节点类型必须为 self_built / external")
	}
	if msg := requireNodeName(&n); msg != "" {
		return nil, errors.New(msg)
	}
	if msg := a.validateNodeRoute(&n); msg != "" {
		return nil, errors.New(msg)
	}
	if in.ID == 0 {
		id, err := a.st.CreateNode(n)
		if err != nil {
			return nil, err
		}
		a.sbRebuildLog()
		return a.st.GetNode(id)
	}
	if err := a.st.UpdateNode(n); err != nil {
		return nil, err
	}
	a.sbRebuildLog()
	return a.st.GetNode(n.ID)
}

func (a *API) mcpManageNodeSource(ctx context.Context, in mcpManageNodeSourceIn) (any, error) {
	switch strings.ToLower(strings.TrimSpace(in.Action)) {
	case "upsert":
		if strings.TrimSpace(in.URL) == "" {
			return nil, errors.New("订阅地址 url 必填")
		}
		en := true
		if in.Enabled != nil {
			en = *in.Enabled
		}
		src := store.NodeSource{ID: in.ID, Name: strings.TrimSpace(in.Name), URL: strings.TrimSpace(in.URL), Enabled: en, GroupIDs: in.GroupIDs}
		if in.ID == 0 {
			id, err := a.st.CreateSource(src)
			if err != nil {
				return nil, err
			}
			return a.st.GetSource(id)
		}
		if err := a.st.UpdateSource(src); err != nil {
			return nil, err
		}
		return a.st.GetSource(in.ID)
	case "fetch":
		src, err := a.st.GetSource(in.ID)
		if err != nil || src == nil {
			return nil, errors.New("订阅源不存在")
		}
		cnt, ferr := a.fetchSource(ctx, src, in.GroupIDs)
		if ferr != "" {
			return nil, fmt.Errorf("抓取失败: %s", ferr)
		}
		a.invalidateAllLinks()
		return J{"source_id": src.ID, "imported": cnt}, nil
	case "delete":
		src, err := a.st.GetSource(in.ID)
		if err != nil || src == nil {
			return nil, errors.New("订阅源不存在")
		}
		if !in.Confirm {
			return J{"dry_run": true, "source_id": src.ID, "name": src.Name, "message": "即将删除该订阅源及其导入的所有外部节点，请确认后传 confirm=true 执行。"}, nil
		}
		if err := a.st.DeleteSource(in.ID); err != nil {
			return nil, err
		}
		a.invalidateAllLinks()
		return J{"deleted": true, "source_id": in.ID}, nil
	default:
		return nil, errors.New("action 必须为 upsert | fetch | delete")
	}
}
