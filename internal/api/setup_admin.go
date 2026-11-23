package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"qingzhou/internal/acmesh"
)

// 引导式配置（/admin/setup）用到的几个辅助接口。向导本身只是把现有的服务器 /
// Cloudflare / 证书 / TLS / 入站接口按顺序串起来；这里只补它们缺的三块：
// 环境预检、acme.sh 安装、解析检测。全部仅限 JWT 管理员（API Token 不可用）。

// lookupHostFn is swapped in tests.
var lookupHostFn = func(ctx context.Context, host string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, host)
}

// acmeInstalledFn / acmeInstallFn are swapped in tests (the real ones shell out).
var acmeInstalledFn = func(ctx context.Context) bool { return acmesh.Installed(ctx, acmesh.LocalRunner{}) }
var acmeInstallFn = func(ctx context.Context, email string) error {
	return acmesh.Install(ctx, acmesh.LocalRunner{}, email)
}

// GET /api/admin/setup/preflight — what the wizard needs to decide which steps
// can be skipped: is a Cloudflare token saved, is acme.sh present on the panel
// host, which ACME email is configured. Never returns the token itself.
func (a *API) handleAdminSetupPreflight(w http.ResponseWriter, r *http.Request) {
	if !a.requireJWTAdmin(w, r) {
		return
	}
	tok, _ := a.st.GetSetting("cf_api_token")
	email, _ := a.st.GetSetting("acme_email")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	ok(w, J{
		"cf_configured":  strings.TrimSpace(tok) != "",
		"acme_email":     strings.TrimSpace(email),
		"acme_installed": acmeInstalledFn(ctx),
	})
}

// POST /api/admin/setup/acme/install {email?} — installs the pinned acme.sh on
// the panel host (same code path the certificate page relies on). A supplied
// email is remembered as the ACME account email.
func (a *API) handleAdminSetupAcmeInstall(w http.ResponseWriter, r *http.Request) {
	if !a.requireJWTAdmin(w, r) {
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	email := strings.TrimSpace(req.Email)
	if email != "" {
		if !strings.Contains(email, "@") || strings.ContainsAny(email, " \t\r\n'\"`$;|&<>\\") {
			fail(w, http.StatusBadRequest, "邮箱格式无效")
			return
		}
		if err := a.st.SetSetting("acme_email", email); err != nil {
			fail(w, http.StatusInternalServerError, "保存邮箱失败")
			return
		}
	} else {
		email, _ = a.st.GetSetting("acme_email")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	if err := acmeInstallFn(ctx, email); err != nil {
		fail(w, http.StatusBadGateway, "安装 acme.sh 失败："+err.Error())
		return
	}
	ok(w, J{"acme_installed": acmeInstalledFn(ctx)})
}

// POST /api/admin/setup/dns-check {domain, server_id?} — resolves the domain
// from the panel and compares it to the server's IPv4 / IPv6 so the wizard can
// tell "not propagated yet" from "points somewhere else". IPv6 is advisory:
// a missing AAAA never makes the check fail.
func (a *API) handleAdminSetupDNSCheck(w http.ResponseWriter, r *http.Request) {
	if !a.requireJWTAdmin(w, r) {
		return
	}
	var req struct {
		Domain   string `json:"domain"`
		ServerID int64  `json:"server_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	domain := normalizeDomainField(req.Domain)
	if !isValidDNSName(domain) {
		fail(w, http.StatusBadRequest, "域名格式无效，请填写完整域名（如 taxi.example.com）")
		return
	}
	var wantV4, wantV6 string
	if req.ServerID != 0 {
		sv, err := a.st.GetServer(req.ServerID)
		if err != nil || sv == nil {
			fail(w, http.StatusNotFound, "服务器不存在")
			return
		}
		if ip := net.ParseIP(strings.Trim(strings.TrimSpace(sv.Host), "[]")); ip != nil && ip.To4() != nil {
			wantV4 = ip.String()
		}
		if ip := net.ParseIP(strings.Trim(strings.TrimSpace(sv.HostV6), "[]")); ip != nil && ip.To4() == nil {
			wantV6 = ip.String()
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	addrs, err := lookupHostFn(ctx, domain)
	resolved := []string{}
	var v4, v6 []string
	if err == nil {
		for _, s := range addrs {
			ip := net.ParseIP(s)
			if ip == nil {
				continue
			}
			resolved = append(resolved, ip.String())
			if ip.To4() != nil {
				v4 = append(v4, ip.String())
			} else {
				v6 = append(v6, ip.String())
			}
		}
		sort.Strings(resolved)
	}
	contains := func(list []string, want string) bool {
		for _, s := range list {
			if s == want {
				return true
			}
		}
		return false
	}
	// "ok": the domain resolves and (when we know the server IPv4) includes it.
	okV4 := len(v4) > 0 && (wantV4 == "" || contains(v4, wantV4))
	status := "ok"
	switch {
	case len(resolved) == 0:
		status = "unresolved" // 尚未生效 / 记录不存在
	case !okV4:
		status = "mismatch" // 解析到了别的地址
	}
	ok(w, J{
		"domain":      domain,
		"status":      status,
		"resolved":    resolved,
		"expected_v4": wantV4,
		"expected_v6": wantV6,
		"v6_ok":       wantV6 == "" || contains(v6, wantV6),
	})
}

func (a *API) registerSetupRoutes(ar chi.Router) {
	ar.Get("/api/admin/setup/preflight", a.handleAdminSetupPreflight)
	ar.Post("/api/admin/setup/acme/install", a.handleAdminSetupAcmeInstall)
	ar.Post("/api/admin/setup/dns-check", a.handleAdminSetupDNSCheck)
}
