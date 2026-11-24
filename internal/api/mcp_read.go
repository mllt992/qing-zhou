package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"qingzhou/internal/intervalcfg"
	"qingzhou/internal/sbproc"
	"qingzhou/internal/sbver"
	"qingzhou/internal/store"
	"qingzhou/internal/subconv"
)

type mcpEmptyIn struct{}

type mcpRangeIn struct {
	Range string `json:"range,omitempty" jsonschema:"统计时间范围：7d | 14d | 30d | 90d，默认 7d"`
}

type mcpTrafficReportIn struct {
	Range      string `json:"range,omitempty" jsonschema:"统计时间范围：7d | 14d | 30d | 90d，默认 7d"`
	Query      string `json:"query,omitempty" jsonschema:"可选按用户名/邮箱搜索用户用量"`
	LimitUsers int    `json:"limit_users,omitempty" jsonschema:"返回用户用量行数上限，默认 20，最大 100"`
}

type mcpAlertsIn struct {
	UnreadOnly bool `json:"unread_only,omitempty" jsonschema:"是否只返回未读告警"`
	Limit      int  `json:"limit,omitempty" jsonschema:"最多返回条数，默认 50"`
}

type mcpListUsersIn struct {
	Query string `json:"query,omitempty" jsonschema:"按用户名/邮箱/备注模糊搜索"`
	Limit int    `json:"limit,omitempty" jsonschema:"最多返回条数，默认 50，最大 200"`
}

type mcpServerIDIn struct {
	ServerID int64 `json:"server_id,omitempty" jsonschema:"服务器 ID，0 或留空表示面板本机"`
}

