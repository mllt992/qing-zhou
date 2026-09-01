package api

import (
	"fmt"
	"regexp"
	"strings"

	"qingzhou/internal/store"
	"qingzhou/internal/telegram"
)

// Telegram message templates are stored as settings tg_tpl_<key>. Empty means
// "use the built-in default", same contract as the Clash/sing-box subscription
// templates: an admin who never touched them keeps picking up layout fixes on
// upgrade; an admin who saved an override keeps that override until they
// restore the default.
//
// Placeholders are {{name}}. Values that come from user or package data are
// HTML-escaped before substitution; a few vars (bar, items, footer, panel_link,
// help) are pre-rendered fragments and must not be escaped again.

type tgTplVar struct {
	Key  string
	Desc string
}

type tgTplSpec struct {
	Key  string
	Name string // shown in the settings UI
	Vars []tgTplVar
}

var (
	tvSite     = tgTplVar{"site", "站点名称"}
	tvUser     = tgTplVar{"username", "用户名"}
	tvPanel    = tgTplVar{"panel", "面板访问地址（纯文本）"}
	tvPLink    = tgTplVar{"panel_link", "「打开面板」可点击链接"}
	tvBar      = tgTplVar{"bar", "用量进度条，如 ▓▓▓▓▓▓▓▓░░  82%"}
	tvUsed     = tgTplVar{"used", "已用流量"}
	tvTotal    = tgTplVar{"total", "总流量额度；没有额度时为「—」"}
	tvRemain   = tgTplVar{"remaining", "剩余流量"}
	tvRemainPc = tgTplVar{"remain_pct", "剩余百分比数字，不含 %"}
	tvUnmeter  = tgTplVar{"unmetered", "兼容字段，当前始终为空"}
	tvSummary  = tgTplVar{"summary", "流量一行摘要"}
	tvItems    = tgTplVar{"items", "套餐列表，由「套餐条目」模板拼出"}
	tvFooter   = tgTplVar{"footer", "底部操作链接（打开面板续费 / 查看）"}
)

func tgCommon(extra ...tgTplVar) []tgTplVar {
	out := []tgTplVar{tvSite, tvUser, tvPanel, tvPLink}
	return append(out, extra...)
}

var tgTplSpecs = []tgTplSpec{
	{Key: "sub", Name: "订阅 /sub", Vars: tgCommon(
		tgTplVar{"url", "通用订阅地址"},
		tgTplVar{"url_clash", "Clash 格式订阅"},
		tgTplVar{"url_singbox", "sing-box 格式订阅"},
		tgTplVar{"url_surge", "Surge 格式订阅"},
		tgTplVar{"url_base64", "base64 节点列表"},
	)},
	{Key: "traffic", Name: "流量 /traffic", Vars: tgCommon(tvBar, tvUsed, tvTotal, tvRemain, tvRemainPc, tvUnmeter, tvSummary)},
	{Key: "plans", Name: "套餐 /plan", Vars: tgCommon(tvItems)},
	{Key: "plan_item", Name: "套餐条目", Vars: []tgTplVar{
		{"name", "套餐名"},
		{"status", "状态：生效中 / 排队中 / 已过期 / 已用尽"},
		{"duration", "时长，如「 · 30 天」；没有则为空"},
		{"traffic", "该份流量摘要（已用 / 总量 / 剩余）"},
		{"expiry", "到期时间；不过期则为「不过期」；排队中为预计生效时间"},
		{"used", "该份已用流量"},
		{"total", "该份总量"},
		{"remaining", "该份剩余"},
	}},
	{Key: "status", Name: "总览 /status", Vars: tgCommon(tvBar, tvUsed, tvTotal, tvRemain, tvRemainPc, tvUnmeter, tvSummary, tvItems)},
	{Key: "help", Name: "帮助 /help", Vars: tgCommon()},
	{Key: "bound", Name: "绑定成功", Vars: tgCommon(
		tgTplVar{"help", "帮助全文，按「帮助」模板渲染"},
	)},
	{Key: "expiry_soon", Name: "即将到期", Vars: tgCommon(
		tgTplVar{"plan", "即将到期的套餐名"},
		tgTplVar{"expiry", "到期时间"},
		tgTplVar{"left", "还剩多久，如「3 天」或「12 小时」"},
		tvFooter,
	)},
	{Key: "expired", Name: "已到期", Vars: tgCommon(
		tgTplVar{"plan", "已到期的套餐名"},
		tgTplVar{"expiry", "到期时间"},
		tvFooter,
	)},
	{Key: "traffic_low", Name: "流量不足", Vars: tgCommon(tvBar, tvUsed, tvTotal, tvRemain, tvRemainPc, tvFooter)},
	{Key: "traffic_out", Name: "流量用尽", Vars: tgCommon(tvBar, tvUsed, tvTotal, tvFooter)},
}

