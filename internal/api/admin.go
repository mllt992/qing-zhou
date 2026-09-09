package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"qingzhou/internal/intervalcfg"
	"qingzhou/internal/store"
	"qingzhou/internal/subconv"
)

// secretSettings are never returned in plaintext and cannot be cleared blindly.
var secretSettings = map[string]bool{
	"oauth2_config": true,
	"jwt_secret":    true,
	"smtp_pass":     true,
	"cf_api_token":  true,
	// A GitHub PAT. It only lifts the unauthenticated rate limit on release
	// lookups, but it is still a bearer credential for the admin's account —
	// it must not come back out of the settings API the way a hostname does.
	"update_github_token": true,
	"telegram_bot_token":  true,
}

// clearableSecrets are secretSettings that an empty submission *clears* rather
// than leaves alone.
//
// The blanket rule below treats "" as "left blank, keep the current secret",
// which protects a required credential from being wiped by a half-filled form.
// That is wrong for an optional one: the GitHub token is a pure opt-in (without
// it release lookups just run anonymously), so an admin who pastes a bad token
// would otherwise be stuck — every update check fails 401 and there is no way
// back to the working anonymous path short of editing the DB. The form always
// loads "***" for a secret that is set, so "" here can only be a deliberate
// select-all-delete, never an omission.
var clearableSecrets = map[string]bool{
	"update_github_token": true,
	// Optional: emptying the token is how the admin turns the bot off.
	"telegram_bot_token": true,
}

// immutableSettings cannot be written through the settings API at all.
//
// update_repo names the GitHub repo the self-updater installs from. Left
// writable, an admin session (a stolen token, or XSS) could repoint it at an
// attacker's repo: GitHub publishes a correct sha256 for whatever asset is
// uploaded there, so every integrity check passes and the downloaded binary
// replaces the running one and is exec'd — persistent code execution as the
// panel's uid, which on a typical deployment is root. It stays overridable via
// QZ_UPDATE_REPO, which requires host access the attacker doesn't have.
var immutableSettings = map[string]bool{
	"oauth2_config": true, // validated and saved atomically by the dedicated OAuth2 endpoint
	"jwt_secret":    true, // never rotate the signing key through the API
	"update_repo":   true,
}

// settingEnv maps a setting key to the env var that overrides it (env wins in
// buildMailer). Used to surface the *effective* config in the panel.
var settingEnv = map[string]string{
	"public_base":    "QZ_PUBLIC_BASE",
	"smtp_host":      "QZ_SMTP_HOST",
	"smtp_port":      "QZ_SMTP_PORT",
	"smtp_user":      "QZ_SMTP_USER",
	"smtp_pass":      "QZ_SMTP_PASS",
	"smtp_from":      "QZ_SMTP_FROM",
	"smtp_from_name": "QZ_SMTP_FROM_NAME",
	"smtp_security":  "QZ_SMTP_SECURITY",
	// Same precedence the updater itself applies (see New in router.go): env
	// wins over the DB value, so the panel has to show the env one as effective
	// or a host-configured token reads as "not set".
	"update_github_token":   "QZ_UPDATE_GITHUB_TOKEN",
	"telegram_bot_token":    "QZ_TELEGRAM_BOT_TOKEN",
	"telegram_bot_username": "QZ_TELEGRAM_BOT_USERNAME",
}

