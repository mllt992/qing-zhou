package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"qingzhou/internal/store"
)

var validPkgTypes = map[string]bool{"traffic": true, "plan": true}
var queueKeyRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// validatePackage rejects nonsensical/negative package fields. A negative price
// would credit points on "purchase"; a negative traffic would reduce quota while
// charging; a plan with no duration never expires by accident.
func validatePackage(p *store.Package) string {
	p.QueueKey = strings.TrimSpace(p.QueueKey)
	if !validPkgTypes[p.Type] {
		return "商品类型必须为 traffic / plan"
	}
	if p.Name == "" {
		return "商品名称不能为空"
	}
	if p.Type != "plan" && p.QueueKey != "" {
		return "只有订阅计划支持续期组"
	}
	if p.QueueKey != "" && !queueKeyRe.MatchString(p.QueueKey) {
		return "续期组须为 1-64 位字母、数字、点、下划线或短横线，并以字母或数字开头"
	}
	if p.PricePoints < 0 {
		return "价格不能为负"
	}
	if p.TrafficBytes <= 0 {
		return "流量必须大于 0"
	}
	if p.DurationDays < 0 {
		return "有效期不能为负"
	}
	if msg := validateOptions(p); msg != "" {
		return msg
	}
	// With options set, the columns checked here are mirrored from the first one
	// (applyDefaultOption), so this still guards the single-duration case only.
	if p.Type == "plan" && len(p.Options) == 0 && p.DurationDays <= 0 {
		return "订阅套餐必须设置正的有效期（天）"
	}
	return ""
}

// maxPlanOptions caps the duration list: it renders as a row of chips in the shop,
// and a package with dozens of lengths is a pricing table, not a choice.
const maxPlanOptions = 8

// validateOptions checks the selectable durations. Duplicated days are the one
// that really matters: a purchase names its option BY days, so two rows sharing a
// length would make the price charged depend on list order.
func validateOptions(p *store.Package) string {
	if len(p.Options) == 0 {
		return ""
	}
	if p.Type != "plan" {
		return "只有订阅计划支持多时长选择"
	}
	if len(p.Options) > maxPlanOptions {
		return fmt.Sprintf("时长选项最多 %d 个", maxPlanOptions)
	}
	seen := map[int64]bool{}
	for _, o := range p.Options {
		if o.Days <= 0 {
			return "每个时长选项都要填写正的天数"
		}
		if seen[o.Days] {
			return fmt.Sprintf("时长选项重复：%d 天出现了两次", o.Days)
		}
		seen[o.Days] = true
		if o.PricePoints < 0 {
			return "时长选项的价格不能为负"
		}
		if o.TrafficBytes <= 0 {
			return "每个时长选项的流量必须大于 0"
		}
	}
	return ""
}

func (a *API) handleAdminListPackages(w http.ResponseWriter, r *http.Request) {
	pkgs, err := a.st.ListPackages()
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取商品失败")
		return
	}
	subs, _ := a.st.PlanSubscriberCounts()
	for _, p := range pkgs {
		if p.Type == "plan" {
			p.GroupIDs, _ = a.st.PlanGroupIDs(p.ID)
		}
		// Unlike node groups, buyer restrictions apply to every package type.
		p.UserGroupIDs, _ = a.st.PackageUserGroupIDs(p.ID)
		p.Subscribers = subs[p.ID]
	}
	ok(w, pkgs)
}

