package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"qingzhou/internal/acmesh"
	"qingzhou/internal/certcheck"
	"qingzhou/internal/singbox"
	"qingzhou/internal/store"
)

// certPublic is the API view of a managed certificate. It NEVER includes the
// PEM bodies — those are returned only by the explicit export endpoint — and it
// derives a coarse status/days-left from not_after for the list UI.
func certPublic(c *store.Cert) map[string]interface{} {
	now := time.Now().Unix()
	daysLeft := int64(-1)
	status := "unknown"
	if c.NotAfter > 0 {
		daysLeft = (c.NotAfter - now) / 86400
		switch {
		case now >= c.NotAfter:
			status = "expired"
		case daysLeft <= 30:
			status = "expiring"
		default:
			status = "valid"
		}
	}
	if c.DecryptFailed {
		status = "decrypt_failed"
	}
	// Only meaningful for a self-signed cert: it is what a client has to pin,
	// since no CA vouches for it. Surfaced so an admin configuring a client by
	// hand can copy it instead of falling back to "skip verification". Never
	// computed for a CA-issued cert — it rotates on renewal and a pinned copy
	// would silently start rejecting the server.
	selfSigned := !c.DecryptFailed && singbox.IsSelfSignedCert(c.CertPEM)
	fingerprint := ""
	if selfSigned {
		fingerprint = singbox.CertFingerprintSHA256(c.CertPEM)
	}
	return J{
		"self_signed":     selfSigned,
		"sha256":          fingerprint,
		"id":              c.ID,
		"name":            c.Name,
		"domain":          c.Domain,
		"source":          c.Source,
		"acme_method":     c.AcmeMethod,
		"acme_profile":    c.AcmeProfile,
		"actual_key_type": c.ActualKeyType,
		"actual_chain":    c.ActualChain,
		"last_verify_at":  c.LastVerifyAt,
		"verify_error":    c.VerifyError,
		"not_after":       c.NotAfter,
		"days_left":       daysLeft,
		"status":          status,
		"auto_renew":      c.AutoRenew,
		"last_renew_at":   c.LastRenewAt,
		"last_error":      c.LastError,
		"created_at":      c.CreatedAt,
		"updated_at":      c.UpdatedAt,
		"decrypt_failed":  c.DecryptFailed,
	}
}

func (a *API) handleAdminListCerts(w http.ResponseWriter, r *http.Request) {
	list, err := a.st.ListCerts()
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取失败")
		return
	}
	out := make([]map[string]interface{}, 0, len(list))
	for _, c := range list {
		out = append(out, certPublic(c))
	}
	ok(w, out)
}

// certDir returns the directory where acme.sh installs cert/key files, derived
// from the local sing-box config path (same convention as handleAdminAcmeCert).
func (a *API) certDir() string {
	cfgPath := a.sbSetting("QZ_SINGBOX_CONFIG", "sb_config_path", "/etc/sing-box/config.json")
	return path.Join(path.Dir(cfgPath), "certs")
}

