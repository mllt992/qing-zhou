package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"qingzhou/internal/idgen"
	"qingzhou/internal/intervalcfg"
	"qingzhou/internal/store"
	"qingzhou/internal/version"
)

// ---- Agent report (public, token-authenticated) ----

// handleMonitorReport receives metrics from a probe agent. Authenticates via
// the probe_token in the Authorization: Bearer header.
func (a *API) handleMonitorReport(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	tok = strings.TrimSpace(tok)
	if tok == "" {
		fail(w, 401, "缺少认证 token")
		return
	}

	sv, err := a.st.GetServerByProbeToken(tok)
	if err != nil {
		fail(w, 500, "服务器查询失败")
		return
	}
	if sv == nil {
		fail(w, 403, "无效的 token 或探针未启用")
		return
	}

	var m store.ServerMetrics
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		fail(w, 400, "请求格式错误")
		return
	}

	if err := a.st.InsertMetrics(sv.ID, m); err != nil {
		fail(w, 500, "写入指标失败")
		return
	}

	_ = a.st.TouchProbeSeen(sv.ID)
	_ = a.st.UpdateServerStatus(sv.ID, "online")

	// New probes use this response as a tiny control plane and reset their live
	// timer without a service restart. Old probes already ignore the body beyond
	// draining it, so adding the field is wire-compatible during rolling updates.
	ok(w, J{
		"ok":                     true,
		"probe_interval_seconds": int64(intervalcfg.Probe(a.st) / time.Second),
	})
}

// handleDownloadAgent serves the pre-compiled probe agent binary.
func (a *API) handleDownloadAgent(w http.ResponseWriter, r *http.Request) {
	arch := chi.URLParam(r, "arch") // linux-amd64 or linux-arm64
	path, err := probeBinaryPath(arch)
	if err != nil {
		if errors.Is(err, errUnsupportedProbeArch) {
			fail(w, 400, "不支持的架构，可选: linux-amd64, linux-arm64")
		} else {
			fail(w, 404, "探针二进制不存在")
		}
		return
	}

	http.ServeFile(w, r, path)
}

// handleDownloadInstallScript serves a one-click install script that downloads
// the probe binary and sets up systemd. Usage: bash <(curl -sL <panel>/api/monitor/install.sh) <token>
func (a *API) handleDownloadInstallScript(w http.ResponseWriter, r *http.Request) {
	panelURL := a.publicBase(r)
	script := `#!/bin/bash
# qingzhou-probe one-click installer
# Usage: bash <(curl -sL ` + panelURL + `/api/monitor/install.sh) <probe_token>
set -e
TOKEN="${1:-}"
if [ -z "$TOKEN" ]; then
  echo "用法: bash install.sh <probe_token>"
  echo "Token 在面板「服务器监控」页面获取"
  exit 1
fi
ARCH=$(uname -m)
case $ARCH in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
  *) echo "不支持的架构: $ARCH"; exit 1 ;;
esac
BIN_URL="` + panelURL + `/api/monitor/agent/linux-${ARCH}"
INSTALL_PATH="/usr/local/bin/qingzhou-probe"
ENV_FILE="/etc/qingzhou-probe.env"
SERVICE_FILE="/etc/systemd/system/qingzhou-probe.service"

echo "[1/4] 下载探针二进制 (${ARCH})..."
# 下载到同目录临时文件，成功后原子替换。直接覆盖正在运行的二进制会触发
# ETXTBSY(Text file busy)，导致 curl 写入失败(退出码 23)——升级场景必现。
TMP_PATH="${INSTALL_PATH}.new"
trap 'rm -f "$TMP_PATH"' EXIT
if command -v curl &> /dev/null; then
  CURL_RC=0
  HTTP_CODE=$(curl -sSL -w "%{http_code}" -o "$TMP_PATH" "$BIN_URL") || CURL_RC=$?
  if [ "$CURL_RC" != "0" ]; then
    echo "❌ 下载失败: curl 退出码 $CURL_RC"
    echo "   URL: $BIN_URL"
    echo "   请检查网络连接和DNS解析"
    exit 1
  fi
  if [ "$HTTP_CODE" != "200" ]; then
    echo "❌ 下载失败: HTTP $HTTP_CODE"
    echo "   URL: $BIN_URL"
    exit 1
  fi
elif command -v wget &> /dev/null; then
  wget -qO "$TMP_PATH" "$BIN_URL" || { echo "❌ 下载失败"; exit 1; }
else
  echo "❌ 错误: 需要 curl 或 wget"
  exit 1
fi
if [ ! -s "$TMP_PATH" ]; then
  echo "❌ 下载的文件为空，请检查 URL 是否正确"
  echo "   URL: $BIN_URL"
  exit 1
fi
chmod +x "$TMP_PATH"
# 原子替换：rename() 对正在运行的旧二进制是安全的，不会 ETXTBSY。
mv -f "$TMP_PATH" "$INSTALL_PATH"
trap - EXIT
echo "   已安装到 $INSTALL_PATH ($(du -h "$INSTALL_PATH" | cut -f1))"

echo "[2/4] 创建环境配置文件..."
cat > "$ENV_FILE" << EOF
QZ_PROBE_SERVER=` + panelURL + `
QZ_PROBE_TOKEN=${TOKEN}
EOF
chmod 600 "$ENV_FILE"

echo "[3/4] 创建 systemd 服务..."
cat > "$SERVICE_FILE" << 'EOF'
[Unit]
Description=Qingzhou Monitor Probe
After=network.target
[Service]
Type=simple
EnvironmentFile=/etc/qingzhou-probe.env
ExecStart=/usr/local/bin/qingzhou-probe
Restart=always
RestartSec=10
[Install]
WantedBy=multi-user.target
EOF

echo "[4/4] 启动服务..."
systemctl daemon-reload
systemctl enable qingzhou-probe &>/dev/null
systemctl restart qingzhou-probe

sleep 1
if systemctl is-active --quiet qingzhou-probe; then
  echo ""
  echo "✅ 探针安装完成！"
  echo "   服务状态: 运行中"
  echo "   查看日志: journalctl -u qingzhou-probe -f"
else
  echo ""
  echo "⚠️  服务启动失败，请检查日志:"
  echo "   journalctl -u qingzhou-probe -n 20 --no-pager"
  exit 1
fi
`
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\"install-probe.sh\"")
	w.Write([]byte(script))
}