func (a *API) handleAdminCreatePackage(w http.ResponseWriter, r *http.Request) {
	var p store.Package
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if msg := validatePackage(&p); msg != "" {
		fail(w, http.StatusBadRequest, msg)
		return
	}
	if p.Stock == 0 {
		p.Stock = -1 // default unlimited
	}
	id, err := a.st.CreatePackage(p)
	if err != nil {
		fail(w, http.StatusInternalServerError, "创建商品失败")
		return
	}
	if p.Type == "plan" {
		_ = a.st.SetPlanGroups(id, p.GroupIDs)
	}
	// Unlike the node-group binding above, this one must not fail silently: a
	// package with no user-group rows is PUBLIC, so a dropped write would put a
	// restricted package on sale to everyone. Undo the create and report it.
	if err := a.st.SetPackageUserGroups(id, p.UserGroupIDs); err != nil {
		_ = a.st.DeletePackage(id)
		fail(w, http.StatusInternalServerError, "保存可购买用户组失败，商品未创建（避免误开放给所有人）")
		return
	}
	created, _ := a.st.GetPackage(id)
	if created != nil {
		if created.Type == "plan" {
			created.GroupIDs, _ = a.st.PlanGroupIDs(id)
		}
		created.UserGroupIDs, _ = a.st.PackageUserGroupIDs(id)
	}
	ok(w, created)
}

// handleAdminReorderPackages persists a new display order for the shop/admin
// package list. Body: {"ids":[...]} — the full set of package ids in the desired
// order. sort_order is rewritten to match; no other package field is touched.
func (a *API) handleAdminReorderPackages(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.IDs) == 0 {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if err := a.st.ReorderPackages(req.IDs); err != nil {
		fail(w, http.StatusInternalServerError, "保存排序失败")
		return
	}
	ok(w, J{"count": len(req.IDs)})
}

func (a *API) handleAdminUpdatePackage(w http.ResponseWriter, r *http.Request) {
	id := atoi(chi.URLParam(r, "id"))
	if id <= 0 {
		fail(w, http.StatusBadRequest, "无效的商品 id")
		return
	}
	// Decode onto the stored row: UpdatePackage writes every column, but the edit
	// form posts neither enabled nor sort_order. Into a zero value, editing an
	// on-sale package (fixing a typo in its name) set enabled=0 and dropped it
	// out of the user shop, silently, with no warning in the UI.
	p, err := a.st.GetPackage(int64(id))
	if err != nil || p == nil {
		fail(w, http.StatusNotFound, "商品不存在")
		return
	}
	if err := json.NewDecoder(r.Body).Decode(p); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if msg := validatePackage(p); msg != "" {
		fail(w, http.StatusBadRequest, msg)
		return
	}
	p.ID = int64(id)
	if err := a.st.UpdatePackage(*p); err != nil {
		fail(w, http.StatusInternalServerError, "更新商品失败")
		return
	}
	if p.Type == "plan" {
		_ = a.st.SetPlanGroups(id, p.GroupIDs)
	}
	// Must not fail silently — see handleAdminCreatePackage. SetPackageUserGroups
	// is transactional, so a failure leaves the previous bindings in place (the
	// safe direction: still restricted); the other package fields are already
	// saved, so say plainly that only this part didn't stick.
	if err := a.st.SetPackageUserGroups(id, p.UserGroupIDs); err != nil {
		fail(w, http.StatusInternalServerError, "商品已保存，但可购买用户组未能更新（仍沿用原设置），请重试")
		return
	}
	updated, _ := a.st.GetPackage(id)
	if updated != nil {
		if updated.Type == "plan" {
			updated.GroupIDs, _ = a.st.PlanGroupIDs(id)
		}
		updated.UserGroupIDs, _ = a.st.PackageUserGroupIDs(id)
	}
	ok(w, updated)
}

func (a *API) handleAdminDeletePackage(w http.ResponseWriter, r *http.Request) {
	id := atoi(chi.URLParam(r, "id"))
	if id <= 0 {
		fail(w, http.StatusBadRequest, "无效的商品 id")
		return
	}
	// Guard: never hard-delete a package that users still hold a plan bucket for —
	// it would dangle their bucket (metering + granting nodes for a package that no
	// longer exists) and break the dashboard. Force "下架" first. Keyed on real
	// buckets, not current_plan_id, so stacked/older holders are not missed.
	if subs, _ := a.st.PackagePlanHolders(id); len(subs) > 0 {
		fail(w, http.StatusBadRequest,
			fmt.Sprintf("该商品仍有 %d 位用户持有，请先「下架」（退款并清空其套餐）后再删除", len(subs)))
		return
	}
	if err := a.st.DeletePackage(id); err != nil {
		fail(w, http.StatusInternalServerError, "删除商品失败")
		return
	}
	ok(w, nil)
}

