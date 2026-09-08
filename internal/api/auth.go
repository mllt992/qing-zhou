package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"qingzhou/internal/auth"
	"qingzhou/internal/idgen"
	"qingzhou/internal/store"
	"qingzhou/internal/version"
)

type ctxKey string

const (
	ctxUserID   ctxKey = "uid"
	ctxRole     ctxKey = "role"
	ctxJti      ctxKey = "jti"
	ctxAuthKind ctxKey = "auth_kind" // "jwt" | "api_token"
	ctxScopes   ctxKey = "scopes"    // []string when auth_kind=api_token
	ctxTokenID  ctxKey = "token_id"

	cookieName = "qz_token"
	tokenTTL   = 7 * 24 * time.Hour
)

// clientIP returns the caller's source IP. Forwarded headers (set by a reverse
// proxy) are only honored when the direct peer is a trusted proxy; otherwise the
// real socket peer is used so an untrusted client cannot spoof its IP.
func clientIP(r *http.Request) string {
	peer := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		peer = host
	}
	if peerTrusted(r) {
		if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
			return xr
		}
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
				return first
			}
		}
	}
	return peer
}

// issueLogin records a login session and returns a JWT bound to it (so it can
// be listed and revoked). Also sets the auth cookie.
func (a *API) issueLogin(w http.ResponseWriter, r *http.Request, u *store.User) (string, error) {
	jti, err := idgen.RandToken(12)
	if err != nil {
		return "", err
	}
	if err := a.st.CreateSession(u.ID, jti, clientIP(r), r.UserAgent()); err != nil {
		return "", err
	}
	tok, err := auth.Issue(a.secret, u.ID, u.Role, jti, tokenTTL)
	if err != nil {
		return "", err
	}
	setAuthCookie(w, tok, a.isHTTPS(r))
	return tok, nil
}

func (a *API) handleHealth(w http.ResponseWriter, r *http.Request) {
	ok(w, J{"status": "ok", "time": time.Now().Unix(), "version": version.Current()})
}

// registerMode is "open" (anyone) | "code" (needs reg code) | "closed".
// Falls back to the legacy registration_open boolean when unset.
func (a *API) registerMode() string {
	switch m, _ := a.st.GetSetting("register_mode"); m {
	case "open", "code", "closed":
		return m
	}
	if open, _ := a.st.GetSettingBool("registration_open"); open {
		return "open"
	}
	return "closed"
}

// handleConfig exposes public site config the frontend needs before login.
func (a *API) handleConfig(w http.ResponseWriter, r *http.Request) {
	verify, _ := a.st.GetSettingBool("email_verify_required")
	rate, _ := a.st.GetSettingInt64("points_per_cny", 10)
	mode := a.registerMode()
	siteName, _ := a.st.GetSetting("site_name")
	if siteName == "" {
		siteName = "轻舟"
	}
	siteDesc, _ := a.st.GetSetting("site_description")
	homeMode, _ := a.st.GetSetting("homepage_mode")
	if homeMode == "" {
		homeMode = "monitor"
	}
	homeURL, _ := a.st.GetSetting("homepage_url")
	helpMode, _ := a.st.GetSetting("help_docs_mode")
	if helpMode != "external" {
		helpMode = "builtin"
	}
	helpURL, _ := a.st.GetSetting("help_docs_url")
	ok(w, J{
		"register_mode":         mode,
		"registration_open":     mode != "closed",
		"email_verify_required": verify,
		// Whether this panel can send mail at all. The login dialog needs it to
		// decide whether 找回密码 is a real offer or a dead end — with no SMTP
		// there is no way to deliver the link. Not a secret: anyone can observe
		// it by trying the flow once.
		"email_enabled": a.mailerConfigured(),
		// Same non-secret as email_enabled: anyone can observe it by talking
		// to the bot. The account page uses it to hide a dead bind card.
		"telegram_enabled": a.telegramConfigured(),
		"points_per_cny":   rate,
		"site_name":        siteName,
		"site_description": siteDesc,
		"homepage_mode":    homeMode,
		"homepage_url":     homeURL,
		"help_docs_mode":   helpMode,
		"help_docs_url":    helpURL,
		"app_version":      version.Current(),
	})
}