// ---- Admin monitoring endpoints ----

func (a *API) handleMonitorDashboard(w http.ResponseWriter, r *http.Request) {
	freshSince := time.Now().Add(-intervalcfg.OnlineWindow(a.st)).Unix()
	total, online, expiring, err := a.st.CountProbeServersSince(freshSince)
	if err != nil {
		fail(w, 500, "查询失败")
		return
	}
	unread, _ := a.st.UnreadAlertCount()

	// Aggregate latest metrics across all probe servers.
	views, _ := a.st.ListProbeServersWithMetrics()
	var totalCPU float64
	var totalMemUsed, totalMemTotal, totalDiskUsed, totalDiskTotal int64
	var count int
	add := func(m *store.ServerMetrics) {
		if m == nil {
			return
		}
		totalCPU += m.CPUPercent
		totalMemUsed += m.MemUsed
		totalMemTotal += m.MemTotal
		totalDiskUsed += m.DiskUsed
		totalDiskTotal += m.DiskTotal
		count++
	}
	for _, v := range views {
		add(v.Metrics)
	}
	// The counts come from a SQL count over the servers table, which the panel's
	// own machine is deliberately absent from. Left out, the header would say
	// "0 台在线" on a panel that is plainly monitoring itself right below.
	latest, _ := a.st.GetLatestMetricsForAll()
	if local := a.localMonitorServer(latest); local != nil {
		total++
		now := time.Now()
		if local.LastSeen >= freshSince {
			online++
		}
		// Same 3-day window CountProbeServers uses.
		if local.ExpiryDate > now.Unix() && local.ExpiryDate <= now.AddDate(0, 0, 3).Unix() {
			expiring++
		}
		add(latest[store.LocalNodeID])
	}

	ok(w, J{
		"total_servers": total,
		"online":        online,
		"offline":       total - online,
		"expiring_soon": expiring,
		"alerts_unread": unread,
		"summary": J{
			"total_cpu":        totalCPU,
			"total_mem_used":   totalMemUsed,
			"total_mem_total":  totalMemTotal,
			"total_disk_used":  totalDiskUsed,
			"total_disk_total": totalDiskTotal,
		},
	})
}

