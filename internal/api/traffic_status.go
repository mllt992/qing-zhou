package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"qingzhou/internal/intervalcfg"
	"qingzhou/internal/store"
)

// handleServerTrafficStatus answers "what did this machine's network look like
// between these two times?" from rows already stored by the probe. It does not
// poll sing-box, capture packets, or read anything but server_metrics.
//
// Query: from, to — unix seconds (milliseconds accepted). The window is
// clamped to the metrics retention horizon and rejected when it is wider than
// 30 days or entirely in the future / already pruned.
func (a *API) handleServerTrafficStatus(w http.ResponseWriter, r *http.Request) {
	// id 0 is the panel's own machine (store.LocalNodeID). atoi of a missing
	// param is also 0, but this route always has {id}.
	id := atoi(chi.URLParam(r, "id"))
	from, err1 := parseUnixQuery(r.URL.Query().Get("from"))
	to, err2 := parseUnixQuery(r.URL.Query().Get("to"))
	if err1 != nil || err2 != nil {
		fail(w, 400, "请提供起止时间")
		return
	}
	gap := int64(intervalcfg.OnlineWindow(a.st) / time.Second)
	status, err := a.st.QueryTrafficStatus(id, from, to, time.Now().Unix(), gap)
	if errors.Is(err, store.ErrTrafficStatusRange) {
		fail(w, 400, "时间范围无效：需要落在最近 35 天内，且跨度不超过 30 天")
		return
	}
	if err != nil {
		fail(w, 500, "查询流量状态失败")
		return
	}
	ok(w, status)
}

// parseUnixQuery accepts unix seconds, or milliseconds when the value is too
// big to be seconds. The date picker sends milliseconds.
func parseUnixQuery(raw string) (int64, error) {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return 0, errors.New("bad time")
	}
	if n > 1_000_000_000_000 {
		n /= 1000
	}
	return n, nil
}
