package api

import (
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"qingzhou/frontend"
	"qingzhou/internal/assets"
	"qingzhou/internal/mailer"
	"qingzhou/internal/sbctl"
	"qingzhou/internal/store"
	"qingzhou/internal/telegram"
	"qingzhou/internal/updater"
)

type API struct {
	st       *store.Store
	secret   []byte
	mailer   *mailer.Mailer // may be nil if SMTP is not configured
	authRL   *rateLimiter
	resendRL *rateLimiter
	probeRL  *rateLimiter // rate limit for agent report endpoint
	subRL    *rateLimiter // subscription-address swaps, per user
	// Old-password attempts on 修改密码, per user. That check is the
	// re-authentication gate in front of a takeover: a stolen session already
	// authenticates the request, so whoever holds it only needs the old password
	// to seize the account permanently — the change kicks every other session,
	// including the real owner's. Unthrottled it was an online brute force with
	// no cost and no trace. See handleChangePassword.
	pwRL *rateLimiter

	sbctl   *sbctl.Controller // native sing-box orchestrator; nil if not enabled
	updater *updater.Manager  // GitHub-release self-updater

	linkMu    sync.Mutex
	linkCache map[int64]linkCacheEntry

	// In-flight per-node sing-box reinstalls; see nodever_admin.go.
	upgradeMu   sync.Mutex
	upgradeJobs map[int64]*nodeUpgradeJob
	// In-flight remote probe installs/upgrades. Kept separate from sing-box jobs:
	// they manage different services and expose their state on different pages.
	probeUpgradeMu   sync.Mutex
	probeUpgradeJobs map[int64]*nodeUpgradeJob

	// Per-Telegram-user command limiter. The bind token is already gated by
	// resendRL; this one stops a bound chat from hammering /sub.
	tgRL *rateLimiter
	// Tests replace Telegram I/O; production leaves these nil.
	tgSendFn   func(chatID int64, html string) error
	tgClientFn func(token string) *telegram.Client

	// Where server rows may keep SSH private keys as files. Empty disables the
	// feature; see sshctl/keyfile.go.
	sshKeyDir string

	// Node restart-loop watching (restartalert.go). Both are buffered and both
	// are written to with a non-blocking send, so neither config deployment nor
	// the watcher can ever wait on the one behind it.
	restartCh chan restartEvent
	opsCh     chan string
	// monitorIntervalCh resets the panel host's local sampler when an admin
	// changes the same cadence remote probes receive. Coalesced and non-blocking.
	monitorIntervalCh chan struct{}
}

// SetSSHKeyDir points the panel at the directory holding SSH private key files.
func (a *API) SetSSHKeyDir(dir string) { a.sshKeyDir = dir }

// SetSbController attaches the native sing-box controller so admin changes to
// inbounds/TLS/users can trigger a config rebuild. Safe to leave unset.
func (a *API) SetSbController(c *sbctl.Controller) { a.sbctl = c }

// sbRebuild regenerates and applies the sing-box config if the native
// controller is attached; a no-op otherwise. Errors are returned to the caller.
func (a *API) sbRebuild() error {
	if a.sbctl == nil {
		return nil
	}
	return a.sbctl.Rebuild()
}

// sbRebuildLog triggers a rebuild after an already-committed change, without
// blocking the HTTP response. A full rebuild pushes config to every server over
// SSH (up to 90s per unreachable machine); holding the response open that long
// got cut off by the reverse proxy (「无法加载响应数据」) even though the save
// succeeded. The scheduler coalesces bursts and records a per-target status the
// admin UI can poll; a failed push self-heals on the next controller tick.
func (a *API) sbRebuildLog() {
	if a.sbctl == nil {
		return
	}
	a.sbctl.ScheduleRebuild()
}

// sbSyncInterval is the worst-case delay before a change that doesn't force its
// own rebuild reaches the nodes. Falls back to the controller's own default when
// no controller is attached.
func (a *API) sbSyncInterval() time.Duration {
	if a.sbctl == nil {
		return 10 * time.Minute
	}
	return a.sbctl.SyncInterval()
}

