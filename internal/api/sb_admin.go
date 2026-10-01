package api

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"qingzhou/internal/acmesh"
	"qingzhou/internal/certcheck"
	"qingzhou/internal/sbctl"
	"qingzhou/internal/sbproc"
	"qingzhou/internal/singbox"
	"qingzhou/internal/store"
)

// ===== native sing-box (B2) admin: TLS/Reality profiles & inbounds =====

// POST /api/admin/sb/reality-keypair — fresh x25519 keypair + short_id.
func (a *API) handleAdminRealityKeypair(w http.ResponseWriter, r *http.Request) {
	priv, pub, err := singbox.GenerateRealityKeypair()
	if err != nil {
		fail(w, http.StatusInternalServerError, "生成密钥失败")
		return
	}
	sid, _ := singbox.GenerateShortID(8)
	ok(w, J{"private_key": priv, "public_key": pub, "short_id": sid})
}

// POST /api/admin/sb/tls/self-signed {server_name, days?} — generate a
// self-signed certificate + key for the given SNI, returned as PEM. Used by the
// 证书 TLS drawer's one-click button; the operator still reviews and saves.
func (a *API) handleAdminSelfSignedCert(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServerName string `json:"server_name"`
		Days       int    `json:"days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if strings.TrimSpace(req.ServerName) == "" {
		fail(w, http.StatusBadRequest, "SNI 域名必填")
		return
	}
	certPEM, keyPEM, err := singbox.GenerateSelfSignedCert(req.ServerName, req.Days)
	if err != nil {
		fail(w, http.StatusInternalServerError, "生成自签证书失败："+err.Error())
		return
	}
	ok(w, J{"certificate": certPEM, "key": keyPEM})
}

// handleAdminQuickSelfSignedTls generates a self-signed cert AND saves it as a
// ready-to-bind TLS entry in one call — the "一键绑定证书" path for mixed
// (HTTP/SOCKS5) inbounds that want to become HTTPS proxies without a domain. The
// SNI defaults to the address clients dial for the inbound's server (its host,
// else the panel's node host), so the cert's SAN matches; it also embeds an IP
// SAN when that address is an IP. Returns the saved TLS entry (with its id).
func (a *API) handleAdminQuickSelfSignedTls(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServerID   int64  `json:"server_id"`
		ServerName string `json:"server_name"` // optional override (domain or IP)
		Name       string `json:"name"`        // optional TLS entry name
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	sni := strings.TrimSpace(req.ServerName)
	if sni == "" {
		if req.ServerID != 0 {
			if sv, _ := a.st.GetServer(req.ServerID); sv != nil {
				sni = strings.TrimSpace(sv.Host)
			}
		}
		if sni == "" {
			sni = strings.TrimSpace(a.nodeHost())
		}
	}
	if sni == "" {
		fail(w, http.StatusBadRequest, "无法确定证书域名/IP：请在该服务器或「系统设置 → 面板访问地址」配置 host，或手动填写 SNI")
		return
	}
	certPEM, keyPEM, err := singbox.GenerateSelfSignedCert(sni, 3650)
	if err != nil {
		fail(w, http.StatusInternalServerError, "生成自签证书失败："+err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "自签-" + sni
	}
	server := map[string]interface{}{
		"enabled":     true,
		"server_name": sni,
		"certificate": certPEM,
		"key":         keyPEM,
	}
	// insecure=true on the client side: a self-signed cert won't chain to a public
	// CA, so clients (and 轻舟's own link renderers) must skip verification.
	client := map[string]interface{}{
		"insecure": true,
		"utls":     map[string]interface{}{"enabled": true, "fingerprint": "chrome"},
	}
	sj, _ := json.Marshal(server)
	cj, _ := json.Marshal(client)
	newID, err := a.st.SaveSbTls(&store.SbTls{ServerID: req.ServerID, Name: name, Mode: "tls", ServerJSON: string(sj), ClientJSON: string(cj)})
	if err != nil {
		fail(w, http.StatusInternalServerError, "保存证书失败")
		return
	}
	saved, _ := a.st.GetSbTls(newID)
	ok(w, saved)
}

// sbSetting resolves a sing-box config value the same way the controller does:
// env var → DB setting → default. Keeps the ACME reload command and cert dir in
// sync with where the local sing-box actually reads its config / systemd unit.
func (a *API) sbSetting(envKey, settingKey, def string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	if v, _ := a.st.GetSetting(settingKey); v != "" {
		return v
	}
	return def
}

// POST /api/admin/sb/tls/acme {name, server_id, server_name, method, cf_token, email}
// — issue a real Let's Encrypt certificate via acme.sh on the local host, install
// it to a stable path, and save a TLS profile that references those paths
// (tls.certificate_path / key_path). acme.sh's cron renews it in place; sing-box
// picks up the new file on its next reload. Local host only for now; remote
// servers must issue on-box or paste a cert.
func (a *API) handleAdminAcmeCert(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string `json:"name"`
		ServerID   int64  `json:"server_id"`
		ServerName string `json:"server_name"` // the domain to issue for
		Method     string `json:"method"`      // "http-01" | "webroot" | "dns-cf"
		CFToken    string `json:"cf_token"`
		Webroot    string `json:"webroot"`
		Email      string `json:"email"`
		Profile    string `json:"acme_profile"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	domain := strings.TrimSpace(req.ServerName)
	if req.Name == "" || domain == "" {
		fail(w, http.StatusBadRequest, "名称和域名必填")
		return
	}
	if req.ServerID != 0 {
		fail(w, http.StatusBadRequest, "在线申请当前仅支持本机；远程服务器请在该机器上用 acme.sh 申请后粘贴证书，或用自签")
		return
	}
	method := acmesh.Method(strings.TrimSpace(req.Method))
	if method == "" {
		method = acmesh.MethodHTTP01
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

	unit := a.sbSetting("QZ_SINGBOX_UNIT", "sb_systemd_unit", "sing-box")
	cfgPath := a.sbSetting("QZ_SINGBOX_CONFIG", "sb_config_path", "/etc/sing-box/config.json")
	certDir := path.Join(path.Dir(cfgPath), "certs")

	// acme.sh can block on DNS propagation (dns_cf sleeps ~2min); allow headroom.
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	res, err := acmesh.Issue(ctx, acmesh.LocalRunner{}, acmesh.IssueOpts{
		Domain:         domain,
		Method:         method,
		CFToken:        strings.TrimSpace(req.CFToken),
		Webroot:        strings.TrimSpace(req.Webroot),
		Email:          strings.TrimSpace(req.Email),
		CertDir:        certDir,
		ReloadCmd:      "systemctl restart " + unit,
		KeyLength:      keyLength,
		PreferredChain: preferredChain,
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
	if _, err := certcheck.Verify(certPEM, keyPEM, domain, time.Now()); err != nil {
		fail(w, http.StatusBadGateway, "签发结果核验失败，TLS 配置未保存："+err.Error())
		return
	}

	server := map[string]interface{}{
		"enabled":          true,
		"server_name":      domain,
		"certificate_path": res.CertPath,
		"key_path":         res.KeyPath,
	}
	client := map[string]interface{}{
		"utls": map[string]interface{}{"enabled": true, "fingerprint": "chrome"},
	}
	sj, _ := json.Marshal(server)
	cj, _ := json.Marshal(client)
	id, err := a.st.SaveSbTls(&store.SbTls{ServerID: req.ServerID, Name: req.Name, Mode: "tls", ServerJSON: string(sj), ClientJSON: string(cj)})
	if err != nil {
		fail(w, http.StatusInternalServerError, "证书已申请，但保存配置失败")
		return
	}
	a.sbScheduleServer(req.ServerID)
	saved, _ := a.st.GetSbTls(id)
	ok(w, saved)
}

// validateCertKeyPair reports whether the PEM certificate and key are both
// parseable and form a matching pair (the key belongs to the cert). Returns a
// user-facing Chinese error describing the first problem found, or nil when ok.
func validateCertKeyPair(certPEM, keyPEM string) error {
	if _, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM)); err != nil {
		return fmt.Errorf("证书与私钥无效或不匹配：%v", err)
	}
	return nil
}

// GET /api/admin/sb/sni-test?host=www.microsoft.com[:port]&port=8443
// Tests TCP handshake latency to an SNI host (default port 443, or ?port=). Takes
// 3 samples and reports min/avg/max + resolve info. Used by the TLS editor to
// preview how well a candidate SNI domain performs before saving, and by the
// Reality handshake target test (which may use a non-443 port).
// normalizeSniAddr turns a user-supplied host (and optional explicit port) into
// a dialable host:port, or reports that the host is malformed.
//
// "是否已经带端口了" 曾用 strings.Contains(host, ":") 判断，裸 IPv6 字面量天生
// 含冒号，于是既不补端口、后面的 SplitHostPort 又解析不了，任何 IPv6 host 都
// 直接被判 "host 格式错误"。改用 SplitHostPort 试解析：成功即说明已带端口。
//
// 但只做这一步会让校验变宽松：JoinHostPort 会给任何含冒号的 host 加方括号，
// 于是 "a:b:c" 这类垃圾会被包成 "[a:b:c]:443" 一路通过，最终以 "无法连接"
// 而不是 "格式错误" 报错，掩盖了真正的原因。所以含冒号的 host 必须真的能被
// ParseIP 解析成 IP 才放行。
func normalizeSniAddr(host, port string) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", fmt.Errorf("empty host")
	}
	// 已经是 host:port（含 [v6]:port）就原样用，显式 port 参数让位于内联端口。
	// 注意 SplitHostPort(":443") 是成功的（host 为空），必须单独挡掉。
	if h, _, err := net.SplitHostPort(host); err == nil {
		if h == "" {
			return "", fmt.Errorf("empty host in %q", host)
		}
		return host, nil
	}
	if port == "" {
		port = "443"
	}
	h := host
	if len(h) > 1 && h[0] == '[' && h[len(h)-1] == ']' {
		h = h[1 : len(h)-1]
	}
	if h == "" {
		return "", fmt.Errorf("empty host")
	}
	// 到这里 h 应当是域名或 IP 字面量。域名和 IPv4 都不含冒号；含冒号就只能是
	// IPv6，必须解析得出来——否则是 "a:b:c" / "[2001:db8::1"（括号未闭合）之类。
	if strings.Contains(h, ":") && net.ParseIP(h) == nil {
		return "", fmt.Errorf("malformed host %q", host)
	}
	return net.JoinHostPort(h, port), nil
}

