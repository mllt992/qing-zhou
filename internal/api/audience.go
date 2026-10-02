package api

import (
	"net/http"
	"qingzhou/internal/store"
	"strconv"
)

func (a *API) handleAdminAudienceCounts(w http.ResponseWriter, r *http.Request) {
	snapshot, err := a.st.AudienceCounts()
	if err != nil {
		fail(w, 500, "读取节点权限失败")
		return
	}
	ok(w, J{"nodes": snapshot.NodeCounts, "groups": snapshot.GroupCounts})
}

func (a *API) handleAdminAudience(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	scope := r.URL.Query().Get("scope")
	if err != nil || ((scope == "node" || scope == "group") && id <= 0) || (scope == "server" && id < 0) || (scope != "node" && scope != "group" && scope != "server" && scope != "package") {
		fail(w, 400, "无效的权限查询")
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	if page > 1000000 {
		fail(w, 400, "页码无效")
		return
	}
	snapshot, err := a.st.ScopedAudience(scope, id)
	if err != nil {
		fail(w, 500, "读取用户权限失败")
		return
	}
	var rows []store.AudienceUser
	switch scope {
	case "node":
		rows = snapshot.Nodes[id]
	case "group":
		rows = snapshot.Groups[id]
	case "server":
		rows = snapshot.Servers[id]
	case "package":
		rows = snapshot.Packages[id]
	}
	// Bound response size while preserving the exact count and zero-use members.
	const size = 50
	total := len(rows)
	start := (page - 1) * size
	if start < 0 || start > total {
		start = total
	}
	end := start + size
	if end > total {
		end = total
	}
	rows = rows[start:end]
	if rows == nil {
		rows = []store.AudienceUser{}
	}
	ok(w, J{"users": rows, "total": total, "page": page, "page_size": size})
}