func (a *API) handleMonitorServers(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	onlineWindow := now.Add(-intervalcfg.OnlineWindow(a.st)).Unix()
	latest, _ := a.st.GetLatestMetricsForAll() // one query instead of one per server
	probeJobs := a.probeUpgradeSnapshot()
	// Return ALL servers (not just probe-enabled) so the UI can manage probes,
	// with the panel's own machine at the head — it has no servers row and needs
	// none, but it is the machine the admin is most likely to want to see.
	servers, err := a.serversWithLocal(latest)
	if err != nil {
		fail(w, 500, "查询失败")
		return
	}
	cycles := make(map[int64]store.TrafficCycleQuery, len(servers))
	for _, sv := range servers {
		cycles[sv.ID] = store.TrafficCycleQuery{
			Start:          store.TrafficCycleStart(now, sv.TrafficResetDay, sv.TrafficResetMinute).Unix(),
			AccountingMode: sv.TrafficAccountingMode,
		}
	}
	monthUsage, err := a.st.TrafficUsageForBillingCycles(cycles)
	if err != nil {
		fail(w, 500, "查询本周期流量失败")
		return
	}

	type serverResp struct {
		ID                    int64                    `json:"id"`
		Name                  string                   `json:"name"`
		Host                  string                   `json:"host"`
		Local                 bool                     `json:"local"`
		Enabled               bool                     `json:"enabled"`
		ProbeEnabled          bool                     `json:"probe_enabled"`
		ProbeToken            string                   `json:"probe_token"`
		ProbeVersion          string                   `json:"probe_version"`
		ProbeTarget           string                   `json:"probe_target_version"`
		ProbeOutdated         bool                     `json:"probe_outdated"`
		ProbeUpgrading        bool                     `json:"probe_upgrading"`
		ProbeUpgradeError     string                   `json:"probe_upgrade_error"`
		ProbeUpgradeOutput    string                   `json:"probe_upgrade_output"`
		ProbeUpgradedAt       int64                    `json:"probe_upgraded_at"`
		PublicVisible         bool                     `json:"public_visible"`
		HomeVisibility        string                   `json:"home_visibility,omitempty"`
		PublicShowTraffic     bool                     `json:"public_show_traffic"`
		PublicShowPrice       bool                     `json:"public_show_price"`
		Provider              string                   `json:"provider"`
		Location              string                   `json:"location"`
		Spec                  string                   `json:"spec"`
		Price                 float64                  `json:"price"`
		ExpiryDate            int64                    `json:"expiry_date"`
		ExpiryNotifyEnabled   bool                     `json:"expiry_notify_enabled"`
		ExpiryNotifyDays      int                      `json:"expiry_notify_days"`
		ExpiryNotifyMode      string                   `json:"expiry_notify_mode"`
		ExpiryNotifyCount     int                      `json:"expiry_notify_count"`
		TrafficLimitBytes     int64                    `json:"traffic_limit_bytes"`
		TrafficResetDay       int                      `json:"traffic_reset_day"`
		TrafficResetMinute    int                      `json:"traffic_reset_minute"`
		TrafficAlertPercent   int                      `json:"traffic_alert_percent"`
		TrafficAccountingMode string                   `json:"traffic_accounting_mode"`
		TrafficCycleStart     int64                    `json:"traffic_cycle_start"`
		TrafficNextReset      int64                    `json:"traffic_next_reset"`
		DaysLeft              *int                     `json:"days_left"`
		Status                string                   `json:"status"`
		LastSeen              int64                    `json:"last_seen"`
		Metrics               *store.ServerMetrics     `json:"metrics"`
		MonthTraffic          store.ServerTrafficUsage `json:"month_traffic"`
		Notes                 string                   `json:"notes"`
	}

	var out []serverResp
	for _, sv := range servers {
		maskServerSecrets(sv)
		status := "offline"
		if sv.LastSeen >= onlineWindow {
			status = "online"
		}
		var m *store.ServerMetrics
		if sv.ProbeEnabled {
			m = latest[sv.ID]
		}
		probeVersion := ""
		if m != nil {
			probeVersion = m.ProbeVersion
		}
		if sv.ID == store.LocalNodeID {
			probeVersion = version.Current()
		}
		probeOutdated := sv.ID != store.LocalNodeID && sv.ProbeEnabled && m != nil &&
			(probeVersion == "" || (!version.IsDev() && version.Compare(probeVersion, version.Current()) < 0))
		probeJob := probeJobs[sv.ID]
		var dl *int
		if sv.ExpiryDate > 0 {
			d := int(time.Unix(sv.ExpiryDate, 0).Sub(now).Hours() / 24)
			if d < 0 {
				d = 0
			}
			dl = &d
		}
		out = append(out, serverResp{
			ID:                    sv.ID,
			Name:                  sv.Name,
			Host:                  sv.Host,
			Local:                 sv.ID == store.LocalNodeID,
			Enabled:               sv.Enabled,
			ProbeEnabled:          sv.ProbeEnabled,
			ProbeToken:            sv.ProbeToken,
			ProbeVersion:          probeVersion,
			ProbeTarget:           version.Current(),
			ProbeOutdated:         probeOutdated,
			ProbeUpgrading:        probeJob.Running,
			ProbeUpgradeError:     probeJob.Error,
			ProbeUpgradeOutput:    probeJob.Output,
			ProbeUpgradedAt:       probeJob.FinishedAt,
			PublicVisible:         sv.PublicVisible,
			HomeVisibility:        a.homeVisibilityFor(sv),
			PublicShowTraffic:     sv.PublicShowTraffic,
			PublicShowPrice:       sv.PublicShowPrice,
			Provider:              sv.Provider,
			Location:              sv.Location,
			Spec:                  sv.Spec,
			Price:                 sv.Price,
			ExpiryDate:            sv.ExpiryDate,
			ExpiryNotifyEnabled:   sv.ExpiryNotifyEnabled,
			ExpiryNotifyDays:      sv.ExpiryNotifyDays,
			ExpiryNotifyMode:      sv.ExpiryNotifyMode,
			ExpiryNotifyCount:     sv.ExpiryNotifyCount,
			TrafficLimitBytes:     sv.TrafficLimitBytes,
			TrafficResetDay:       sv.TrafficResetDay,
			TrafficResetMinute:    sv.TrafficResetMinute,
			TrafficAlertPercent:   sv.TrafficAlertPercent,
			TrafficAccountingMode: sv.TrafficAccountingMode,
			TrafficCycleStart:     cycles[sv.ID].Start,
			TrafficNextReset:      store.TrafficCycleNext(now, sv.TrafficResetDay, sv.TrafficResetMinute).Unix(),
			DaysLeft:              dl,
			Status:                status,
			LastSeen:              sv.LastSeen,
			Metrics:               m,
			MonthTraffic:          monthUsage[sv.ID],
			Notes:                 sv.Notes,
		})
	}
	if out == nil {
		out = []serverResp{}
	}
	ok(w, out)
}

func (a *API) handleServerMetrics(w http.ResponseWriter, r *http.Request) {
	id := atoi(chi.URLParam(r, "id"))
	rangeStr := r.URL.Query().Get("range")
	if rangeStr == "" {
		rangeStr = "24h"
	}

	var since time.Duration
	switch rangeStr {
	case "1h":
		since = time.Hour
	case "6h":
		since = 6 * time.Hour
	case "24h":
		since = 24 * time.Hour
	case "7d":
		since = 7 * 24 * time.Hour
	case "30d":
		since = 30 * 24 * time.Hour
	default:
		since = 24 * time.Hour
	}

	sinceTs := time.Now().Add(-since).Unix()
	data, err := a.st.ListMetrics(id, sinceTs)
	if err != nil {
		fail(w, 500, "查询指标失败")
		return
	}
	if data == nil {
		data = []*store.ServerMetrics{}
	}
	usageByServer, err := a.st.TrafficUsageForAllSince(sinceTs)
	if err != nil {
		fail(w, 500, "查询流量汇总失败")
		return
	}
	ok(w, J{
		"server_id":     id,
		"range":         rangeStr,
		"data":          data,
		"traffic_usage": usageByServer[id],
	})
}

func (a *API) handleMonitorAlerts(w http.ResponseWriter, r *http.Request) {
	unreadOnly := r.URL.Query().Get("unread") == "1"
	alerts, err := a.st.ListAlerts(unreadOnly)
	if err != nil {
		fail(w, 500, "查询告警失败")
		return
	}
	if alerts == nil {
		alerts = []*store.ServerAlert{}
	}
	ok(w, alerts)
}