func (a *API) registerMCPReadTools(srv *mcp.Server) {
	// 1. diagnose_panel
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "diagnose_panel",
		Description: "一站式面板健康与故障体检：汇总部署检查清单、本机及远程 sing-box 版本与 with_v2ray_api 状态、配置同步报错、漏填 node_host_override 检测、未绑定节点卡片的入站、未分组节点、空分组套餐、损坏的中转链路与未读监控告警。排查『没节点』『流量为 0』『连不上』等问题时首选调用。",
	}, "mcp:read", func(ctx context.Context, _ mcpAuthInfo, _ mcpEmptyIn) (any, error) {
		return a.buildPanelDiagnostics()
	})

	// 2. get_overview_stats
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "get_overview_stats",
		Description: "获取站点概览统计：总用户数、活跃用户、今日新增、在线人数、累计与周期流量、积分与上架套餐数。",
	}, "mcp:read", func(_ context.Context, _ mcpAuthInfo, in mcpRangeIn) (any, error) {
		ov, err := a.st.Overview()
		if err != nil {
			return nil, fmt.Errorf("读取概览失败: %w", err)
		}
		win := a.st.UserOnlineWindowSec()
		online, _ := a.st.OnlineCount(win)
		onlineUsers, _ := a.st.OnlineUsers(win, 20)
		days := rangeDays(in.Range)
		cur, prev, _ := a.st.PeriodStats(days)
		return J{
			"total_users":   ov.TotalUsers,
			"active_users":  ov.ActiveUsers,
			"new_today":     ov.NewToday,
			"total_traffic": ov.TotalTraffic,
			"points_issued": ov.PointsIssued,
			"packages_on":   ov.PackagesOn,
			"online":        online,
			"online_users":  onlineUsers,
			"online_window": win,
			"range_days":    days,
			"period":        cur,
			"period_prev":   prev,
		}, nil
	})

	// 3. get_monitor_status
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "get_monitor_status",
		Description: "获取本机与所有远程服务器的实时监控状态（在线/离线、CPU/内存/磁盘/网速/负载、到期与流量配额提醒、未读告警数）。不包含任何 SSH 私钥或探针 Token。",
	}, "mcp:read", func(_ context.Context, _ mcpAuthInfo, _ mcpEmptyIn) (any, error) {
		return a.buildMonitorSnapshot()
	})

	// 4. get_traffic_report
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "get_traffic_report",
		Description: "获取流量与运营统计报表：按日站点总流量、各套餐销售与消耗统计、用户流量排行榜。",
	}, "mcp:read", func(_ context.Context, _ mcpAuthInfo, in mcpTrafficReportIn) (any, error) {
		days := rangeDays(in.Range)
		daily, _ := a.st.SiteDailyTraffic(days)
		pkgStats, _ := a.st.PackageStats()
		limit := in.LimitUsers
		if limit <= 0 {
			limit = 20
		}
		if limit > 100 {
			limit = 100
		}
		userRows, totalUsers, _ := a.st.UserStats(store.UserStatFilter{
			Query: strings.TrimSpace(in.Query),
			Days:  days,
			Sort:  "used",
			Desc:  true,
			Limit: limit,
		})
		return J{
			"range_days":    days,
			"daily_traffic": fillDays(daily, days),
			"packages":      pkgStats,
			"top_users":     userRows,
			"users_total":   totalUsers,
		}, nil
	})

	// 5. list_alerts
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "list_alerts",
		Description: "查询服务器与节点监控告警列表（默认返回最近 50 条）。",
	}, "mcp:read", func(_ context.Context, _ mcpAuthInfo, in mcpAlertsIn) (any, error) {
		alerts, err := a.st.ListAlerts(in.UnreadOnly)
		if err != nil {
			return nil, fmt.Errorf("读取告警失败: %w", err)
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 50
		}
		if len(alerts) > limit {
			alerts = alerts[:limit]
		}
		unread, _ := a.st.UnreadAlertCount()
		return J{"unread_count": unread, "alerts": alerts}, nil
	})

	// 6. list_servers
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "list_servers",
		Description: "列出面板本机（server_id=0）和全部远程服务器，附带各机 sing-box 版本、with_v2ray_api 状态与配置下发同步状态。SSH 密码/私钥和探针 Token 均已脱敏。",
	}, "mcp:read", func(_ context.Context, _ mcpAuthInfo, _ mcpEmptyIn) (any, error) {
		servers, err := a.st.ListServers()
		if err != nil {
			return nil, fmt.Errorf("读取服务器失败: %w", err)
		}
		observed, _ := a.st.NodeSingboxAll()
		jobs := a.upgradeSnapshot()
		syncStatuses := map[int64]any{}
		if a.sbctl != nil {
			for k, v := range a.sbctl.SyncStatuses() {
				syncStatuses[k] = v
			}
		}
		safeServers := make([]J, 0, len(servers))
		for _, sv := range servers {
			cp := *sv
			maskServerSecrets(&cp)
			cp.ProbeToken = ""
			safeServers = append(safeServers, J{
				"server":      cp,
				"singbox":     viewFor(sv.ID, sv.Name, sv.Host, false, sv.Enabled, observed, jobs),
				"sync_status": syncStatuses[sv.ID],
			})
		}
		return J{
			"local": J{
				"server_id":   store.LocalNodeID,
				"name":        store.LocalNodeName,
				"node_host":   a.nodeHost(),
				"singbox":     viewFor(store.LocalNodeID, store.LocalNodeName, "", true, true, observed, jobs),
				"sync_status": syncStatuses[store.LocalNodeID],
			},
			"servers":                 safeServers,
			"min_supported_singbox":   sbver.MinSupported,
			"current_release_singbox": sbver.CurrentReleaseVersion,
		}, nil
	})

	// 7. list_tls_and_certs
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "list_tls_and_certs",
		Description: "列出全部 TLS / Reality 配置和托管证书摘要（SNI、公钥、有效期、状态等；私钥与 PEM 永不回显）。",
	}, "mcp:read", func(_ context.Context, _ mcpAuthInfo, _ mcpEmptyIn) (any, error) {
		tlsList, err := a.st.ListSbTls()
		if err != nil {
			return nil, fmt.Errorf("读取 TLS 失败: %w", err)
		}
		certs, err := a.st.ListCerts()
		if err != nil {
			return nil, fmt.Errorf("读取证书失败: %w", err)
		}
		safeTLS := make([]J, 0, len(tlsList))
		for _, t := range tlsList {
			var sj, cj map[string]any
			_ = json.Unmarshal([]byte(t.ServerJSON), &sj)
			_ = json.Unmarshal([]byte(t.ClientJSON), &cj)
			item := J{
				"id":             t.ID,
				"server_id":      t.ServerID,
				"name":           t.Name,
				"mode":           t.Mode,
				"cert_id":        t.CertID,
				"sni":            mcpMapStr(sj, "server_name"),
				"decrypt_failed": t.DecryptFailed,
				"created_at":     t.CreatedAt,
				"updated_at":     t.UpdatedAt,
			}
			if t.Mode == "reality" {
				item["public_key"] = mcpNestedStr(cj, "reality", "public_key")
				if r, ok := sj["reality"].(map[string]any); ok {
					item["short_id"] = r["short_id"]
					item["handshake"] = r["handshake"]
				}
				item["fingerprint"] = mcpNestedStr(cj, "utls", "fingerprint")
			} else {
				item["insecure"] = mcpMapBool(cj, "insecure")
				if t.CertID != 0 {
					if c, _ := a.st.GetCert(t.CertID); c != nil && !c.DecryptFailed {
						item["cert_info"] = certInfoFromPEM(c.CertPEM)
					}
				} else if certPEM, ok := sj["certificate"].(string); ok && certPEM != "" {
					item["cert_info"] = certInfoFromPEM(certPEM)
				}
			}
			safeTLS = append(safeTLS, item)
		}
		safeCerts := make([]map[string]any, 0, len(certs))
		for _, c := range certs {
			safeCerts = append(safeCerts, certPublic(c))
		}
		return J{"tls_profiles": safeTLS, "managed_certs": safeCerts}, nil
	})

	// 8. list_inbounds
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "list_inbounds",
		Description: "列出全部 sing-box 入站配置与代理出口（含所属机器、协议、端口、TLS、中转链路、凭据数；出口密码已脱敏）。",
	}, "mcp:read", func(_ context.Context, _ mcpAuthInfo, _ mcpEmptyIn) (any, error) {
		ibs, err := a.st.ListSbInbounds()
		if err != nil {
			return nil, fmt.Errorf("读取入站失败: %w", err)
		}
		egresses, _ := a.st.ListSbEgresses()
		usersByTag, _ := a.st.BuildUsersByTag(time.Now().Unix())
		serverNames := a.serverNameMap()
		rows := make([]J, 0, len(ibs))
		for _, ib := range ibs {
			creds, accts := inboundCredentialStats(usersByTag[ib.Tag])
			rows = append(rows, J{
				"id":                  ib.ID,
				"server_id":           ib.ServerID,
				"server_name":         serverNames[ib.ServerID],
				"type":                ib.Type,
				"tag":                 ib.Tag,
				"listen":              ib.Listen,
				"listen_port":         ib.ListenPort,
				"tls_id":              ib.TlsID,
				"enabled":             ib.Enabled,
				"upstream_inbound_id": ib.UpstreamInboundID,
				"upstream_broken":     ib.UpstreamBroken,
				"egress_id":           ib.EgressID,
				"options":             ib.Options,
				"credential_count":    creds,
				"account_count":       accts,
			})
		}
		refs := a.countEgressRefs()
		safeEgresses := make([]map[string]any, 0, len(egresses))
		for _, eg := range egresses {
			safeEgresses = append(safeEgresses, egressJSON(eg, refs[eg.ID]))
		}
		return J{"inbounds": rows, "egresses": safeEgresses}, nil
	})

	// 9. list_nodes_and_groups
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "list_nodes_and_groups",
		Description: "列出全部节点（自建/外部）、节点分组、机场订阅源、当前免费分组及有效节点对外地址（node_host）。",
	}, "mcp:read", func(_ context.Context, _ mcpAuthInfo, _ mcpEmptyIn) (any, error) {
		nodes, err := a.st.ListNodes()
		if err != nil {
			return nil, fmt.Errorf("读取节点失败: %w", err)
		}
		groups, err := a.st.ListGroups()
		if err != nil {
			return nil, fmt.Errorf("读取分组失败: %w", err)
		}
		sources, _ := a.st.ListSources()
		freeGroup, _ := a.st.GetSettingInt64("free_group_id", 0)
		return J{
			"node_host":     a.nodeHost(),
			"free_group_id": freeGroup,
			"nodes":         nodes,
			"groups":        groups,
			"sources":       sources,
		}, nil
	})

	// 10. list_packages
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "list_packages",
		Description: "列出全部套餐与流量包，包含绑定的节点分组、用户组限制及当前在用订阅人数。",
	}, "mcp:read", func(_ context.Context, _ mcpAuthInfo, _ mcpEmptyIn) (any, error) {
		pkgs, err := a.st.ListPackages()
		if err != nil {
			return nil, fmt.Errorf("读取套餐失败: %w", err)
		}
		subs, _ := a.st.PlanSubscriberCounts()
		for _, p := range pkgs {
			if p.Type == "plan" {
				p.GroupIDs, _ = a.st.PlanGroupIDs(p.ID)
			}
			p.UserGroupIDs, _ = a.st.PackageUserGroupIDs(p.ID)
			p.Subscribers = subs[p.ID]
		}
		return J{"packages": pkgs}, nil
	})

	// 11. list_users
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "list_users",
		Description: "查询用户列表（支持按用户名/邮箱/备注搜索），返回用户状态、活跃套餐、剩余流量和订阅获取记录。",
	}, "mcp:read", func(_ context.Context, _ mcpAuthInfo, in mcpListUsersIn) (any, error) {
		limit := in.Limit
		if limit <= 0 {
			limit = 50
		}
		if limit > 200 {
			limit = 200
		}
		users, err := a.st.ListUsers(strings.TrimSpace(in.Query), limit)
		if err != nil {
			return nil, fmt.Errorf("读取用户失败: %w", err)
		}
		ids := make([]int64, len(users))
		for i, u := range users {
			ids[i] = u.ID
		}
		groups, _ := a.st.UserGroupIDsBulk(ids)
		buckets, _ := a.st.ListBucketsBulk(ids)
		win := a.st.UserOnlineWindowSec()
		out := make([]J, 0, len(users))
		for _, u := range users {
			bs := buckets[u.ID]
			if bs == nil {
				bs = []*store.Bucket{}
			}
			out = append(out, adminUserViewWithWindow(u, groups[u.ID], bs, win))
		}
		return J{"users": out, "count": len(out)}, nil
	})

	// 12. get_settings
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "get_settings",
		Description: "读取当前系统设置（含环境变量覆盖来源说明；所有密码/Token/密钥均脱敏为 ***，不会返回明文）。",
	}, "mcp:read", func(_ context.Context, _ mcpAuthInfo, _ mcpEmptyIn) (any, error) {
		return a.buildSafeSettingsMap()
	})

	// 13. check_singbox_config
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "check_singbox_config",
		Description: "为指定机器（server_id=0 为面板本机）生成预览 sing-box 配置并运行 sing-box check 校验合法性与端口冲突。",
	}, "mcp:read", func(ctx context.Context, _ mcpAuthInfo, in mcpServerIDIn) (any, error) {
		return a.runSingboxConfigCheck(ctx, in.ServerID)
	})

	// 14. detect_node_host
	addMCPTool(a, srv, &mcp.Tool{
		Name:        "detect_node_host",
		Description: "探测面板本机的公网出口 IPv4/IPv6 与候选节点对外地址（用于填写 node_host_override）。",
	}, "mcp:read", func(ctx context.Context, _ mcpAuthInfo, _ mcpEmptyIn) (any, error) {
		pctx, cancel := context.WithTimeout(ctx, nodeHostProbeTimeout)
		defer cancel()
		echoed := detectEgressIPs(pctx)
		out := append([]nodeHostCandidate{}, echoed...)
		out = append(out, a.configuredNodeHostCandidates()...)
		out = append(out, localInterfaceCandidates(len(echoed) == 0)...)
		out = recommendMeasured(dedupeNodeHostCandidates(out))
		curOverride, _ := a.st.GetSetting("node_host_override")
		return J{
			"current_override": curOverride,
			"effective_host":   a.nodeHost(),
			"candidates":       out,
		}, nil
	})

	// Resources
	srv.AddResource(&mcp.Resource{
		URI:         "qingzhou://health",
		Name:        "Panel Health & Diagnostics",
		Description: "轻舟面板当前健康状态与一站式故障诊断快照（JSON）。",
		MIMEType:    "application/json",
	}, func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		auth, ok := mcpAuthFromContext(ctx)
		if !ok || !store.APITokenHasScope(auth.Scopes, "mcp:read") {
			return nil, errors.New("权限不足：读取 qingzhou://health 需要 mcp:read scope")
		}
		diag, err := a.buildPanelDiagnostics()
		if err != nil {
			return nil, err
		}
		b, _ := json.MarshalIndent(diag, "", "  ")
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{
				URI:      "qingzhou://health",
				MIMEType: "application/json",
				Text:     string(b),
			}},
		}, nil
	})

	srv.AddResource(&mcp.Resource{
		URI:         "qingzhou://topology",
		Name:        "Panel Node Topology",
		Description: "轻舟面板完整拓扑快照：机器 → 入站 → 中转/代理出口 → 节点 → 分组 → 套餐（JSON）。",
		MIMEType:    "application/json",
	}, func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		auth, ok := mcpAuthFromContext(ctx)
		if !ok || !store.APITokenHasScope(auth.Scopes, "mcp:read") {
			return nil, errors.New("权限不足：读取 qingzhou://topology 需要 mcp:read scope")
		}
		topo, err := a.buildTopologySnapshot()
		if err != nil {
			return nil, err
		}
		b, _ := json.MarshalIndent(topo, "", "  ")
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{
				URI:      "qingzhou://topology",
				MIMEType: "application/json",
				Text:     string(b),
			}},
		}, nil
	})
}