func (a *API) handleAdminSniTest(w http.ResponseWriter, r *http.Request) {
	host := strings.TrimSpace(r.URL.Query().Get("host"))
	if host == "" {
		fail(w, http.StatusBadRequest, "缺少 host 参数")
		return
	}
	addr, err := normalizeSniAddr(host, strings.TrimSpace(r.URL.Query().Get("port")))
	if err != nil {
		fail(w, http.StatusBadRequest, "host 格式错误")
		return
	}
	// 回显给前端的是不带端口的主机名；normalizeSniAddr 已保证 addr 可拆分。
	h, _, _ := net.SplitHostPort(addr)

	type sample struct {
		MS    float64 `json:"ms"`
		Error string  `json:"error,omitempty"`
	}
	const probes = 3
	samples := make([]sample, 0, probes)
	var sum, okCount float64
	var min, max float64
	for i := 0; i < probes; i++ {
		t0 := time.Now()
		conn, derr := net.DialTimeout("tcp", addr, 3*time.Second)
		dt := time.Since(t0).Seconds() * 1000
		if derr != nil {
			samples = append(samples, sample{MS: dt, Error: derr.Error()})
			continue
		}
		_ = conn.Close()
		samples = append(samples, sample{MS: dt})
		sum += dt
		okCount++
		if min == 0 || dt < min {
			min = dt
		}
		if dt > max {
			max = dt
		}
		time.Sleep(80 * time.Millisecond)
	}

	avg := 0.0
	status := "ok"
	if okCount > 0 {
		avg = sum / okCount
	} else {
		status = "unreachable"
	}
	if okCount > 0 && okCount < float64(probes) {
		status = "partial" // some probes failed
	}
	ok(w, J{
		"host":    h,
		"addr":    addr,
		"status":  status,
		"ok":      int(okCount),
		"total":   probes,
		"min_ms":  min,
		"avg_ms":  avg,
		"max_ms":  max,
		"samples": samples,
	})
}

// ---- sb_tls ----

// certInfoFromPEM 解析 PEM 证书，返回有效期信息（用于列表展示过期提醒）。
func certInfoFromPEM(pemStr string) map[string]interface{} {
	if pemStr == "" {
		return nil
	}
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return map[string]interface{}{"error": "PEM 解析失败"}
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return map[string]interface{}{"error": "证书解析失败: " + err.Error()}
	}
	now := time.Now()
	daysLeft := int(cert.NotAfter.Sub(now).Hours() / 24)
	return map[string]interface{}{
		"subject":    cert.Subject.CommonName,
		"issuer":     cert.Issuer.CommonName,
		"not_before": cert.NotBefore.Unix(),
		"not_after":  cert.NotAfter.Unix(),
		"days_left":  daysLeft,
		"expired":    now.After(cert.NotAfter),
		"expiring":   daysLeft >= 0 && daysLeft <= 14,
	}
}

// publicTLSJSON preserves the list/editor metadata while withholding existing
// private keys. Editing and cloning fetch the unredacted row through the scoped
// export endpoint, so a redacted value is never written back as a replacement.
func publicTLSJSON(raw string) string {
	var value map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &value) != nil || value == nil {
		return "{}"
	}
	// Only remove fields known to contain private keys. RawMessage preserves
	// unknown extension fields (including large integers) without reshaping them.
	delete(value, "key")
	if rawReality, ok := value["reality"]; ok {
		var reality map[string]json.RawMessage
		if json.Unmarshal(rawReality, &reality) != nil {
			delete(value, "reality")
		} else {
			delete(reality, "private_key")
			value["reality"], _ = json.Marshal(reality)
		}
	}
	result, _ := json.Marshal(value)
	return string(result)
}

// decodeTLSJSON keeps extension numbers lossless during structured edits.
func decodeTLSJSON(raw string) (map[string]interface{}, error) {
	result := map[string]interface{}{}
	if strings.TrimSpace(raw) == "" {
		return result, nil
	}
	if !json.Valid([]byte(raw)) {
		return nil, errors.New("invalid TLS JSON")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		return nil, err
	}
	if result == nil {
		result = map[string]interface{}{}
	}
	return result, nil
}

func updateTLSFingerprint(client map[string]interface{}, fingerprint string) {
	utls, _ := client["utls"].(map[string]interface{})
	if utls == nil {
		utls = map[string]interface{}{}
	}
	utls["enabled"], utls["fingerprint"] = true, fpOrDefault(fingerprint)
	client["utls"] = utls
}

// POST /api/admin/sb/tls/{id}/export is the explicit secret-bearing details
// fetch for edit/clone. Ordinary list reads never return existing private keys.
func (a *API) handleAdminExportSbTls(w http.ResponseWriter, r *http.Request) {
	profile, err := a.st.GetSbTls(atoi(chi.URLParam(r, "id")))
	if err != nil || profile == nil {
		fail(w, http.StatusNotFound, "配置不存在")
		return
	}
	if profile.DecryptFailed {
		fail(w, http.StatusBadRequest, "配置无法解密，请确认 QZ_SECRET_KEY")
		return
	}
	ok(w, profile)
}

func (a *API) handleAdminListSbTls(w http.ResponseWriter, r *http.Request) {
	list, err := a.st.ListSbTls()
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取失败")
		return
	}
	// 为每个证书 TLS 模式的配置附带证书有效期信息
	out := make([]map[string]interface{}, 0, len(list))
	for _, t := range list {
		m := map[string]interface{}{
			"id":          t.ID,
			"server_id":   t.ServerID,
			"name":        t.Name,
			"mode":        t.Mode,
			"server_json": publicTLSJSON(t.ServerJSON),
			"client_json": t.ClientJSON,
			"cert_id":     t.CertID,
			"sort_order":  t.SortOrder,
			"created_at":  t.CreatedAt,
			"updated_at":  t.UpdatedAt,
		}
		if t.Mode == "tls" {
			if t.CertID != 0 {
				// Managed cert: expiry comes from the certificates row, not inline PEM.
				if c, _ := a.st.GetCert(t.CertID); c != nil && !c.DecryptFailed {
					m["cert_info"] = certInfoFromPEM(c.CertPEM)
				}
			} else {
				var sj map[string]interface{}
				if json.Unmarshal([]byte(t.ServerJSON), &sj) == nil {
					if cert, ok := sj["certificate"].(string); ok && cert != "" {
						m["cert_info"] = certInfoFromPEM(cert)
					}
				}
			}
		}
		out = append(out, m)
	}
	ok(w, out)
}

func (a *API) handleAdminSaveSbTls(w http.ResponseWriter, r *http.Request) {
	var t store.SbTls
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if strings.TrimSpace(t.Name) == "" {
		fail(w, http.StatusBadRequest, "名称必填")
		return
	}
	id := chi.URLParam(r, "id")
	if id != "" {
		t.ID = atoi(id)
	}
	newID, err := a.st.SaveSbTls(&t)
	if err != nil {
		fail(w, http.StatusInternalServerError, "保存失败")
		return
	}
	if err := a.sbRebuild(); err != nil {
		fail(w, http.StatusBadGateway, "已保存，但应用到 sing-box 失败："+err.Error())
		return
	}
	saved, _ := a.st.GetSbTls(newID)
	ok(w, saved)
}

// handleAdminReorderSbTls persists a new display order for the TLS list.
// Body: {"ids":[...]} — TLS ids in the desired global order (the page groups by
// machine but sort_order is global, so the client submits the whole list).
// Only sort_order is rewritten: TLS profiles are referenced by id, so their
// order never reaches a node's config and no rebuild is needed.
func (a *API) handleAdminReorderSbTls(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.IDs) == 0 {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if err := a.st.ReorderSbTls(req.IDs); err != nil {
		fail(w, http.StatusInternalServerError, "保存排序失败")
		return
	}
	ok(w, J{"count": len(req.IDs)})
}