// handleMonitorHeatmap returns a server×time-bucket status matrix for the
// availability heatmap. Y axis = servers, X axis = time buckets (48 columns).
// Each cell value: 0=正常, 1=高负载, 2=严重负载, 3=离线/无数据.
// Query: range=1h|6h|24h|7d (default 24h).
func (a *API) handleMonitorHeatmap(w http.ResponseWriter, r *http.Request) {
	servers, buckets, matrix, rangeStr, bucketSec, good := a.buildHeatmap(w, r, false)
	if !good {
		return
	}
	type srvInfo struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	rows := make([]srvInfo, len(servers))
	for i, s := range servers {
		rows[i] = srvInfo{ID: s.ID, Name: s.Name}
	}
	if rows == nil {
		rows = []srvInfo{}
	}
	ok(w, J{
		"servers":     rows,
		"buckets":     buckets,
		"matrix":      matrix,
		"state_count": 4,
		"range":       rangeStr,
		"bucket_sec":  bucketSec,
	})
}

// handleMonitorPublicHeatmap is the public (no-auth) version: returns only
// server names (no IDs) for the public monitoring dashboard.
func (a *API) handleMonitorPublicHeatmap(w http.ResponseWriter, r *http.Request) {
	servers, buckets, matrix, rangeStr, bucketSec, good := a.buildHeatmap(w, r, true)
	if !good {
		return
	}
	type srvInfo struct {
		Name string `json:"name"`
	}
	rows := make([]srvInfo, len(servers))
	for i, s := range servers {
		rows[i] = srvInfo{Name: s.Name}
	}
	if rows == nil {
		rows = []srvInfo{}
	}
	ok(w, J{
		"servers":     rows,
		"buckets":     buckets,
		"matrix":      matrix,
		"state_count": 4,
		"range":       rangeStr,
		"bucket_sec":  bucketSec,
	})
}

// handleMonitorPublicSparklines returns downsampled recent CPU / network history
// per probe-enabled server for the public dashboard's mini charts. No auth.
func (a *API) handleMonitorPublicSparklines(w http.ResponseWriter, r *http.Request) {
	rangeStr := r.URL.Query().Get("range")
	var dur time.Duration
	switch rangeStr {
	case "6h":
		dur = 6 * time.Hour
	case "24h":
		dur = 24 * time.Hour
	default:
		rangeStr = "1h"
		dur = time.Hour
	}
	const points = 32
	startTs := time.Now().Add(-dur).Unix()
	bucketSec := int64(dur.Seconds()) / points
	if bucketSec < 1 {
		bucketSec = 1
	}

	latest, _ := a.st.GetLatestMetricsForAll()
	servers, err := a.serversWithLocal(latest)
	if err != nil {
		fail(w, 500, "查询失败")
		return
	}
	// server_id -> row index (publicly listed machines only, stable order)
	idx := map[int64]int{}
	type spark struct {
		Name string    `json:"name"`
		CPU  []float64 `json:"cpu"`
		Up   []int64   `json:"net_up"`
		Down []int64   `json:"net_down"`
	}
	var rows []spark
	for _, sv := range servers {
		// Same gate as the public server list — otherwise a machine kept off the
		// status page would still be there in outline, named, in the sparklines.
		if !sv.ProbeEnabled || !sv.PublicVisible {
			continue
		}
		idx[sv.ID] = len(rows)
		rows = append(rows, spark{
			Name: sv.Name,
			CPU:  make([]float64, points),
			Up:   make([]int64, points),
			Down: make([]int64, points),
		})
	}

	metrics, err := a.st.ListAllMetricsSince(startTs)
	if err != nil {
		fail(w, 500, "查询失败")
		return
	}
	// Accumulate into buckets, then average per bucket.
	cpuSum := make([][]float64, len(rows))
	upSum := make([][]int64, len(rows))
	downSum := make([][]int64, len(rows))
	cnt := make([][]int, len(rows))
	for i := range rows {
		cpuSum[i] = make([]float64, points)
		upSum[i] = make([]int64, points)
		downSum[i] = make([]int64, points)
		cnt[i] = make([]int, points)
	}
	for _, m := range metrics {
		ri, ok := idx[m.ServerID]
		if !ok {
			continue
		}
		b := int((m.Ts - startTs) / bucketSec)
		if b < 0 {
			b = 0
		} else if b >= points {
			b = points - 1
		}
		cpuSum[ri][b] += m.CPUPercent
		upSum[ri][b] += m.NetTx
		downSum[ri][b] += m.NetRx
		cnt[ri][b]++
	}
	for i := range rows {
		for b := 0; b < points; b++ {
			if c := cnt[i][b]; c > 0 {
				rows[i].CPU[b] = cpuSum[i][b] / float64(c)
				rows[i].Up[b] = upSum[i][b] / int64(c)
				rows[i].Down[b] = downSum[i][b] / int64(c)
			}
		}
	}
	if rows == nil {
		rows = []spark{}
	}
	ok(w, J{"servers": rows, "range": rangeStr, "points": points})
}

// heatRow is an internal row representation shared by admin/public heatmap.
type heatRow struct {
	ID   int64
	Name string
}

