package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"qingzhou/internal/auth"
)

// POST /api/user/password {old_password, new_password} — change own password.
func (a *API) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	uid, _ := r.Context().Value(ctxUserID).(int64)
	jti, _ := r.Context().Value(ctxJti).(string)
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if len(req.NewPassword) < 6 {
		fail(w, http.StatusBadRequest, "新密码至少 6 位")
		return
	}
	u, err := a.st.UserByID(uid)
	if err != nil || u == nil {
		fail(w, http.StatusUnauthorized, "未登录")
		return
	}
	// Throttle the old-password check. It is the re-authentication gate in front
	// of an account takeover — a stolen session already passes authMiddleware, so
	// its holder only needs this one secret, and succeeding logs every other
	// session out (the real owner's included). Unthrottled, guessing it was free.
	//
	// Keyed per user, not per IP: the attacker picks the IP, the victim does not.
	// Counted on every attempt rather than only on failure — nobody legitimately
	// changes their password five times in ten minutes, and counting failures
	// alone lets an attacker reset the window with a known-good password.
	if a.pwRL != nil && !a.pwRL.allow(fmt.Sprintf("p%d", uid)) {
		fail(w, http.StatusTooManyRequests, "尝试过于频繁，请 10 分钟后再试")
		return
	}
	if !auth.CheckPassword(u.PasswordHash, req.OldPassword) {
		fail(w, http.StatusBadRequest, "原密码错误")
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		fail(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	if err := a.st.UpdatePassword(uid, hash); err != nil {
		fail(w, http.StatusInternalServerError, "保存失败")
		return
	}
	// Log out other devices for safety; keep the current session.
	_ = a.st.DeleteUserSessionsExcept(uid, jti)
	ok(w, J{"message": "密码已修改，其他设备已退出登录"})
}

// StartMaintenance periodically prunes stale sessions / tokens and sweeps the
// rate limiter.
func (a *API) StartMaintenance(ctx context.Context, interval time.Duration) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		a.maintain()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				a.maintain()
			}
		}
	}()
}

func (a *API) maintain() {
	cutoff := time.Now().Add(-tokenTTL).Unix()
	if n, err := a.st.CleanupSessions(cutoff); err == nil && n > 0 {
		log.Printf("maintenance: pruned %d stale session(s)", n)
	}
	a.st.CleanupEmailTokens()
	a.st.CleanupTelegramBindTokens()
	// Sweep every limiter — otherwise resendRL/probeRL entries accumulate for the
	// process lifetime (unbounded memory; the probe endpoint is IP-keyed).
	for _, rl := range []*rateLimiter{a.authRL, a.resendRL, a.probeRL, a.subRL, a.pwRL, a.tgRL, a.reauthUserRL, a.reauthIPRL} {
		if rl != nil {
			rl.sweep()
		}
	}
}
