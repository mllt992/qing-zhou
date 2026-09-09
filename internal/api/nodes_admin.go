package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"qingzhou/internal/store"
	"qingzhou/internal/subconv"
)

// ---- nodes ----

func (a *API) handleAdminListNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := a.st.ListNodes()
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取节点失败")
		return
	}
	ok(w, nodes)
}

// handleAdminReorderNodes persists a new display order for the node page and the
// subscriptions it generates. Body: {"ids":[...]} — node ids in the desired
// global order. Only sort_order is rewritten. No sing-box rebuild is needed
// (server config keys off inbounds, not node order), but subscriptions render in
// this order, so refresh the link cache by touching nothing else.
func (a *API) handleAdminReorderNodes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.IDs) == 0 {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if err := a.st.ReorderNodes(req.IDs); err != nil {
		fail(w, http.StatusInternalServerError, "保存排序失败")
		return
	}
	ok(w, J{"count": len(req.IDs)})
}

func (a *API) handleAdminCreateNode(w http.ResponseWriter, r *http.Request) {
	var n store.Node
	if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if n.Type != "self_built" && n.Type != "external" {
		fail(w, http.StatusBadRequest, "节点类型必须为 self_built / external")
		return
	}
	if msg := a.validateNodeRoute(&n); msg != "" {
		fail(w, http.StatusBadRequest, msg)
		return
	}
	if n.Type == "external" && n.ShareLink != "" {
		if p, err := subconv.ParseLink(n.ShareLink); err == nil {
			if n.Protocol == "" {
				n.Protocol = p.Protocol
			}
			if n.Name == "" {
				n.Name = p.Name
			}
		}
	}
	id, err := a.st.CreateNode(n)
	if err != nil {
		fail(w, http.StatusInternalServerError, "创建节点失败")
		return
	}
	created, _ := a.st.GetNode(id)
	a.sbRebuildLog()
	ok(w, created)
}

func (a *API) handleAdminUpdateNode(w http.ResponseWriter, r *http.Request) {
	id := atoi(chi.URLParam(r, "id"))
	// Decode onto the stored row: UpdateNode writes every column, but the edit
	// form only posts a few. Into a zero value, saving a node — even just
	// toggling 启用 — blanked its share_link and protocol, dropping it out of the
	// generated sing-box config and every user's subscription.
	n, err := a.st.GetNode(int64(id))
	if err != nil || n == nil {
		fail(w, http.StatusNotFound, "节点不存在")
		return
	}
	if err := json.NewDecoder(r.Body).Decode(n); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	n.ID = int64(id)
	if msg := a.validateNodeRoute(n); msg != "" {
		fail(w, http.StatusBadRequest, msg)
		return
	}
	if err := a.st.UpdateNode(*n); err != nil {
		fail(w, http.StatusInternalServerError, "更新节点失败")
		return
	}
	a.sbRebuildLog()
	ok(w, nil)
}

// validateNodeRoute turns a self-built node into a safe logical route. The
// physical inbound remains the listener; a positive override selects the first
// landing hop and must not create a cycle with the landing's existing chain.
func (a *API) validateNodeRoute(n *store.Node) string {
	if n.Type != "self_built" {
		n.RouteUpstreamInboundID = 0
		n.RouteUpstreamBroken = false
		return ""
	}
	if n.InboundTag == "" {
		return "请选择入口入站"
	}
	entry, err := a.st.GetSbInboundByTag(n.InboundTag)
	if err != nil || entry == nil {
		return "所选入口入站不存在"
	}
	if n.Protocol == "" {
		n.Protocol = entry.Type
	}
	if n.RouteUpstreamInboundID == 0 {
		n.RouteUpstreamBroken = false
		return ""
	}
	if entry.Type == "mixed" {
		return "Mixed 入站暂不支持按逻辑线路分流，请使用独立入站"
	}
	target, err := a.st.GetSbInbound(n.RouteUpstreamInboundID)
	if err != nil || target == nil {
		return "所选落地入站不存在"
	}
	if !target.Enabled {
		return "所选落地入站已停用"
	}
	allowed := map[string]bool{"vless": true, "vmess": true, "trojan": true, "shadowsocks": true, "hysteria2": true, "tuic": true}
	if !allowed[target.Type] {
		return "该落地协议暂不支持由线路机拨号"
	}
	seen := map[int64]bool{entry.ID: true}
	cur := target
	for cur != nil {
		if len(seen) > 16 {
			return "固定落地链路层级过深（最多 16 跳）"
		}
		if seen[cur.ID] {
			return "逻辑线路存在环路，流量会在机器间循环"
		}
		seen[cur.ID] = true
		if cur.UpstreamInboundID == 0 {
			break
		}
		cur, _ = a.st.GetSbInbound(cur.UpstreamInboundID)
		if cur == nil {
			return "固定落地的后续链路包含已失效入站"
		}
		if !cur.Enabled {
			return "固定落地的后续链路包含已停用入站"
		}
	}
	n.RouteUpstreamBroken = false
	return ""
}