func (a *API) handleAdminDeleteSbTls(w http.ResponseWriter, r *http.Request) {
	if err := a.st.DeleteSbTls(atoi(chi.URLParam(r, "id"))); err != nil {
		if errors.Is(err, store.ErrInUse) {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		fail(w, http.StatusInternalServerError, "删除失败")
		return
	}
	a.sbRebuildLog()
	ok(w, nil)
}

// POST /api/admin/sb/tls/reality — convenience: build a complete Reality TLS
// profile (server + client JSON) with a freshly generated keypair.
func (a *API) handleAdminCreateRealityTls(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name            string `json:"name"`
		ServerID        int64  `json:"server_id"`
		ServerName      string `json:"server_name"`      // SNI shown to clients, e.g. www.tesla.com
		HandshakeServer string `json:"handshake_server"` // real TLS dest, defaults to ServerName
		HandshakePort   int    `json:"handshake_port"`   // defaults 443
		Fingerprint     string `json:"fingerprint"`      // utls fp, defaults chrome
		// Pre-generated keys from frontend (optional; backend generates if empty)
		PrivateKey string   `json:"private_key"`
		PublicKey  string   `json:"public_key"`
		ShortID    string   `json:"short_id"`  // 单个（向后兼容）
		ShortIDs   []string `json:"short_ids"` // 多个（优先）
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.ServerName = strings.TrimSpace(req.ServerName)
	if req.Name == "" || req.ServerName == "" {
		fail(w, http.StatusBadRequest, "名称和 SNI 必填")
		return
	}
	if req.HandshakeServer == "" {
		req.HandshakeServer = req.ServerName
	}
	if req.HandshakePort == 0 {
		req.HandshakePort = 443
	}

	priv, pub := req.PrivateKey, req.PublicKey
	// Generate keys only if not provided by frontend
	if priv == "" || pub == "" {
		var err error
		priv, pub, err = singbox.GenerateRealityKeypair()
		if err != nil {
			fail(w, http.StatusInternalServerError, "生成密钥失败")
			return
		}
	}
	// 构建 short_id 列表：优先用 short_ids 数组，否则用单个 short_id
	sids := make([]string, 0, len(req.ShortIDs))
	for _, s := range req.ShortIDs {
		if s = strings.TrimSpace(s); s != "" {
			sids = append(sids, s)
		}
	}
	if len(sids) == 0 {
		sid := strings.TrimSpace(req.ShortID)
		if sid == "" {
			sid, _ = singbox.GenerateShortID(8)
		}
		sids = []string{sid}
	}

	server := map[string]interface{}{
		"enabled":     true,
		"server_name": req.ServerName,
		"reality": map[string]interface{}{
			"enabled":     true,
			"handshake":   map[string]interface{}{"server": req.HandshakeServer, "server_port": req.HandshakePort},
			"private_key": priv,
			"short_id":    sids,
		},
	}
	client := map[string]interface{}{
		"server_name": req.ServerName,
		"public_key":  pub,
		"short_id":    sids[0],
		"reality":     map[string]interface{}{"public_key": pub},
		"utls":        map[string]interface{}{"enabled": true, "fingerprint": fpOrDefault(req.Fingerprint)},
	}
	sj, _ := json.Marshal(server)
	cj, _ := json.Marshal(client)
	id, err := a.st.SaveSbTls(&store.SbTls{ServerID: req.ServerID, Name: req.Name, Mode: "reality", ServerJSON: string(sj), ClientJSON: string(cj)})
	if err != nil {
		fail(w, http.StatusInternalServerError, "保存失败")
		return
	}
	saved, _ := a.st.GetSbTls(id)
	ok(w, saved)
}

var validFingerprints = map[string]bool{
	"chrome": true, "firefox": true, "safari": true, "ios": true, "android": true,
	"edge": true, "360": true, "qq": true, "random": true, "randomized": true,
}

func fpOrDefault(fp string) string {
	if validFingerprints[fp] {
		return fp
	}
	return "chrome"
}

// tlsVersion normalizes a TLS version string to sing-box's form ("1.2"/"1.3").
func tlsVersion(v string) string {
	switch v {
	case "1.2", "1.3":
		return v
	}
	return ""
}

// PUT /api/admin/sb/tls/reality/{id} — edit a Reality profile's name/SNI/
// handshake/short_ids from structured fields, preserving the existing keypair.
func (a *API) handleAdminUpdateRealityTls(w http.ResponseWriter, r *http.Request) {
	id := atoi(chi.URLParam(r, "id"))
	cur, _ := a.st.GetSbTls(id)
	if cur == nil {
		fail(w, http.StatusNotFound, "配置不存在")
		return
	}
	var req struct {
		Name            string   `json:"name"`
		ServerID        int64    `json:"server_id"`
		ServerName      string   `json:"server_name"`
		HandshakeServer string   `json:"handshake_server"`
		HandshakePort   int      `json:"handshake_port"`
		Fingerprint     string   `json:"fingerprint"`
		ShortIDs        []string `json:"short_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.ServerName = strings.TrimSpace(req.ServerName)
	if req.Name == "" || req.ServerName == "" {
		fail(w, http.StatusBadRequest, "名称和 SNI 必填")
		return
	}
	if req.HandshakeServer == "" {
		req.HandshakeServer = req.ServerName
	}
	if req.HandshakePort == 0 {
		req.HandshakePort = 443
	}
	server, serverErr := decodeTLSJSON(cur.ServerJSON)
	client, clientErr := decodeTLSJSON(cur.ClientJSON)
	if cur.DecryptFailed || serverErr != nil || clientErr != nil {
		fail(w, http.StatusBadRequest, "已有 TLS 配置不可用，拒绝覆盖")
		return
	}
	if server == nil {
		server = map[string]interface{}{"enabled": true}
	}
	server["server_name"] = req.ServerName
	reality, _ := server["reality"].(map[string]interface{})
	if reality == nil {
		reality = map[string]interface{}{"enabled": true}
		server["reality"] = reality
	}
	handshake, _ := reality["handshake"].(map[string]interface{})
	if handshake == nil {
		handshake = map[string]interface{}{}
	}
	handshake["server"], handshake["server_port"] = req.HandshakeServer, req.HandshakePort
	reality["handshake"] = handshake
	// 更新 short_id 列表（如果前端提供了）
	if len(req.ShortIDs) > 0 {
		sids := make([]string, 0, len(req.ShortIDs))
		for _, s := range req.ShortIDs {
			if s = strings.TrimSpace(s); s != "" {
				sids = append(sids, s)
			}
		}
		if len(sids) > 0 {
			reality["short_id"] = sids
			if client == nil {
				client = map[string]interface{}{}
			}
			client["short_id"] = sids[0]
		}
	}
	if client == nil {
		client = map[string]interface{}{}
	}
	client["server_name"] = req.ServerName
	updateTLSFingerprint(client, req.Fingerprint)
	sj, _ := json.Marshal(server)
	cj, _ := json.Marshal(client)
	if _, err := a.st.SaveSbTls(&store.SbTls{ID: id, ServerID: req.ServerID, Name: req.Name, Mode: "reality", ServerJSON: string(sj), ClientJSON: string(cj)}); err != nil {
		fail(w, http.StatusInternalServerError, "保存失败")
		return
	}
	a.sbRebuildLog()
	saved, _ := a.st.GetSbTls(id)
	ok(w, saved)
}

// POST /api/admin/sb/tls/cert  and  PUT /api/admin/sb/tls/cert/{id} — create or
// edit a regular (non-Reality) TLS profile from structured fields. On edit, a
// blank certificate/key keeps the existing one (so the cert needn't be re-pasted).
func (a *API) handleAdminSaveCertTls(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string   `json:"name"`
		ServerID    int64    `json:"server_id"`
		ServerName  string   `json:"server_name"`
		Certificate string   `json:"certificate"`
		Key         string   `json:"key"`
		CertID      int64    `json:"cert_id"` // reference a managed certificate instead of inline PEM
		Insecure    bool     `json:"insecure"`
		ALPN        []string `json:"alpn"`
		Fingerprint string   `json:"fingerprint"`
		MinVersion  string   `json:"min_version"`
		MaxVersion  string   `json:"max_version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.ServerName = strings.TrimSpace(req.ServerName)
	if req.Name == "" || req.ServerName == "" {
		fail(w, http.StatusBadRequest, "名称和 SNI 必填")
		return
	}
	var id int64
	if p := chi.URLParam(r, "id"); p != "" {
		id = atoi(p)
	}
	serverBase, clientBase := map[string]interface{}{}, map[string]interface{}{}
	if id != 0 {
		current, err := a.st.GetSbTls(id)
		if err != nil || current == nil {
			fail(w, http.StatusNotFound, "配置不存在")
			return
		}
		if current.DecryptFailed {
			fail(w, http.StatusBadRequest, "配置无法解密，请确认 QZ_SECRET_KEY")
			return
		}
		var serverErr, clientErr error
		serverBase, serverErr = decodeTLSJSON(current.ServerJSON)
		clientBase, clientErr = decodeTLSJSON(current.ClientJSON)
		if serverErr != nil || clientErr != nil {
			fail(w, http.StatusBadRequest, "已有 TLS 配置格式无效，拒绝覆盖")
			return
		}
	}
	if serverBase == nil {
		serverBase = map[string]interface{}{}
	}
	if clientBase == nil {
		clientBase = map[string]interface{}{}
	}
	// These known form-controlled options may be deliberately cleared. Preserve
	// all unknown extension fields and only replace options represented by the form.
	for _, key := range []string{"alpn", "min_version", "max_version", "reality", "certificate_path", "key_path"} {
		delete(serverBase, key)
	}
	// A profile referencing a managed certificate carries no inline PEM: the cert
	// bytes are injected at build time from the certificates table (single source
	// of truth), and its client params are pinned to a real, verified cert.
	if req.CertID != 0 {
		cert, err := a.st.GetCert(req.CertID)
		if err != nil || cert == nil {
			fail(w, http.StatusBadRequest, "引用的证书不存在")
			return
		}
		sni := req.ServerName
		if cert.Domain != "" {
			sni = cert.Domain // SNI must match the cert; the cert's domain wins
		}
		server := serverBase
		server["enabled"], server["server_name"] = true, sni
		for _, key := range []string{"certificate", "key", "certificate_path", "key_path"} {
			delete(server, key)
		}
		if len(req.ALPN) > 0 {
			server["alpn"] = req.ALPN
		}
		if v := tlsVersion(req.MinVersion); v != "" {
			server["min_version"] = v
		}
		if v := tlsVersion(req.MaxVersion); v != "" {
			server["max_version"] = v
		}
		// A real (verified) cert means the client must NOT skip verification;
		// self-signed certs referenced here still honor the caller's insecure flag.
		insecure := req.Insecure
		if cert.Source == "acme" || cert.Source == "paste" {
			insecure = false
		}
		client := clientBase
		client["insecure"] = insecure
		updateTLSFingerprint(client, req.Fingerprint)
		sj, _ := json.Marshal(server)
		cj, _ := json.Marshal(client)
		newID, err := a.st.SaveSbTls(&store.SbTls{ID: id, ServerID: req.ServerID, Name: req.Name, Mode: "tls", CertID: req.CertID, ServerJSON: string(sj), ClientJSON: string(cj)})
		if err != nil {
			fail(w, http.StatusInternalServerError, "保存失败")
			return
		}
		a.sbRebuildLog()
		saved, _ := a.st.GetSbTls(newID)
		ok(w, saved)
		return
	}
	// On edit, keep existing cert/key when the form leaves them blank.
	if id != 0 && (req.Certificate == "" || req.Key == "") {
		if cur, _ := a.st.GetSbTls(id); cur != nil {
			var s map[string]interface{}
			_ = json.Unmarshal([]byte(cur.ServerJSON), &s)
			if req.Certificate == "" {
				req.Certificate, _ = s["certificate"].(string)
			}
			if req.Key == "" {
				req.Key, _ = s["key"].(string)
			}
		}
	}
	if req.Certificate == "" || req.Key == "" {
		fail(w, http.StatusBadRequest, "证书和私钥必填")
		return
	}
	// Pre-validate the PEM pair here so a bad paste is caught at save time with a
	// clear message, instead of surfacing later as an opaque sing-box apply error.
	if err := validateCertKeyPair(req.Certificate, req.Key); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	server := serverBase
	server["enabled"], server["server_name"] = true, req.ServerName
	server["certificate"], server["key"] = req.Certificate, req.Key
	if len(req.ALPN) > 0 {
		server["alpn"] = req.ALPN
	}
	if v := tlsVersion(req.MinVersion); v != "" {
		server["min_version"] = v
	}
	if v := tlsVersion(req.MaxVersion); v != "" {
		server["max_version"] = v
	}
	client := clientBase
	client["insecure"] = req.Insecure
	updateTLSFingerprint(client, req.Fingerprint)
	sj, _ := json.Marshal(server)
	cj, _ := json.Marshal(client)
	newID, err := a.st.SaveSbTls(&store.SbTls{ID: id, ServerID: req.ServerID, Name: req.Name, Mode: "tls", ServerJSON: string(sj), ClientJSON: string(cj)})
	if err != nil {
		fail(w, http.StatusInternalServerError, "保存失败")
		return
	}
	a.sbRebuildLog()
	saved, _ := a.st.GetSbTls(newID)
	ok(w, saved)
}

// ---- sb_egresses ----

// maskEgress copies an egress with its password masked as "***" (same
// convention as server SSH credentials): the plaintext never leaves the
// server; a client sending "***" back on save means "keep the stored value".
func maskEgress(e *store.SbEgress) *store.SbEgress {
	cp := *e
	if cp.Password != "" || cp.DecryptFailed {
		cp.Password = "***"
	}
	return &cp
}

func (a *API) handleAdminListSbEgresses(w http.ResponseWriter, r *http.Request) {
	list, err := a.st.ListSbEgresses()
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取失败")
		return
	}
	// 附带每个出口被多少个入站引用，前端用来提示删除限制。
	refs := a.countEgressRefs()
	out := make([]map[string]interface{}, 0, len(list))
	for _, e := range list {
		out = append(out, egressJSON(e, refs[e.ID]))
	}
	ok(w, out)
}

// egressJSON renders one egress for the admin UI: password masked, and the
// "decide for me" fields resolved to what the generated config will actually
// carry. Returning the raw "" / 0 sentinels would make the UI show a blank
// where a real, load-bearing default is in force.
func egressJSON(e *store.SbEgress, inboundCount int) map[string]interface{} {
	m := maskEgress(e)
	return map[string]interface{}{
		"id": m.ID, "name": m.Name, "type": m.Type, "host": m.Host, "port": m.Port,
		"username": m.Username, "password": m.Password,
		"tls_enabled": m.TLSEnabled, "sni": m.SNI,
		"tls_cert_id": m.TLSCertID, "tls_insecure": m.TLSInsecure,
		"udp_mode":                     m.UDPMode,
		"udp_mode_effective":           m.EffectiveUDPMode(),
		"connect_timeout_ms":           m.ConnectTimeoutMS,
		"connect_timeout_effective_ms": m.EffectiveConnectTimeoutMS(),
		"inbound_count":                inboundCount,
		"created_at":                   m.CreatedAt, "updated_at": m.UpdatedAt,
	}
}

// countEgressRefs returns how many inbounds point at each egress.
func (a *API) countEgressRefs() map[int64]int {
	refs := map[int64]int{}
	inbounds, _ := a.st.ListSbInbounds()
	for _, ib := range inbounds {
		if ib.EgressID != 0 {
			refs[ib.EgressID]++
		}
	}
	return refs
}

func (a *API) handleAdminSaveSbEgress(w http.ResponseWriter, r *http.Request) {
	var e store.SbEgress
	if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if idStr := chi.URLParam(r, "id"); idStr != "" {
		e.ID = int64(atoi(idStr))
	}
	e.Name = strings.TrimSpace(e.Name)
	e.Host = strings.TrimSpace(e.Host)
	e.Username = strings.TrimSpace(e.Username)
	// 供应商常给 "host:443" 整串，贴进地址栏而端口留表单默认值时，生成的
	// outbound server 是个解析不了的假主机名，该出口的所有流量都会超时。
	// 自动把地址里的端口拆出来，以地址中的端口为准（也兼容 [v6]:port）。
	if h, p, err := net.SplitHostPort(e.Host); err == nil && h != "" {
		if n := atoi(p); n > 0 && n <= 65535 {
			e.Host, e.Port = h, int(n)
		}
	}
	if e.Type != "socks" && e.Type != "http" {
		fail(w, http.StatusBadRequest, "类型仅支持 socks / http")
		return
	}
	if e.Name == "" || e.Host == "" || e.Port <= 0 || e.Port > 65535 {
		fail(w, http.StatusBadRequest, "名称、地址必填，端口需在 1-65535")
		return
	}
	// UDP 策略：空串＝按类型自动决定（见 SbEgress.EffectiveUDPMode），显式值只认这两个。
	// 拒绝未知值而不是静默回落，否则前端拼错一个字符就会得到一份与界面不符的配置。
	switch e.UDPMode {
	case "", store.UDPModePassthrough, store.UDPModeBlock:
	default:
		fail(w, http.StatusBadRequest, "UDP 策略仅支持 passthrough / block")
		return
	}
	// HTTP 出站在 sing-box 里根本没有 UDP 通路，passthrough 只会让 UDP 沉默地失败一遍再失败。
	// 与其存一个界面显示「透传」、实际等于阻断的值，不如直接拦下来。
	if e.UDPMode == store.UDPModePassthrough && e.Type == "http" {
		fail(w, http.StatusBadRequest, "HTTP 出口不支持 UDP 透传（sing-box 的 http 出站没有 UDP 通路），请选择「阻断」")
		return
	}
	if e.ConnectTimeoutMS < 0 || e.ConnectTimeoutMS > 60000 {
		fail(w, http.StatusBadRequest, "连接超时需在 0-60000 毫秒（0＝使用默认值）")
		return
	}
	// TLS 到代理这一跳（HTTPS 代理）。sing-box 的 socks 出站没有 tls 选项，
	// 存下来只会被静默忽略——配置看着是加密的，实际是明文，凭据裸奔。宁可在这里拒绝。
	e.SNI = strings.TrimSpace(e.SNI)
	if e.TLSEnabled && e.Type != "http" {
		fail(w, http.StatusBadRequest, "仅 HTTP 类型支持 TLS（sing-box 的 SOCKS5 出站没有 TLS 选项）")
		return
	}
	if !e.TLSEnabled {
		// 关掉 TLS 时一并清空附属字段，避免库里留下与实际行为不符的残值：
		// 下次有人打开开关会以为 SNI / 信任证书还生效。
		e.SNI, e.TLSCertID, e.TLSInsecure = "", 0, false
	}
	if e.TLSCertID != 0 {
		// 这里引用的是「信任锚」：用它校验上游代理的证书，面板不会把它发出去。
		// 悬空引用会让构建时退回系统根证书、握手失败，报错离原因很远，先拦掉。
		c, err := a.st.GetCert(e.TLSCertID)
		if err != nil || c == nil {
			fail(w, http.StatusBadRequest, "所选信任证书不存在")
			return
		}
		if c.DecryptFailed {
			fail(w, http.StatusBadRequest, "所选信任证书无法解密（QZ_SECRET_KEY 变更？）")
			return
		}
		// SNI 留空时取证书域名：证书是签给域名的，而代理地址通常是 IP，
		// 不补这一步握手必然报 "doesn't contain any IP SANs"。
		if e.SNI == "" {
			e.SNI = c.Domain
		}
	}
	// "***" 表示客户端未改动密码：保留库中原值。
	if e.Password == "***" && e.ID != 0 {
		if stored, _ := a.st.GetSbEgress(e.ID); stored != nil {
			e.Password = stored.Password
		}
	}
	newID, err := a.st.SaveSbEgress(&e)
	if err != nil {
		fail(w, http.StatusInternalServerError, "保存失败")
		return
	}
	// 凭据/地址变更影响所有引用此出口的服务器配置，全量重建兜底。
	a.sbRebuildLog()
	if saved, _ := a.st.GetSbEgress(newID); saved != nil {
		ok(w, egressJSON(saved, a.countEgressRefs()[saved.ID]))
		return
	}
	ok(w, nil)
}

// handleAdminCloneSbEgress duplicates an egress server-side. See
// store.CloneSbEgress for why this cannot be a client-side copy-and-POST.
//
// No sbRebuildLog: a fresh egress has no inbound pointing at it, so no
// generated config changes until the admin binds it — and that binding already
// triggers a rebuild.
func (a *API) handleAdminCloneSbEgress(w http.ResponseWriter, r *http.Request) {
	cloned, err := a.st.CloneSbEgress(atoi(chi.URLParam(r, "id")))
	if errors.Is(err, store.ErrEgressUndecryptable) {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "克隆失败")
		return
	}
	if cloned == nil {
		fail(w, http.StatusNotFound, "代理出口不存在")
		return
	}
	ok(w, egressJSON(cloned, 0))
}