// buildHeatmap computes the server×time-bucket matrix shared by the admin and
// public heatmap endpoints. On error it writes the response and returns ok=false.
//
// publicOnly drops machines the admin has chosen not to announce. The two
// callers differ only in that, and getting it backwards would publish exactly
// what the flag exists to keep private — so it is a parameter rather than
// something each caller filters afterwards.
func (a *API) buildHeatmap(w http.ResponseWriter, r *http.Request, publicOnly bool) (rows []heatRow, buckets []int64, matrix [][]int, rangeStr string, bucketSec int64, success bool) {
	rangeStr = r.URL.Query().Get("range")
	if rangeStr == "" {
		rangeStr = "24h"
	}
	var dur time.Duration
	switch rangeStr {
	case "1h":
		dur = time.Hour
	case "6h":
		dur = 6 * time.Hour
	case "24h":
		dur = 24 * time.Hour
	case "7d":
		dur = 7 * 24 * time.Hour
	default:
		dur = 24 * time.Hour
	}

	const cols = 48
	now := time.Now()
	startTs := now.Add(-dur).Unix()
	bucketSec = int64(dur.Seconds()) / cols
	if bucketSec < 1 {
		bucketSec = 1
	}

	// Probe-enabled servers (rows), in stable order.
	latest, _ := a.st.GetLatestMetricsForAll()
	servers, err := a.serversWithLocal(latest)
	if err != nil {
		fail(w, 500, "查询失败")
		return nil, nil, nil, "", 0, false
	}
	idx := map[int64]int{} // server_id -> row index
	for _, sv := range servers {
		if !sv.ProbeEnabled || (publicOnly && !sv.PublicVisible) {
			continue
		}
		idx[sv.ID] = len(rows)
		rows = append(rows, heatRow{ID: sv.ID, Name: sv.Name})
	}
	if len(rows) == 0 {
		return rows, []int64{}, [][]int{}, rangeStr, bucketSec, true
	}

	// Bucket start timestamps (oldest first).
	buckets = make([]int64, cols)
	for i := 0; i < cols; i++ {
		buckets[i] = startTs + int64(i)*bucketSec
	}

	// Matrix init: 3=无数据(sentinel). Real classes: 0=正常,1=高负载,2=严重负载.
	// Keep the sentinel in the response so clients can distinguish a monitoring gap
	// from a sampled critical load instead of painting both states red.
	matrix = make([][]int, len(rows))
	for i := range matrix {
		matrix[i] = make([]int, cols)
		for j := range matrix[i] {
			matrix[i][j] = 3
		}
	}

	// Bucketed aggregation: track per-bucket worst (max) load class per server.
	all, err := a.st.ListAllMetricsSince(startTs)
	if err != nil {
		fail(w, 500, "查询指标失败")
		return nil, nil, nil, "", 0, false
	}
	for _, m := range all {
		ri, ok := idx[m.ServerID]
		if !ok {
			continue
		}
		b := int((m.Ts - startTs) / bucketSec)
		if b < 0 || b >= cols {
			continue
		}
		cpu := m.CPUPercent
		var memPct, diskPct float64
		if m.MemTotal > 0 {
			memPct = float64(m.MemUsed) / float64(m.MemTotal) * 100
		}
		if m.DiskTotal > 0 {
			diskPct = float64(m.DiskUsed) / float64(m.DiskTotal) * 100
		}
		class := 0
		if cpu >= 70 || memPct >= 70 || diskPct >= 70 {
			class = 1
		}
		if cpu >= 90 || memPct >= 90 || diskPct >= 90 {
			class = 2
		}
		cur := matrix[ri][b]
		if cur == 3 || class > cur {
			matrix[ri][b] = class
		}
	}
	return rows, buckets, matrix, rangeStr, bucketSec, true
}

func (a *API) handleMarkAlertRead(w http.ResponseWriter, r *http.Request) {
	id := atoi(chi.URLParam(r, "id"))
	if err := a.st.MarkAlertRead(id); err != nil {
		fail(w, 500, "标记失败")
		return
	}
	ok(w, nil)
}

func (a *API) handleMarkAllAlertsRead(w http.ResponseWriter, r *http.Request) {
	n, err := a.st.MarkAllAlertsRead()
	if err != nil {
		fail(w, 500, "标记失败")
		return
	}
	ok(w, J{"count": n})
}