// POST /api/admin/packages/{id}/retire — 下架: disable the package, and for every
// user still on this plan refund their latest purchase (points + entitlement)
// and clear their plan. Data (orders) is kept, marked refunded.
func (a *API) handleAdminRetirePackage(w http.ResponseWriter, r *http.Request) {
	id := atoi(chi.URLParam(r, "id"))
	if id <= 0 {
		fail(w, http.StatusBadRequest, "无效的商品 id")
		return
	}
	operatorID, _ := r.Context().Value(ctxUserID).(int64)
	// Act on every user who actually holds a bucket for this package (bucket model),
	// not just whoever's current_plan_id points here — otherwise a user who bought
	// this plan then bought another keeps this one for free with no refund.
	subs, _ := a.st.PackagePlanHolders(id)
	refunded, cleared := 0, 0
	for _, uid := range subs {
		// Refund EVERY still-refundable order for this (user, package), not only the
		// latest — a stacked/renewed subscriber paid for each period. Prorated by
		// default ("") so an already-consumed order returns only its unused remainder.
		orders, _ := a.st.RefundableOrdersForPackage(uid, id)
		for _, oid := range orders {
			if _, _, err := a.st.RefundOrder(oid, operatorID, "", a.syncEntitlement); err == nil {
				refunded++
			}
		}
		// Then remove any leftover plan bucket (refunds shrink but may leave a
		// used-up remnant) and null the legacy pointer if it still points here.
		if err := a.st.ClearPlanBucket(uid, id); err == nil {
			cleared++
		}
		a.invalidateLinks(uid)
	}
	if err := a.st.SetPackageEnabled(id, false); err != nil {
		fail(w, http.StatusInternalServerError, "下架失败")
		return
	}
	a.sbRebuildLog()
	ok(w, J{"subscribers": len(subs), "refunded": refunded, "cleared": cleared})
}

// POST /api/admin/packages/{id}/enable — 上架: re-enable a package for sale. Does
// not re-grant anything to past subscribers.
func (a *API) handleAdminEnablePackage(w http.ResponseWriter, r *http.Request) {
	id := atoi(chi.URLParam(r, "id"))
	if id <= 0 {
		fail(w, http.StatusBadRequest, "无效的商品 id")
		return
	}
	if err := a.st.SetPackageEnabled(id, true); err != nil {
		fail(w, http.StatusInternalServerError, "上架失败")
		return
	}
	ok(w, nil)
}

// POST /api/admin/users/{id}/points {amount, note}
func (a *API) handleAdminRecharge(w http.ResponseWriter, r *http.Request) {
	uid := atoi(chi.URLParam(r, "id"))
	if uid <= 0 {
		fail(w, http.StatusBadRequest, "无效的用户 id")
		return
	}
	var req struct {
		Amount int64  `json:"amount"`
		Note   string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Amount == 0 {
		fail(w, http.StatusBadRequest, "金额不能为空")
		return
	}
	operatorID, _ := r.Context().Value(ctxUserID).(int64)
	txType := "admin_recharge"
	if req.Amount < 0 {
		txType = "adjust"
	}
	balance, err := a.st.AdjustPoints(uid, req.Amount, txType, operatorID, req.Note)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrUserNotFound):
			fail(w, http.StatusNotFound, "用户不存在")
		case errors.Is(err, store.ErrNegativeBalance):
			fail(w, http.StatusBadRequest, "扣减后积分会为负，操作被拒绝")
		default:
			fail(w, http.StatusInternalServerError, "操作失败")
		}
		return
	}
	ok(w, J{"user_id": uid, "balance": balance})
}