// POST /api/admin/certs/acme {name, domain, method?, webroot?, acme_profile?}
// Issues a real Let's Encrypt certificate on the PANEL HOST (DNS-01 needs no
// access to the node) and stores the PEM in the DB as a global certificate any
// node's inbound can then reference. The Cloudflare token is read from settings,
// never taken from the request body.
func (a *API) handleAdminCertAcme(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Domain  string `json:"domain"`
		Method  string `json:"method"` // dns-cf (default) | http-01 | webroot
		Webroot string `json:"webroot"`
		Profile string `json:"acme_profile"` // auto (default) | compatible | modern
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	name := strings.TrimSpace(req.Name)
	domain := strings.TrimSpace(req.Domain)
	if name == "" || domain == "" {
		fail(w, http.StatusBadRequest, "名称和域名必填")
		return
	}
	method := acmesh.Method(strings.TrimSpace(req.Method))
	if method == "" {
		method = acmesh.MethodCFDNS
	}
	profile := strings.TrimSpace(req.Profile)
	if profile == "" {
		profile = acmesh.ProfileAuto
	}
	keyLength, preferredChain, err := acmesh.OptionsForProfile(profile)
	if err != nil {
		fail(w, http.StatusBadRequest, "证书兼容策略无效")
		return
	}
	cfToken, _ := a.st.GetSetting("cf_api_token")
	email, _ := a.st.GetSetting("acme_email")
	if method == acmesh.MethodCFDNS && strings.TrimSpace(cfToken) == "" {
		fail(w, http.StatusBadRequest, "请先在「系统设置」填写 Cloudflare API Token（Zone→DNS→Edit 权限）")
		return
	}

	// acme.sh can block on DNS propagation (dns_cf sleeps ~2min); allow headroom.
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	res, err := acmesh.Issue(ctx, acmesh.LocalRunner{}, acmesh.IssueOpts{
		Domain:         domain,
		Method:         method,
		CFToken:        strings.TrimSpace(cfToken),
		Webroot:        strings.TrimSpace(req.Webroot),
		Email:          strings.TrimSpace(email),
		CertDir:        a.certDir(),
		KeyLength:      keyLength,
		PreferredChain: preferredChain,
		// ReloadCmd intentionally empty: 轻舟 pushes the renewed cert to nodes
		// itself (see StartCertRenew), rather than relying on a local systemd unit.
	})
	if err != nil {
		fail(w, http.StatusBadGateway, "证书申请失败："+err.Error())
		return
	}
	certPEM, keyPEM, err := acmesh.ReadPEM(ctx, acmesh.LocalRunner{}, res)
	if err != nil {
		fail(w, http.StatusInternalServerError, "证书已签发，但读取文件失败："+err.Error())
		return
	}
	verified, err := certcheck.Verify(certPEM, keyPEM, domain, time.Now())
	if err != nil {
		fail(w, http.StatusBadGateway, "签发结果核验失败，证书未保存、不会下发到节点："+err.Error())
		return
	}
	id, err := a.st.SaveCert(&store.Cert{
		Name:          name,
		Domain:        domain,
		Source:        "acme",
		AcmeMethod:    string(method),
		AcmeProfile:   profile,
		CertPEM:       certPEM,
		KeyPEM:        keyPEM,
		ActualKeyType: verified.KeyType,
		ActualChain:   verified.Chain,
		LastVerifyAt:  verified.VerifiedAt,
		AutoRenew:     true,
		LastRenewAt:   time.Now().Unix(),
	})
	if err != nil {
		fail(w, http.StatusInternalServerError, "保存证书失败")
		return
	}
	saved, _ := a.st.GetCert(id)
	ok(w, certPublic(saved))
}

// POST /api/admin/certs/paste {name, domain?, certificate, key}
// Stores an externally-issued cert+key. domain defaults to the leaf CN.
func (a *API) handleAdminCertPaste(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Domain      string `json:"domain"`
		Certificate string `json:"certificate"`
		Key         string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || req.Certificate == "" || req.Key == "" {
		fail(w, http.StatusBadRequest, "名称、证书和私钥必填")
		return
	}
	if err := validateCertKeyPair(req.Certificate, req.Key); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	domain := strings.TrimSpace(req.Domain)
	if domain == "" {
		if info := certInfoFromPEM(req.Certificate); info != nil {
			if cn, _ := info["subject"].(string); cn != "" {
				domain = cn
			}
		}
	}
	id, err := a.st.SaveCert(&store.Cert{
		Name:      name,
		Domain:    domain,
		Source:    "paste",
		CertPEM:   req.Certificate,
		KeyPEM:    req.Key,
		AutoRenew: false, // pasted certs aren't ours to renew
	})
	if err != nil {
		fail(w, http.StatusInternalServerError, "保存证书失败")
		return
	}
	saved, _ := a.st.GetCert(id)
	ok(w, certPublic(saved))
}