// handleAdminParseEgressLink turns pasted vendor credentials into candidate
// egress rows. Read-only: nothing is stored, the client saves what it wants
// through the normal create endpoint so validation stays in one place.
//
// Multi-line input is parsed line by line and failures are reported per line
// rather than failing the batch: a vendor's mail with a stray header line
// should still yield the twenty proxies under it.
func (a *API) handleAdminParseEgressLink(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	lines := strings.Split(strings.ReplaceAll(body.Text, "\r\n", "\n"), "\n")
	// A paste is a human action; this only exists so a runaway paste can't turn
	// into an unbounded response. Reported back rather than trimmed quietly: a
	// silent cap is indistinguishable from "all of it was imported".
	const maxLines = 500
	truncated := 0
	if len(lines) > maxLines {
		truncated = len(lines) - maxLines
		lines = lines[:maxLines]
	}
	items := []map[string]interface{}{}
	errs := []map[string]interface{}{}
	for i, line := range lines {
		p, err := parseEgressLink(line)
		if errors.Is(err, errEgressLinkEmpty) {
			continue
		}
		if err != nil {
			errs = append(errs, J{"line": i + 1, "text": strings.TrimSpace(line), "error": err.Error()})
			continue
		}
		e := p.Egress
		if e.Name == "" {
			e.Name = egressLinkFallbackName(e)
		}
		// The password goes back in the clear, unlike every other egress
		// response. It is not a disclosure: it arrived in this request body
		// seconds ago, typed by the same admin, and the client needs it to POST
		// the row it is about to create. Nothing is read out of the database here.
		items = append(items, J{
			"name": e.Name, "type": e.Type, "host": e.Host, "port": e.Port,
			"username": e.Username, "password": e.Password,
			"tls_enabled": e.TLSEnabled, "sni": e.SNI,
			"type_guessed": p.TypeGuessed,
		})
	}
	if len(items) == 0 && len(errs) == 0 {
		fail(w, http.StatusBadRequest, "没有可解析的内容")
		return
	}
	ok(w, J{"items": items, "errors": errs, "truncated": truncated})
}

