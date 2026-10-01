package api

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"qingzhou/internal/intervalcfg"
	"qingzhou/internal/store"
)

type onboardingStep struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Path     string `json:"path"`
	Done     bool   `json:"done"`
	Optional bool   `json:"optional"`
	Severity string `json:"severity"`
}

// Read-only guide: use persisted observations, never open SSH connections or
// install/rebuild anything from an overview refresh. Return no stored secrets.
func (a *API) handleAdminOnboarding(w http.ResponseWriter, r *http.Request) {
	servers, err := a.st.ListServers()
	if err != nil {
		fail(w, 500, "读取部署状态失败")
		return
	}
	observed, err := a.st.NodeSingboxAll()
	if err != nil {
		fail(w, 500, "读取节点检测状态失败")
		return
	}
	inbounds, err := a.st.ListSbInbounds()
	if err != nil {
		fail(w, 500, "读取入站状态失败")
		return
	}
	now := time.Now().Unix()
	active := map[int64]string{}
	for _, server := range servers {
		if server.Enabled {
			active[server.ID] = server.Name
		}
	}
	localUsed := false
	for _, inbound := range inbounds {
		if inbound.Enabled && inbound.ServerID == store.LocalNodeID {
			localUsed = true
		}
	}
	if localUsed || (len(active) == 0 && observed[store.LocalNodeID] != nil) {
		active[store.LocalNodeID] = store.LocalNodeName
	}
	ids := make([]int64, 0, len(active))
	for id := range active {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	freshFor := int64((2 * intervalcfg.Reconcile(a.st)) / time.Second)
	if freshFor < 15*60 {
		freshFor = 15 * 60
	}
	connected := false
	statsReady := len(ids) > 0
	missing := []string{}
	unknown := []string{}
	for _, id := range ids {
		n := observed[id]
		if n == nil || n.Error != "" || n.Version == "" || n.CheckedAt < now-freshFor || n.CheckedAt > now+60 {
			statsReady = false
			unknown = append(unknown, active[id])
			continue
		}
		connected = true
		if !n.HasV2RayAPI {
			statsReady = false
			missing = append(missing, active[id])
		}
	}
	enabledInbound := false
	for _, ib := range inbounds {
		if ib.Enabled {
			if _, ok := active[ib.ServerID]; ok {
				enabledInbound = true
			}
		}
	}
	var usablePlan bool
	err = a.st.DB().QueryRow(`SELECT EXISTS(SELECT 1 FROM packages p JOIN plan_groups pg ON pg.package_id=p.id
 JOIN node_group_members gm ON gm.group_id=pg.group_id JOIN nodes n ON n.id=gm.node_id
 WHERE p.enabled=1 AND p.type='plan' AND n.enabled=1)`).Scan(&usablePlan)
	if err != nil {
		fail(w, 500, "读取套餐分组状态失败")
		return
	}
	base := a.siteBase()
	parsed, _ := url.Parse(base)
	https := parsed != nil && parsed.Scheme == "https" && parsed.Host != ""
	panelDetail := "请配置面板访问地址与 HTTPS；若监听 127.0.0.1，需配置反向代理。"
	if https {
		panelDetail = "已配置 HTTPS 访问地址；清单不代替外网连通性和证书验证。"
	}
	serverDetail := "添加服务器或在面板本机安装 sing-box，再到服务器页重新检测。"
	if connected {
		serverDetail = "至少一台启用节点近期完成 sing-box 检测。"
	}
	statsDetail := "请先添加节点并检测 sing-box 能力。"
	statsSeverity := "warning"
	if statsReady {
		statsDetail = "所有启用节点最近的检测都包含 with_v2ray_api。"
	}
	if len(missing) > 0 {
		statsSeverity = "error"
		statsDetail = "缺少 with_v2ray_api：" + strings.Join(missing, "、") + "。流量无法计量，配额无法执行，请到服务器页安装面板构建。"
	}
	if len(unknown) > 0 {
		statsDetail += " 待检测或结果已过期：" + strings.Join(unknown, "、") + "。"
	}
	var reg, announcement bool
	if err = a.st.DB().QueryRow(`SELECT EXISTS(SELECT 1 FROM reg_codes WHERE enabled=1 AND (max_uses=0 OR used<max_uses))`).Scan(&reg); err != nil {
		fail(w, 500, "读取注册码状态失败")
		return
	}
	if err = a.st.DB().QueryRow(`SELECT EXISTS(SELECT 1 FROM announcements WHERE enabled=1)`).Scan(&announcement); err != nil {
		fail(w, 500, "读取公告状态失败")
		return
	}
	steps := []onboardingStep{
		{ID: "panel", Title: "面板访问地址与 HTTPS", Detail: panelDetail, Path: "/admin/settings", Done: https, Severity: "warning"},
		{ID: "server", Title: "至少一台节点已添加且近期可访问", Detail: serverDetail, Path: "/admin/servers", Done: connected, Severity: "warning"},
		{ID: "metering", Title: "sing-box 流量统计插件", Detail: statsDetail, Path: "/admin/servers", Done: statsReady, Severity: statsSeverity},
		{ID: "inbound", Title: "启用节点上至少一个入站已启用", Detail: "到 sing-box 管理创建入站并配置 TLS / Reality。", Path: "/admin/singbox", Done: enabledInbound, Severity: "warning"},
		{ID: "plan", Title: "至少一个上架套餐已绑定可用节点分组", Detail: "套餐需关联含启用节点的分组，避免用户开通后订阅没有节点。", Path: "/admin/packages", Done: usablePlan, Severity: "warning"},
		{ID: "optional", Title: "可选：注册码、公告与邮件服务", Detail: fmt.Sprintf("注册码：%s；公告：%s；SMTP：%s。按实际需要配置，不影响清单完成。", onboardingConfigured(reg), onboardingConfigured(announcement), onboardingConfigured(a.mailerConfigured())), Path: "/admin/settings", Done: reg && announcement && a.mailerConfigured(), Optional: true, Severity: "warning"},
	}
	complete := true
	for i := range steps {
		if steps[i].Done {
			steps[i].Severity = "success"
		} else if !steps[i].Optional {
			complete = false
		}
	}
	ok(w, J{"complete": complete, "steps": steps, "checked_at": now})
}
func onboardingConfigured(ok bool) string {
	if ok {
		return "已配置"
	}
	return "未配置"
}