var defaultTGTemplates = map[string]string{
	"sub": `🔗 <b>订阅地址</b>

通用
<code>{{url}}</code>

Clash
<code>{{url_clash}}</code>

sing-box
<code>{{url_singbox}}</code>

点按上方地址即可复制。请当作密码保管，不要转发给他人。`,

	"traffic": `📊 <b>流量用量</b>

{{bar}}

已用　{{used}}
总量　{{total}}
剩余　{{remaining}}{{unmetered}}`,

	"plans": `📦 <b>我的套餐</b>

{{items}}`,

	"plan_item": `<b>{{name}}</b>　{{status}}{{duration}}
流量　{{traffic}}
到期　{{expiry}}`,

	"status": `👤 <b>{{username}}</b>  ·  {{site}}

📊 <b>流量</b>
{{bar}}
{{used}} / {{total}}  ·  剩余 {{remaining}}

📦 <b>套餐</b>
{{items}}`,

	"help": `<b>{{site}}</b> 助手

/sub　订阅地址
/plan　我的套餐
/traffic　流量用量
/status　账户总览
/unbind　解除绑定

订阅地址请当作密码保管，不要转发。`,

	"bound": `已绑定 <b>{{site}}</b> 账号 <b>{{username}}</b>。

{{help}}`,

	"expiry_soon": `⏰ <b>套餐即将到期</b>

套餐　<b>{{plan}}</b>
到期　{{expiry}}
剩余　{{left}}

{{footer}}`,

	"expired": `⏰ <b>套餐已到期</b>

套餐　<b>{{plan}}</b>
到期　{{expiry}}

{{footer}}`,

	"traffic_low": `📉 <b>流量不足</b>

{{bar}}
已用　{{used}} / {{total}}
剩余　{{remaining}}（{{remain_pct}}%）

{{footer}}`,

	"traffic_out": `📉 <b>流量已用尽</b>

{{bar}}
已用　{{used}} / {{total}}

{{footer}}`,
}

func tgTplSettingKey(key string) string { return "tg_tpl_" + key }

func (a *API) tgTpl(key string) string {
	if a != nil && a.st != nil {
		if v, _ := a.st.GetSetting(tgTplSettingKey(key)); strings.TrimSpace(v) != "" {
			return v
		}
	}
	return defaultTGTemplates[key]
}

var tplVarRe = regexp.MustCompile(`\{\{[a-z0-9_]+\}\}`)

func applyTpl(tpl string, vars map[string]string) string {
	if tpl == "" {
		return ""
	}
	return tplVarRe.ReplaceAllStringFunc(tpl, func(m string) string {
		k := m[2 : len(m)-2]
		if v, ok := vars[k]; ok {
			return v
		}
		return m
	})
}

func (a *API) tgBaseVars(username string) map[string]string {
	site := a.siteName()
	panel := a.siteBase()
	m := map[string]string{
		"site":       telegram.Escape(site),
		"username":   telegram.Escape(username),
		"panel":      telegram.Escape(panel),
		"panel_link": "",
		"footer":     "",
	}
	if panel != "" {
		m["panel_link"] = `<a href="` + telegram.Escape(panel) + `">打开面板</a>`
	}
	return m
}

func tgFooter(panel, site, action string) string {
	if panel == "" {
		return "请打开「" + telegram.Escape(site) + "」" + telegram.Escape(action)
	}
	return `<a href="` + telegram.Escape(panel) + `">` + telegram.Escape(action) + `</a>`
}

// tgBar is a 10-cell usage bar. Empty quota has no meaningful ratio, so it gets
// a short label instead of a fake 0% or 100%. The last argument is retained for
// template/test source compatibility; unlimited traffic is no longer supported.
func tgBar(used, total int64, _ bool) string {
	if total <= 0 {
		return "—　暂无额度"
	}
	pct := used * 100 / total
	if pct > 100 {
		pct = 100
	}
	if pct < 0 {
		pct = 0
	}
	const cells = 10
	filled := int((pct*int64(cells) + 50) / 100)
	if filled > cells {
		filled = cells
	}
	return strings.Repeat("▓", filled) + strings.Repeat("░", cells-filled) + fmt.Sprintf("  %d%%", pct)
}