// POST /api/admin/certs/self-signed {name, server_name, days?}
func (a *API) handleAdminCertSelfSigned(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string `json:"name"`
		ServerName string `json:"server_name"`
		Days       int    `json:"days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	name := strings.TrimSpace(req.Name)
	sni := strings.TrimSpace(req.ServerName)
	if name == "" || sni == "" {
		fail(w, http.StatusBadRequest, "名称和 SNI 必填")
		return
	}
	certPEM, keyPEM, err := singbox.GenerateSelfSignedCert(sni, req.Days)
	if err != nil {
		fail(w, http.StatusInternalServerError, "生成自签证书失败："+err.Error())
		return
	}
	id, err := a.st.SaveCert(&store.Cert{
		Name:      name,
		Domain:    sni,
		Source:    "selfsigned",
		CertPEM:   certPEM,
		KeyPEM:    keyPEM,
		AutoRenew: false,
	})
	if err != nil {
		fail(w, http.StatusInternalServerError, "保存证书失败")
		return
	}
	saved, _ := a.st.GetCert(id)
	ok(w, certPublic(saved))
}

// PUT /api/admin/certs/{id} {name?, auto_renew?} — edit metadata only.
func (a *API) handleAdminUpdateCert(w http.ResponseWriter, r *http.Request) {
	c, err := a.st.GetCert(atoi(chi.URLParam(r, "id")))
	if err != nil || c == nil {
		fail(w, http.StatusNotFound, "证书不存在")
		return
	}
	var req struct {
		Name      *string `json:"name"`
		AutoRenew *bool   `json:"auto_renew"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		c.Name = strings.TrimSpace(*req.Name)
	}
	if req.AutoRenew != nil {
		c.AutoRenew = *req.AutoRenew
	}
	if c.DecryptFailed {
		fail(w, http.StatusBadRequest, "证书无法解密，拒绝写回以免覆盖为空——请确认 QZ_SECRET_KEY")
		return
	}
	if _, err := a.st.SaveCert(c); err != nil {
		fail(w, http.StatusInternalServerError, "保存失败")
		return
	}
	saved, _ := a.st.GetCert(c.ID)
	ok(w, certPublic(saved))
}

// POST /api/admin/certs/{id}/renew — force a renewal now (ACME certs only).
func (a *API) handleAdminCertRenew(w http.ResponseWriter, r *http.Request) {
	c, err := a.st.GetCert(atoi(chi.URLParam(r, "id")))
	if err != nil || c == nil {
		fail(w, http.StatusNotFound, "证书不存在")
		return
	}
	if c.Source != "acme" {
		fail(w, http.StatusBadRequest, "仅 ACME 证书支持续期；粘贴/自签证书请重新导入")
		return
	}
	cfToken, _ := a.st.GetSetting("cf_api_token")
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	if err := a.renewOneCert(ctx, c, cfToken, a.certDir(), true); err != nil {
		_ = a.st.SetCertRenewError(c.ID, err.Error())
		fail(w, http.StatusBadGateway, "续期失败："+err.Error())
		return
	}
	saved, _ := a.st.GetCert(c.ID)
	ok(w, certPublic(saved))
}

// Reject the former GET explicitly; otherwise the SPA catch-all can return an
// HTML 200 and make old clients believe they downloaded a certificate.
func (a *API) handleLegacyCertExport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", http.MethodPost)
	w.Header().Set("Cache-Control", "no-store")
	fail(w, http.StatusMethodNotAllowed, "私钥导出请使用 POST 并完成密码二次验证")
}

// POST /api/admin/certs/{id}/export — return the raw PEM for copy/download.
func (a *API) handleAdminExportCert(w http.ResponseWriter, r *http.Request) {
	c, err := a.st.GetCert(atoi(chi.URLParam(r, "id")))
	if err != nil || c == nil {
		fail(w, http.StatusNotFound, "证书不存在")
		return
	}
	if c.DecryptFailed {
		fail(w, http.StatusBadRequest, "证书无法解密——请确认 QZ_SECRET_KEY")
		return
	}
	ok(w, J{"certificate": c.CertPEM, "key": c.KeyPEM})
}

// DELETE /api/admin/certs/{id} — refused while any TLS profile references it.
func (a *API) handleAdminDeleteCert(w http.ResponseWriter, r *http.Request) {
	if err := a.st.DeleteCert(atoi(chi.URLParam(r, "id"))); err != nil {
		if errors.Is(err, store.ErrInUse) {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		fail(w, http.StatusInternalServerError, "删除失败")
		return
	}
	ok(w, nil)
}