func (a *API) handleAdminDeleteSbEgress(w http.ResponseWriter, r *http.Request) {
	if err := a.st.DeleteSbEgress(int64(atoi(chi.URLParam(r, "id")))); err != nil {
		if errors.Is(err, store.ErrInUse) {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		fail(w, http.StatusInternalServerError, "删除失败")
		return
	}
	ok(w, nil)
}

// egressProxyURL renders an egress as a curl --proxy URL. socks uses socks5h so
// DNS resolves at the proxy (matching how sing-box dials it); a plaintext http
// proxy stays http, and a TLS one becomes https so curl wraps the hop the way
// sing-box will. Credentials are percent-encoded via url.URL.
//
// The second return is the extra curl flags the URL needs. The --resolve entry
// is the load-bearing one: curl validates the proxy's certificate against the
// host in the proxy URL and offers no flag to set a proxy's SNI, so a TLS
// egress dialed by IP has to carry its SNI hostname in the URL and be pointed
// back at the real address here. --resolve pre-populates curl's DNS cache, which
// the proxy connection shares — verified against curl 8.x, where the entry
// visibly redirects the proxy dial.
func egressProxyURL(e *store.SbEgress) (string, []string) {
	// Same gate as egressOutbound: TLS only ever applies to an http egress, so a
	// stale flag on a socks row can't reroute the check to the SNI hostname
	// while the scheme (correctly) stays socks5h.
	tlsOn := e.TLSEnabled && e.Type == "http"
	scheme := "http"
	switch {
	case e.Type == "socks":
		scheme = "socks5h"
	case tlsOn:
		scheme = "https"
	}
	host, port := e.Host, fmt.Sprintf("%d", e.Port)
	var extra []string
	// Swap in the SNI only when we can redirect it back to the dial address,
	// i.e. the address is a literal IP (--resolve takes an IP target). With a
	// hostname address the URL keeps it and curl validates against that name.
	if tlsOn && e.SNI != "" && e.SNI != e.Host && net.ParseIP(e.Host) != nil {
		extra = append(extra, "--resolve", e.SNI+":"+port+":"+e.Host)
		host = e.SNI
	}
	if tlsOn && e.TLSInsecure {
		extra = append(extra, "--proxy-insecure")
	}
	u := url.URL{Scheme: scheme, Host: net.JoinHostPort(host, port)}
	if e.Username != "" {
		u.User = url.UserPassword(e.Username, e.Password)
	}
	return u.String(), extra
}

// handleAdminTestSbEgress runs a live connectivity check THROUGH a proxy egress,
// executed on a node that will actually route through it (?server_id=), because
// many static-IP proxies allow only whitelisted client IPs — a test from the
// panel host would give a misleading result. Absent server_id, it auto-picks the
// first inbound bound to this egress; falling back to the panel host (0).
//
// ?n=N (2..32) switches to a concurrency probe: N connections at once instead of
// one, reporting how many survive. See egressProbeScript for why one-at-a-time
// cannot see the failure that matters here.
func (a *API) handleAdminTestSbEgress(w http.ResponseWriter, r *http.Request) {
	id := int64(atoi(chi.URLParam(r, "id")))
	eg, err := a.st.GetSbEgress(id)
	if err != nil || eg == nil {
		fail(w, http.StatusNotFound, "代理出口不存在")
		return
	}
	if eg.DecryptFailed {
		fail(w, http.StatusBadRequest, "该出口的密码无法解密（QZ_SECRET_KEY 变更？），请重新编辑保存后再测试")
		return
	}
	if a.sbctl == nil {
		fail(w, http.StatusServiceUnavailable, "sing-box 控制器未启用，无法测试")
		return
	}

	// Pick the node to test from.
	var serverID int64
	var picked bool
	if s := r.URL.Query().Get("server_id"); s != "" {
		serverID = int64(atoi(s))
		picked = true
	} else if ibs, _ := a.st.ListSbInbounds(); ibs != nil {
		for _, ib := range ibs {
			if ib.EgressID == id {
				serverID, picked = ib.ServerID, true
				break
			}
		}
	}
	viaName := "本机（面板）"
	if serverID != 0 {
		if sv, _ := a.st.GetServer(serverID); sv != nil {
			viaName = sv.Name
		}
	}

	// n>1 switches from "is it up" to "how many connections will it take at
	// once" — see handleAdminTestSbEgress's doc comment.
	concurrency := int(atoi(r.URL.Query().Get("n")))
	if concurrency > maxEgressProbeConns {
		concurrency = maxEgressProbeConns
	}

	proxy, extra := egressProxyURL(eg)
	var trustPEM string
	if eg.TLSEnabled && eg.Type == "http" && eg.TLSCertID != 0 {
		if c, _ := a.st.GetCert(eg.TLSCertID); c != nil && !c.DecryptFailed {
			trustPEM = c.CertPEM
		}
	}
	script := egressCheckPrelude(proxy, extra, trustPEM, concurrency > 1)
	if concurrency > 1 {
		script += egressProbeScript(concurrency)
	} else {
		script += egressSingleCheckScript()
	}

	// The whole budget, sized against the server's 30s WriteTimeout (main.go).
	// Past that the response is torn off the wire and the panel shows a transport
	// error for a check that had already reached a verdict — the same trap that
	// made 一键重装 report a false failure.
	//
	// Two things have to fit inside it, not one: this deadline plus
	// RunOnServer's WaitDelay, which it spends prying the pipes away from any
	// child that outlived the kill. 20+2 leaves 8s of headroom. The curl budgets
	// in the scripts above are chosen to land first, so this is the backstop
	// rather than the normal exit.
	const checkBudget = 20 * time.Second
	ctx, cancel := context.WithTimeout(r.Context(), checkBudget)
	defer cancel()
	out, runErr := a.sbctl.RunOnServer(ctx, serverID, script)
	out = strings.TrimSpace(out)

	resp := J{"via_server_id": serverID, "via_server": viaName, "auto_picked": !picked}
	if runErr != nil {
		resp["ok"] = false
		// "exit status 1" tells the admin nothing, and it is what a killed shell
		// reports. Name the actual cause when the budget is what ran out.
		if ctx.Err() != nil {
			out = fmt.Sprintf("检测超时（超过 %ds 未完成）——出口很可能完全无响应；%s",
				int(checkBudget.Seconds()), strings.TrimSpace(out))
		} else if out == "" {
			out = runErr.Error()
		}
		// By runes: the timeout message above is Chinese, so a byte cut can land
		// inside a character and the tail arrives at the panel as replacement
		// glyphs on top of an already-unhappy path.
		out = truncateRunesAPI(out, 500)
		resp["output"] = strings.TrimSpace(out)
		ok(w, resp)
		return
	}
	if concurrency > 1 {
		resp["mode"] = "probe"
		resp["requested"] = concurrency
		for k, v := range parseEgressProbeOutput(out) {
			resp[k] = v
		}
		ok(w, resp)
		return
	}
	// Success: last line is time_total (seconds), the rest is the exit IP.
	ip, latencyMs := out, 0
	if nl := strings.LastIndexByte(out, '\n'); nl >= 0 {
		ip = strings.TrimSpace(out[:nl])
		if f := parseFloat(strings.TrimSpace(out[nl+1:])); f > 0 {
			latencyMs = int(f * 1000)
		}
	}
	resp["ok"] = true
	resp["ip"] = ip
	resp["latency_ms"] = latencyMs
	ok(w, resp)
}

// maxEgressProbeConns caps the concurrency probe. Well above any plausible
// vendor limit, and low enough that N parallel curls on the node stay a
// diagnostic rather than a load test against someone else's service.
const maxEgressProbeConns = 32

// egressCheckPrelude emits the shell shared by both check modes: the proxy URL,
// the extra curl flags as "$@", and — only when something needs one — a private
// scratch dir removed on every exit path, holding the pinned trust anchor
// and/or the probe's per-connection results.
//
// The dir is conditional so a plain connectivity check keeps working on a node
// whose shell environment has no usable mktemp; that check needed no temp file
// before this change and must not start needing one.
//
// One trap, one directory: an earlier version set a second `trap ... EXIT` for
// the CA file, and in sh the later trap silently replaces the earlier one.
func egressCheckPrelude(proxy string, extra []string, trustPEM string, needScratch bool) string {
	s := `P=` + shellQuoteAPI(proxy) + `
set --`
	for _, arg := range extra {
		s += ` ` + shellQuoteAPI(arg)
	}
	s += "\n"
	hasPEM := strings.TrimSpace(trustPEM) != ""
	if !needScratch && !hasPEM {
		return s
	}
	s += `D=$(mktemp -d) || exit 1
trap 'rm -rf "$D"' EXIT
`
	// The heredoc is quoted so the PEM goes in verbatim, and the sentinel
	// contains underscores, which base64 never produces — no line of the PEM
	// can end it early.
	if hasPEM {
		s += `cat > "$D/ca.pem" <<'QZ_EGRESS_PEM_EOF'
` + strings.TrimRight(trustPEM, "\n") + `
QZ_EGRESS_PEM_EOF
set -- "$@" --proxy-cacert "$D/ca.pem"
`
	}
	return s
}

// egressSingleCheckScript curls an IP-echo service through the proxy, trying a
// few in case one is blocked; -w appends the total time. -f makes an HTTP error
// (e.g. 407 bad auth) a non-zero exit so we report failure rather than echoing
// an error page.
//
// -m 6 × 3 URLs = 18s worst case, inside the handler's 20s budget so the script
// reaches its own verdict instead of being killed mid-way. This is the endpoint
// that pronounces an egress up or down, so the per-URL budget is set as high as
// the deadline allows rather than as low as it can go: a measured fetch through
// a healthy proxy takes 1–3s, and calling a 5s one "不通" would be a false
// verdict on a working egress. (It was 10s before, which is what pushed the
// handler past WriteTimeout.)
func egressSingleCheckScript() string {
	return `for U in ` + egressEchoURLs + `; do
  OUT=$(curl -fsS -m 6 --proxy "$P" "$@" -w '\n%{time_total}' "$U" 2>&1) && { printf '%s' "$OUT"; exit 0; }
done
printf '%s' "$OUT" >&2; exit 1`
}

const egressEchoURLs = `https://api.ip.sb/ip https://ifconfig.me/ip https://ipinfo.io/ip`

// egressProbeScript opens n connections through the proxy at once and reports
// each outcome on its own line ("ok <seconds>" / "err <message>").
//
// A single sequential check cannot see the failure this is for. Static-IP
// vendors meter concurrent connections, and every inbound bound to an egress
// shares one account, so the limit is reached by ordinary browsing — one page
// opens dozens of sockets across a dozen domains — and the connections past the
// cap are dropped by the provider. What the user sees is a page that loads
// half-way while a long-lived app socket on the same tunnel is untroubled, and
// what the panel sees, checking one connection at a time, is a healthy egress.
//
// A warm-up picks the echo URL first so a blocked service isn't mistaken for a
// concurrency ceiling, and so all n connections are measuring the same thing.
//
// Budget: warm-up 3 × 4s, then one parallel round of 6s — 18s worst case,
// inside the handler's 20s. The results are written to files and collected
// after `wait`, not streamed, so interleaved writes can't split a line.
//
// The warm-up is stricter than the single check's 6s on purpose. Failing it is
// not a verdict on the egress — the message sends the admin to 测试连通, which
// is the endpoint that decides up-or-down and gets the looser budget.
func egressProbeScript(n int) string {
	return `U=''
for C in ` + egressEchoURLs + `; do
  curl -fsS -m 4 --proxy "$P" "$@" -o /dev/null "$C" && { U=$C; break; }
done
[ -n "$U" ] || { echo "预检失败：单条连接就不通，先用「测试连通」定位" >&2; exit 1; }
mkdir -p "$D/r"
i=1
while [ $i -le ` + strconv.Itoa(n) + ` ]; do
  (
    if OUT=$(curl -fsS -m 6 --proxy "$P" "$@" -o /dev/null -w '%{time_total}' "$U" 2>&1); then
      printf 'ok %s\n' "$OUT" > "$D/r/$i"
    else
      printf 'err %s\n' "$(printf '%s' "$OUT" | tr '\n' ' ' | cut -c1-120)" > "$D/r/$i"
    fi
  ) &
  i=$((i+1))
done
wait
cat "$D"/r/* 2>/dev/null`
}

// egressProbeError is one distinct failure message from a concurrency probe,
// with how many of the connections hit it.
type egressProbeError struct {
	Msg   string `json:"msg"`
	Count int    `json:"count"`
}

// parseEgressProbeOutput turns the probe's per-connection lines into the
// summary the UI renders. Reported fields are deliberately raw counts and
// latency spread rather than a verdict: a partial failure is the signal, and
// whether 3 failures out of 16 means "capped at 13" or "flaky" is a judgement
// for whoever is holding the vendor's contract.
func parseEgressProbeOutput(out string) map[string]interface{} {
	var lat []int
	okCount, failCount := 0, 0
	errCounts := map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "ok "):
			okCount++
			if f := parseFloat(strings.TrimSpace(line[3:])); f > 0 {
				lat = append(lat, int(f*1000))
			}
		case strings.HasPrefix(line, "err "):
			failCount++
			msg := strings.TrimSpace(line[4:])
			if msg == "" {
				msg = "未知错误"
			}
			errCounts[msg]++
		}
	}
	res := map[string]interface{}{
		"ok":         failCount == 0 && okCount > 0,
		"ok_count":   okCount,
		"fail_count": failCount,
	}
	if okCount+failCount == 0 {
		// The script exited 0 but said nothing — nothing to summarise, so hand
		// the raw output over rather than rendering a confident "0 / 0".
		res["output"] = strings.TrimSpace(out)
	}
	if len(lat) > 0 {
		sort.Ints(lat)
		res["latency_min_ms"] = lat[0]
		res["latency_p50_ms"] = lat[len(lat)/2]
		res["latency_max_ms"] = lat[len(lat)-1]
	}
	// Distinct messages, most frequent first — 16 copies of the same timeout is
	// one fact, not sixteen.
	errList := []egressProbeError{}
	for m, c := range errCounts {
		errList = append(errList, egressProbeError{Msg: m, Count: c})
	}
	sort.Slice(errList, func(i, j int) bool {
		if errList[i].Count != errList[j].Count {
			return errList[i].Count > errList[j].Count
		}
		return errList[i].Msg < errList[j].Msg
	})
	res["errors"] = errList
	return res
}

