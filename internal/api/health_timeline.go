package api

import (
	"errors"
	"net/http"
	"time"

	"qingzhou/internal/intervalcfg"
	"qingzhou/internal/store"
)

// handleHealthTimeline is the admin overview strip: every probe-enabled
// machine on one time axis, read only from stored server_metrics.
//
// Query: from, to — unix seconds (milliseconds accepted). Same retention and
// span rules as the per-server traffic status view.
func (a *API) handleHealthTimeline(w http.ResponseWriter, r *http.Request) {
	a.serveHealthTimeline(w, r, false)
}

// handleUserHealthTimeline is the homepage copy for any logged-in user. It
// exists only while 「首页显示机器健康」 is on (404 otherwise), and lists only
// machines the public monitor already announces (probe on + public_visible),
// so a normal user never learns of a machine an admin chose to hide. The
// payload is the same strip shape: id, name, local flag, per-bucket probe state
// and NIC byte deltas — no host, IP, SSH or credential fields exist on it.
func (a *API) handleUserHealthTimeline(w http.ResponseWriter, r *http.Request) {
	if on, _ := a.st.GetSettingBool("homepage_machine_health"); !on {
		fail(w, http.StatusNotFound, "首页未开启机器健康")
		return
	}
	a.serveHealthTimeline(w, r, true)
}

func (a *API) serveHealthTimeline(w http.ResponseWriter, r *http.Request, publicOnly bool) {
	from, err1 := parseUnixQuery(r.URL.Query().Get("from"))
	to, err2 := parseUnixQuery(r.URL.Query().Get("to"))
	if err1 != nil || err2 != nil {
		fail(w, 400, "请提供起止时间")
		return
	}
	latest, _ := a.st.GetLatestMetricsForAll()
	machines, err := a.healthMachines(latest, publicOnly)
	if err != nil {
		fail(w, 500, "查询机器失败")
		return
	}
	gap := int64(intervalcfg.OnlineWindow(a.st) / time.Second)
	tl, err := a.st.QueryHealthTimeline(machines, from, to, time.Now().Unix(), gap)
	if errors.Is(err, store.ErrTrafficStatusRange) {
		fail(w, 400, "时间范围无效：需要落在最近 35 天内，且跨度不超过 30 天")
		return
	}
	if err != nil {
		fail(w, 500, "查询机器健康时间线失败")
		return
	}
	ok(w, tl)
}

// healthMachines is the overview row list: the panel itself, then every
// server whose probe is turned on. A probe that was never enabled is not an
// outage and is left off the strip.
//
// publicOnly limits the rows to what /api/monitor/public already shows:
// probe enabled and public_visible, the panel itself included only if it too
// is marked public.
func (a *API) healthMachines(latest map[int64]*store.ServerMetrics, publicOnly bool) ([]store.HealthMachine, error) {
	servers, err := a.serversWithLocal(latest)
	if err != nil {
		return nil, err
	}
	out := make([]store.HealthMachine, 0, len(servers)+1)
	hasLocal := false
	for _, sv := range servers {
		if publicOnly {
			if !sv.ProbeEnabled || !sv.PublicVisible {
				continue
			}
		} else if sv.ID == store.LocalNodeID {
			hasLocal = true
		} else if !sv.ProbeEnabled {
			continue
		}
		name := sv.Name
		if name == "" && sv.ID == store.LocalNodeID {
			name = store.LocalNodeName
		}
		out = append(out, store.HealthMachine{
			ID: sv.ID, Name: name, Local: sv.ID == store.LocalNodeID,
		})
	}
	if !hasLocal && !publicOnly {
		out = append([]store.HealthMachine{{
			ID: store.LocalNodeID, Name: store.LocalNodeName, Local: true,
		}}, out...)
	}
	return out, nil
}
