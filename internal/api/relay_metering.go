package api

import (
	"encoding/json"
	"net/http"
	"qingzhou/internal/store"
)

const cumulativeMeteringSetting = "traffic_cumulative_metering"

func (a *API) handleGetRelayMetering(w http.ResponseWriter, r *http.Request) {
	links, err := a.st.RelayMeteringLinks()
	if err != nil {
		fail(w, 500, "读取链路计量失败")
		return
	}
	cumulative, _ := a.st.GetSetting(cumulativeMeteringSetting)
	var started int
	if err = a.st.DB().QueryRow(`SELECT COUNT(*) FROM traffic_metering_state WHERE mode='cumulative'`).Scan(&started); err != nil {
		fail(w, 500, "读取采集状态失败")
		return
	}
	credentials, err := a.st.RelayCredentialViews()
	if err != nil {
		fail(w, 500, "读取兼容凭据状态失败")
		return
	}
	out := J{"credentials": credentials, "enabled": a.st.RelayMeteringEnabled(), "cumulative_enabled": cumulative == "true", "cumulative_started": started > 0, "links": links, "compatibility_retained": true}
	if a.sbctl != nil {
		out["sync"] = a.sbctl.SyncStatuses()
	}
	ok(w, out)
}
func (a *API) handlePutRelayMetering(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled    bool `json:"enabled"`
		Cumulative bool `json:"cumulative_enabled"`
		Confirm    bool `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || !in.Confirm {
		fail(w, http.StatusBadRequest, "请确认内部凭据生成、配置重启及统计模式变更的影响")
		return
	}
	if a.sbctl == nil {
		fail(w, http.StatusServiceUnavailable, "sing-box 控制器未启用")
		return
	}
	var started int
	if err := a.st.DB().QueryRow(`SELECT COUNT(*) FROM traffic_metering_state WHERE mode='cumulative'`).Scan(&started); err != nil {
		fail(w, 500, "读取采集状态失败")
		return
	}
	if !in.Cumulative && started > 0 {
		fail(w, http.StatusConflict, "已有节点进入累计采集，不能直接切回清零；请保留累计模式，避免重复扣费")
		return
	}
	if err := a.st.ConfigureTrafficMetering(in.Enabled, in.Cumulative); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	a.sbctl.ScheduleRebuild()
	ok(w, J{"started": true, "message": "已保存，按落地优先顺序下发；旧凭据继续兼容，节点配置变更可能中断现有连接"})
}

func (a *API) handleRelayCredentialChange(w http.ResponseWriter, r *http.Request) {
	var in struct {
		store.RelayCredentialView
		Action  string `json:"action"`
		Confirm bool   `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || !in.Confirm {
		fail(w, http.StatusBadRequest, "请确认该旧凭据的停用或恢复，以及手工中转的迁移状态")
		return
	}
	if a.sbctl == nil {
		fail(w, http.StatusServiceUnavailable, "sing-box 控制器未启用")
		return
	}
	actor, _ := r.Context().Value(ctxUserID).(int64)
	if err := a.st.ChangeRelayCredential(actor, in.RelayCredentialView, in.Action); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	a.sbctl.ScheduleRebuild()
	ok(w, J{"started": true, "message": "已记录凭据变更请求，请刷新查看节点下发确认"})
}