func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}

	u, err := a.st.UserByUsername(strings.TrimSpace(req.Username))
	if err != nil {
		fail(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	if u == nil {
		// Spend comparable time so response timing doesn't reveal whether the
		// username exists (user enumeration).
		auth.DummyCompare(req.Password)
		fail(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	if !auth.CheckPassword(u.PasswordHash, req.Password) {
		fail(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	if u.Status == "banned" {
		fail(w, http.StatusForbidden, "账号已被封禁")
		return
	}
	// Unverified accounts may still log in. Resend-verify lives behind
	// auth (/api/user/resend-verify, 个人中心), and mail scanners routinely
	// consume the one-shot link — blocking login here would leave the user
	// with no self-serve recovery. Node credentials are withheld in handleSub.

	tok, err := a.issueLogin(w, r, u)
	if err != nil {
		fail(w, http.StatusInternalServerError, "签发令牌失败")
		return
	}
	ok(w, J{"token": tok, "user": userView(u)})
}

func (a *API) handleMe(w http.ResponseWriter, r *http.Request) {
	uid, _ := r.Context().Value(ctxUserID).(int64)
	u, err := a.st.UserByID(uid)
	if err != nil {
		fail(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	if u == nil {
		fail(w, http.StatusNotFound, "用户不存在")
		return
	}
	ok(w, userView(u))
}

func (a *API) handleLogout(w http.ResponseWriter, r *http.Request) {
	if jti, _ := r.Context().Value(ctxJti).(string); jti != "" {
		_ = a.st.DeleteSessionByJti(jti)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	ok(w, nil)
}

func (a *API) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokStr := bearerOrCookie(r)
		if tokStr == "" {
			fail(w, http.StatusUnauthorized, "未登录")
			return
		}
		// Machine API tokens (Bearer only). Cookie-bound browser sessions stay on JWT.
		if strings.HasPrefix(tokStr, "qz_at_") && strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			at, err := a.st.LookupAPIToken(tokStr)
			if err != nil || at == nil {
				// Same message as a bad JWT — do not advertise token existence.
				fail(w, http.StatusUnauthorized, "登录已失效，请重新登录")
				return
			}
			a.st.TouchAPIToken(at.ID)
			ctx := context.WithValue(r.Context(), ctxUserID, at.CreatedBy)
			ctx = context.WithValue(ctx, ctxRole, "admin")
			ctx = context.WithValue(ctx, ctxAuthKind, "api_token")
			ctx = context.WithValue(ctx, ctxScopes, at.Scopes)
			ctx = context.WithValue(ctx, ctxTokenID, at.ID)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		claims, err := auth.Parse(a.secret, tokStr)
		if err != nil {
			fail(w, http.StatusUnauthorized, "登录已过期，请重新登录")
			return
		}
		// Session must still exist (supports remote logout / revocation).
		if claims.ID == "" || !a.st.SessionValid(claims.ID) {
			fail(w, http.StatusUnauthorized, "登录已失效，请重新登录")
			return
		}
		a.st.TouchSession(claims.ID)
		ctx := context.WithValue(r.Context(), ctxUserID, claims.UserID)
		ctx = context.WithValue(ctx, ctxRole, claims.Role)
		ctx = context.WithValue(ctx, ctxJti, claims.ID)
		ctx = context.WithValue(ctx, ctxAuthKind, "jwt")
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *API) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role, _ := r.Context().Value(ctxRole).(string)
		if role != "admin" {
			fail(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearerOrCookie(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	if c, err := r.Cookie(cookieName); err == nil {
		return c.Value
	}
	return ""
}

func userView(u *store.User) J {
	email := ""
	if u.Email.Valid {
		email = u.Email.String
	}
	return J{
		"id":             u.ID,
		"username":       u.Username,
		"email":          email,
		"email_verified": u.EmailVerified,
		"role":           u.Role,
		"is_admin":       u.Role == "admin",
		"status":         u.Status,
		"points":         u.Points,
	}
}
