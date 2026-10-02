package api

import (
	"net/http"
	"strconv"
	"time"
)

func (a *API) handleAdminMachineUsage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("server"), 10, 64)
	if err != nil || id < 0 {
		fail(w, 400, "请选择机器")
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days != 7 && days != 14 && days != 30 {
		days = 30
	}
	now := time.Now()
	to := now.Unix() + 1
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -days+1).Unix()
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	if page > 1000000 {
		fail(w, 400, "页码无效")
		return
	}
	report, err := a.st.MachineUsage(id, from, to, page)
	if err != nil {
		fail(w, 500, "读取机器用户用量失败")
		return
	}
	ok(w, J{"report": report, "from": from, "to": to, "days": days, "page": page, "page_size": 50, "retention_days": 35, "source": "retained_user_samples"})
}