// GET /api/admin/orders?q=  — all orders (joined with username), optional search.
func (a *API) handleAdminListOrders(w http.ResponseWriter, r *http.Request) {
	orders, err := a.st.ListOrdersAdmin(r.URL.Query().Get("q"), 300)
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取订单失败")
		return
	}
	ok(w, orders)
}

// GET /api/admin/users/{id}/orders — one user's consumption records.
func (a *API) handleAdminUserOrders(w http.ResponseWriter, r *http.Request) {
	id := atoi(chi.URLParam(r, "id"))
	orders, err := a.st.ListOrders(id, 200)
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取订单失败")
		return
	}
	ok(w, orders)
}

// GET /api/admin/users/{id}/plans — one user's independently-metered buckets.
func (a *API) handleAdminUserPlans(w http.ResponseWriter, r *http.Request) {
	id := atoi(chi.URLParam(r, "id"))
	buckets, err := a.st.ListBuckets(id)
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取套餐失败")
		return
	}
	pkgNames, _ := a.st.PackageNames()
	ok(w, buildPlanViews(buckets, pkgNames))
}

// DELETE /api/admin/users/{id}/plans/{planID} — revoke one of a user's份.
//
// Deliberately NOT a refund: the points stay spent and the order row keeps its
// 'success' status, because this is the "take this back" action (mis-assignment,
// abuse, a comp that shouldn't have gone out), not the "undo this purchase" one.
// Refunding is POST /api/admin/orders/{id}/refund, which returns points and
// reverses exactly that order. The UI says which is which at the confirm step.
func (a *API) handleAdminDeleteUserPlan(w http.ResponseWriter, r *http.Request) {
	uid := atoi(chi.URLParam(r, "id"))
	pid := atoi(chi.URLParam(r, "planID"))
	if uid <= 0 || pid <= 0 {
		fail(w, http.StatusBadRequest, "无效的参数")
		return
	}
	if !a.st.UserExists(uid) {
		fail(w, http.StatusNotFound, "用户不存在")
		return
	}
	b, err := a.st.DeleteBucket(uid, pid)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrBucketNotFound):
			fail(w, http.StatusNotFound, err.Error())
		case errors.Is(err, store.ErrBucketProtected):
			fail(w, http.StatusBadRequest, err.Error())
		default:
			fail(w, http.StatusInternalServerError, "移除失败")
		}
		return
	}
	// The removed bucket carried its own node credentials, so any cached link list
	// still advertises an identity the config is about to drop.
	a.invalidateLinks(uid)
	a.sbRebuildLog()
	ok(w, J{"deleted": pid, "name": b.Name, "kind": b.Kind})
}