func mcpMapStr(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	s, _ := m[k].(string)
	return s
}

func mcpMapBool(m map[string]any, k string) bool {
	if m == nil {
		return false
	}
	b, _ := m[k].(bool)
	return b
}

func mcpNestedStr(m map[string]any, k1, k2 string) string {
	if m == nil {
		return ""
	}
	if inner, ok := m[k1].(map[string]any); ok {
		s, _ := inner[k2].(string)
		return s
	}
	return ""
}

func (a *API) serverNameMap() map[int64]string {
	m := map[int64]string{store.LocalNodeID: store.LocalNodeName}
	if servers, err := a.st.ListServers(); err == nil {
		for _, sv := range servers {
			m[sv.ID] = sv.Name
		}
	}
	return m
}

func (a *API) buildSafeSettingsMap() (J, error) {
	all, err := a.st.AllSettings()
	if err != nil {
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}
	var envKeys []string
	for k, envName := range settingEnv {
		if v := os.Getenv(envName); v != "" {
			all[k] = v
			envKeys = append(envKeys, k)
		}
	}
	all[intervalcfg.SettingProbeSeconds] = strconv.FormatInt(int64(intervalcfg.Probe(a.st)/time.Second), 10)
	all[intervalcfg.SettingStatsMinutes] = strconv.FormatInt(int64(intervalcfg.Stats(a.st)/time.Minute), 10)
	all[intervalcfg.SettingReconcileMinutes] = strconv.FormatInt(int64(intervalcfg.Reconcile(a.st)/time.Minute), 10)
	for _, k := range intervalcfg.RuntimeSettingKeys {
		if v, locked := intervalcfg.EnvSettingValue(k); locked {
			all[k] = v
			envKeys = append(envKeys, k)
		}
	}
	for k := range all {
		if secretSettings[k] && all[k] != "" {
			all[k] = "***"
		}
	}
	// Trim large template strings so get_settings stays compact for LLM context.
	for _, big := range []string{"sub_clash_template", "sub_singbox_template", "sb_base_config", brandIconSetting} {
		if len(all[big]) > 200 {
			all[big] = fmt.Sprintf("<configured: %d bytes>", len(all[big]))
		}
	}
	if all["sub_clash_udp"] == "" {
		all["sub_clash_udp"] = "1"
	}
	return J{
		"settings":             all,
		"env_locked_keys":      envKeys,
		"effective_node_host":  a.nodeHost(),
		"effective_site_base":  a.siteBase(),
		"telegram_configured":  a.telegramConfigured(),
		"telegram_bot_name":    a.telegramUsername(),
		"smtp_configured":      a.mailerConfigured(),
		"default_clash_loaded": all["sub_clash_template"] == "" && subconv.DefaultClashTemplate != "",
	}, nil
}