func (a *API) handleUpdateServerMonitor(w http.ResponseWriter, r *http.Request) {
	id := atoi(chi.URLParam(r, "id"))

	// Partial update: only monitor-related fields.
	var body struct {
		ProbeEnabled          *bool    `json:"probe_enabled"`
		PublicVisible         *bool    `json:"public_visible"`
		HomeVisibility        *string  `json:"home_visibility"`
		PublicShowTraffic     *bool    `json:"public_show_traffic"`
		PublicShowPrice       *bool    `json:"public_show_price"`
		ExpiryDate            *int64   `json:"expiry_date"`
		ExpiryNotifyEnabled   *bool    `json:"expiry_notify_enabled"`
		ExpiryNotifyDays      *int     `json:"expiry_notify_days"`
		ExpiryNotifyMode      *string  `json:"expiry_notify_mode"`
		ExpiryNotifyCount     *int     `json:"expiry_notify_count"`
		TrafficLimitBytes     *int64   `json:"traffic_limit_bytes"`
		TrafficResetDay       *int     `json:"traffic_reset_day"`
		TrafficResetMinute    *int     `json:"traffic_reset_minute"`
		TrafficAlertPercent   *int     `json:"traffic_alert_percent"`
		TrafficAccountingMode *string  `json:"traffic_accounting_mode"`
		Provider              *string  `json:"provider"`
		Location              *string  `json:"location"`
		Spec                  *string  `json:"spec"`
		Price                 *float64 `json:"price"`
		Notes                 *string  `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	if body.ExpiryNotifyDays != nil && (*body.ExpiryNotifyDays < 1 || *body.ExpiryNotifyDays > 365) {
		fail(w, 400, "到期提前天数必须在 1–365 之间")
		return
	}
	if body.ExpiryNotifyMode != nil && *body.ExpiryNotifyMode != "count" && *body.ExpiryNotifyMode != "daily" {
		fail(w, 400, "到期重复方式无效")
		return
	}
	if body.ExpiryNotifyCount != nil && (*body.ExpiryNotifyCount < 1 || *body.ExpiryNotifyCount > 365) {
		fail(w, 400, "到期提醒次数必须在 1–365 之间")
		return
	}
	if body.TrafficLimitBytes != nil && *body.TrafficLimitBytes < 0 {
		fail(w, 400, "月流量上限不能小于 0")
		return
	}
	if body.TrafficResetDay != nil && (*body.TrafficResetDay < 1 || *body.TrafficResetDay > 31) {
		fail(w, 400, "流量重置日必须在 1–31 之间")
		return
	}
	if body.TrafficResetMinute != nil && (*body.TrafficResetMinute < 0 || *body.TrafficResetMinute > 1439) {
		fail(w, 400, "流量重置时间无效")
		return
	}
	if body.TrafficAlertPercent != nil && (*body.TrafficAlertPercent < 1 || *body.TrafficAlertPercent > 100) {
		fail(w, 400, "流量告警阈值必须在 1–100% 之间")
		return
	}
	if body.TrafficAccountingMode != nil && !store.IsTrafficAccountingMode(*body.TrafficAccountingMode) {
		fail(w, 400, "流量计费口径无效")
		return
	}

	// The panel's own machine has no servers row, so its settable fields live in
	// settings instead. Everything an admin records about a rented box still
	// applies — including, most of all, the expiry date: this is the machine
	// whose lapsing takes the whole service down. probe_enabled is the one field
	// that genuinely does not apply (the panel samples itself, there is no probe
	// to switch off) and is accepted and ignored.
	if id == store.LocalNodeID {
		if body.HomeVisibility != nil {
			if err := a.setLocalHomeVisibility(*body.HomeVisibility); err != nil {
				if errors.Is(err, errLocalHomeVisibility) {
					fail(w, 400, err.Error())
					return
				}
				fail(w, 500, "更新失败")
				return
			}
		} else if body.PublicVisible != nil {
			mode := localHomeAdmin
			if *body.PublicVisible {
				mode = localHomePublic
			}
			if err := a.setLocalHomeVisibility(mode); err != nil {
				fail(w, 500, "更新失败")
				return
			}
		}
		asset := a.st.LocalAsset()
		if body.Provider != nil {
			asset.Provider = *body.Provider
		}
		if body.Location != nil {
			asset.Location = *body.Location
		}
		if body.Spec != nil {
			asset.Spec = *body.Spec
		}
		if body.Price != nil {
			asset.Price = *body.Price
		}
		if body.PublicShowTraffic != nil {
			asset.PublicShowTraffic = *body.PublicShowTraffic
		}
		if body.PublicShowPrice != nil {
			asset.PublicShowPrice = *body.PublicShowPrice
		}
		if body.ExpiryDate != nil {
			asset.ExpiryDate = *body.ExpiryDate
		}
		if body.ExpiryNotifyEnabled != nil {
			asset.ExpiryNotifyEnabled = *body.ExpiryNotifyEnabled
		}
		if body.ExpiryNotifyDays != nil {
			asset.ExpiryNotifyDays = *body.ExpiryNotifyDays
		}
		if body.ExpiryNotifyMode != nil {
			asset.ExpiryNotifyMode = *body.ExpiryNotifyMode
		}
		if body.ExpiryNotifyCount != nil {
			asset.ExpiryNotifyCount = *body.ExpiryNotifyCount
		}
		if body.TrafficLimitBytes != nil {
			asset.TrafficLimitBytes = *body.TrafficLimitBytes
		}
		if body.TrafficResetDay != nil {
			asset.TrafficResetDay = *body.TrafficResetDay
		}
		if body.TrafficResetMinute != nil {
			asset.TrafficResetMinute = *body.TrafficResetMinute
		}
		if body.TrafficAlertPercent != nil {
			asset.TrafficAlertPercent = *body.TrafficAlertPercent
		}
		if body.TrafficAccountingMode != nil {
			asset.TrafficAccountingMode = *body.TrafficAccountingMode
		}
		if body.Notes != nil {
			asset.Notes = *body.Notes
		}
		if err := a.st.SetLocalAsset(asset); err != nil {
			fail(w, 500, "更新失败")
			return
		}
		latest, _ := a.st.GetLatestMetricsForAll()
		ok(w, a.localMonitorServer(latest))
		return
	}

	sv, err := a.st.GetServer(id)
	if err != nil {
		fail(w, 500, "读取服务器失败")
		return
	}
	if sv == nil {
		fail(w, 404, "服务器不存在")
		return
	}

	if body.PublicVisible != nil {
		sv.PublicVisible = *body.PublicVisible
	}
	if body.PublicShowTraffic != nil {
		sv.PublicShowTraffic = *body.PublicShowTraffic
	}
	if body.PublicShowPrice != nil {
		sv.PublicShowPrice = *body.PublicShowPrice
	}
	if body.ProbeEnabled != nil {
		sv.ProbeEnabled = *body.ProbeEnabled
		if sv.ProbeEnabled && sv.ProbeToken == "" {
			// Auto-generate probe token when enabling.
			tok, terr := idgen.RandToken(24)
			if terr != nil {
				fail(w, 500, "生成 token 失败")
				return
			}
			sv.ProbeToken = tok
		}
	}
	if body.ExpiryDate != nil {
		sv.ExpiryDate = *body.ExpiryDate
	}
	if body.ExpiryNotifyEnabled != nil {
		sv.ExpiryNotifyEnabled = *body.ExpiryNotifyEnabled
	}
	if body.ExpiryNotifyDays != nil {
		sv.ExpiryNotifyDays = *body.ExpiryNotifyDays
	}
	if body.ExpiryNotifyMode != nil {
		sv.ExpiryNotifyMode = *body.ExpiryNotifyMode
	}
	if body.ExpiryNotifyCount != nil {
		sv.ExpiryNotifyCount = *body.ExpiryNotifyCount
	}
	if body.TrafficLimitBytes != nil {
		sv.TrafficLimitBytes = *body.TrafficLimitBytes
	}
	if body.TrafficResetDay != nil {
		sv.TrafficResetDay = *body.TrafficResetDay
	}
	if body.TrafficResetMinute != nil {
		sv.TrafficResetMinute = *body.TrafficResetMinute
	}
	if body.TrafficAlertPercent != nil {
		sv.TrafficAlertPercent = *body.TrafficAlertPercent
	}
	if body.TrafficAccountingMode != nil {
		sv.TrafficAccountingMode = *body.TrafficAccountingMode
	}
	if body.Provider != nil {
		sv.Provider = *body.Provider
	}
	if body.Location != nil {
		sv.Location = *body.Location
	}
	if body.Spec != nil {
		sv.Spec = *body.Spec
	}
	if body.Price != nil {
		sv.Price = *body.Price
	}
	if body.Notes != nil {
		sv.Notes = *body.Notes
	}

	if err := a.st.UpdateServer(*sv); err != nil {
		fail(w, 500, "更新失败")
		return
	}

	saved, _ := a.st.GetServer(id)
	maskServerSecrets(saved)
	ok(w, saved)
}

// handleCalibrateServerTraffic aligns this cycle's probe-derived IN+OUT with
// the provider control panel's current-used figure. The correction is keyed to
// the current cycle start, so it expires naturally at the next reset.
func (a *API) handleCalibrateServerTraffic(w http.ResponseWriter, r *http.Request) {
	idParam := strings.TrimSpace(chi.URLParam(r, "id"))
	id := atoi(idParam)
	if id < 0 || (id == 0 && idParam != "0") {
		fail(w, 400, "服务器 ID 无效")
		return
	}
	var body struct {
		UsedBytes *int64 `json:"used_bytes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, 400, "请求格式错误")
		return
	}
	if body.UsedBytes == nil {
		fail(w, 400, "缺少当前已用流量")
		return
	}
	if *body.UsedBytes < 0 {
		fail(w, 400, "当前已用流量不能小于 0")
		return
	}

	resetDay, resetMinute, accountingMode := 0, 0, store.TrafficAccountingSum
	if id == store.LocalNodeID {
		asset := a.st.LocalAsset()
		resetDay, resetMinute = asset.TrafficResetDay, asset.TrafficResetMinute
		accountingMode = asset.TrafficAccountingMode
	} else {
		sv, err := a.st.GetServer(id)
		if err != nil {
			fail(w, 500, "读取服务器失败")
			return
		}
		if sv == nil {
			fail(w, 404, "服务器不存在")
			return
		}
		resetDay, resetMinute = sv.TrafficResetDay, sv.TrafficResetMinute
		accountingMode = sv.TrafficAccountingMode
	}

	now := time.Now()
	cycleStart := store.TrafficCycleStart(now, resetDay, resetMinute).Unix()
	if err := a.st.SetTrafficCalibrationForMode(id, cycleStart, accountingMode, *body.UsedBytes, now.Unix()); err != nil {
		fail(w, 500, "校准流量失败")
		return
	}
	usage, err := a.st.TrafficUsageForBillingCycles(map[int64]store.TrafficCycleQuery{id: {
		Start: cycleStart, AccountingMode: accountingMode,
	}})
	if err != nil {
		fail(w, 500, "读取校准结果失败")
		return
	}
	ok(w, J{"usage": usage[id], "cycle_start": cycleStart})
}