// handleAdminSbSyncStatus returns the last config-apply outcome per server, so
// the UI can show a sync badge after an async (non-blocking) rebuild. Keys are
// server ids as strings (0 = local panel, -1 = full rebuild).
func (a *API) handleAdminSbSyncStatus(w http.ResponseWriter, r *http.Request) {
	if a.sbctl == nil {
		ok(w, J{})
		return
	}
	out := map[string]interface{}{}
	for id, st := range a.sbctl.SyncStatuses() {
		out[fmt.Sprintf("%d", id)] = st
	}
	ok(w, out)
}

// handleAdminSbResync re-queues a config push after a failed sync, so a machine
// that failed for a transient reason (node rebooting, SSH hiccup) can be retried
// from the 链路拓扑 page instead of waiting for the next periodic pass or faking
// an edit. Body: {"server_id":N} — omit it to re-push every machine.
//
// Queued, never awaited: an SSH apply is bounded at 90s per node while the HTTP
// server's WriteTimeout is 30s, so doing it inline would report a torn
// connection ("下发失败") for pushes that actually succeeded. The caller polls
// /api/admin/sb/sync-status for the outcome.
func (a *API) handleAdminSbResync(w http.ResponseWriter, r *http.Request) {
	if a.sbctl == nil {
		fail(w, http.StatusServiceUnavailable, "sing-box 控制器未初始化")
		return
	}
	var req struct {
		ServerID *int64 `json:"server_id"`
	}
	// An empty body is a valid "re-push everything" request.
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.ServerID == nil {
		a.sbctl.ScheduleRebuild()
		ok(w, J{"queued": "all"})
		return
	}
	if *req.ServerID < 0 {
		fail(w, http.StatusBadRequest, "无效的服务器 ID")
		return
	}
	a.sbctl.ScheduleRebuildServer(*req.ServerID)
	ok(w, J{"queued": *req.ServerID})
}

// ---- sb_inbounds ----

func (a *API) handleAdminListSbInbounds(w http.ResponseWriter, r *http.Request) {
	list, err := a.st.ListSbInbounds()
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取失败")
		return
	}
	// 为每个入站附带当前用户数（按 tag 统计有权限的用户）
	usersByTag, _ := a.st.BuildUsersByTag(time.Now().Unix())
	out := make([]map[string]interface{}, 0, len(list))
	for _, n := range list {
		m := map[string]interface{}{
			"id":                  n.ID,
			"server_id":           n.ServerID,
			"type":                n.Type,
			"tag":                 n.Tag,
			"listen":              n.Listen,
			"listen_port":         n.ListenPort,
			"tls_id":              n.TlsID,
			"options":             n.Options,
			"enabled":             n.Enabled,
			"sort_order":          n.SortOrder,
			"upstream_inbound_id": n.UpstreamInboundID,
			"egress_id":           n.EgressID,
			"created_at":          n.CreatedAt,
			"updated_at":          n.UpdatedAt,
			"user_count":          len(usersByTag[n.Tag]),
		}
		out = append(out, m)
	}
	ok(w, out)
}