func (a *API) buildMonitorSnapshot() (J, error) {
	now := time.Now()
	onlineWindow := now.Add(-intervalcfg.OnlineWindow(a.st)).Unix()
	total, online, expiring, err := a.st.CountProbeServersSince(onlineWindow)
	if err != nil {
		return nil, fmt.Errorf("查询监控统计失败: %w", err)
	}
	unread, _ := a.st.UnreadAlertCount()
	latest, _ := a.st.GetLatestMetricsForAll()
	servers, err := a.serversWithLocal(latest)
	if err != nil {
		return nil, fmt.Errorf("查询服务器列表失败: %w", err)
	}
	if local := a.localMonitorServer(latest); local != nil {
		total++
		if local.LastSeen >= onlineWindow {
			online++
		}
		if local.ExpiryDate > now.Unix() && local.ExpiryDate <= now.AddDate(0, 0, 3).Unix() {
			expiring++
		}
	}
	machines := make([]J, 0, len(servers))
	for _, sv := range servers {
		m := latest[sv.ID]
		isOnline := sv.LastSeen >= onlineWindow
		row := J{
			"id":                  sv.ID,
			"name":                sv.Name,
			"host":                sv.Host,
			"local":               sv.ID == store.LocalNodeID,
			"enabled":             sv.Enabled,
			"probe_enabled":       sv.ProbeEnabled,
			"online":              isOnline,
			"last_seen":           sv.LastSeen,
			"expiry_date":         sv.ExpiryDate,
			"traffic_limit_bytes": sv.TrafficLimitBytes,
			"location":            sv.Location,
			"provider":            sv.Provider,
		}
		if m != nil {
			row["metrics"] = m
		}
		machines = append(machines, row)
	}
	return J{
		"total_servers": total,
		"online":        online,
		"offline":       total - online,
		"expiring_soon": expiring,
		"alerts_unread": unread,
		"machines":      machines,
	}, nil
}

