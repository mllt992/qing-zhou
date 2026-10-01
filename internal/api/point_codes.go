package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"qingzhou/internal/store"
)

func (a *API) handleRedeemPoints(w http.ResponseWriter, r *http.Request) {
	uid, _ := r.Context().Value(ctxUserID).(int64)
	if !a.redeemRL.allow("user:"+itoa(uid)) || !a.redeemIPRL.allow(clientIP(r)) {
		fail(w, 429, "兑换请求过于频繁，请稍后再试")
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	balance, points, err := a.st.RedeemPointCode(uid, req.Code)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrPointCodeLocked):
			fail(w, 429, err.Error())
		case errors.Is(err, store.ErrPointCodeMissing), errors.Is(err, store.ErrPointCodeUsed), errors.Is(err, store.ErrPointCodeExpired), errors.Is(err, store.ErrPointCodeDisabled), errors.Is(err, store.ErrPointBalanceOverflow):
			fail(w, 400, err.Error())
		default:
			fail(w, 500, "兑换失败，请稍后重试")
		}
		return
	}
	ok(w, J{"balance": balance, "points": points})
}
func (a *API) handleGeneratePointCodes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Points int64  `json:"points"`
		Count  int    `json:"count"`
		Days   int    `json:"days"`
		Note   string `json:"note"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	if req.Points < 1 || req.Points > 1_000_000_000 || req.Count < 1 || req.Count > 100 || req.Days < 1 || req.Days > 3650 || len([]rune(req.Note)) > 256 {
		fail(w, 400, "请填写有效的面额、数量、有效期和备注")
		return
	}
	uid, _ := r.Context().Value(ctxUserID).(int64)
	codes, err := a.st.GeneratePointCodes(uid, req.Points, req.Count, req.Days, req.Note)
	if err != nil {
		fail(w, 500, "生成失败，请稍后重试")
		return
	}
	ok(w, J{"codes": codes})
}
func (a *API) handleListPointCodes(w http.ResponseWriter, r *http.Request) {
	codes, err := a.st.ListPointCodes(r.URL.Query().Get("status"), atoi(r.URL.Query().Get("before")), 100)
	if err != nil {
		fail(w, 400, "读取兑换码失败或筛选无效")
		return
	}
	ok(w, codes)
}
func (a *API) handleDisablePointCodes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	count, err := a.st.DisablePointCodes(req.IDs)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	ok(w, J{"disabled": count})
}