func (a *API) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	all, err := a.st.AllSettings()
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取配置失败")
		return
	}
	// Surface the effective value when an env var overrides the DB setting, so
	// env-configured sing-box / SMTP shows up in the panel instead of looking empty.
	var envKeys []string
	for k, env := range settingEnv {
		if v := os.Getenv(env); v != "" {
			all[k] = v
			envKeys = append(envKeys, k)
		}
	}
	// Runtime intervals need unit conversion rather than the raw duration strings
	// used by their environment variables ("10m", "1h"). Always surface an
	// effective value, including on a pre-upgrade DB before Seed has populated
	// the new keys.
	all[intervalcfg.SettingProbeSeconds] = strconv.FormatInt(int64(intervalcfg.Probe(a.st)/time.Second), 10)
	all[intervalcfg.SettingStatsMinutes] = strconv.FormatInt(int64(intervalcfg.Stats(a.st)/time.Minute), 10)
	all[intervalcfg.SettingReconcileMinutes] = strconv.FormatInt(int64(intervalcfg.Reconcile(a.st)/time.Minute), 10)
	all["_user_online_window_seconds"] = strconv.FormatInt(a.st.UserOnlineWindowSec(), 10)
	for _, k := range intervalcfg.RuntimeSettingKeys {
		if v, locked := intervalcfg.EnvSettingValue(k); locked {
			all[k] = v
			envKeys = append(envKeys, k)
		}
	}
	for k := range all {
		if secretSettings[k] && all[k] != "" {
			all[k] = "***" // mask but show that it is set
		}
	}
	// Surface built-in default templates when the DB value is empty, so the
	// admin can see and edit the effective config rather than a blank box.
	if all["sub_clash_template"] == "" {
		all["sub_clash_template"] = subconv.DefaultClashTemplate
	}
	// Default on: missing key means advertise udp:true (historical behaviour).
	if all["sub_clash_udp"] == "" {
		all["sub_clash_udp"] = "1"
	}
	if all["sub_singbox_template"] == "" {
		all["sub_singbox_template"] = subconv.DefaultSingboxTemplate
	}
	for _, spec := range tgTplSpecs {
		k := tgTplSettingKey(spec.Key)
		if all[k] == "" {
			all[k] = defaultTGTemplates[spec.Key]
		}
	}
	all["_env_keys"] = strings.Join(envKeys, ",") // which fields are env-controlled
	ok(w, all)
}

func (a *API) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var in map[string]string
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if raw, submitted := in[telegramCustomCommandsSetting]; submitted {
		normalized, err := normalizeTelegramCustomCommands(raw)
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		in[telegramCustomCommandsSetting] = normalized
	}
	if err := a.validateHelpDocsSettings(in); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	runtimeChanged, err := a.validateRuntimeIntervals(in)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	// Bot username is derived from getMe. If a real token submission changes,
	// ignore the form's stale username regardless of map iteration order and
	// clear the cache after all writes.
	tokenChanged := false
	if submitted, ok := in["telegram_bot_token"]; ok && submitted != "***" {
		current, _ := a.st.GetSetting("telegram_bot_token")
		tokenChanged = strings.TrimSpace(submitted) != strings.TrimSpace(current)
	}
	for k, v := range in {
		if tokenChanged && k == "telegram_bot_username" {
			continue
		}
		if strings.HasPrefix(k, "_") {
			continue // UI-only fields like _env_keys
		}
		if immutableSettings[k] {
			continue // host-only settings; see immutableSettings
		}
		if _, locked := intervalcfg.EnvSettingValue(k); locked {
			continue // preserve the DB fallback while the host override is active
		}
		if secretSettings[k] && (v == "***" || (v == "" && !clearableSecrets[k])) {
			continue // keep current secret: masked sentinel or left blank
		}
		// If the submitted template equals the built-in default, store empty
		// so "留空用内置" semantics are preserved (future default updates
		// will still take effect).
		if k == "sub_clash_template" && v == subconv.DefaultClashTemplate {
			v = ""
		}
		if k == "sub_singbox_template" && v == subconv.DefaultSingboxTemplate {
			v = ""
		}
		v = normalizeTGTplSetting(k, v)
		if err := a.st.SetSetting(k, v); err != nil {
			fail(w, http.StatusInternalServerError, "保存配置失败")
			return
		}
		// This one is baked into every node's generated route table, so saving it
		// has to reach the nodes — otherwise the switch reads as broken until the
		// next unrelated edit happens to trigger a rebuild.
		//
		// Through sbRebuildLog, not a.sbctl directly: the controller is documented
		// as optional ("Safe to leave unset" on SetSbController) and is nil between
		// api.New and SetSbController, so reaching for it raw is a nil dereference
		// waiting for the one caller that never wires it.
		if k == store.SettingBlockPrivateEgress {
			a.sbRebuildLog()
		}
	}
	if tokenChanged {
		if err := a.st.SetSetting("telegram_bot_username", ""); err != nil {
			fail(w, http.StatusInternalServerError, "保存配置失败")
			return
		}
	}
	if runtimeChanged {
		a.notifyRuntimeIntervalsChanged()
	}
	a.handleGetSettings(w, r)
}