func (a *API) trafficVars(username string, buckets []*store.Bucket) map[string]string {
	m := a.tgBaseVars(username)
	tr := dashboardTraffic(buckets)
	m["used"] = fmtBytes(tr.Used)
	m["unmetered"] = ""
	switch {
	case tr.Total == 0:
		m["used"] = fmtBytes(0)
		m["total"] = "—"
		m["remaining"] = "—"
		m["remain_pct"] = ""
		m["summary"] = "暂无可用流量额度"
		m["bar"] = tgBar(0, 0, false)
	default:
		m["total"] = fmtBytes(tr.Total)
		m["remaining"] = fmtBytes(tr.Remaining)
		m["remain_pct"] = fmt.Sprintf("%d", tr.Remaining*100/tr.Total)
		m["summary"] = fmt.Sprintf("已用 %s / %s，剩余 %s", fmtBytes(tr.Used), fmtBytes(tr.Total), fmtBytes(tr.Remaining))
		m["bar"] = tgBar(tr.Used, tr.Total, false)
	}
	return m
}

func (a *API) renderPlanItems(userID int64) string {
	buckets, _ := a.st.ListBuckets(userID)
	names, _ := a.st.PackageNames()
	views := buildPlanViews(buckets, names)
	if len(views) == 0 {
		return "当前没有套餐。"
	}
	itemTpl := a.tgTpl("plan_item")
	parts := make([]string, 0, len(views))
	for _, p := range views {
		parts = append(parts, applyTpl(itemTpl, planItemVars(p)))
	}
	return strings.Join(parts, "\n\n")
}

func planItemVars(p planView) map[string]string {
	dur := ""
	if p.DurationDays > 0 {
		dur = fmt.Sprintf(" · %d 天", p.DurationDays)
	}
	expiry := "不过期"
	switch {
	case p.Status == "queued" && p.ActivateBy > 0:
		expiry = "预计生效 " + fmtUnix(p.ActivateBy)
	case p.Status == "queued":
		expiry = "排队中"
	case p.ExpiryAt > 0:
		expiry = fmtUnix(p.ExpiryAt)
	}
	total, remaining := fmtBytes(p.TrafficLimit), fmtBytes(p.Remaining)
	return map[string]string{
		"name":      telegram.Escape(p.Name),
		"status":    planStatusLabel(p.Status),
		"duration":  dur,
		"traffic":   formatPlanTraffic(p),
		"expiry":    expiry,
		"used":      fmtBytes(p.Used),
		"total":     total,
		"remaining": remaining,
	}
}

func (a *API) renderTGHelp(username string) string {
	base := applyTpl(a.tgTpl("help"), a.tgBaseVars(username))
	if custom := a.telegramCustomHelp(); custom != "" {
		return strings.TrimSpace(base) + "\n\n" + custom
	}
	return base
}

func (a *API) renderTGBound(username string) string {
	m := a.tgBaseVars(username)
	m["help"] = a.renderTGHelp(username)
	return applyTpl(a.tgTpl("bound"), m)
}

func (a *API) renderTGSub(username, url string) string {
	m := a.tgBaseVars(username)
	esc := telegram.Escape(url)
	m["url"] = esc
	m["url_clash"] = esc + "?format=clash"
	m["url_singbox"] = esc + "?format=singbox"
	m["url_surge"] = esc + "?format=surge"
	m["url_base64"] = esc + "?format=base64"
	return applyTpl(a.tgTpl("sub"), m)
}

func (a *API) renderTGTraffic(userID int64, username string) string {
	buckets, _ := a.st.ListBuckets(userID)
	return applyTpl(a.tgTpl("traffic"), a.trafficVars(username, buckets))
}

func (a *API) renderTGPlans(userID int64, username string) string {
	m := a.tgBaseVars(username)
	m["items"] = a.renderPlanItems(userID)
	return applyTpl(a.tgTpl("plans"), m)
}

func (a *API) renderTGStatus(userID int64, username string) string {
	buckets, _ := a.st.ListBuckets(userID)
	m := a.trafficVars(username, buckets)
	m["items"] = a.renderPlanItems(userID)
	return applyTpl(a.tgTpl("status"), m)
}

func defaultTGTemplateViews() []J {
	out := make([]J, 0, len(tgTplSpecs))
	for _, s := range tgTplSpecs {
		vars := make([]J, 0, len(s.Vars))
		for _, v := range s.Vars {
			vars = append(vars, J{"key": v.Key, "desc": v.Desc})
		}
		out = append(out, J{
			"key":  s.Key,
			"name": s.Name,
			"vars": vars,
			"body": defaultTGTemplates[s.Key],
		})
	}
	return out
}

func normalizeTGTplSetting(key, value string) string {
	if !strings.HasPrefix(key, "tg_tpl_") {
		return value
	}
	def := defaultTGTemplates[strings.TrimPrefix(key, "tg_tpl_")]
	if def != "" && value == def {
		return ""
	}
	return value
}