// ---- Public monitoring (no auth required) ----

// handleMonitorPublic returns a sanitized view of probe-enabled servers for the
// public monitoring dashboard. No authentication required; sensitive fields
// (SSH keys, probe tokens, host IPs) are excluded.
func (a *API) handleMonitorPublic(w http.ResponseWriter, r *http.Request) {
	latestAll, _ := a.st.GetLatestMetricsForAll()
	servers, err := a.serversWithLocal(latestAll)
	if err != nil {
		fail(w, 500, "查询失败")
		return
	}

	now := time.Now()
	onlineWindow := now.Add(-intervalcfg.OnlineWindow(a.st)).Unix()

	type pubMetrics struct {
		CPUPercent     float64 `json:"cpu_percent"`
		MemUsed        int64   `json:"mem_used"`
		MemTotal       int64   `json:"mem_total"`
		SwapUsed       int64   `json:"swap_used"`
		SwapTotal      int64   `json:"swap_total"`
		DiskUsed       int64   `json:"disk_used"`
		DiskTotal      int64   `json:"disk_total"`
		NetUp          int64   `json:"net_up"`
		NetDown        int64   `json:"net_down"`
		Load1          float64 `json:"load1"`
		Load5          float64 `json:"load5"`
		Load15         float64 `json:"load15"`
		TCPConnections int     `json:"tcp_connections"`
		ProcessCount   int     `json:"process_count"`
		Uptime         int64   `json:"uptime"`
		Platform       string  `json:"platform"`
		Arch           string  `json:"arch"`
	}

	type pubTraffic struct {
		UsedBytes      int64  `json:"used_bytes"`
		LimitBytes     int64  `json:"limit_bytes"`
		CycleStart     int64  `json:"cycle_start"`
		NextReset      int64  `json:"next_reset"`
		AccountingMode string `json:"accounting_mode"`
		SampleCount    int64  `json:"sample_count"`
		CoverageStart  int64  `json:"coverage_start"`
		Calibrated     bool   `json:"calibrated"`
	}
	type pubServer struct {
		Name     string      `json:"name"`
		Status   string      `json:"status"`
		Location string      `json:"location"`
		Provider string      `json:"provider"`
		Spec     string      `json:"spec"`
		DaysLeft *int        `json:"days_left"`
		Metrics  *pubMetrics `json:"metrics"`
		Price    *float64    `json:"price,omitempty"`
		Traffic  *pubTraffic `json:"traffic,omitempty"`
		LastSeen int64       `json:"last_seen"`
	}
	cycles := make(map[int64]store.TrafficCycleQuery)
	for _, sv := range servers {
		if sv.ProbeEnabled && sv.PublicVisible && sv.PublicShowTraffic {
			cycles[sv.ID] = store.TrafficCycleQuery{
				Start:          store.TrafficCycleStart(now, sv.TrafficResetDay, sv.TrafficResetMinute).Unix(),
				AccountingMode: sv.TrafficAccountingMode,
			}
		}
	}
	usage, err := a.st.TrafficUsageForBillingCycles(cycles)
	if err != nil {
		fail(w, 500, "查询本周期流量失败")
		return
	}

	latest := latestAll // already fetched above; one query, and this endpoint is unauthenticated/spammable
	var out []pubServer
	for _, sv := range servers {
		// Monitored and announced are two different decisions: an admin may want
		// to watch a machine without telling the world it exists. Everything that
		// existed before this flag was public, so the column defaults to visible
		// and only the panel's own machine starts hidden.
		if !sv.ProbeEnabled || !sv.PublicVisible {
			continue
		}
		status := "offline"
		if sv.LastSeen >= onlineWindow {
			status = "online"
		}
		var pm *pubMetrics
		if m := latest[sv.ID]; m != nil {
			pm = &pubMetrics{
				CPUPercent:     m.CPUPercent,
				MemUsed:        m.MemUsed,
				MemTotal:       m.MemTotal,
				SwapUsed:       m.SwapUsed,
				SwapTotal:      m.SwapTotal,
				DiskUsed:       m.DiskUsed,
				DiskTotal:      m.DiskTotal,
				NetUp:          m.NetTx,
				NetDown:        m.NetRx,
				Load1:          m.Load1,
				Load5:          m.Load5,
				Load15:         m.Load15,
				TCPConnections: m.TCPConnections,
				ProcessCount:   m.ProcessCount,
				Uptime:         m.Uptime,
				Platform:       m.Platform,
				Arch:           m.Arch,
			}
		}
		var dl *int
		if sv.ExpiryDate > 0 {
			d := int(time.Unix(sv.ExpiryDate, 0).Sub(now).Hours() / 24)
			if d < 0 {
				d = 0
			}
			dl = &d
		}
		var price *float64
		if sv.PublicShowPrice {
			p := sv.Price
			price = &p
		}
		var traffic *pubTraffic
		if cycle, shown := cycles[sv.ID]; shown {
			u := usage[sv.ID]
			traffic = &pubTraffic{
				UsedBytes: u.Total, LimitBytes: sv.TrafficLimitBytes,
				CycleStart:     cycle.Start,
				NextReset:      store.TrafficCycleNext(now, sv.TrafficResetDay, sv.TrafficResetMinute).Unix(),
				AccountingMode: u.AccountingMode, SampleCount: u.SampleCount,
				CoverageStart: u.CoverageStart, Calibrated: u.Calibrated,
			}
		}
		out = append(out, pubServer{
			Name:     sv.Name,
			Status:   status,
			Location: sv.Location,
			Provider: sv.Provider,
			Spec:     sv.Spec,
			DaysLeft: dl,
			Metrics:  pm,
			Price:    price,
			Traffic:  traffic,
			LastSeen: sv.LastSeen,
		})
	}
	if out == nil {
		out = []pubServer{}
	}
	ok(w, J{"servers": out})
}

