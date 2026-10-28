package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"qingzhou/internal/store"
)

// Per-user controls for monthly-reset plans (#96). Each acts on one renewal
// line of one user and rebuilds that user's config, since 预支 or a new monthly
// quota can bring a paused user back (or pause one).

type planLineReq struct {
	QueueKey     string `json:"queue_key"`
	Mode         *int64 `json:"mode"`
	MonthlyBytes int64  `json:"monthly_bytes"`
}

func (a *API) planLineRequest(w http.ResponseWriter, r *http.Request) (int64, *planLineReq, bool) {
	uid := atoi(chi.URLParam(r, "id"))
	var req planLineReq
	if uid <= 0 || json.NewDecoder(r.Body).Decode(&req) != nil || req.QueueKey == "" {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return 0, nil, false
	}
	if !a.st.UserExists(uid) {
		fail(w, http.StatusNotFound, "用户不存在")
		return 0, nil, false
	}
	return uid, &req, true
}

func (a *API) planLineDone(w http.ResponseWriter, uid int64, err error, body J) {
	if err != nil {
		switch {
		case errors.Is(err, store.ErrLineNotFound), errors.Is(err, store.ErrLineNotPeriodic):
			fail(w, http.StatusBadRequest, err.Error())
		default:
			fail(w, http.StatusBadRequest, "保存失败，请稍后再试")
		}
		return
	}
	a.invalidateLinks(uid)
	a.sbRebuildLog()
	ok(w, body)
}

// PUT /api/admin/users/{id}/plan-lines/advance {queue_key, mode}
// mode: -1 跟随套餐, 0 关闭, 1 开启.
func (a *API) handleAdminSetLineAdvance(w http.ResponseWriter, r *http.Request) {
	uid, req, good := a.planLineRequest(w, r)
	if !good {
		return
	}
	if req.Mode == nil {
		fail(w, http.StatusBadRequest, "请选择预支设置")
		return
	}
	a.planLineDone(w, uid, a.st.SetLineAdvance(uid, req.QueueKey, *req.Mode), J{"mode": *req.Mode})
}

// PUT /api/admin/users/{id}/plan-lines/monthly {queue_key, monthly_bytes}
func (a *API) handleAdminSetLineMonthly(w http.ResponseWriter, r *http.Request) {
	uid, req, good := a.planLineRequest(w, r)
	if !good {
		return
	}
	a.planLineDone(w, uid, a.st.SetLineMonthlyQuota(uid, req.QueueKey, req.MonthlyBytes), J{"monthly_bytes": req.MonthlyBytes})
}

// POST /api/admin/users/{id}/plan-lines/convert {queue_key} — switch already
// sold lump-sum份 in this line to monthly reset.
func (a *API) handleAdminConvertLine(w http.ResponseWriter, r *http.Request) {
	uid, req, good := a.planLineRequest(w, r)
	if !good {
		return
	}
	n, err := a.st.ConvertLineToPeriodic(uid, req.QueueKey)
	a.planLineDone(w, uid, err, J{"converted": n})
}

// GET /api/admin/users/{id}/period-resets — recent monthly refills.
func (a *API) handleAdminPeriodResets(w http.ResponseWriter, r *http.Request) {
	uid := atoi(chi.URLParam(r, "id"))
	list, err := a.st.ListPeriodResets(uid, 50)
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取失败")
		return
	}
	ok(w, list)
}
