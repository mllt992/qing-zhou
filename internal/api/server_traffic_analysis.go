package api

import (
	"math"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"qingzhou/internal/store"
)

type trafficCapacityProjection struct {
	Available                bool    `json:"available"`
	Reason                   string  `json:"reason"`
	SampleDays               float64 `json:"sample_days"`
	RemainingDays            float64 `json:"remaining_days"`
	DailyRateBytes           int64   `json:"daily_rate_bytes"`
	PerUserDailyBytes        int64   `json:"per_user_daily_bytes"`
	ProjectedCycleTotalBytes int64   `json:"projected_cycle_total_bytes"`
	EstimatedAdditionalUsers int     `json:"estimated_additional_users"`
	EstimatedExhaustionAt    int64   `json:"estimated_exhaustion_at"`
}

func finiteInt64(v float64) int64 {
	if math.IsNaN(v) || v <= 0 {
		return 0
	}
	if math.IsInf(v, 1) || v >= math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(math.Round(v))
}

func buildTrafficCapacity(now, nextReset time.Time, limitBytes int64, usage store.ServerTrafficUsage,
	attribution store.ServerTrafficAttribution, recent store.ServerTrafficUsage) trafficCapacityProjection {
	p := trafficCapacityProjection{Reason: "当前数据不足，暂时无法估算"}
	if limitBytes <= 0 {
		p.Reason = "未设置周期流量上限"
		return p
	}
	if !usage.Calibrated && usage.SampleCount < 2 {
		p.Reason = "周期流量仍在采集中"
		return p
	}
	if attribution.ActiveUsers <= 0 || attribution.CoverageStart <= 0 {
		p.Reason = "尚未采集到本机的活跃用户来源"
		return p
	}
	coverageStart := attribution.CoverageStart
	if recent.CoverageStart > coverageStart {
		coverageStart = recent.CoverageStart
	}
	seconds := float64(now.Unix() - coverageStart)
	if seconds < 6*3600 {
		p.Reason = "按服务器归因数据不足 6 小时，继续采集后再估算"
		return p
	}
	p.SampleDays = seconds / 86400
	p.RemainingDays = math.Max(0, nextReset.Sub(now).Hours()/24)
	if recent.Total <= 0 || p.RemainingDays <= 0 {
		p.Reason = "近期没有足够的物理网卡流量可用于估算"
		return p
	}
	dailyRate := float64(recent.Total) / p.SampleDays
	perUserDaily := dailyRate / float64(attribution.ActiveUsers)
	futureExisting := dailyRate * p.RemainingDays
	projected := float64(usage.Total) + futureExisting
	p.DailyRateBytes = finiteInt64(dailyRate)
	p.PerUserDailyBytes = finiteInt64(perUserDaily)
	p.ProjectedCycleTotalBytes = finiteInt64(projected)
	remaining := math.Max(0, float64(limitBytes-usage.Total))
	if dailyRate > 0 {
		daysToExhaust := remaining / dailyRate
		// Beyond the reset boundary the quota starts over, so such a date is not
		// an exhaustion prediction at all (and bounding it avoids Duration overflow
		// on huge quotas with tiny observed rates).
		if daysToExhaust <= p.RemainingDays {
			p.EstimatedExhaustionAt = now.Add(time.Duration(daysToExhaust*24) * time.Hour).Unix()
		}
	}
	costPerNewUser := perUserDaily * p.RemainingDays
	spareAfterExisting := math.Max(0, remaining-futureExisting)
	if costPerNewUser > 0 {
		capacity := math.Floor(spareAfterExisting / costPerNewUser)
		if capacity > math.MaxInt32 {
			capacity = math.MaxInt32
		}
		p.EstimatedAdditionalUsers = int(capacity)
	}
	p.Available = true
	p.Reason = "按归因窗口内的人均日消耗，预留现有活跃用户到本周期结束后的保守估算"
	return p
}

func (a *API) handleServerTrafficAnalysis(w http.ResponseWriter, r *http.Request) {
	id := atoi(chi.URLParam(r, "id"))
	sv, err := a.st.GetServer(id)
	if err != nil || sv == nil {
		fail(w, http.StatusNotFound, "服务器不存在")
		return
	}
	now := time.Now()
	cycleStart := store.TrafficCycleStart(now, sv.TrafficResetDay, sv.TrafficResetMinute)
	nextReset := store.TrafficCycleNext(now, sv.TrafficResetDay, sv.TrafficResetMinute)
	usageByServer, err := a.st.TrafficUsageForBillingCycles(map[int64]store.TrafficCycleQuery{
		id: {Start: cycleStart.Unix(), AccountingMode: sv.TrafficAccountingMode},
	})
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取周期流量失败")
		return
	}
	daily, err := a.st.ServerTrafficDaily(id, cycleStart.Unix(), sv.TrafficAccountingMode)
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取流量趋势失败")
		return
	}
	attribution, err := a.st.ServerTrafficAttribution(id, cycleStart.Unix(), 10)
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取流量来源失败")
		return
	}
	var recent store.ServerTrafficUsage
	if attribution.CoverageStart > 0 {
		recent, err = a.st.RawServerTrafficSince(id, attribution.CoverageStart, sv.TrafficAccountingMode)
		if err != nil {
			fail(w, http.StatusInternalServerError, "读取近期流量失败")
			return
		}
	}
	service, err := a.st.ServerServiceTraffic(id, cycleStart.Unix())
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取业务观测失败")
		return
	}
	if a.sbctl != nil && service.Quality.LastSuccess > 0 && now.Unix()-service.Quality.LastSuccess > int64(2*a.sbctl.SyncInterval()/time.Second) {
		service.Quality.Status = "stale"
		service.UserCoverageComplete = false
	}
	usage := usageByServer[id]
	projection := buildTrafficCapacity(now, nextReset, sv.TrafficLimitBytes, usage, attribution, recent)
	if !service.UserCoverageComplete {
		projection.Available = false
		projection.EstimatedAdditionalUsers = 0
		projection.Reason = "中转用户或统计覆盖不完整，暂不估算可新增人数"
	}
	ok(w, J{
		"schema_version": 2, "service": service,
		"server_id": id, "server_name": sv.Name,
		"accounting_mode": sv.TrafficAccountingMode,
		"cycle_start":     cycleStart.Unix(), "next_reset": nextReset.Unix(),
		"limit_bytes": sv.TrafficLimitBytes, "usage": usage,
		"daily": daily, "attribution": attribution,
		"projection": projection,
	})
}