// handleAdminReorderSbInbounds persists a new display order for the inbound
// list. Body: {"ids":[...]} — inbound ids in the desired global order (the page
// groups by machine but sort_order is global, so the client submits the whole
// list). Only sort_order is rewritten, and no rebuild is triggered: order
// decides where an inbound appears in the generated `inbounds` array and nothing
// else — sing-box dispatches by listen port and tag — so the running nodes need
// no push. The next rebuild picks the new order up on its own.
func (a *API) handleAdminReorderSbInbounds(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.IDs) == 0 {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if err := a.st.ReorderSbInbounds(req.IDs); err != nil {
		fail(w, http.StatusInternalServerError, "保存排序失败")
		return
	}
	ok(w, J{"count": len(req.IDs)})
}

var sbInboundTypes = map[string]bool{"vless": true, "hysteria2": true, "tuic": true, "trojan": true, "vmess": true, "shadowsocks": true, "anytls": true, "hysteria": true, "mixed": true}

func (a *API) handleAdminSaveSbInbound(w http.ResponseWriter, r *http.Request) {
	var n store.SbInbound
	// On update, decode onto the stored row. SaveSbInbound writes every column,
	// including relay_secret — which is json:"-" and therefore CANNOT come from
	// the client. Decoding into a zero value blanked it on every save, including
	// a plain enable/disable toggle: the landing inbound then lazily minted a new
	// secret while the relay inbound's server kept dialling with the old one, so
	// the chain stayed down until some unrelated change rebuilt that server.
	// sort_order was lost the same way.
	if idStr := chi.URLParam(r, "id"); idStr != "" {
		cur, err := a.st.GetSbInbound(int64(atoi(idStr)))
		if err != nil || cur == nil {
			fail(w, http.StatusNotFound, "入站不存在")
			return
		}
		n = *cur
	}
	if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	n.Type = strings.TrimSpace(n.Type)
	n.Tag = strings.TrimSpace(n.Tag)
	if !sbInboundTypes[n.Type] {
		fail(w, http.StatusBadRequest, "不支持的协议类型")
		return
	}
	if n.Tag == "" || n.ListenPort <= 0 || n.ListenPort > 65535 {
		fail(w, http.StatusBadRequest, "tag 必填、端口需在 1-65535")
		return
	}
	// Tags are internal identifiers (sing-box config keys, relay tag suffixes,
	// the node→group join key); keep them whitespace-free so they stay greppable
	// in a config dump and unambiguous in logs. The displayed node name is a
	// separate field on the 节点 page and is free to contain spaces.
	if strings.ContainsAny(n.Tag, " \t\r\n") {
		fail(w, http.StatusBadRequest, "tag 不能包含空格")
		return
	}
	// A mixed (HTTP/SOCKS5) inbound is a plain proxy for tools like 1Panel/Docker,
	// not a circumvention protocol: it may carry a normal-cert TLS block (→ HTTPS
	// proxy) but never Reality, which HTTP/SOCKS clients cannot speak.
	if n.Type == "mixed" && n.TlsID != 0 {
		if tls, _ := a.st.GetSbTls(n.TlsID); tls != nil && strings.Contains(tls.ServerJSON, "\"reality\"") {
			fail(w, http.StatusBadRequest, "mixed 代理不支持 Reality，请选普通 TLS 证书或留空")
			return
		}
	}
	// 端口冲突检测：同服务器同端口不允许重复
	if id := chi.URLParam(r, "id"); id != "" {
		n.ID = atoi(id)
	}
	if conflict, existingTag, err := a.st.SbInboundPortConflict(&n); err != nil {
		fail(w, http.StatusInternalServerError, "端口冲突检测失败")
		return
	} else if conflict {
		fail(w, http.StatusBadRequest, fmt.Sprintf("端口 %d 已被入站「%s」占用", n.ListenPort, existingTag))
		return
	}
	if n.Options != "" {
		var opts map[string]interface{}
		if err := json.Unmarshal([]byte(n.Options), &opts); err != nil {
			fail(w, http.StatusBadRequest, "options 不是合法 JSON")
			return
		}
		// Shadowsocks-2022 needs a server PSK; generate one if absent.
		if n.Type == "shadowsocks" {
			method, _ := opts["method"].(string)
			if pw, _ := opts["password"].(string); pw == "" {
				key := make([]byte, singbox.SSKeyLen(method))
				if _, err := rand.Read(key); err != nil {
					fail(w, http.StatusInternalServerError, "生成密钥失败")
					return
				}
				opts["password"] = base64.StdEncoding.EncodeToString(key)
				if b, err := json.Marshal(opts); err == nil {
					n.Options = string(b)
				}
			}
		}
	}
	// 落地中转与第三方代理出口是两种互斥的出网方式。
	if n.UpstreamInboundID != 0 && n.EgressID != 0 {
		fail(w, http.StatusBadRequest, "落地入站与代理出口只能二选一")
		return
	}
	if n.EgressID != 0 {
		eg, err := a.st.GetSbEgress(n.EgressID)
		if err != nil || eg == nil {
			fail(w, http.StatusBadRequest, "所选代理出口不存在")
			return
		}
	}
	// Relay validation: the upstream must be an existing, different inbound.
	// Multi-hop chains (落地机自己再挂下一跳落地/出口) are allowed — per-server
	// wiring handles them naturally — but the chain must not loop back onto this
	// inbound, or traffic would circulate between machines forever.
	if n.UpstreamInboundID != 0 {
		if n.UpstreamInboundID == n.ID {
			fail(w, http.StatusBadRequest, "落地入站不能是自己")
			return
		}
		up, err := a.st.GetSbInbound(n.UpstreamInboundID)
		if err != nil || up == nil {
			fail(w, http.StatusBadRequest, "所选落地入站不存在")
			return
		}
		seen := map[int64]bool{n.ID: true}
		for cur := up; cur != nil; {
			if seen[cur.ID] || len(seen) > 16 {
				fail(w, http.StatusBadRequest, "落地链路存在环路（或层级过深），流量会在机器间死循环")
				return
			}
			seen[cur.ID] = true
			if cur.UpstreamInboundID == 0 {
				break
			}
			cur, _ = a.st.GetSbInbound(cur.UpstreamInboundID)
		}
	}
	// Capture the pre-save upstream so a rebuild also reaches the OLD landing
	// server when the upstream is changed or cleared.
	var oldUpstream int64
	if n.ID != 0 {
		if prev, _ := a.st.GetSbInbound(n.ID); prev != nil {
			oldUpstream = prev.UpstreamInboundID
		}
	}
	newID, err := a.st.SaveSbInbound(&n)
	if err != nil {
		fail(w, http.StatusInternalServerError, "保存失败（tag 可能重复）")
		return
	}
	// Push config asynchronously: a synchronous per-server SSH push can exceed the
	// reverse proxy timeout on a slow/unreachable node, and the admin then sees a
	// save error even though the row is committed (「第一次报错第二次成功」). The
	// scheduler applies in the background; the 入站/拓扑 page polls sync status.
	// A relay inbound's landing lives on another server that must also rebuild to
	// inject (or drop) the relay credential in its users[].
	a.sbScheduleServer(append([]int64{n.ServerID}, a.landingServerIDs(n.UpstreamInboundID, oldUpstream)...)...)
	saved, _ := a.st.GetSbInbound(newID)
	ok(w, saved)
}

// landingServerIDs maps landing inbound id(s) to the server(s) hosting them, so
// those servers can be rebuilt to pick up (or drop) the relay credential in
// their users[]. Zero ids are ignored.
func (a *API) landingServerIDs(landingIDs ...int64) []int64 {
	var out []int64
	for _, id := range landingIDs {
		if id == 0 {
			continue
		}
		if up, _ := a.st.GetSbInbound(id); up != nil {
			out = append(out, up.ServerID)
		}
	}
	return out
}

// POST /api/admin/sb/inbounds/{id}/ack-upstream — dismiss the "landing deleted,
// now exiting from this machine" warning without changing the config. The other
// resolution is to re-point the inbound, which clears the flag on save; this one
// is for the admin who looked and decided the direct exit is fine.
func (a *API) handleAdminAckUpstreamBroken(w http.ResponseWriter, r *http.Request) {
	id := atoi(chi.URLParam(r, "id"))
	if id <= 0 {
		fail(w, http.StatusBadRequest, "无效的入站 id")
		return
	}
	if err := a.st.AckUpstreamBroken(id); err != nil {
		fail(w, http.StatusInternalServerError, "操作失败")
		return
	}
	ok(w, nil)
}

func (a *API) handleAdminDeleteSbInbound(w http.ResponseWriter, r *http.Request) {
	inboundID := int64(atoi(chi.URLParam(r, "id")))
	// 先查询归属服务器，删除后只重建该服务器，避免影响其他服务器。
	ib, _ := a.st.GetSbInbound(inboundID)
	// relayServers host the inbounds that pointed at this one as their landing;
	// DeleteSbInbound un-chained them, so they must rebuild to drop the upstream
	// outbound that dialed the now-deleted inbound.
	relayServers, err := a.st.DeleteSbInbound(inboundID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "删除失败")
		return
	}
	if ib != nil {
		// If this was a relay inbound, its landing server must also rebuild to drop
		// the now-unused relay credential. Async — see handleAdminSaveSbInbound.
		ids := append([]int64{ib.ServerID}, a.landingServerIDs(ib.UpstreamInboundID)...)
		a.sbScheduleServer(append(ids, relayServers...)...)
	} else {
		// 查不到归属服务器（异常情况）时全量重建兜底，避免被删的 inbound
		// 残留在某个运行中的配置里。
		a.sbRebuildLog()
	}
	ok(w, nil)
}

