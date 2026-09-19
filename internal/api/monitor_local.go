package api

import (
	"context"
	"errors"
	"log"
	"time"

	"qingzhou/internal/intervalcfg"
	"qingzhou/internal/store"
	"qingzhou/internal/sysmetrics"
	"qingzhou/internal/version"
)

// The panel's own machine is a machine too. Watching it used to mean adding a
// servers row for it and installing the probe — except a servers row means "SSH
// in and deploy sing-box there", which for the local machine is both wrong
// (sbctl already manages it in-process, so it would be configured twice and
// restarted from two directions) and unnecessary. So the panel reads /proc
// itself and files the result under the server_id it already uses for itself
// everywhere else, store.LocalNodeID.
//
// No probe binary, no token, no systemd unit, no server row.

// settingLocalPublic is the legacy boolean for the unauthenticated status page.
// New installs and explicit three-state writes use settingLocalHomeVisibility.
const settingLocalPublic = "monitor_local_public"

// settingLocalHomeVisibility is the three-state homepage gate for 面板本机:
// public (everyone), admin (logged-in administrators only), hidden (nobody).
// Default admin — the old homepage always injected the local card for admins,
// while keeping it off the public page until opted in.
const settingLocalHomeVisibility = "monitor_local_home_visibility"

const (
	localHomePublic = "public"
	localHomeAdmin  = "admin"
	localHomeHidden = "hidden"
)

var errLocalHomeVisibility = errors.New("本机首页显示方式无效")

// StartLocalMetrics samples this host into the metrics table until ctx ends.
func (a *API) StartLocalMetrics(ctx context.Context) {
	if !sysmetrics.Supported() {
		// Not an error worth logging loudly: the readings come from /proc, and a
		// panel on Windows or macOS is a development one.
		return
	}
	go func() {
		// CPU percentage and network speed are deltas, so the first sample has
		// neither. Prime the sampler and drop it rather than opening every
		// panel's history with a fabricated idle minute.
		sampler := &sysmetrics.Sampler{}
		sampler.Sample()

		t := time.NewTimer(intervalcfg.Probe(a.st))
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-a.monitorIntervalCh:
				resetAPITimer(t, intervalcfg.Probe(a.st))
			case <-t.C:
				m := sampler.Sample()
				if err := a.st.InsertMetrics(store.LocalNodeID, store.ServerMetrics{
					ProbeVersion: version.Current(),
					CPUPercent:   m.CPUPercent, MemUsed: m.MemUsed, MemTotal: m.MemTotal,
					SwapUsed: m.SwapUsed, SwapTotal: m.SwapTotal,
					DiskUsed: m.DiskUsed, DiskTotal: m.DiskTotal,
					NetRx: m.NetRx, NetTx: m.NetTx,
					NetRxTotal: m.NetRxTotal, NetTxTotal: m.NetTxTotal,
					NetTotalsValid: m.NetTotalsValid,
					Load1:          m.Load1, Load5: m.Load5, Load15: m.Load15,
					TCPConnections: m.TCPConnections, ProcessCount: m.ProcessCount,
					Uptime: m.Uptime, Hostname: m.Hostname, Platform: m.Platform,
					Kernel: m.Kernel, Arch: m.Arch,
				}); err != nil {
					log.Printf("local metrics: %v", err)
				}
				t.Reset(intervalcfg.Probe(a.st))
			}
		}
	}()
}

func resetAPITimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

// localHomeVisibility reports where the panel's own machine appears.
// Missing three-state setting falls back to the legacy public boolean:
// true → public, false/absent → admin (the historical admin-homepage inject).
func (a *API) localHomeVisibility() string {
	if v, err := a.st.GetSetting(settingLocalHomeVisibility); err == nil {
		switch v {
		case localHomePublic, localHomeAdmin, localHomeHidden:
			return v
		}
	}
	legacy, _ := a.st.GetSettingBool(settingLocalPublic)
	if legacy {
		return localHomePublic
	}
	return localHomeAdmin
}