// POST /api/admin/users/{id}/plans/{planID}/traffic {delta_bytes} — add or
// subtract quota on one of a user's份. Not a refund and not a purchase: the
// order row and the points ledger stay put. Use this to correct a mis-grant or
// to gift extra traffic on an existing份; to take a queued/unused份 back
// wholesale, DELETE the plan instead (its unused quota goes with it).
func (a *API) handleAdminAdjustUserPlanTraffic(w http.ResponseWriter, r *http.Request) {
	uid := atoi(chi.URLParam(r, "id"))
	pid := atoi(chi.URLParam(r, "planID"))
	if uid <= 0 || pid <= 0 {
		fail(w, http.StatusBadRequest, "无效的参数")
		return
	}
	var req struct {
		DeltaBytes int64 `json:"delta_bytes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if !a.st.UserExists(uid) {
		fail(w, http.StatusNotFound, "用户不存在")
		return
	}
	b, err := a.st.AdjustBucketTraffic(uid, pid, req.DeltaBytes)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrBucketNotFound):
			fail(w, http.StatusNotFound, err.Error())
		case errors.Is(err, store.ErrBucketProtected),
			errors.Is(err, store.ErrZeroDelta),
			errors.Is(err, store.ErrTrafficFloor),
			errors.Is(err, store.ErrBucketFinished):
			fail(w, http.StatusBadRequest, err.Error())
		default:
			fail(w, http.StatusInternalServerError, "调整失败")
		}
		return
	}
	// Adding traffic can bring an exhausted份 back online; subtracting can
	// exhaust the head and promote the next queued one. Either way the cached
	// link list and the node config have to catch up.
	a.invalidateLinks(uid)
	a.sbRebuildLog()
	ok(w, J{
		"id":            b.ID,
		"name":          b.Name,
		"kind":          b.Kind,
		"traffic_limit": b.TrafficLimit,
		"used":          b.Used(),
	})
}

// DELETE /api/admin/orders/{id} — purge an order record. Only allowed for
// orphaned orders (the user has been deleted); active users' orders should be
// refunded, not silently dropped.
func (a *API) handleAdminDeleteOrder(w http.ResponseWriter, r *http.Request) {
	id := atoi(chi.URLParam(r, "id"))
	o, _ := a.st.GetOrder(id)
	if o == nil {
		fail(w, http.StatusNotFound, "订单不存在")
		return
	}
	if a.st.UserExists(o.UserID) {
		fail(w, http.StatusBadRequest, "该用户仍存在，请用退款而非删除记录")
		return
	}
	if err := a.st.DeleteOrder(id); err != nil {
		fail(w, http.StatusInternalServerError, "删除失败")
		return
	}
	ok(w, J{"deleted": id})
}

// GET /api/admin/orders/{id}/refund-preview?mode= — compute (read-only) what a
// refund would return under the current policy, so the admin can confirm the
// prorated amount before acting. mode: ""|prorated|full.
func (a *API) handleAdminRefundPreview(w http.ResponseWriter, r *http.Request) {
	id := atoi(chi.URLParam(r, "id"))
	q, err := a.st.RefundPreview(id, r.URL.Query().Get("mode"))
	if err != nil {
		if errors.Is(err, store.ErrOrderNotFound) {
			fail(w, http.StatusNotFound, "订单不存在")
			return
		}
		fail(w, http.StatusInternalServerError, "计算退款失败")
		return
	}
	ok(w, q)
}

// POST /api/admin/orders/{id}/refund — refund a purchase: return points (prorated
// to the unused portion by default), undo the entitlement, mark the order
// 'refunded' (record kept). Body/query mode: ""|prorated|full.
func (a *API) handleAdminRefundOrder(w http.ResponseWriter, r *http.Request) {
	id := atoi(chi.URLParam(r, "id"))
	operatorID, _ := r.Context().Value(ctxUserID).(int64)
	o, _ := a.st.GetOrder(id)
	if o == nil {
		fail(w, http.StatusNotFound, "订单不存在")
		return
	}
	if !a.st.UserExists(o.UserID) {
		fail(w, http.StatusBadRequest, "用户已删除，无法退款（可删除该记录）")
		return
	}
	// mode may come from the query string or a small JSON body; both optional.
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		var body struct {
			Mode string `json:"mode"`
		}
		if json.NewDecoder(r.Body).Decode(&body) == nil {
			mode = body.Mode
		}
	}
	updated, quote, err := a.st.RefundOrder(id, operatorID, mode, a.syncEntitlement)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrAlreadyRefunded):
			fail(w, http.StatusConflict, "该订单已退款")
		case errors.Is(err, store.ErrOrderNotFound):
			fail(w, http.StatusNotFound, "订单不存在")
		default:
			fail(w, http.StatusBadGateway, "退款失败，已回滚："+err.Error())
		}
		return
	}
	a.invalidateLinks(o.UserID)
	a.sbRebuildLog()
	ok(w, J{"order_id": id, "user_id": o.UserID, "points": updated.Points,
		"refund_points": quote.RefundPoints, "refund_ratio": quote.Ratio,
		"traffic_total": updated.TrafficLimit, "expiry_at": updated.ExpiryAt})
}