// validateHelpDocsSettings validates the effective pair before any setting is
// written. That keeps a bad external URL from partially saving the rest of a
// large settings form and, critically, prevents javascript: URLs from reaching
// the public config consumed by the browser.
func (a *API) validateHelpDocsSettings(in map[string]string) error {
	mode, modeSubmitted := in["help_docs_mode"]
	if modeSubmitted {
		mode = strings.TrimSpace(mode)
		if mode != "builtin" && mode != "external" {
			return fmt.Errorf("帮助文档模式无效")
		}
		in["help_docs_mode"] = mode
	} else {
		mode, _ = a.st.GetSetting("help_docs_mode")
		if mode == "" {
			mode = "builtin"
		}
	}

	rawURL, urlSubmitted := in["help_docs_url"]
	if urlSubmitted {
		rawURL = strings.TrimSpace(rawURL)
		in["help_docs_url"] = rawURL
	} else {
		rawURL, _ = a.st.GetSetting("help_docs_url")
	}
	if rawURL == "" {
		if mode == "external" {
			return fmt.Errorf("选择外部帮助文档时，请填写外部文档 URL")
		}
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("外部帮助文档 URL 必须是完整的 http:// 或 https:// 地址")
	}
	return nil
}

type runtimeIntervalSpec struct {
	label string
	def   int64
	min   int64
	max   int64
	unit  string
}

var runtimeIntervalSpecs = map[string]runtimeIntervalSpec{
	intervalcfg.SettingProbeSeconds: {
		label: "探针采集间隔",
		def:   intervalcfg.DefaultProbeSeconds, min: intervalcfg.MinProbeSeconds,
		max: intervalcfg.MaxProbeSeconds, unit: "秒",
	},
	intervalcfg.SettingStatsMinutes: {
		label: "流量统计间隔",
		def:   intervalcfg.DefaultStatsMinutes, min: intervalcfg.MinStatsMinutes,
		max: intervalcfg.MaxStatsMinutes, unit: "分钟",
	},
	intervalcfg.SettingReconcileMinutes: {
		label: "完整健康检查间隔",
		def:   intervalcfg.DefaultReconcileMinutes, min: intervalcfg.MinReconcileMinutes,
		max: intervalcfg.MaxReconcileMinutes, unit: "分钟",
	},
}

// validateRuntimeIntervals rejects the entire request before the settings loop
// writes anything. It also canonicalises numbers so values such as whitespace
// cannot produce a UI/runtime disagreement after a successful save.
func (a *API) validateRuntimeIntervals(in map[string]string) (bool, error) {
	touched := false
	changed := false
	values := map[string]int64{}
	for key, spec := range runtimeIntervalSpecs {
		raw, submitted := in[key]
		if submitted {
			touched = true
		}
		env, locked := intervalcfg.EnvSettingValue(key)
		if locked {
			raw = env
		} else if !submitted {
			raw, _ = a.st.GetSetting(key)
		}
		if strings.TrimSpace(raw) == "" {
			raw = strconv.FormatInt(spec.def, 10)
		}
		n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || n < spec.min || n > spec.max {
			return false, fmt.Errorf("%s必须是 %d–%d %s之间的整数", spec.label, spec.min, spec.max, spec.unit)
		}
		values[key] = n
		if submitted {
			in[key] = strconv.FormatInt(n, 10)
			if !locked {
				currentRaw, _ := a.st.GetSetting(key)
				current, currentErr := strconv.ParseInt(strings.TrimSpace(currentRaw), 10, 64)
				if currentErr != nil || current < spec.min || current > spec.max {
					current = spec.def
				}
				changed = changed || current != n
			}
		}
	}
	if !touched {
		return false, nil
	}
	if values[intervalcfg.SettingReconcileMinutes] < values[intervalcfg.SettingStatsMinutes] {
		return false, fmt.Errorf("完整健康检查间隔不能小于流量统计间隔")
	}
	return changed, nil
}

// handleGetDefaultTemplates returns the built-in Clash/sing-box subscription
// templates, so the settings UI can show the actual default (and let the admin
// load it to edit) instead of a blank box. Saving either field back equal to its
// default clears the override — see handlePutSettings — so the built-in stays
// live and future updates take effect.
func (a *API) handleGetDefaultTemplates(w http.ResponseWriter, r *http.Request) {
	ok(w, J{
		"clash":    subconv.DefaultClashTemplate,
		"singbox":  subconv.DefaultSingboxTemplate,
		"telegram": defaultTGTemplateViews(),
	})
}
