package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"qingzhou/internal/store"
)

// GET /api/user/packages — items on sale and visible to this user (packages
// restricted to user groups they're not in are hidden).
func (a *API) handleUserPackages(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	if u == nil {
		fail(w, http.StatusUnauthorized, "未登录")
		return
	}
	pkgs, err := a.st.ListPackagesForUser(u.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取商品失败")
		return
	}
	ok(w, pkgs)
}

// POST /api/user/purchase {package_id, duration_days?}
//
// duration_days picks one of the package's selectable durations; 0/absent buys
// the default one (and is the only valid value for a single-duration package).
func (a *API) handlePurchase(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	if u == nil {
		fail(w, http.StatusUnauthorized, "未登录")
		return
	}
	var req struct {
		PackageID      int64  `json:"package_id"`
		DurationDays   int64  `json:"duration_days"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.PackageID <= 0 {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	// Cap the client key so it can't be abused as unbounded storage.
	if len(req.IdempotencyKey) > 80 {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	pkg, err := a.st.GetPackage(req.PackageID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "服务器错误")
		return
	}
	if pkg == nil {
		fail(w, http.StatusNotFound, "商品不存在")
		return
	}

	result, err := a.st.PurchaseDuration(u.ID, pkg, req.DurationDays, req.IdempotencyKey, func(updated *store.User, resetUsed bool) error {
		return a.syncEntitlement(updated, resetUsed)
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrOptionNotFound):
			fail(w, http.StatusBadRequest, "所选时长已调整，请刷新后重新选择")
		case errors.Is(err, store.ErrInsufficientFunds):
			fail(w, http.StatusPaymentRequired, "积分不足")
		case errors.Is(err, store.ErrPackageDisabled):
			fail(w, http.StatusBadRequest, "商品已下架")
		case errors.Is(err, store.ErrPackageNoTraffic):
			fail(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, store.ErrPackageNotAllowed):
			fail(w, http.StatusForbidden, "该商品仅限指定用户组购买")
		case errors.Is(err, store.ErrOutOfStock):
			fail(w, http.StatusConflict, "商品库存不足")
		case errors.Is(err, store.ErrUserNotFound):
			fail(w, http.StatusNotFound, "用户不存在")
		default:
			// includes sing-box sync failure → transaction rolled back, points safe
			fail(w, http.StatusBadGateway, "购买失败，已回滚（请稍后重试）")
		}
		return
	}

	a.invalidateLinks(u.ID)
	a.sbRebuildLog()
	nu := result.User
	ok(w, J{
		"order_id":      result.Order.ID,
		"points":        nu.Points,
		"traffic_total": nu.TrafficLimit,
		"expiry_at":     nu.ExpiryAt,
	})
}

// GET /api/user/orders
func (a *API) handleUserOrders(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	if u == nil {
		fail(w, http.StatusUnauthorized, "未登录")
		return
	}
	orders, err := a.st.ListOrders(u.ID, 100)
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取订单失败")
		return
	}
	ok(w, orders)
}

// GET /api/user/points
func (a *API) handleUserPoints(w http.ResponseWriter, r *http.Request) {
	u := a.currentUser(r)
	if u == nil {
		fail(w, http.StatusUnauthorized, "未登录")
		return
	}
	txs, err := a.st.ListTransactions(u.ID, 100)
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取积分流水失败")
		return
	}
	ok(w, J{"balance": u.Points, "transactions": txs})
}