func (a *API) buildPanelDiagnostics() (J, error) {
	servers, err := a.st.ListServers()
	if err != nil {
		return nil, err
	}
	observed, _ := a.st.NodeSingboxAll()
	inbounds, err := a.st.ListSbInbounds()
	if err != nil {
		return nil, err
	}
	nodes, err := a.st.ListNodes()
	if err != nil {
		return nil, err
	}
	groups, err := a.st.ListGroups()
	if err != nil {
		return nil, err
	}
	pkgs, err := a.st.ListPackages()
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	issues := []J{}
	addIssue := func(severity, code, title, detail, fix string) {
		issues = append(issues, J{
			"severity": severity,
			"code":     code,
			"title":    title,
			"detail":   detail,
			"fix":      fix,
		})
	}

	// 1. HTTPS public_base check
	base := a.siteBase()
	parsed, _ := url.Parse(base)
	hasHTTPS := parsed != nil && parsed.Scheme == "https" && parsed.Host != ""
	if !hasHTTPS {
		addIssue("warning", "missing_https_public_base", "未配置 HTTPS 面板访问地址",
			fmt.Sprintf("当前 public_base=%q", base),
			"在系统设置（update_settings）配置 https:// 面板访问地址，或在反向代理前配置证书。")
	}

	// 2. Machine sing-box capabilities
	activeMachines := map[int64]string{}
	for _, sv := range servers {
		if sv.Enabled {
			activeMachines[sv.ID] = sv.Name
		}
	}
	hasLocalEnabledInbound := false
	enabledInboundCount := 0
	for _, ib := range inbounds {
		if ib.Enabled {
			enabledInboundCount++
			if ib.ServerID == store.LocalNodeID {
				hasLocalEnabledInbound = true
			}
		}
	}
	if hasLocalEnabledInbound || (len(activeMachines) == 0 && observed[store.LocalNodeID] != nil) {
		activeMachines[store.LocalNodeID] = store.LocalNodeName
	}
	machineIDs := make([]int64, 0, len(activeMachines))
	for id := range activeMachines {
		machineIDs = append(machineIDs, id)
	}
	sort.Slice(machineIDs, func(i, j int) bool { return machineIDs[i] < machineIDs[j] })
	for _, id := range machineIDs {
		obs := observed[id]
		if obs == nil || obs.Version == "" {
			addIssue("warning", "singbox_unprobed", fmt.Sprintf("机器「%s」尚未检测到 sing-box", activeMachines[id]),
				"未探测到 sing-box 版本或连接失败。",
				"检查该机是否已安装 sing-box，或调用 upgrade_server_singbox / test_server_ssh。")
			continue
		}
		if obs.Error != "" {
			addIssue("error", "singbox_probe_error", fmt.Sprintf("机器「%s」sing-box 探测报错", activeMachines[id]),
				obs.Error,
				"检查该机服务状态或 SSH 连接。")
		}
		if !obs.HasV2RayAPI {
			addIssue("error", "missing_v2ray_api", fmt.Sprintf("机器「%s」缺少 with_v2ray_api", activeMachines[id]),
				fmt.Sprintf("当前版本 %s 缺少 v2ray_api，所有用户流量统计将为 0、配额不生效。", obs.Version),
				"调用 upgrade_server_singbox 重装面板发布的含 with_v2ray_api 构建。")
		}
		if (sbver.Info{Version: obs.Version}).TooOld() {
			addIssue("error", "singbox_too_old", fmt.Sprintf("机器「%s」sing-box 版本过低", activeMachines[id]),
				fmt.Sprintf("当前 %s < 最低支持 %s，面板会停止下发配置。", obs.Version, sbver.MinSupported),
				"调用 upgrade_server_singbox 升级 sing-box。")
		}
	}

	// 3. Critical pitfall: missing node_host_override for local nodes!
	effectiveHost := a.nodeHost()
	if (hasLocalEnabledInbound || len(nodes) > 0) && effectiveHost == "" {
		addIssue("error", "missing_node_host", "未配置「节点对外地址」（node_host_override）",
			"本机节点没有服务器记录，留空 node_host_override 且无已启用远程服务器时，所有用户的自建节点订阅列表将为空（共 0 个节点）。",
			"先调用 detect_node_host 获取本机公网 IP，再调用 update_settings 设置 node_host_override。")
	}

	// 4. Orphan inbounds (enabled non-mixed inbound not bound to any enabled self_built node)
	boundTags := map[string]bool{}
	enabledNodesByGroup := map[int64]int{}
	ungroupedNodes := []string{}
	brokenRoutes := []string{}
	for _, n := range nodes {
		if n.Type == "self_built" && n.InboundTag != "" && n.Enabled {
			boundTags[n.InboundTag] = true
		}
		if n.Enabled {
			if len(n.GroupIDs) == 0 {
				ungroupedNodes = append(ungroupedNodes, fmt.Sprintf("#%d(%s)", n.ID, n.Name))
			}
			for _, gid := range n.GroupIDs {
				enabledNodesByGroup[gid]++
			}
		}
		if n.RouteUpstreamBroken {
			brokenRoutes = append(brokenRoutes, fmt.Sprintf("节点#%d(%s)", n.ID, n.Name))
		}
	}
	orphanInbounds := []string{}
	for _, ib := range inbounds {
		if ib.UpstreamBroken {
			brokenRoutes = append(brokenRoutes, fmt.Sprintf("入站#%d(%s)", ib.ID, ib.Tag))
		}
		if ib.Enabled && ib.Type != "mixed" && !boundTags[ib.Tag] {
			orphanInbounds = append(orphanInbounds, fmt.Sprintf("#%d(%s/%s:%d)", ib.ID, ib.Tag, ib.Type, ib.ListenPort))
		}
	}
	if enabledInboundCount == 0 {
		addIssue("warning", "no_enabled_inbounds", "没有任何已启用的入站",
			"当前没有可用的 sing-box 入站监听。",
			"调用 quick_setup_node 一键创建 TLS + 入站 + 节点 + 分组。")
	}
	if len(orphanInbounds) > 0 {
		addIssue("warning", "orphan_inbounds", "存在已启用入站但未创建对应自建节点卡片",
			"以下入站不会出现在用户订阅中："+strings.Join(orphanInbounds, "、"),
			"调用 upsert_node 创建 type=self_built 节点绑定该 inbound_tag 并加入节点分组。")
	}
	if len(ungroupedNodes) > 0 {
		addIssue("warning", "ungrouped_nodes", "存在已启用节点未加入任何分组",
			"未分组节点不会授权给任何套餐："+strings.Join(ungroupedNodes, "、"),
			"调用 upsert_node 将节点加入分组（如默认分组）。")
	}
	if len(brokenRoutes) > 0 {
		addIssue("error", "broken_relay_routes", "存在失效的中转/固定落地链路",
			strings.Join(brokenRoutes, "、"),
			"重新指向有效落地入站或解除中转。")
	}

	// 5. Packages bound to empty groups
	emptyPlanNames := []string{}
	for _, p := range pkgs {
		if !p.Enabled || p.Type != "plan" {
			continue
		}
		gids, _ := a.st.PlanGroupIDs(p.ID)
		usable := 0
		for _, gid := range gids {
			usable += enabledNodesByGroup[gid]
		}
		if usable == 0 {
			emptyPlanNames = append(emptyPlanNames, fmt.Sprintf("#%d(%s)", p.ID, p.Name))
		}
	}
	if len(emptyPlanNames) > 0 {
		addIssue("error", "empty_group_packages", "上架套餐未绑定任何含启用节点的分组",
			"以下套餐用户开通后订阅为空："+strings.Join(emptyPlanNames, "、"),
			"调用 upsert_package 绑定含可用节点的 group_ids，或向对应分组添加节点。")
	}

	// 6. Sync failures
	syncStatuses := map[string]any{}
	if a.sbctl != nil {
		for sid, st := range a.sbctl.SyncStatuses() {
			syncStatuses[strconv.FormatInt(sid, 10)] = st
			if st.Error != "" {
				addIssue("error", "sync_failed", fmt.Sprintf("机器 %d 配置下发失败", sid),
					st.Error,
					"先调用 check_singbox_config 检查配置，修复后调用 resync_singbox 重推。")
			}
		}
	}

	unreadAlerts, _ := a.st.ListAlerts(true)
	if len(unreadAlerts) > 10 {
		unreadAlerts = unreadAlerts[:10]
	}

	return J{
		"checked_at":           now,
		"healthy":              len(issues) == 0,
		"issue_count":          len(issues),
		"issues":               issues,
		"effective_node_host":  effectiveHost,
		"effective_site_base":  base,
		"summary": J{
			"servers":          len(servers),
			"inbounds":         len(inbounds),
			"enabled_inbounds": enabledInboundCount,
			"nodes":            len(nodes),
			"groups":           len(groups),
			"packages":         len(pkgs),
			"unread_alerts":    len(unreadAlerts),
		},
		"sync_statuses": syncStatuses,
		"unread_alerts": unreadAlerts,
	}, nil
}