// localPublicVisible reports whether the panel's own machine is listed on the
// public status page.
func (a *API) localPublicVisible() bool {
	return a.localHomeVisibility() == localHomePublic
}

func normalizeLocalHomeVisibility(v string) (string, bool) {
	switch v {
	case localHomePublic, localHomeAdmin, localHomeHidden:
		return v, true
	default:
		return "", false
	}
}

func (a *API) setLocalHomeVisibility(v string) error {
	mode, ok := normalizeLocalHomeVisibility(v)
	if !ok {
		return errLocalHomeVisibility
	}
	if err := a.st.SetSetting(settingLocalHomeVisibility, mode); err != nil {
		return err
	}
	return a.st.SetSettingBool(settingLocalPublic, mode == localHomePublic)
}

// localMonitorServer builds the synthetic servers row standing for the panel's
// own machine, or nil when there is nothing to show — no metrics at all means
// either a fresh panel that has not sampled yet or a platform that cannot.
//
// Returning a *store.Server rather than a bespoke shape is deliberate: every
// monitor endpoint already walks a []*store.Server and derives status, metrics
// and public visibility from it, so the local machine can be prepended to that
// list and handled by the code that was already there.
func (a *API) localMonitorServer(latest map[int64]*store.ServerMetrics) *store.Server {
	m := latest[store.LocalNodeID]
	if m == nil {
		return nil
	}
	asset := a.st.LocalAsset()
	return &store.Server{
		ID:   store.LocalNodeID,
		Name: store.LocalNodeName,
		// The panel's machine is usually rented too, and its expiry is the one
		// that takes the whole service with it rather than a single node.
		Provider:              asset.Provider,
		Location:              asset.Location,
		Spec:                  asset.Spec,
		Price:                 asset.Price,
		ExpiryDate:            asset.ExpiryDate,
		ExpiryNotifyEnabled:   asset.ExpiryNotifyEnabled,
		ExpiryNotifyDays:      asset.ExpiryNotifyDays,
		ExpiryNotifyMode:      asset.ExpiryNotifyMode,
		ExpiryNotifyCount:     asset.ExpiryNotifyCount,
		TrafficLimitBytes:     asset.TrafficLimitBytes,
		TrafficResetDay:       asset.TrafficResetDay,
		TrafficResetMinute:    asset.TrafficResetMinute,
		TrafficAlertPercent:   asset.TrafficAlertPercent,
		TrafficAccountingMode: asset.TrafficAccountingMode,
		PublicShowTraffic:     asset.PublicShowTraffic,
		PublicShowPrice:       asset.PublicShowPrice,
		Notes:                 asset.Notes,
		// Host stays empty: the panel's address is not a secret, but printing it
		// on a status page next to "this is the control panel" is free targeting
		// help. The hostname in the metrics is what the detail view shows.
		Enabled: true,
		// The panel collects for this machine unconditionally, so as far as every
		// consumer is concerned its "probe" is always on. There is no token: the
		// UI must not offer an install command for a machine that needs none.
		ProbeEnabled:  true,
		PublicVisible: a.localPublicVisible(),
		// No servers row means no last_seen column to touch; the newest sample is
		// exactly as good an answer, and it cannot drift from the data.
		LastSeen: m.Ts,
		Status:   "online",
	}
}

func (a *API) homeVisibilityFor(sv *store.Server) string {
	if sv != nil && sv.ID == store.LocalNodeID {
		return a.localHomeVisibility()
	}
	return ""
}

// serversWithLocal is ListServers plus the panel's own machine at the head.
func (a *API) serversWithLocal(latest map[int64]*store.ServerMetrics) ([]*store.Server, error) {
	servers, err := a.st.ListServers()
	if err != nil {
		return nil, err
	}
	if local := a.localMonitorServer(latest); local != nil {
		servers = append([]*store.Server{local}, servers...)
	}
	return servers, nil
}