func (a *API) handleAdminDeleteNode(w http.ResponseWriter, r *http.Request) {
	if err := a.st.DeleteNode(atoi(chi.URLParam(r, "id"))); err != nil {
		fail(w, http.StatusInternalServerError, "删除节点失败")
		return
	}
	a.sbRebuildLog()
	ok(w, nil)
}

// POST /api/admin/nodes/import {links, group_ids}
func (a *API) handleAdminImportNodes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Links    string  `json:"links"`
		GroupIDs []int64 `json:"group_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	proxies := subconv.ParseList(req.Links)
	n := 0
	for _, p := range proxies {
		id, err := a.st.CreateNode(store.Node{
			Type: "external", Name: p.Name, Protocol: p.Protocol,
			ShareLink: p.Raw, Enabled: true, GroupIDs: req.GroupIDs,
		})
		if err == nil && id > 0 {
			n++
		}
	}
	ok(w, J{"imported": n})
}

// ---- groups ----

func (a *API) handleAdminListGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := a.st.ListGroups()
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取分组失败")
		return
	}
	// Attach node_count: count how many nodes reference each group.
	counts := map[int64]int64{}
	if nodes, err := a.st.ListNodes(); err == nil {
		for _, n := range nodes {
			for _, gid := range n.GroupIDs {
				counts[gid]++
			}
		}
	}
	out := make([]J, 0, len(groups))
	for _, g := range groups {
		out = append(out, J{
			"id":          g.ID,
			"name":        g.Name,
			"description": g.Description,
			"is_ai":       g.IsAI,
			"sort_order":  g.SortOrder,
			"created_at":  g.CreatedAt,
			"node_count":  counts[g.ID],
		})
	}
	ok(w, out)
}

func (a *API) handleAdminCreateGroup(w http.ResponseWriter, r *http.Request) {
	var g store.NodeGroup
	if err := json.NewDecoder(r.Body).Decode(&g); err != nil || g.Name == "" {
		fail(w, http.StatusBadRequest, "分组名称不能为空")
		return
	}
	id, err := a.st.CreateGroup(g)
	if err != nil {
		fail(w, http.StatusInternalServerError, "创建分组失败")
		return
	}
	ok(w, J{"id": id})
}

func (a *API) handleAdminUpdateGroup(w http.ResponseWriter, r *http.Request) {
	var g store.NodeGroup
	if err := json.NewDecoder(r.Body).Decode(&g); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	g.ID = atoi(chi.URLParam(r, "id"))
	if err := a.st.UpdateGroup(g); err != nil {
		fail(w, http.StatusInternalServerError, "更新分组失败")
		return
	}
	ok(w, nil)
}

func (a *API) handleAdminDeleteGroup(w http.ResponseWriter, r *http.Request) {
	if err := a.st.DeleteGroup(atoi(chi.URLParam(r, "id"))); err != nil {
		fail(w, http.StatusInternalServerError, "删除分组失败")
		return
	}
	ok(w, nil)
}

// handleAdminInbounds lists 轻舟's own sing-box inbounds (tag/type) so admins can
// bind self-built nodes to them.
func (a *API) handleAdminInbounds(w http.ResponseWriter, r *http.Request) {
	list, err := a.st.ListSbInbounds()
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取入站失败: "+err.Error())
		return
	}
	ok(w, list)
}

// ---- node sources (机场订阅) ----

func (a *API) handleAdminListSources(w http.ResponseWriter, r *http.Request) {
	srcs, err := a.st.ListSources()
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取订阅源失败")
		return
	}
	ok(w, srcs)
}

func (a *API) handleAdminCreateSource(w http.ResponseWriter, r *http.Request) {
	var s store.NodeSource
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil || s.URL == "" {
		fail(w, http.StatusBadRequest, "订阅地址不能为空")
		return
	}
	id, err := a.st.CreateSource(s)
	if err != nil {
		fail(w, http.StatusInternalServerError, "创建订阅源失败")
		return
	}
	ok(w, J{"id": id})
}

func (a *API) handleAdminUpdateSource(w http.ResponseWriter, r *http.Request) {
	id := atoi(chi.URLParam(r, "id"))
	// Decode onto the stored row — see handleAdminUpdateNode.
	src, err := a.st.GetSource(int64(id))
	if err != nil || src == nil {
		fail(w, http.StatusNotFound, "订阅源不存在")
		return
	}
	if err := json.NewDecoder(r.Body).Decode(src); err != nil || src.URL == "" {
		fail(w, http.StatusBadRequest, "订阅地址不能为空")
		return
	}
	src.ID = int64(id)
	if err := a.st.UpdateSource(*src); err != nil {
		fail(w, http.StatusInternalServerError, "更新订阅源失败")
		return
	}
	ok(w, nil)
}

func (a *API) handleAdminDeleteSource(w http.ResponseWriter, r *http.Request) {
	if err := a.st.DeleteSource(atoi(chi.URLParam(r, "id"))); err != nil {
		fail(w, http.StatusInternalServerError, "删除订阅源失败")
		return
	}
	ok(w, nil)
}

// POST /api/admin/node-sources/{id}/fetch {group_ids}
func (a *API) handleAdminFetchSource(w http.ResponseWriter, r *http.Request) {
	id := atoi(chi.URLParam(r, "id"))
	src, err := a.st.GetSource(id)
	if err != nil || src == nil {
		fail(w, http.StatusNotFound, "订阅源不存在")
		return
	}
	var req struct {
		GroupIDs []int64 `json:"group_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	// Group binding lives on the source so it survives periodic auto-sync. If
	// the request explicitly carries group_ids, commit them with successful nodes;
	// otherwise reuse whatever the source already has.
	// nil means reuse the live binding inside the replacement transaction, so
	// a slow refresh cannot overwrite a group edit made while it was fetching.
	count, ferr := a.fetchSource(r.Context(), src, req.GroupIDs)
	if r.Context().Err() != nil {
		return
	}
	if ferr != "" {
		fail(w, http.StatusBadGateway, "抓取失败: "+ferr)
		return
	}
	ok(w, J{"imported": count})
}