func (a *API) buildTopologySnapshot() (J, error) {
	servers, err := a.st.ListServers()
	if err != nil {
		return nil, err
	}
	inbounds, err := a.st.ListSbInbounds()
	if err != nil {
		return nil, err
	}
	egresses, _ := a.st.ListSbEgresses()
	nodes, err := a.st.ListNodes()
	if err != nil {
		return nil, err
	}
	groups, err := a.st.ListGroups()
	if err != nil {
		return nil, err
	}
	pkgs, err := a.st.ListPackages()
	if err != nil {
		return nil, err
	}
	machines := []J{{
		"id":      store.LocalNodeID,
		"name":    store.LocalNodeName,
		"host":    a.nodeHost(),
		"enabled": true,
		"local":   true,
	}}
	for _, sv := range servers {
		machines = append(machines, J{
			"id":      sv.ID,
			"name":    sv.Name,
			"host":    sv.ClientHost(),
			"host_v6": sv.ClientHostV6(),
			"enabled": sv.Enabled,
			"local":   false,
		})
	}
	pkgLinks := make([]J, 0, len(pkgs))
	for _, p := range pkgs {
		gids, _ := a.st.PlanGroupIDs(p.ID)
		pkgLinks = append(pkgLinks, J{
			"id":        p.ID,
			"name":      p.Name,
			"type":      p.Type,
			"enabled":   p.Enabled,
			"group_ids": gids,
		})
	}
	egList := make([]J, 0, len(egresses))
	for _, eg := range egresses {
		egList = append(egList, J{"id": eg.ID, "name": eg.Name, "type": eg.Type, "host": eg.Host, "port": eg.Port})
	}
	return J{
		"machines": machines,
		"inbounds": inbounds,
		"egresses": egList,
		"nodes":    nodes,
		"groups":   groups,
		"packages": pkgLinks,
	}, nil
}