// buildPreviewConfig renders the sing-box config for a specific server
// (serverID <= 0 = local). Shared by the preview and correctness-check
// endpoints so both validate exactly what they display.
func (a *API) buildPreviewConfig(serverID int64) ([]byte, error) {
	byTag, err := a.st.BuildUsersByTag(nowUnix())
	if err != nil {
		return nil, fmt.Errorf("构建用户映射失败: %w", err)
	}
	base, _ := a.st.GetSetting("sb_base_config")
	if base == "" {
		base = singbox.DefaultBaseConfig
	}
	listen, _ := a.st.GetSetting("sb_v2ray_listen")
	if listen == "" {
		listen = "127.0.0.1:18080"
	}
	if serverID > 0 {
		return a.st.BuildSingboxConfigForServer(serverID, base, listen, byTag)
	}
	return a.st.BuildSingboxConfig(base, listen, byTag)
}

// GET /api/admin/sb/preview?server_id=N — render the sing-box config for a
// specific server (server_id=0 or omitted = local).
func (a *API) handleAdminSbPreview(w http.ResponseWriter, r *http.Request) {
	cfg, err := a.buildPreviewConfig(atoi(r.URL.Query().Get("server_id")))
	if err != nil {
		fail(w, http.StatusInternalServerError, "生成配置失败")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(cfg)
}

// GET /api/admin/sb/check?server_id=N — generate the config for a server and
// validate it with `sing-box check`, the same gate Apply uses before shipping.
// Exposed read-only so the admin can catch a bad config before it goes live.
// Always returns 200 with {ok,stage,output,...}; ok=false means "config has a
// problem", not "the request failed".
func (a *API) handleAdminSbCheck(w http.ResponseWriter, r *http.Request) {
	serverID := atoi(r.URL.Query().Get("server_id"))
	cfg, err := a.buildPreviewConfig(serverID)
	if err != nil {
		ok(w, J{"ok": false, "stage": "generate", "output": "生成配置失败：" + err.Error()})
		return
	}

	// Structural sanity + a couple of panel-level lints that don't need the
	// binary (empty inbounds is a common "why is my node dead" cause).
	var doc struct {
		Inbounds []struct {
			Tag        string `json:"tag"`
			ListenPort int    `json:"listen_port"`
		} `json:"inbounds"`
		Outbounds []json.RawMessage `json:"outbounds"`
	}
	if err := json.Unmarshal(cfg, &doc); err != nil {
		ok(w, J{"ok": false, "stage": "json", "output": "生成的配置不是合法 JSON：" + err.Error()})
		return
	}
	var warnings []string
	if len(doc.Inbounds) == 0 {
		warnings = append(warnings, "该机器没有任何入站，inbounds 为空（配置能通过校验但不会提供任何服务）。")
	}
	seen := map[int]string{}
	for _, ib := range doc.Inbounds {
		if ib.ListenPort == 0 {
			continue
		}
		if prev, dup := seen[ib.ListenPort]; dup {
			warnings = append(warnings, fmt.Sprintf("端口 %d 被多个入站占用（%s、%s），启动时会冲突。", ib.ListenPort, prev, ib.Tag))
		}
		seen[ib.ListenPort] = ib.Tag
	}

	base := J{"inbounds": len(doc.Inbounds), "outbounds": len(doc.Outbounds), "warnings": warnings}

	bin := sbproc.FindSingBoxBin()
	if bin == "" {
		base["ok"] = len(warnings) == 0
		base["stage"] = "no-binary"
		base["output"] = "未找到 sing-box 可执行文件，仅校验了 JSON 结构（未做 sing-box check）。可设置 QZ_SINGBOX_BIN 或把 sing-box 装到 PATH 后重试。"
		ok(w, base)
		return
	}

	tmp, err := os.CreateTemp("", "qz-precheck-*.json")
	if err != nil {
		fail(w, http.StatusInternalServerError, "创建临时文件失败")
		return
	}
	path := tmp.Name()
	defer os.Remove(path)
	if _, err := tmp.Write(cfg); err != nil {
		tmp.Close()
		fail(w, http.StatusInternalServerError, "写入临时文件失败")
		return
	}
	tmp.Close()

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	out, cerr := exec.CommandContext(ctx, bin, "check", "-c", path).CombinedOutput()
	base["stage"] = "check"
	if cerr != nil {
		base["ok"] = false
		base["output"] = strings.TrimSpace(string(out) + "\n" + cerr.Error())
		ok(w, base)
		return
	}
	base["ok"] = len(warnings) == 0
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		msg = "sing-box check 通过，配置合法。"
	}
	base["output"] = msg
	ok(w, base)
}

func nowUnix() int64 { return time.Now().Unix() }

// GET /api/admin/sb/port-check?server_id=N&port=443
// 测试入站端口是否对外开放（TCP 握手）。server_id=0 表示本机（测 127.0.0.1），
// 远程服务器测其 host:port。
func (a *API) handleAdminPortCheck(w http.ResponseWriter, r *http.Request) {
	port := atoi(r.URL.Query().Get("port"))
	if port <= 0 || port > 65535 {
		fail(w, http.StatusBadRequest, "端口需在 1-65535")
		return
	}
	serverID := atoi(r.URL.Query().Get("server_id"))
	host := "127.0.0.1"
	if serverID > 0 {
		sv, err := a.st.GetServer(serverID)
		if err != nil || sv == nil {
			fail(w, http.StatusNotFound, "服务器不存在")
			return
		}
		host = sv.Host
		if host == "" {
			fail(w, http.StatusBadRequest, "该服务器未配置主机地址")
			return
		}
	}
	// net.JoinHostPort brackets IPv6 literals correctly ("[::1]:443"); a plain
	// "%s:%d" would produce an unparseable address for an IPv6 host.
	addr := net.JoinHostPort(host, itoa(port))
	t0 := time.Now()
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	ms := time.Since(t0).Seconds() * 1000
	if err != nil {
		ok(w, J{"reachable": false, "addr": addr, "ms": ms, "error": err.Error()})
		return
	}
	_ = conn.Close()
	ok(w, J{"reachable": true, "addr": addr, "ms": ms})
}

// GET /api/admin/sb/import-remote/list-files?server_id=N — SSH into the remote
// server and list .json config files in common sing-box directories.
func (a *API) handleAdminImportRemoteListFiles(w http.ResponseWriter, r *http.Request) {
	serverID := atoi(r.URL.Query().Get("server_id"))
	if serverID <= 0 {
		fail(w, http.StatusBadRequest, "server_id 必填")
		return
	}
	sv, err := a.st.GetServer(serverID)
	if err != nil || sv == nil {
		fail(w, http.StatusNotFound, "服务器不存在")
		return
	}

	rm := a.newRemoteManager(15 * time.Second)
	cfg := sbctl.SSHConfigFor(sv)

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	files, err := rm.ListRemoteConfigFiles(ctx, cfg)
	if err != nil {
		fail(w, http.StatusBadGateway, "扫描远程文件失败: "+err.Error())
		return
	}

	ok(w, J{"files": files})
}

// GET /api/admin/sb/import-remote/preview?server_id=N&config_path=xxx — SSH
// into the remote server, read the specified config file, and return the
// inbounds array for the admin to review before importing.
func (a *API) handleAdminImportRemotePreview(w http.ResponseWriter, r *http.Request) {
	serverID := atoi(r.URL.Query().Get("server_id"))
	configPath := strings.TrimSpace(r.URL.Query().Get("config_path"))
	if serverID <= 0 {
		fail(w, http.StatusBadRequest, "server_id 必填")
		return
	}
	if configPath == "" {
		fail(w, http.StatusBadRequest, "config_path 必填")
		return
	}
	sv, err := a.st.GetServer(serverID)
	if err != nil || sv == nil {
		fail(w, http.StatusNotFound, "服务器不存在")
		return
	}

	rm := a.newRemoteManager(15 * time.Second)
	cfg := sbctl.SSHConfigFor(sv)

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	raw, err := rm.ReadRemoteConfigAtPath(ctx, cfg, configPath)
	if err != nil {
		fail(w, http.StatusBadGateway, "读取远程配置失败: "+err.Error())
		return
	}

	// Parse the inbounds from the raw config
	var full struct {
		Inbounds []json.RawMessage `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &full); err != nil {
		fail(w, http.StatusBadGateway, "远程配置 JSON 解析失败: "+err.Error())
		return
	}
	if len(full.Inbounds) == 0 {
		fail(w, http.StatusBadGateway, "远程配置中未找到入站协议")
		return
	}

	// Extract type, tag, listen_port from each inbound for display
	type inboundInfo struct {
		Type       string          `json:"type"`
		Tag        string          `json:"tag"`
		ListenPort int             `json:"listen_port"`
		Raw        json.RawMessage `json:"raw"`
	}
	var inbounds []inboundInfo
	for _, ib := range full.Inbounds {
		var meta struct {
			Type       string `json:"type"`
			Tag        string `json:"tag"`
			Listen     string `json:"listen"`
			ListenPort int    `json:"listen_port"`
		}
		_ = json.Unmarshal(ib, &meta)
		inbounds = append(inbounds, inboundInfo{
			Type: meta.Type, Tag: meta.Tag, ListenPort: meta.ListenPort, Raw: ib,
		})
	}

	ok(w, J{
		"server_id":   serverID,
		"server_name": sv.Name,
		"config_path": configPath,
		"inbounds":    inbounds,
	})
}
