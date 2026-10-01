package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"qingzhou/internal/auth"
	"qingzhou/internal/store"
)

const stepUpTTL = 5 * time.Minute
const stepUpHeader = "X-QZ-Step-Up"

// Explicit, stable scopes leave room for additional verification methods without
// changing protected handlers. A proof never grants ordinary login authority.
const (
	stepUpBackup          = "admin:backup"
	stepUpCertExport      = "admin:cert-export"
	stepUpUpdate          = "admin:update"
	stepUpSSH             = "admin:ssh"
	stepUpSettings        = "admin:security-settings"
	stepUpUsers           = "admin:user-security"
	stepUpSubscription    = "user:subscription-reset"
	stepUpNodeCredentials = "user:node-credentials-reset"
)

func validStepUpScope(scope string, admin bool) bool {
	switch scope {
	case stepUpSubscription, stepUpNodeCredentials:
		return true
	case stepUpBackup, stepUpCertExport, stepUpUpdate, stepUpSSH, stepUpSettings, stepUpUsers:
		return admin
	}
	return false
}

type stepUpClaims struct {
	UserID          int64  `json:"uid"`
	Session         string `json:"session"`
	Scope           string `json:"scope"`
	PasswordVersion string `json:"pv"`
	Method          string `json:"method"`
	jwt.RegisteredClaims
}

// Domain-separated keys keep proofs unusable as login JWTs, and prevent the
// embedded password version from exposing a password hash for offline guessing.
func (a *API) stepUpKey(purpose string) []byte {
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte("qingzhou/step-up/v1/" + purpose))
	return mac.Sum(nil)
}
func (a *API) passwordVersion(hash string) string {
	mac := hmac.New(sha256.New, a.stepUpKey("password-version"))
	mac.Write([]byte(hash))
	return hex.EncodeToString(mac.Sum(nil))
}
func (a *API) issueStepUp(u *store.User, jti, scope string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := stepUpClaims{
		UserID: u.ID, Session: jti, Scope: scope, PasswordVersion: a.passwordVersion(u.PasswordHash), Method: "password",
		RegisteredClaims: jwt.RegisteredClaims{Audience: jwt.ClaimStrings{"qingzhou-step-up"}, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(ttl))},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(a.stepUpKey("proof"))
}

// POST /api/user/reauth {password, scope, method?: "password"}.
// Passwords/proofs are never logged or put in URLs, cookies, or persistent state.
func (a *API) handleReauth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	uid, _ := r.Context().Value(ctxUserID).(int64)
	jti, _ := r.Context().Value(ctxJti).(string)
	// Charge both dimensions even when one is exhausted. IP changes cannot evade
	// the account limit; many stolen accounts cannot evade the shared IP limit.
	userAllowed := a.reauthUserRL != nil && a.reauthUserRL.allow(itoa(uid))
	ipAllowed := a.reauthIPRL != nil && a.reauthIPRL.allow(clientIP(r))
	if !userAllowed || !ipAllowed {
		w.Header().Set("Retry-After", "600")
		fail(w, http.StatusTooManyRequests, "验证尝试过于频繁，请稍后再试")
		return
	}
	var req struct {
		Password string `json:"password"`
		Scope    string `json:"scope"`
		Method   string `json:"method"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if req.Method != "" && req.Method != "password" {
		fail(w, http.StatusBadRequest, "不支持的验证方式")
		return
	}
	u, err := a.st.UserByID(uid)
	if err != nil || u == nil || u.Status != "active" || jti == "" || !a.st.SessionValidForUser(jti, uid) {
		auth.DummyCompare(req.Password)
		fail(w, http.StatusForbidden, "身份验证失败")
		return
	}
	if !auth.CheckPassword(u.PasswordHash, req.Password) {
		fail(w, http.StatusForbidden, "身份验证失败")
		return
	}
	if !validStepUpScope(req.Scope, u.Role == "admin") {
		fail(w, http.StatusForbidden, "不支持的操作范围")
		return
	}
	proof, err := a.issueStepUp(u, jti, req.Scope, stepUpTTL)
	if err != nil {
		fail(w, http.StatusInternalServerError, "身份验证失败")
		return
	}
	ok(w, J{"proof": proof, "scope": req.Scope, "expires_in": int(stepUpTTL.Seconds())})
}

func (a *API) requireStepUp(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			uid, _ := r.Context().Value(ctxUserID).(int64)
			jti, _ := r.Context().Value(ctxJti).(string)
			kind, _ := r.Context().Value(ctxAuthKind).(string)
			// Preserve the pre-existing scoped machine backup contract. All other
			// protected routes require a password-verified human session.
			if kind == "api_token" && scope == stepUpBackup && r.Method == http.MethodGet && r.URL.Path == "/api/admin/backup" {
				next.ServeHTTP(w, r)
				return
			}
			if kind != "jwt" || jti == "" {
				fail(w, http.StatusForbidden, "此操作需要登录会话和密码二次验证，API 令牌不可用")
				return
			}
			// Check live account, session ownership and password version every time.
			// Password changes retaining the current session still invalidate all proofs.
			u, err := a.st.UserByID(uid)
			if err != nil || u == nil || u.Status != "active" || !a.st.SessionValidForUser(jti, uid) || !validStepUpScope(scope, u.Role == "admin") {
				fail(w, http.StatusForbidden, "身份验证失败")
				return
			}
			proof := r.Header.Get(stepUpHeader)
			claims := &stepUpClaims{}
			valid := false
			if proof != "" && len(proof) <= 4096 {
				tok, err := jwt.ParseWithClaims(proof, claims, func(t *jwt.Token) (any, error) {
					return a.stepUpKey("proof"), nil
				}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithAudience("qingzhou-step-up"), jwt.WithExpirationRequired())
				valid = err == nil && tok.Valid && claims.UserID == uid && claims.Session == jti && claims.Scope == scope && claims.Method == "password" && hmac.Equal([]byte(claims.PasswordVersion), []byte(a.passwordVersion(u.PasswordHash)))
			}
			if !valid {
				writeJSON(w, http.StatusForbidden, J{"code": http.StatusForbidden, "msg": "需要密码二次验证", "data": J{"error": "step_up_required", "scope": scope}})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