// fetchSource downloads a source URL, parses links, and replaces the source's
// nodes. Returns the imported count and an error string (empty on success).
const maxSourceBytes = 8 << 20

func (a *API) fetchSource(ctx context.Context, src *store.NodeSource, groupIDs []int64) (int, string) {
	failed := func(err error) (int, string) {
		msg := err.Error()
		if saveErr := a.st.ReplaceSourceNodes(src.ID, nil, groupIDs, msg); saveErr != nil {
			msg += "; 保存抓取错误失败: " + saveErr.Error()
		}
		return 0, msg
	}
	if msg := validFetchURL(src.URL); msg != "" {
		return failed(fmt.Errorf("%s", msg))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
	if err != nil {
		return failed(err)
	}
	// One API-owned client is shared by manual fetches and periodic sync.
	resp, err := a.sourceClient.Do(req)
	if err != nil {
		return failed(err)
	}
	defer resp.Body.Close()
	// Only a complete representation may replace the last successful snapshot.
	// In particular, 204 and 206 are not valid subscription responses.
	if resp.StatusCode != http.StatusOK {
		return failed(fmt.Errorf("HTTP %d", resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSourceBytes+1))
	if err != nil {
		return failed(fmt.Errorf("读取订阅失败: %w", err))
	}
	if len(body) > maxSourceBytes {
		return failed(fmt.Errorf("订阅响应超过 8 MiB"))
	}
	// Some subscriptions mislabel plain/base64 bodies as text/html; inspect the
	// actual body so those existing sources continue to work.
	if strings.HasPrefix(http.DetectContentType(body), "text/html") {
		return failed(fmt.Errorf("订阅返回 HTML 页面，保留上次成功结果"))
	}
	proxies := subconv.ParseList(string(body))
	if len(proxies) == 0 {
		return failed(fmt.Errorf("订阅未包含有效节点，保留上次成功结果"))
	}
	if err := ctx.Err(); err != nil {
		return failed(err)
	}
	nodes := make([]store.Node, 0, len(proxies))
	for _, p := range proxies {
		nodes = append(nodes, store.Node{Name: p.Name, Protocol: p.Protocol, ShareLink: p.Raw})
	}
	if err := a.st.ReplaceSourceNodes(src.ID, nodes, groupIDs, ""); err != nil {
		return failed(err)
	}
	return len(nodes), ""
}

// StartSourceSync periodically refreshes enabled node sources.
func (a *API) StartSourceSync(ctx context.Context, interval time.Duration, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				srcs, err := a.st.ListSources()
				if err != nil {
					continue
				}
				for _, s := range srcs {
					if ctx.Err() != nil {
						return
					}
					if !s.Enabled {
						continue
					}
					if _, ferr := a.fetchSource(ctx, s, nil); ferr != "" {
						log.Printf("source sync %q: %s", s.Name, ferr)
					}
				}
			}
		}
	}()
}