// sbScheduleServer queues an async rebuild of specific servers (0 = local panel),
// deduplicating and skipping zero ids. Used by save/delete paths that previously
// blocked on a synchronous per-server SSH push and could time out.
func (a *API) sbScheduleServer(serverIDs ...int64) {
	if a.sbctl == nil {
		return
	}
	seen := map[int64]bool{}
	for _, id := range serverIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		a.sbctl.ScheduleRebuildServer(id)
	}
}

func New(st *store.Store, secret []byte, mail *mailer.Mailer) *API {
	a := &API{
		st: st, secret: secret, mailer: mail,
		authRL:   newRateLimiter(20, time.Minute),   // 20 auth attempts / IP / min
		resendRL: newRateLimiter(3, 10*time.Minute), // 3 verify resends / user / 10min
		probeRL:  newRateLimiter(60, time.Minute),   // 60 probe reports / IP / min
		pwRL:     newRateLimiter(5, 10*time.Minute), // 5 修改密码 attempts / user / 10min
		// Each address swap revokes the previous one, so a loop of them — a stuck
		// retry, a double-click, a misbehaving script — leaves the user with a
		// subscription that never stays valid long enough to import. Generous
		// enough that nobody swapping addresses on purpose will notice.
		subRL:             newRateLimiter(5, 10*time.Minute), // 5 address swaps / user / 10min
		tgRL:              newRateLimiter(20, time.Minute),   // 20 bot commands / telegram user / min
		linkCache:         make(map[int64]linkCacheEntry),
		restartCh:         make(chan restartEvent, restartEventQueue),
		opsCh:             make(chan string, opsMessageQueue),
		monitorIntervalCh: make(chan struct{}, 1),
	}
	// Self-updater: repo + optional GitHub token come from env or DB settings,
	// falling back to the project's canonical repo.
	a.updater = updater.New(
		func() string {
			// Host-controlled only. The stored setting is read as a legacy
			// fallback but can no longer be written through the settings API
			// (see immutableSettings) — repointing the update source is
			// equivalent to arbitrary code execution on this host.
			if v := os.Getenv("QZ_UPDATE_REPO"); v != "" {
				return v
			}
			v, _ := st.GetSetting("update_repo")
			return v
		},
		func() string {
			if v := os.Getenv("QZ_UPDATE_GITHUB_TOKEN"); v != "" {
				return v
			}
			v, _ := st.GetSetting("update_github_token")
			return v
		},
	)
	return a
}

func (a *API) notifyRuntimeIntervalsChanged() {
	if a.sbctl != nil {
		a.sbctl.NotifyIntervalsChanged()
	}
	select {
	case a.monitorIntervalCh <- struct{}{}:
	default:
	}
}

