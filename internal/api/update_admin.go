package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"

	"qingzhou/internal/updater"
	"qingzhou/internal/version"
)

// handleUpdateCheck queries GitHub for the latest release and reports whether a
// newer version is available, along with the changelog (release body).
func (a *API) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if a.updater == nil {
		fail(w, http.StatusServiceUnavailable, "更新服务未启用")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	res, err := a.updater.Check(ctx)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	ok(w, res)
}

// handleUpdateStatus returns the current progress of an in-flight update, plus
// the running binary's version so the UI can detect a successful restart (the
// version flips once the re-execed process answers again).
func (a *API) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if a.updater == nil {
		fail(w, http.StatusServiceUnavailable, "更新服务未启用")
		return
	}
	st := a.updater.State()
	ok(w, J{
		"status":         st.Status,
		"message":        st.Message,
		"percent":        st.Percent,
		"target_version": st.TargetVersion,
		"started_at":     st.StartedAt,
		"current":        version.Current(),
	})
}

// handleUpdateReleases lists the recent releases so the admin can pick a
// specific version instead of only ever taking "latest" — which is the only way
// off a release that turned out to be broken once it is no longer the newest.
func (a *API) handleUpdateReleases(w http.ResponseWriter, r *http.Request) {
	if a.updater == nil {
		fail(w, http.StatusServiceUnavailable, "更新服务未启用")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	list, err := a.updater.ListReleases(ctx, 30)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	ok(w, J{"current": version.Current(), "releases": list})
}

// handleUpdateApply starts the download → verify → snapshot → swap → restart sequence in
// the background and returns immediately; the client polls the status endpoint.
//
// An optional {"version": "vX.Y.Z"} pins the release; omitting it keeps the
// original "install the latest" behaviour, so an old client calling this with
// no body is unaffected.
func (a *API) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if a.updater == nil {
		fail(w, http.StatusServiceUnavailable, "更新服务未启用")
		return
	}
	var req struct {
		Version string `json:"version"`
	}
	// A missing or empty body is the "latest" case, not an error.
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&req)
	}
	if err := a.updater.ApplyVersion(time.Now().Unix(), req.Version); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	ok(w, J{"started": true, "version": req.Version})
}

// handleUpdateRollbackState reports whether the previous binary is still on
// disk, and which version it is.
func (a *API) handleUpdateRollbackState(w http.ResponseWriter, r *http.Request) {
	if a.updater == nil {
		fail(w, http.StatusServiceUnavailable, "更新服务未启用")
		return
	}
	ok(w, a.updater.RollbackState())
}

// handleUpdateRollback swaps the previously-installed binary back in. No
// network, no download — this is the path that still works when the running
// release is broken or GitHub is unreachable.
func (a *API) handleUpdateRollback(w http.ResponseWriter, r *http.Request) {
	if a.updater == nil {
		fail(w, http.StatusServiceUnavailable, "更新服务未启用")
		return
	}
	if err := a.updater.Rollback(time.Now().Unix()); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	ok(w, J{"started": true})
}

// StartHostedProbeSync aligns QZ_PROBE_DIR with this panel's own release after
// boot. In-panel self-update used to replace only the panel binary, so the
// first start of a version that knows how to refresh probes is when the hosted
// agents actually catch up. Failures are logged; they must not delay listen.
func (a *API) StartHostedProbeSync(ctx context.Context) {
	if a == nil || a.updater == nil {
		return
	}
	go func() {
		c, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		if note := a.updater.SyncHostedProbes(c); note != "" {
			log.Printf("hosted probe sync: %s", note)
		}
	}()
}

// Snapshot routes are inside the authenticated administrator router group.
func (a *API) handleUpdateSnapshots(w http.ResponseWriter, r *http.Request) {
	if a.updater == nil {
		fail(w, http.StatusServiceUnavailable, "更新服务未启用")
		return
	}
	list, err := a.updater.ListSnapshots()
	if err != nil {
		log.Printf("upgrade snapshots: %v", err)
		fail(w, http.StatusInternalServerError, "读取升级快照失败，请检查服务器日志与目录权限")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	ok(w, J{"snapshots": list, "retention_count": updater.SnapshotRetention})
}

func (a *API) handleUpdateSnapshotDownload(w http.ResponseWriter, r *http.Request) {
	if a.updater == nil {
		fail(w, http.StatusServiceUnavailable, "更新服务未启用")
		return
	}
	f, info, err := a.updater.OpenSnapshot(chi.URLParam(r, "id"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fail(w, http.StatusNotFound, "快照不存在或已过保留期")
			return
		}
		log.Printf("upgrade snapshot download: %v", err)
		fail(w, http.StatusInternalServerError, "读取升级快照失败")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+info.ID+`.db"`)
	w.Header().Set("Content-Length", itoa(info.Size))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := io.Copy(w, f); err != nil {
		log.Printf("upgrade snapshot download: %v", err)
	}
}
