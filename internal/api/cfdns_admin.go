package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"qingzhou/internal/cfdns"
	"qingzhou/internal/singbox"
)

func (a *API) newCFDNSClient(token string) *cfdns.Client {
	if a.cfClientFn != nil {
		return a.cfClientFn(token)
	}
	return &cfdns.Client{Token: token, HTTP: a.upstreamClient}
}

func normalizeDomainField(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".")
	return strings.ToLower(strings.TrimSpace(s))
}

func isValidDNSName(h string) bool {
	if h == "" || len(h) > 253 || strings.ContainsAny(h, " \t\r\n:/@?*") {
		return false
	}
	if net.ParseIP(strings.Trim(h, "[]")) != nil {
		return false
	}
	parts := strings.Split(h, ".")
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if len(p) == 0 || len(p) > 63 || p[0] == '-' || p[len(p)-1] == '-' {
			return false
		}
		for i := 0; i < len(p); i++ {
			c := p[i]
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
				return false
			}
		}
	}
	return true
}

func serverManagedTag(serverID int64) string {
	return fmt.Sprintf("managed-by:qingzhou;server:%d", serverID)
}

// GET /api/admin/cf/zones — lists active Cloudflare zones accessible to the
// saved cf_api_token, so the server subdomain modal can offer a root-domain
// dropdown without asking for the token again.
func (a *API) handleAdminCFZones(w http.ResponseWriter, r *http.Request) {
	if !a.requireJWTAdmin(w, r) {
		return
	}
	tok, _ := a.st.GetSetting("cf_api_token")
	tok = strings.TrimSpace(tok)
	if tok == "" {
		ok(w, J{"configured": false, "zones": []cfdns.Zone{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	zones, err := a.newCFDNSClient(tok).ListZones(ctx)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	ok(w, J{"configured": true, "zones": zones})
}

// POST /api/admin/settings/test-cf-token — verifies a Cloudflare API Token
// (from the request body, or the saved setting when empty / "***") and returns
// the accessible zones.
func (a *API) handleTestCFToken(w http.ResponseWriter, r *http.Request) {
	if !a.requireJWTAdmin(w, r) {
		return
	}
	var req struct {
		Token string `json:"cf_api_token"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	tok := strings.TrimSpace(req.Token)
	if tok == "" || tok == "***" {
		tok, _ = a.st.GetSetting("cf_api_token")
		tok = strings.TrimSpace(tok)
	}
	if tok == "" {
		fail(w, http.StatusBadRequest, "请先填写 Cloudflare API Token")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	zones, err := a.newCFDNSClient(tok).ListZones(ctx)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	ok(w, J{"zones": zones, "count": len(zones)})
}

type serverDNSReq struct {
	Domain    string `json:"domain"`
	DomainV6  string `json:"domain_v6"`
	SyncV4    bool   `json:"sync_v4"`
	SyncV6    bool   `json:"sync_v6"`
	Overwrite bool   `json:"overwrite"`
	Cleanup   bool   `json:"cleanup_old"`
}

// POST /api/admin/servers/{id}/dns — creates/updates DNS-only (grey cloud)
// A/AAAA records in Cloudflare for a server using the saved cf_api_token and
// persists domain/domain_v6 on the server row (SSH stays on server.host).
func (a *API) handleAdminServerCFDNS(w http.ResponseWriter, r *http.Request) {
	if !a.requireJWTAdmin(w, r) {
		return
	}
	id := int64(atoi(chi.URLParam(r, "id")))
	sv, err := a.st.GetServer(id)
	if err != nil || sv == nil {
		fail(w, http.StatusNotFound, "服务器不存在")
		return
	}
	var req serverDNSReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	domain := normalizeDomainField(req.Domain)
	domainV6 := normalizeDomainField(req.DomainV6)
	if domain != "" && !isValidDNSName(domain) {
		fail(w, http.StatusBadRequest, "主域名格式无效，请填写完整子域名（如 taxi.example.com）")
		return
	}
	if domainV6 != "" && !isValidDNSName(domainV6) {
		fail(w, http.StatusBadRequest, "IPv6 子域名格式无效，请填写完整子域名（如 taxi-v6.example.com）")
		return
	}
	if !req.SyncV4 && !req.SyncV6 {
		// Allow saving custom domains without touching Cloudflare when neither sync toggle is on.
		if err := a.st.UpdateServerDomains(sv.ID, domain, domainV6); err != nil {
			fail(w, http.StatusInternalServerError, "保存域名失败")
			return
		}
		a.sbRebuildLog()
		saved, _ := a.st.GetServer(sv.ID)
		maskServerSecrets(saved)
		ok(w, J{
			"server":             saved,
			"results":            []any{},
			"matching_certs":     a.matchingCertsForDomains(domain, domainV6),
			"suggested_wildcard": suggestedWildcardFor(domain, domainV6),
		})
		return
	}

	tok, _ := a.st.GetSetting("cf_api_token")
	tok = strings.TrimSpace(tok)
	if tok == "" {
		fail(w, http.StatusBadRequest, "请先在「系统设置 → 证书 / ACME」填写 Cloudflare API Token")
		return
	}

	v4IP := net.ParseIP(strings.Trim(strings.TrimSpace(sv.Host), "[]"))
	if req.SyncV4 {
		if domain == "" {
			fail(w, http.StatusBadRequest, "请填写要解析的子域名")
			return
		}
		if v4IP == nil || v4IP.To4() == nil {
			fail(w, http.StatusBadRequest, fmt.Sprintf("服务器主机 %q 不是 IPv4 地址，无法自动写入 A 记录", sv.Host))
			return
		}
	}
	v6IP := net.ParseIP(strings.Trim(strings.TrimSpace(sv.HostV6), "[]"))
	if req.SyncV6 {
		targetV6Name := domainV6
		if targetV6Name == "" {
			targetV6Name = domain
		}
		if targetV6Name == "" {
			fail(w, http.StatusBadRequest, "请填写要写入 AAAA 记录的子域名")
			return
		}
		if v6IP == nil || v6IP.To4() != nil {
			fail(w, http.StatusBadRequest, fmt.Sprintf("服务器 IPv6 地址 %q 无效，无法自动写入 AAAA 记录", sv.HostV6))
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	client := a.newCFDNSClient(tok)
	tag := serverManagedTag(sv.ID)
	zoneCache := map[string]*cfdns.Zone{}
	resolveZone := func(name string) (*cfdns.Zone, error) {
		if z, ok := zoneCache[name]; ok {
			return z, nil
		}
		z, err := client.FindZoneForName(ctx, name)
		if err != nil {
			return nil, err
		}
		zoneCache[name] = z
		return z, nil
	}

	results := make([]J, 0, 2)
	if req.SyncV4 {
		z, err := resolveZone(domain)
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		res, err := client.UpsertRecord(ctx, cfdns.UpsertOptions{
			ZoneID:     z.ID,
			Type:       "A",
			Name:       domain,
			Content:    v4IP.To4().String(),
			ManagedTag: tag,
			Overwrite:  req.Overwrite,
		})
		if err != nil {
			var ce *cfdns.ConflictError
			if errors.As(err, &ce) {
				writeJSON(w, http.StatusConflict, J{"code": http.StatusConflict, "msg": ce.Error(), "data": J{"conflict": true, "name": ce.Name, "type": ce.Type, "existing": ce.Existing}})
				return
			}
			fail(w, http.StatusBadGateway, err.Error())
			return
		}
		results = append(results, J{"type": "A", "name": domain, "content": v4IP.To4().String(), "action": res.Action, "zone": z.Name})
	}
	if req.SyncV6 {
		targetV6Name := domainV6
		if targetV6Name == "" {
			targetV6Name = domain
		}
		z, err := resolveZone(targetV6Name)
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		res, err := client.UpsertRecord(ctx, cfdns.UpsertOptions{
			ZoneID:     z.ID,
			Type:       "AAAA",
			Name:       targetV6Name,
			Content:    v6IP.String(),
			ManagedTag: tag,
			Overwrite:  req.Overwrite,
		})
		if err != nil {
			var ce *cfdns.ConflictError
			if errors.As(err, &ce) {
				writeJSON(w, http.StatusConflict, J{"code": http.StatusConflict, "msg": ce.Error(), "data": J{"conflict": true, "name": ce.Name, "type": ce.Type, "existing": ce.Existing}})
				return
			}
			fail(w, http.StatusBadGateway, err.Error())
			return
		}
		results = append(results, J{"type": "AAAA", "name": targetV6Name, "content": v6IP.String(), "action": res.Action, "zone": z.Name})
	}

	var warnings []string
	if req.Cleanup {
		if old := normalizeDomainField(sv.Domain); old != "" && old != domain {
			if z, err := resolveZone(old); err == nil {
				if _, err := client.DeleteManagedRecords(ctx, z.ID, "A", old, tag); err != nil {
					warnings = append(warnings, "清理旧 A 记录失败："+err.Error())
				}
			}
		}
		if old6 := normalizeDomainField(sv.DomainV6); old6 != "" && old6 != domainV6 {
			if z, err := resolveZone(old6); err == nil {
				if _, err := client.DeleteManagedRecords(ctx, z.ID, "AAAA", old6, tag); err != nil {
					warnings = append(warnings, "清理旧 AAAA 记录失败："+err.Error())
				}
			}
		}
	}

	if err := a.st.UpdateServerDomains(sv.ID, domain, domainV6); err != nil {
		fail(w, http.StatusInternalServerError, "DNS 已写入，但保存服务器域名失败")
		return
	}
	a.sbRebuildLog()
	saved, _ := a.st.GetServer(sv.ID)
	maskServerSecrets(saved)
	ok(w, J{
		"server":             saved,
		"results":            results,
		"warnings":           warnings,
		"matching_certs":     a.matchingCertsForDomains(domain, domainV6),
		"suggested_wildcard": suggestedWildcardFor(domain, domainV6),
	})
}

// DELETE /api/admin/servers/{id}/dns — clears the server's domain/domain_v6,
// and when ?delete_cf=1 removes Cloudflare records owned by this server.
func (a *API) handleAdminDeleteServerCFDNS(w http.ResponseWriter, r *http.Request) {
	if !a.requireJWTAdmin(w, r) {
		return
	}
	id := int64(atoi(chi.URLParam(r, "id")))
	sv, err := a.st.GetServer(id)
	if err != nil || sv == nil {
		fail(w, http.StatusNotFound, "服务器不存在")
		return
	}
	var deleted int
	var warnings []string
	if r.URL.Query().Get("delete_cf") == "1" {
		tok, _ := a.st.GetSetting("cf_api_token")
		if strings.TrimSpace(tok) != "" {
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			defer cancel()
			client := a.newCFDNSClient(tok)
			tag := serverManagedTag(sv.ID)
			for _, pair := range [][2]string{{"A", sv.Domain}, {"AAAA", sv.DomainV6}, {"AAAA", sv.Domain}} {
				name := normalizeDomainField(pair[1])
				if name == "" {
					continue
				}
				z, err := client.FindZoneForName(ctx, name)
				if err != nil {
					warnings = append(warnings, err.Error())
					continue
				}
				n, err := client.DeleteManagedRecords(ctx, z.ID, pair[0], name, tag)
				if err != nil {
					warnings = append(warnings, err.Error())
				}
				deleted += n
			}
		}
	}
	if err := a.st.UpdateServerDomains(sv.ID, "", ""); err != nil {
		fail(w, http.StatusInternalServerError, "清除域名失败")
		return
	}
	a.sbRebuildLog()
	saved, _ := a.st.GetServer(sv.ID)
	maskServerSecrets(saved)
	ok(w, J{"server": saved, "deleted_records": deleted, "warnings": warnings})
}

func (a *API) matchingCertsForDomains(domains ...string) []J {
	var targets []string
	for _, d := range domains {
		if d = normalizeDomainField(d); d != "" {
			targets = append(targets, d)
		}
	}
	if len(targets) == 0 {
		return []J{}
	}
	certs, err := a.st.ListCerts()
	if err != nil {
		return []J{}
	}
	now := time.Now().Unix()
	var out []J
	for _, c := range certs {
		if c.DecryptFailed || (c.NotAfter > 0 && c.NotAfter <= now) {
			continue
		}
		if singbox.IsSelfSignedCert(c.CertPEM) {
			continue
		}
		allOK := true
		for _, t := range targets {
			if covered, known := singbox.CertCoversHost(c.CertPEM, t); !known || !covered {
				allOK = false
				break
			}
		}
		if allOK {
			out = append(out, J{"id": c.ID, "name": c.Name, "domain": c.Domain, "not_after": c.NotAfter})
		}
	}
	if out == nil {
		return []J{}
	}
	return out
}

func suggestedWildcardFor(domains ...string) string {
	for _, d := range domains {
		d = normalizeDomainField(d)
		parts := strings.Split(d, ".")
		if len(parts) >= 3 {
			return "*." + strings.Join(parts[1:], ".")
		}
	}
	return ""
}
