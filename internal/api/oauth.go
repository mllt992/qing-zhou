package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"qingzhou/internal/auth"
	"qingzhou/internal/idgen"
	"qingzhou/internal/store"
)

const oauthCallbackPath = "/api/auth/oauth2/callback"
const oauthCookie = "__Host-qz_oauth"

// Each flow needs its own browser proof: tabs share cookies, so a single name
// lets a later start (or another callback's cleanup) invalidate an earlier one.
func oauthStateCookie(rawState string) string { return oauthCookie + "_" + oauthHash(rawState) }

type oauthConfig struct {
	Enabled      bool   `json:"enabled"`
	Name         string `json:"name"`
	Issuer       string `json:"issuer"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RedirectURL  string `json:"redirect_url"`
	AutoRegister bool   `json:"auto_register"`
}

func (a *API) oauthConfig() (oauthConfig, error) {
	c := oauthConfig{Name: "认证中心"}
	s, err := a.st.GetSetting("oauth2_config")
	if err != nil {
		return c, err
	}
	if s != "" {
		err = json.Unmarshal([]byte(s), &c)
	}
	return c, err
}

func oauthURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || len(raw) > 2048 {
		return nil, fmt.Errorf("请填写完整的 HTTPS 地址，不含查询参数或片段")
	}
	return u, nil
}
func (c *oauthConfig) validate() error {
	c.Name = strings.TrimSpace(c.Name)
	c.Issuer = strings.TrimRight(strings.TrimSpace(c.Issuer), "/")
	c.ClientID = strings.TrimSpace(c.ClientID)
	c.RedirectURL = strings.TrimSpace(c.RedirectURL)
	if c.Name == "" {
		c.Name = "认证中心"
	}
	if len(c.Name) > 80 || len(c.ClientID) > 256 || len(c.ClientSecret) > 4096 {
		return fmt.Errorf("配置内容过长")
	}
	if !c.Enabled && c.Issuer == "" && c.RedirectURL == "" {
		return nil
	}
	if _, err := oauthURL(c.Issuer); err != nil {
		return err
	}
	u, err := oauthURL(c.RedirectURL)
	if err != nil {
		return err
	}
	if u.Path != oauthCallbackPath || u.RawPath != "" {
		return fmt.Errorf("回调地址必须以 %s 结尾", oauthCallbackPath)
	}
	if c.ClientID == "" {
		return fmt.Errorf("请填写 Client ID")
	}
	return nil
}
func oauthHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return base64.RawURLEncoding.EncodeToString(h[:])
}
func (c oauthConfig) hash() string { b, _ := json.Marshal(c); return oauthHash(string(b)) }
func oauthMasked(c oauthConfig) oauthConfig {
	if c.ClientSecret != "" {
		c.ClientSecret = "***"
	}
	return c
}

func (a *API) requireOAuthAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := a.currentUser(r)
		if u == nil || u.Status != "active" || u.Role != "admin" {
			fail(w, http.StatusForbidden, "需要有效的管理员账号")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) handleGetOAuth(w http.ResponseWriter, r *http.Request) {
	c, err := a.oauthConfig()
	if err != nil {
		fail(w, 500, "读取认证配置失败")
		return
	}
	ok(w, oauthMasked(c))
}
func (a *API) readOAuthConfig(r *http.Request) (oauthConfig, error) {
	var c oauthConfig
	d := json.NewDecoder(io.LimitReader(r.Body, 16385))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, fmt.Errorf("配置格式错误")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return c, fmt.Errorf("配置格式错误")
	}
	if c.ClientSecret == "***" {
		old, err := a.oauthConfig()
		if err != nil {
			return c, err
		}
		c.ClientSecret = old.ClientSecret
	}
	return c, c.validate()
}
func (a *API) handlePutOAuth(w http.ResponseWriter, r *http.Request) {
	c, err := a.readOAuthConfig(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	b, _ := json.Marshal(c)
	if err = a.st.SetSetting("oauth2_config", string(b)); err != nil {
		fail(w, 500, "保存认证配置失败")
		return
	}
	ok(w, oauthMasked(c))
}

// Every discovery/JWKS/token/userinfo request is bounded and confined to the
// configured issuer origin. Redirects cannot forward client credentials.
type oauthTransport struct {
	base   http.RoundTripper
	origin string
}

func (t oauthTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme+"://"+r.URL.Host != t.origin || r.URL.User != nil {
		return nil, fmt.Errorf("认证端点必须与 Issuer 同源")
	}
	res, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	res.Body.Close()
	if err != nil {
		return nil, err
	}
	if len(b) > 1<<20 {
		return nil, fmt.Errorf("认证响应过大")
	}
	res.Body = io.NopCloser(bytes.NewReader(b))
	return res, nil
}
func (a *API) discoverOAuth(ctx context.Context, c oauthConfig) (context.Context, *oidc.Provider, *oauth2.Config, error) {
	u, err := oauthURL(c.Issuer)
	if err != nil {
		return ctx, nil, nil, err
	}
	client := &http.Client{Transport: oauthTransport{a.sourceClient.Transport, u.Scheme + "://" + u.Host}, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx = oidc.ClientContext(ctx, client)
	p, err := oidc.NewProvider(ctx, c.Issuer)
	if err != nil {
		return ctx, nil, nil, err
	}
	var meta struct {
		Authorization string   `json:"authorization_endpoint"`
		Token         string   `json:"token_endpoint"`
		JWKS          string   `json:"jwks_uri"`
		UserInfo      string   `json:"userinfo_endpoint"`
		PKCE          []string `json:"code_challenge_methods_supported"`
	}
	if err = p.Claims(&meta); err != nil {
		return ctx, nil, nil, err
	}
	for _, raw := range []string{meta.Authorization, meta.Token, meta.JWKS, meta.UserInfo} {
		v, e := oauthURL(raw)
		if e != nil || v.Scheme != u.Scheme || v.Host != u.Host {
			return ctx, nil, nil, fmt.Errorf("认证端点必须是与 Issuer 同源的 HTTPS 地址")
		}
	}
	pkce := false
	for _, method := range meta.PKCE {
		if method == "S256" {
			pkce = true
		}
	}
	if !pkce {
		return ctx, nil, nil, fmt.Errorf("认证中心须支持 PKCE S256")
	}
	ep := p.Endpoint()
	ep.AuthStyle = oauth2.AuthStyleInHeader
	if c.ClientSecret == "" {
		ep.AuthStyle = oauth2.AuthStyleInParams
	}
	return ctx, p, &oauth2.Config{ClientID: c.ClientID, ClientSecret: c.ClientSecret, RedirectURL: c.RedirectURL, Endpoint: ep, Scopes: []string{oidc.ScopeOpenID, "profile", "email"}}, nil
}
func (a *API) oauthSlot(ctx context.Context) bool {
	select {
	case a.oauthSlots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}
func (a *API) handleTestOAuth(w http.ResponseWriter, r *http.Request) {
	c, err := a.readOAuthConfig(r)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if !a.oauthSlot(r.Context()) {
		return
	}
	defer func() { <-a.oauthSlots }()
	_, _, _, err = a.discoverOAuth(r.Context(), c)
	if err != nil {
		fail(w, 400, "无法验证 OIDC discovery，请检查 Issuer、公开 HTTPS 端点和 PKCE S256 支持")
		return
	}
	ok(w, J{"message": "OIDC discovery 验证成功；客户端密钥需通过实际登录验证"})
}

func (a *API) handleOAuthStart(w http.ResponseWriter, r *http.Request) { a.startOAuth(w, r, false) }
func (a *API) handleOAuthBind(w http.ResponseWriter, r *http.Request)  { a.startOAuth(w, r, true) }
func (a *API) startOAuth(w http.ResponseWriter, r *http.Request, bind bool) {
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" {
		fail(w, 415, "请使用 JSON 请求")
		return
	}
	c, err := a.oauthConfig()
	if err != nil || !c.Enabled || c.validate() != nil {
		fail(w, 400, "认证中心尚未启用")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	state := store.OAuthState{ConfigHash: c.hash(), ExpiresAt: time.Now().Add(10 * time.Minute).Unix()}
	if bind {
		u := a.currentUser(r)
		if u == nil {
			fail(w, 401, "请先登录")
			return
		}
		if !a.pwRL.allow(itoa(u.ID)) {
			fail(w, 429, "请稍后再试")
			return
		}
		if !auth.CheckPassword(u.PasswordHash, req.Password) {
			fail(w, 403, "当前密码错误")
			return
		}
		state.UserID = u.ID
		state.SessionID, _ = r.Context().Value(ctxJti).(string)
		if state.SessionID == "" {
			fail(w, 403, "请使用浏览器登录会话")
			return
		}
	}
	if !a.oauthSlot(r.Context()) {
		return
	}
	defer func() { <-a.oauthSlots }()
	_, _, oc, err := a.discoverOAuth(r.Context(), c)
	if err != nil {
		fail(w, 502, "认证中心连接失败，请联系管理员检查配置")
		return
	}
	values := make([]string, 4)
	for i := range values {
		values[i], err = idgen.RandToken(32)
		if err != nil {
			fail(w, 500, "生成登录状态失败")
			return
		}
	}
	state.StateHash = oauthHash(values[0])
	state.BrowserHash = oauthHash(values[1])
	state.Nonce = values[2]
	state.Verifier = values[3]
	if err = a.st.CreateOAuthState(r.Context(), state); err != nil {
		fail(w, 503, "暂时无法发起认证，请稍后再试")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oauthStateCookie(values[0]), Value: values[1], Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: 600})
	w.Header().Set("Cache-Control", "no-store")
	ok(w, J{"authorization_url": oc.AuthCodeURL(values[0], oidc.Nonce(state.Nonce), oauth2.S256ChallengeOption(state.Verifier))})
}

func (a *API) handleUserOAuth(w http.ResponseWriter, r *http.Request) {
	c, err := a.oauthConfig()
	if err != nil {
		fail(w, 500, "读取认证配置失败")
		return
	}
	u := a.currentUser(r)
	if u == nil {
		fail(w, 401, "请先登录")
		return
	}
	binding, err := a.st.OAuthIdentityForUser(u.ID, c.Issuer)
	if err != nil {
		fail(w, 500, "读取绑定失败")
		return
	}
	ok(w, J{"enabled": c.Enabled, "name": c.Name, "bound": binding != nil})
}

func (a *API) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	c, err := a.oauthConfig()
	if err != nil || !c.Enabled || c.validate() != nil {
		fail(w, 400, "认证中心尚未启用，请返回首页重新登录")
		return
	}
	u, _ := url.Parse(c.RedirectURL)
	dest := u.Scheme + "://" + u.Host + "/#/oauth2/callback"
	finish := func(code string) { http.Redirect(w, r, dest+"?error="+code, http.StatusSeeOther) }
	raw := r.URL.Query().Get("state")
	if len(raw) != 43 {
		finish("state")
		return
	}
	cookie, err := r.Cookie(oauthStateCookie(raw))
	if errors.Is(err, http.ErrNoCookie) {
		// Allow a login started before an upgrade to finish. The database still
		// checks the exact browser hash, expiry and single use for this state.
		cookie, err = r.Cookie(oauthCookie)
	}
	if err != nil || len(cookie.Value) != 43 {
		finish("state")
		return
	}
	state, err := a.st.ConsumeOAuthState(r.Context(), oauthHash(raw), oauthHash(cookie.Value))
	if err != nil || state.ConfigHash != c.hash() {
		finish("state")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookie.Name, Value: "", Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	if r.URL.Query().Get("error") != "" {
		finish("denied")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" || len(code) > 4096 {
		finish("state")
		return
	}
	if !a.oauthSlot(r.Context()) {
		return
	}
	defer func() { <-a.oauthSlots }()
	ctx, p, oc, err := a.discoverOAuth(r.Context(), c)
	if err != nil {
		finish("provider")
		return
	}
	tok, err := oc.Exchange(ctx, code, oauth2.VerifierOption(state.Verifier))
	if err != nil {
		finish("provider")
		return
	}
	rawID, _ := tok.Extra("id_token").(string)
	id, err := p.Verifier(&oidc.Config{ClientID: c.ClientID, SupportedSigningAlgs: []string{oidc.RS256}}).Verify(ctx, rawID)
	if err != nil || id.Nonce != state.Nonce || id.Subject == "" || len(id.Subject) > 255 {
		finish("provider")
		return
	}
	if id.AccessTokenHash != "" && id.VerifyAccessToken(tok.AccessToken) != nil {
		finish("provider")
		return
	}
	info, err := p.UserInfo(ctx, oauth2.StaticTokenSource(tok))
	if err != nil || info.Subject != id.Subject {
		finish("provider")
		return
	}
	if state.UserID != 0 {
		ck, e := r.Cookie(cookieName)
		if e != nil {
			finish("session")
			return
		}
		claims, e := auth.Parse(a.secret, ck.Value)
		if e != nil || claims.UserID != state.UserID || claims.ID != state.SessionID || !a.st.SessionValid(claims.ID) {
			finish("session")
			return
		}
		if a.st.BindOAuthIdentity(ctx, state.UserID, c.Issuer, id.Subject) != nil {
			finish("binding")
			return
		}
		http.Redirect(w, r, dest+"?bound=1", http.StatusSeeOther)
		return
	}
	// Serialize only local provisioning, never provider I/O. Retrying an interrupted
	// first login reuses the durable account/credentials and idempotent grants.
	select {
	case a.oauthFinalize <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-a.oauthFinalize }()
	var nu *store.NewUser
	verified := info.EmailVerified && info.Email != "" && len(info.Email) <= 254
	if c.AutoRegister && a.registerMode() == "open" {
		require, _ := a.st.GetSettingBool("email_verify_required")
		if !require || verified {
			suffix, e := idgen.RandHex(12)
			if e != nil {
				finish("server")
				return
			}
			sub, e := idgen.RandToken(24)
			if e != nil {
				finish("server")
				return
			}
			bonus, _ := a.st.GetSettingInt64("signup_bonus_points", 0)
			// No usable local password is assigned. Verified email password recovery
			// can establish one later; bcrypt rejects this explicit unset marker.
			nu = &store.NewUser{Username: "oauth_" + suffix, PasswordHash: "!oauth2", Points: bonus, SubToken: sub}
			if verified {
				nu.Email = info.Email
			}
		}
	}
	cr, err := idgen.NewCredentials()
	if err != nil {
		finish("server")
		return
	}
	uid, provision, err := a.st.OAuthLoginUser(ctx, c.Issuer, id.Subject, nu, verified, cr.UUID, cr.Password)
	if errors.Is(err, store.ErrOAuthEmailExists) {
		finish("email_exists")
		return
	}
	if errors.Is(err, store.ErrOAuthSignupClosed) {
		finish("registration")
		return
	}
	if err != nil {
		finish("server")
		return
	}
	user, err := a.st.UserByID(uid)
	if err != nil || user == nil || user.Status != "active" {
		finish("account")
		return
	}
	if provision {
		if err = a.provisionOAuth(user); err != nil {
			finish("server")
			return
		}
		if err = a.st.MarkOAuthProvisioned(ctx, c.Issuer, id.Subject); err != nil {
			finish("server")
			return
		}
		a.sbRebuildLog()
	}
	if _, err = a.issueLoginWithSecurity(w, r, user, true); err != nil {
		finish("server")
		return
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

func (a *API) provisionOAuth(u *store.User) error {
	if err := a.st.EnsurePoolBucket(u.ID, u.ClientName.String, u.ClientUUID.String, u.ClientSecret.String); err != nil {
		return err
	}
	if err := a.st.EnsureFreeBucket(u.ID, u.Username); err != nil {
		return err
	}
	if err := a.st.EnsureProxyAccount(u.ID); err != nil {
		return err
	}
	traffic, _ := a.st.GetSettingInt64("default_traffic", 10<<30)
	days, _ := a.st.GetSettingInt64("default_expiry_days", 30)
	expiry := int64(0)
	if days > 0 {
		expiry = u.CreatedAt + days*86400
	}
	return a.st.EnsureWelcomeBucket(u.ID, u.Username, traffic, expiry)
}