func (a *API) runSingboxConfigCheck(ctx context.Context, serverID int64) (J, error) {
	cfg, err := a.buildPreviewConfig(serverID)
	if err != nil {
		return J{"ok": false, "stage": "generate", "output": "生成配置失败：" + err.Error()}, nil
	}
	var doc struct {
		Inbounds []struct {
			Tag        string `json:"tag"`
			ListenPort int    `json:"listen_port"`
		} `json:"inbounds"`
		Outbounds []json.RawMessage `json:"outbounds"`
	}
	if err := json.Unmarshal(cfg, &doc); err != nil {
		return J{"ok": false, "stage": "json", "output": "生成的配置不是合法 JSON：" + err.Error()}, nil
	}
	var warnings []string
	if len(doc.Inbounds) == 0 {
		warnings = append(warnings, "该机器没有任何已启用入站，inbounds 为空。")
	}
	res := J{"server_id": serverID, "inbounds": len(doc.Inbounds), "outbounds": len(doc.Outbounds), "warnings": warnings}
	bin := sbproc.FindSingBoxBin()
	if bin == "" {
		res["ok"] = len(warnings) == 0
		res["stage"] = "no-binary"
		res["output"] = "本机未找到 sing-box 可执行文件，仅完成 JSON 结构校验。"
		return res, nil
	}
	tmp, err := os.CreateTemp("", "qz-mcp-check-*.json")
	if err != nil {
		return nil, err
	}
	path := tmp.Name()
	defer os.Remove(path)
	if _, err := tmp.Write(cfg); err != nil {
		tmp.Close()
		return nil, err
	}
	tmp.Close()
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, cerr := exec.CommandContext(cctx, bin, "check", "-c", path).CombinedOutput()
	res["stage"] = "check"
	if cerr != nil {
		res["ok"] = false
		res["output"] = strings.TrimSpace(string(out) + "\n" + cerr.Error())
		return res, nil
	}
	res["ok"] = len(warnings) == 0
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		msg = "sing-box check 通过，配置合法。"
	}
	res["output"] = msg
	return res, nil
}