func (a *API) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	// NOTE: middleware.RealIP is intentionally NOT used — it trusts client
	// X-Forwarded-For/X-Real-IP unconditionally, which would let any client spoof
	// its source IP and bypass the per-IP rate limiters. clientIP() honors those
	// headers only from a trusted proxy peer (see trustedproxy.go).
	r.Use(middleware.Recoverer)
	// gzip responses (JSON API + the embedded JS/CSS bundle). Cheap CPU for a large
	// bandwidth win on the small boxes this targets; skips already-compressed types.
	r.Use(middleware.Compress(5))
	r.Use(middleware.Timeout(30 * time.Second))
	// Cap request bodies so an authenticated client can't drive the process into
	// memory pressure with a multi-GB POST. 8 MiB comfortably covers the largest
	// legitimate payload (pasted airport lists / sing-box config templates).
	r.Use(maxBodyMiddleware(8 << 20))

	// Public
	r.Get("/api/health", a.handleHealth)
	r.Get("/api/config", a.handleConfig)
	r.Get("/api/auth/verify", a.handleVerify)
	r.Get("/sub/{token}", a.handleSub)

	// Auth POST endpoints — rate limited per IP (brute-force / email-bomb).
	r.Group(func(pub chi.Router) {
		pub.Use(a.limit(a.authRL))
		pub.Post("/api/auth/login", a.handleLogin)
		pub.Post("/api/auth/register", a.handleRegister)
		pub.Post("/api/auth/forgot", a.handleForgot)
		pub.Post("/api/auth/reset", a.handleReset)
	})

	// Probe agent endpoints — rate limited, token-authenticated (no JWT).
	r.Group(func(pub chi.Router) {
		pub.Use(a.limit(a.probeRL))
		pub.Post("/api/monitor/report", a.handleMonitorReport)
	})
	r.Get("/api/monitor/agent/{arch}", a.handleDownloadAgent)
	r.Get("/api/monitor/install.sh", a.handleDownloadInstallScript)

	// Public monitoring dashboard (no auth required).
	r.Get("/api/monitor/public", a.handleMonitorPublic)
	r.Get("/api/monitor/public/sparklines", a.handleMonitorPublicSparklines)
	r.Get("/api/monitor/heatmap", a.handleMonitorPublicHeatmap)

	// Authenticated (any logged-in user)
	r.Group(func(pr chi.Router) {
		pr.Use(a.authMiddleware)
		pr.Get("/api/auth/me", a.handleMe)
		pr.Post("/api/auth/logout", a.handleLogout)
		pr.Get("/api/user/dashboard", a.handleDashboard)
		pr.Get("/api/user/plans", a.handleUserPlans)
		pr.Get("/api/user/subscription", a.handleSubscription)
		pr.Get("/api/user/proxies", a.handleUserProxies)
		pr.Get("/api/user/proxy-account", a.handleUserProxyAccount)
		pr.Put("/api/user/proxy-account", a.handleUpdateUserProxyAccount)
		pr.Put("/api/user/proxies/{bucket}", a.handleUpdateUserProxy)
		pr.Post("/api/user/reset-sub", a.handleResetSub)
		pr.Post("/api/user/reset-node-creds", a.handleResetNodeCreds)
		pr.Get("/api/user/packages", a.handleUserPackages)
		pr.Post("/api/user/purchase", a.handlePurchase)
		pr.Get("/api/user/orders", a.handleUserOrders)
		pr.Get("/api/user/points", a.handleUserPoints)
		pr.Get("/api/user/stats/traffic", a.handleUserTrafficStats)
		pr.Post("/api/user/password", a.handleChangePassword)
		pr.Post("/api/user/resend-verify", a.handleResendVerify)
		pr.Post("/api/user/email", a.handleBindEmail)
		pr.Get("/api/user/telegram", a.handleUserTelegram)
		pr.Post("/api/user/telegram/bind-token", a.handleTelegramBindToken)
		pr.Post("/api/user/telegram/unbind", a.handleTelegramUnbind)
		pr.Put("/api/user/telegram/notify", a.handleTelegramNotifyPrefs)
		pr.Get("/api/user/announcements", a.handleUserAnnouncements)
		pr.Post("/api/user/announcements/read", a.handleUserMarkAnnouncementsRead)
		pr.Get("/api/help", a.handleHelpDocs)
		pr.Get("/api/user/sessions", a.handleUserSessions)
		pr.Post("/api/user/sessions/{id}/revoke", a.handleUserRevokeSession)
		pr.Get("/api/user/nodes", a.handleUserNodes)
		pr.Get("/api/user/nodes/ping", a.handleUserNodesPing)
		pr.Post("/api/user/nodes/toggle", a.handleUserToggleNode)
		pr.Post("/api/user/nodes/bulk", a.handleUserBulkNodes)
		pr.Post("/api/user/nodes/disable-all", a.handleUserDisableAllNodes)
		pr.Post("/api/user/nodes/enable-all", a.handleUserEnableAllNodes)
	})

	// Admin only
	r.Group(func(ar chi.Router) {
		ar.Use(a.authMiddleware)
		ar.Use(a.requireAdmin)
		ar.Use(a.enforceAPITokenScope)
		ar.Get("/api/admin/tokens", a.handleAdminListAPITokens)
		ar.Post("/api/admin/tokens", a.handleAdminCreateAPIToken)
		ar.Delete("/api/admin/tokens/{id}", a.handleAdminRevokeAPIToken)
		ar.Get("/api/admin/settings", a.handleGetSettings)
		ar.Put("/api/admin/settings", a.handlePutSettings)
		ar.Get("/api/admin/settings/default-templates", a.handleGetDefaultTemplates)
		ar.Post("/api/admin/settings/test-smtp", a.handleTestSMTP)
		ar.Post("/api/admin/settings/test-telegram", a.handleTestTelegram)
		ar.Get("/api/admin/ops-recipients", a.handleListOpsRecipients)
		ar.Put("/api/admin/ops-recipients/{id}", a.handleSetOpsRecipient)
		ar.Post("/api/admin/ops-recipients/test", a.handleTestOpsAlert)
		ar.Get("/api/admin/settings/detect-node-host", a.handleDetectNodeHost)
		ar.Post("/api/admin/rebuild", a.handleAdminRebuild)
		ar.Get("/api/admin/backup", a.handleAdminBackup)
		// Which sing-box each node runs, plus a per-node reinstall.
		ar.Get("/api/admin/nodes/singbox", a.handleAdminNodeVersions)
		ar.Post("/api/admin/nodes/singbox/refresh", a.handleAdminNodeVersionRefresh)
		ar.Post("/api/admin/nodes/{id}/singbox/upgrade", a.handleAdminNodeSingboxUpgrade)
		ar.Get("/api/admin/update/check", a.handleUpdateCheck)
		ar.Get("/api/admin/update/status", a.handleUpdateStatus)
		ar.Get("/api/admin/update/releases", a.handleUpdateReleases)
		ar.Get("/api/admin/update/rollback", a.handleUpdateRollbackState)
		ar.Post("/api/admin/update/rollback", a.handleUpdateRollback)
		ar.Post("/api/admin/update/apply", a.handleUpdateApply)
		ar.Get("/api/admin/help", a.handleAdminHelpDocs)
		ar.Post("/api/admin/help", a.handleAdminCreateHelpDoc)
		ar.Put("/api/admin/help/{id}", a.handleAdminUpdateHelpDoc)
		ar.Delete("/api/admin/help/{id}", a.handleAdminDeleteHelpDoc)
		ar.Delete("/api/admin/users/{id}", a.handleAdminDeleteUser)
		ar.Post("/api/admin/users/{id}/points", a.handleAdminRecharge)
		ar.Post("/api/admin/users/{id}/assign-plan", a.handleAdminAssignPlan)
		ar.Post("/api/admin/users/{id}/reset-node-creds", a.handleAdminResetNodeCreds)
		ar.Get("/api/admin/packages", a.handleAdminListPackages)
		ar.Post("/api/admin/packages", a.handleAdminCreatePackage)
		ar.Post("/api/admin/packages/reorder", a.handleAdminReorderPackages)
		ar.Put("/api/admin/packages/{id}", a.handleAdminUpdatePackage)
		ar.Post("/api/admin/packages/{id}/retire", a.handleAdminRetirePackage)
		ar.Post("/api/admin/packages/{id}/enable", a.handleAdminEnablePackage)
		ar.Delete("/api/admin/packages/{id}", a.handleAdminDeletePackage)
		ar.Get("/api/admin/orders", a.handleAdminListOrders)
		ar.Get("/api/admin/users/{id}/orders", a.handleAdminUserOrders)
		ar.Get("/api/admin/users/{id}/plans", a.handleAdminUserPlans)
		ar.Delete("/api/admin/users/{id}/plans/{planID}", a.handleAdminDeleteUserPlan)
		ar.Post("/api/admin/users/{id}/plans/{planID}/traffic", a.handleAdminAdjustUserPlanTraffic)
		ar.Get("/api/admin/orders/{id}/refund-preview", a.handleAdminRefundPreview)
		ar.Post("/api/admin/orders/{id}/refund", a.handleAdminRefundOrder)
		ar.Delete("/api/admin/orders/{id}", a.handleAdminDeleteOrder)

		// native sing-box (B2): TLS/Reality profiles, inbounds, config preview
		ar.Post("/api/admin/sb/reality-keypair", a.handleAdminRealityKeypair)
		ar.Get("/api/admin/sb/sni-test", a.handleAdminSniTest)
		ar.Get("/api/admin/sb/tls", a.handleAdminListSbTls)
		ar.Post("/api/admin/sb/tls", a.handleAdminSaveSbTls)
		ar.Post("/api/admin/sb/tls/reality", a.handleAdminCreateRealityTls)
		ar.Put("/api/admin/sb/tls/reality/{id}", a.handleAdminUpdateRealityTls)
		ar.Post("/api/admin/sb/tls/self-signed", a.handleAdminSelfSignedCert)
		ar.Post("/api/admin/sb/tls/quick-selfsigned", a.handleAdminQuickSelfSignedTls)
		ar.Post("/api/admin/sb/tls/acme", a.handleAdminAcmeCert)
		ar.Post("/api/admin/sb/tls/cert", a.handleAdminSaveCertTls)
		ar.Post("/api/admin/sb/tls/reorder", a.handleAdminReorderSbTls)
		ar.Put("/api/admin/sb/tls/cert/{id}", a.handleAdminSaveCertTls)
		ar.Put("/api/admin/sb/tls/{id}", a.handleAdminSaveSbTls)
		ar.Delete("/api/admin/sb/tls/{id}", a.handleAdminDeleteSbTls)

		// Certificate center: managed, reusable certificates issued on the panel
		// host (DNS-01) and referenced by TLS profiles via cert_id.
		ar.Get("/api/admin/certs", a.handleAdminListCerts)
		ar.Post("/api/admin/certs/acme", a.handleAdminCertAcme)
		ar.Post("/api/admin/certs/paste", a.handleAdminCertPaste)
		ar.Post("/api/admin/certs/self-signed", a.handleAdminCertSelfSigned)
		ar.Post("/api/admin/certs/{id}/renew", a.handleAdminCertRenew)
		ar.Get("/api/admin/certs/{id}/export", a.handleAdminExportCert)
		ar.Put("/api/admin/certs/{id}", a.handleAdminUpdateCert)
		ar.Delete("/api/admin/certs/{id}", a.handleAdminDeleteCert)
		ar.Get("/api/admin/sb/egresses", a.handleAdminListSbEgresses)
		ar.Post("/api/admin/sb/egresses", a.handleAdminSaveSbEgress)
		// Before the /{id} routes: chi would otherwise read "parse" as an id.
		ar.Post("/api/admin/sb/egresses/parse", a.handleAdminParseEgressLink)
		ar.Post("/api/admin/sb/egresses/{id}/clone", a.handleAdminCloneSbEgress)
		ar.Put("/api/admin/sb/egresses/{id}", a.handleAdminSaveSbEgress)
		ar.Delete("/api/admin/sb/egresses/{id}", a.handleAdminDeleteSbEgress)
		ar.Post("/api/admin/sb/egresses/{id}/test", a.handleAdminTestSbEgress)
		ar.Get("/api/admin/sb/sync-status", a.handleAdminSbSyncStatus)
		// Re-push config to a machine whose last sync failed. Queued, not awaited.
		ar.Post("/api/admin/sb/resync", a.handleAdminSbResync)
		ar.Get("/api/admin/sb/inbounds", a.handleAdminListSbInbounds)
		ar.Post("/api/admin/sb/inbounds", a.handleAdminSaveSbInbound)
		// Before the /{id} routes: chi would otherwise read "reorder" as an id.
		ar.Post("/api/admin/sb/inbounds/reorder", a.handleAdminReorderSbInbounds)
		ar.Put("/api/admin/sb/inbounds/{id}", a.handleAdminSaveSbInbound)
		ar.Delete("/api/admin/sb/inbounds/{id}", a.handleAdminDeleteSbInbound)
		ar.Post("/api/admin/sb/inbounds/{id}/ack-upstream", a.handleAdminAckUpstreamBroken)
		ar.Get("/api/admin/sb/preview", a.handleAdminSbPreview)
		ar.Get("/api/admin/sb/check", a.handleAdminSbCheck)
		ar.Get("/api/admin/sb/port-check", a.handleAdminPortCheck)
		ar.Get("/api/admin/sb/import-remote/list-files", a.handleAdminImportRemoteListFiles)
		ar.Get("/api/admin/sb/import-remote/preview", a.handleAdminImportRemotePreview)

		// server management (multi-server sing-box orchestration)
		ar.Get("/api/admin/servers", a.handleAdminListServers)
		ar.Post("/api/admin/servers", a.handleAdminCreateServer)
		// Before /{id}: chi would otherwise parse "reorder" as a server id.
		ar.Post("/api/admin/servers/reorder", a.handleAdminReorderServers)
		ar.Put("/api/admin/servers/{id}", a.handleAdminUpdateServer)
		ar.Delete("/api/admin/servers/{id}", a.handleAdminDeleteServer)
		ar.Post("/api/admin/servers/{id}/test", a.handleAdminTestServer)
		ar.Post("/api/admin/servers/{id}/rebuild", a.handleAdminRebuildServer)
		// Recover from a legitimately changed host key (reinstalled/replaced node).
		ar.Post("/api/admin/servers/{id}/clear-host-key", a.handleAdminClearServerHostKey)
		ar.Get("/api/admin/ssh-keys", a.handleAdminListSSHKeys)
		ar.Put("/api/admin/servers/{id}/monitor", a.handleUpdateServerMonitor)
		ar.Put("/api/admin/servers/{id}/traffic-calibration", a.handleCalibrateServerTraffic)

		// monitor probe
		ar.Get("/api/admin/monitor/dashboard", a.handleMonitorDashboard)
		ar.Get("/api/admin/monitor/servers", a.handleMonitorServers)
		ar.Get("/api/admin/monitor/servers/{id}/metrics", a.handleServerMetrics)
		ar.Get("/api/admin/monitor/servers/{id}/traffic-analysis", a.handleServerTrafficAnalysis)
		ar.Post("/api/admin/monitor/servers/{id}/probe/upgrade", a.handleAdminProbeUpgrade)
		ar.Get("/api/admin/monitor/heatmap", a.handleMonitorHeatmap)
		ar.Get("/api/admin/monitor/alerts", a.handleMonitorAlerts)
		ar.Post("/api/admin/monitor/alerts/{id}/read", a.handleMarkAlertRead)
		ar.Post("/api/admin/monitor/alerts/read-all", a.handleMarkAllAlertsRead)

		// nodes / groups / sources (Phase 4.5)
		ar.Get("/api/admin/inbounds", a.handleAdminInbounds)
		ar.Get("/api/admin/nodes", a.handleAdminListNodes)
		ar.Post("/api/admin/nodes", a.handleAdminCreateNode)
		ar.Post("/api/admin/nodes/reorder", a.handleAdminReorderNodes)
		ar.Post("/api/admin/nodes/import", a.handleAdminImportNodes)
		ar.Put("/api/admin/nodes/{id}", a.handleAdminUpdateNode)
		ar.Delete("/api/admin/nodes/{id}", a.handleAdminDeleteNode)
		ar.Get("/api/admin/node-groups", a.handleAdminListGroups)
		ar.Post("/api/admin/node-groups", a.handleAdminCreateGroup)
		ar.Put("/api/admin/node-groups/{id}", a.handleAdminUpdateGroup)
		ar.Delete("/api/admin/node-groups/{id}", a.handleAdminDeleteGroup)
		ar.Get("/api/admin/node-sources", a.handleAdminListSources)
		ar.Post("/api/admin/node-sources", a.handleAdminCreateSource)
		ar.Put("/api/admin/node-sources/{id}", a.handleAdminUpdateSource)
		ar.Post("/api/admin/node-sources/{id}/fetch", a.handleAdminFetchSource)
		ar.Delete("/api/admin/node-sources/{id}", a.handleAdminDeleteSource)

		// users + stats (Phase 5)
		ar.Get("/api/admin/users", a.handleAdminListUsers)
		ar.Post("/api/admin/users", a.handleAdminCreateUser)
		ar.Put("/api/admin/users/{id}", a.handleAdminUpdateUser)

		// user groups — who may buy a package (≠ node-groups above)
		ar.Get("/api/admin/user-groups", a.handleAdminListUserGroups)
		ar.Post("/api/admin/user-groups", a.handleAdminCreateUserGroup)
		ar.Put("/api/admin/user-groups/{id}", a.handleAdminUpdateUserGroup)
		ar.Delete("/api/admin/user-groups/{id}", a.handleAdminDeleteUserGroup)
		ar.Get("/api/admin/user-groups/{id}/members", a.handleAdminUserGroupMembers)
		ar.Put("/api/admin/user-groups/{id}/members", a.handleAdminSetUserGroupMembers)
		ar.Get("/api/admin/stats/overview", a.handleAdminOverview)
		ar.Get("/api/admin/stats/traffic", a.handleAdminTrafficStats)
		ar.Get("/api/admin/stats/distribution", a.handleAdminDistribution)
		ar.Get("/api/admin/stats/packages", a.handleAdminPackageStats)
		ar.Get("/api/admin/stats/users", a.handleAdminUserStats)
		ar.Get("/api/admin/stats/user/{id}/traffic", a.handleAdminUserTraffic)
		// 用量分析：多选用户 × 任意时间范围 × 套餐维度
		ar.Get("/api/admin/stats/usage", a.handleAdminUsage)
		ar.Get("/api/admin/stats/usage/users", a.handleAdminUsageUsers)
		ar.Get("/api/admin/stats/usage/packages", a.handleAdminUsagePackages)

		ar.Get("/api/admin/reg-codes", a.handleAdminListRegCodes)
		ar.Post("/api/admin/reg-codes/generate", a.handleAdminGenerateRegCodes)
		ar.Put("/api/admin/reg-codes/{id}", a.handleAdminUpdateRegCode)
		ar.Delete("/api/admin/reg-codes/{id}", a.handleAdminDeleteRegCode)

		ar.Get("/api/admin/announcements", a.handleAdminListAnnouncements)
		ar.Post("/api/admin/announcements", a.handleAdminCreateAnnouncement)
		ar.Put("/api/admin/announcements/{id}", a.handleAdminUpdateAnnouncement)
		ar.Delete("/api/admin/announcements/{id}", a.handleAdminDeleteAnnouncement)

		ar.Get("/api/admin/manual-notifications/users", a.handleAdminManualNotificationUsers)
		ar.Get("/api/admin/manual-notifications", a.handleAdminListManualNotifications)
		ar.Post("/api/admin/manual-notifications", a.handleAdminCreateManualNotification)
		ar.Get("/api/admin/manual-notifications/{id}", a.handleAdminManualNotificationDetail)
	})

	// sing-box one-click install script. Registered explicitly above the SPA
	// catch-all, otherwise `curl https://<panel>/install-singbox.sh | bash` would
	// fall through and pipe index.html into a shell. It is embedded separately
	// from the SPA (internal/assets) so a frontend rebuild can't drop it.
	r.Get("/install-singbox.sh", serveInstallScript)

	// The password-reset link from the email. Registered here for the same
	// reason as the install script: the SPA catch-all below would otherwise
	// swallow it — and did, silently, which is what made 找回密码 a dead end.
	// See handleResetPage.
	r.Get("/reset", a.handleResetPage)

	// Embedded SPA (must be last; specific routes above take precedence).
	r.Handle("/*", frontend.Handler())

	return r
}

// serveInstallScript serves the embedded sing-box install script.
func serveInstallScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, must-revalidate")
	_, _ = io.WriteString(w, assets.InstallScript())
}