// ---- Background tasks ----

// StartMonitorTasks starts the periodic probe alert checker and metrics pruner.
func (a *API) StartMonitorTasks(ctx context.Context) {
	a.StartLocalMetrics(ctx)
	a.StartRestartWatch(ctx)
	go func() {
		// Run soon after boot, then hourly. The initial delay gives the local
		// sampler and Telegram bot time to settle without postponing a warning for
		// a full hour after a restart.
		t := time.NewTimer(1 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := a.st.CheckProbeAlerts(); err != nil {
					log.Printf("probe alert check: %v", err)
				}
				a.sweepDeviceNotifications(time.Now())
				// Prune old restart samples. Open circuits remain latched until a
				// successful administrator-forced apply explicitly recovers them.
				a.sweepRestartAlerts()
				// Keep enough data for a complete calendar month even on day 31.
				if err := a.st.PruneMetrics(35); err != nil {
					log.Printf("metrics prune: %v", err)
				}
				// traffic_samples grows one row per active identity per stats poll.
				// Prune it on this same maintenance tick — previously it only ran when
				// an admin happened to open the overview page, so on an unattended
				// panel the table grew without bound (DB bloat, slow daily-traffic
				// GROUP BYs, ever-costlier WAL checkpoints).
				if err := a.st.PruneTrafficSamples(35); err != nil {
					log.Printf("traffic samples prune: %v", err)
				}
				if err := a.st.PruneDeviceNotifyState(time.Now().AddDate(-2, 0, 0).Unix()); err != nil {
					log.Printf("device notify prune: %v", err)
				}
				t.Reset(time.Hour)
			}
		}
	}()
}
