package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"qingzhou/internal/assets"
	"qingzhou/internal/sbctl"
	"qingzhou/internal/sbver"
	"qingzhou/internal/store"
)

var (
	errNotFound  = errors.New("服务器不存在")
	errNoCreds   = errors.New("未配置 SSH 密钥或密码，无法连接")
	errLinuxOnly = errors.New("一键升级仅支持 Linux 部署；请在服务器上手动执行安装脚本")
)

func runtimeIsLinux() bool { return runtime.GOOS == "linux" }

// nodeVersionView is one row of the node sing-box list.
type nodeVersionView struct {
	ServerID                  int64  `json:"server_id"` // 0 = the panel's own machine
	Name                      string `json:"name"`
	Host                      string `json:"host"`
	Local                     bool   `json:"local"`
	Enabled                   bool   `json:"enabled"`
	Version                   string `json:"version"`
	Raw                       string `json:"raw"`
	HasV2RayAPI               bool   `json:"has_v2ray_api"`
	HasVisionFramingFix       bool   `json:"has_vision_framing_fix"`
	HasTransportReadBufferFix bool   `json:"has_transport_read_buffer_fix"`
	HasTrojanHandshakeFix     bool   `json:"has_trojan_handshake_fix"`
	CheckedAt                 int64  `json:"checked_at"`
	Error                     string `json:"error"`
	// Reinstall job state, so the list the UI already polls is also what tells it
	// how the reinstall went. See nodeUpgradeJob.
	Upgrading     bool   `json:"upgrading"`
	UpgradeOutput string `json:"upgrade_output,omitempty"`
	UpgradeError  string `json:"upgrade_error,omitempty"`
	UpgradedAt    int64  `json:"upgraded_at,omitempty"`
	// TooOld means the generated config would be rejected by this binary — which
	// on a node means the panel has silently stopped being able to deploy to it.
	TooOld bool `json:"too_old"`
	// Upgradable is false for the local node when the panel is not on Linux, and
	// for remote nodes with no SSH credentials configured.
	Upgradable bool `json:"upgradable"`
}

// GET /api/admin/nodes/singbox — which sing-box each node is running.
//
// The panel already ran `sing-box version` on every remote node to decide
// whether traffic could be metered there, and discarded the answer. An operator
// who installed with the one-line script had no way to see, from the panel,
// what a node ended up with — or that it had been left behind.
func (a *API) handleAdminNodeVersions(w http.ResponseWriter, r *http.Request) {
	observed, err := a.st.NodeSingboxAll()
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取失败")
		return
	}
	servers, err := a.st.ListServers()
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取服务器失败")
		return
	}

	jobs := a.upgradeSnapshot()
	rows := []nodeVersionView{viewFor(store.LocalNodeID, "面板本机", "", true, true, observed, jobs)}
	for _, sv := range servers {
		v := viewFor(sv.ID, sv.Name, sv.Host, false, sv.Enabled, observed, jobs)
		// Without credentials there is no way in, so the button must not pretend.
		v.Upgradable = serverHasCredentials(sv)
		rows = append(rows, v)
	}
	ok(w, J{
		"nodes": rows,
		// Stated rather than hardcoded in the UI so the two cannot drift.
		"min_supported":           sbver.MinSupported,
		"vision_fixed_version":    sbver.VisionFramingFixVersion,
		"transport_fixed_version": sbver.TransportReadBufferFixVersion,
		"trojan_fixed_version":    sbver.TrojanHandshakeFixVersion,
	})
}

func viewFor(id int64, name, host string, local, enabled bool, observed map[int64]*store.NodeSingbox, jobs map[int64]nodeUpgradeJob) nodeVersionView {
	v := nodeVersionView{ServerID: id, Name: name, Host: host, Local: local, Enabled: enabled}
	if local {
		// The local upgrade shells out to bash; the install script is Linux-only.
		v.Upgradable = runtimeIsLinux()
	}
	if n := observed[id]; n != nil {
		v.Version, v.Raw, v.HasV2RayAPI = n.Version, n.Raw, n.HasV2RayAPI
		v.CheckedAt, v.Error = n.CheckedAt, n.Error
		v.HasVisionFramingFix = n.HasVisionFramingFix
		v.HasTransportReadBufferFix = n.HasTransportReadBufferFix
		v.HasTrojanHandshakeFix = n.HasTrojanHandshakeFix
		v.TooOld = sbver.Info{Version: n.Version}.TooOld()
	}
	if j, ok := jobs[id]; ok {
		v.Upgrading, v.UpgradeOutput, v.UpgradeError = j.Running, j.Output, j.Error
		v.UpgradedAt = j.FinishedAt
	}
	return v
}

// POST /api/admin/nodes/singbox/refresh — re-probe every node now.
func (a *API) handleAdminNodeVersionRefresh(w http.ResponseWriter, r *http.Request) {
	if a.sbctl == nil {
		fail(w, http.StatusServiceUnavailable, "sing-box 控制器未启用")
		return
	}
	// Probing every node over SSH can take a while on an unreachable one, so it
	// runs in the background and the client re-reads the list.
	go a.sbctl.RefreshVersions()
	ok(w, J{"started": true})
}

// nodeUpgradeJob is the state of one node's reinstall, kept in memory so the
// node list can report it. Memory rather than a table: a job cannot outlive the
// process that runs it, and a panel restart mid-install leaves the node's
// actual version to be settled by the next probe anyway.
type nodeUpgradeJob struct {
	Running    bool
	FinishedAt int64
	Output     string
	Error      string
}

// upgradeSnapshot copies the job table for reading.
func (a *API) upgradeSnapshot() map[int64]nodeUpgradeJob {
	a.upgradeMu.Lock()
	defer a.upgradeMu.Unlock()
	out := make(map[int64]nodeUpgradeJob, len(a.upgradeJobs))
	for id, j := range a.upgradeJobs {
		out[id] = *j
	}
	return out
}

// claimUpgrade reserves the job slot for one node, reporting false when a
// reinstall is already running there. Two concurrent runs of the install script
// on one machine would race over the same binary and unit file.
func (a *API) claimUpgrade(id int64) bool {
	a.upgradeMu.Lock()
	defer a.upgradeMu.Unlock()
	if a.upgradeJobs == nil {
		a.upgradeJobs = map[int64]*nodeUpgradeJob{}
	}
	if j := a.upgradeJobs[id]; j != nil && j.Running {
		return false
	}
	a.upgradeJobs[id] = &nodeUpgradeJob{Running: true}
	return true
}

func (a *API) finishUpgrade(id int64, out string, err error) {
	a.upgradeMu.Lock()
	defer a.upgradeMu.Unlock()
	j := a.upgradeJobs[id]
	if j == nil {
		return
	}
	j.Running, j.FinishedAt, j.Output = false, time.Now().Unix(), trimOutput(out)
	if err != nil {
		// The script's output is the diagnosis; without it the operator gets
		// "exit status 1" and nothing to act on.
		j.Error = trimOutput(err.Error() + "\n" + out)
	}
}

// POST /api/admin/nodes/{id}/singbox/upgrade — reinstall sing-box on one node.
//
// Starts the job and returns immediately; the client polls the node list, which
// carries the result. It cannot be synchronous: the script downloads a sing-box
// of ~60MB onto the node, which routinely takes longer than the server's
// WriteTimeout (main.go), so the response was torn off mid-flight and the panel
// reported a failure for an install that had in fact succeeded. Same reason
// sbRebuildLog exists — see the note on it in router.go.
//
// What the node did is not lost by going async: the script's output is kept on
// the job and shown when the poll picks it up.
func (a *API) handleAdminNodeSingboxUpgrade(w http.ResponseWriter, r *http.Request) {
	id := atoi(chi.URLParam(r, "id"))
	// Everything that can be judged without touching the node is judged now, so
	// a misconfigured server still answers with a real error instead of a job
	// that fails a minute later somewhere the operator has to go looking.
	if id == store.LocalNodeID {
		if !runtimeIsLinux() {
			fail(w, http.StatusBadRequest, errLinuxOnly.Error())
			return
		}
	} else if err := a.checkRemoteUpgradable(id); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if !a.claimUpgrade(id) {
		fail(w, http.StatusConflict, "该节点正在安装中，请等待本次完成")
		return
	}

	go func() {
		// context.Background, not r.Context: the request is already answered, and
		// inheriting its context would cancel the install the moment it is.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		var out string
		var err error
		if id == store.LocalNodeID {
			out, err = upgradeLocalSingBox(ctx)
		} else {
			out, err = a.upgradeRemoteSingBox(ctx, id)
		}
		a.finishUpgrade(id, out, err)
		// Re-probe so the panel shows the new number rather than the old one.
		// After the job is marked done, so a poll that sees "finished" sees the
		// refreshed version too.
		if err == nil && a.sbctl != nil {
			a.sbctl.RefreshVersions()
		}
	}()
	ok(w, J{"started": true})
}

// checkRemoteUpgradable reports why a node cannot be reinstalled, or nil.
func (a *API) checkRemoteUpgradable(id int64) error {
	sv, err := a.st.GetServer(id)
	if err != nil || sv == nil {
		return errNotFound
	}
	if !serverHasCredentials(sv) {
		return errNoCreds
	}
	return nil
}

func (a *API) upgradeRemoteSingBox(ctx context.Context, id int64) (string, error) {
	sv, err := a.st.GetServer(id)
	if err != nil || sv == nil {
		return "", errNotFound
	}
	if !serverHasCredentials(sv) {
		return "", errNoCreds
	}
	// Generous timeout: the script downloads a binary of tens of megabytes.
	rm := a.newRemoteManager(2 * time.Minute)
	// RefreshVersions invalidates the controller's long-lived binary cache after
	// a successful job. This per-request manager starts with no cache of its own.
	return rm.UpgradeSingBox(ctx, sbctl.SSHConfigFor(sv), assets.InstallScript())
}

// localSingboxBinDir is where the install script puts the binary — it must stay
// in step with BIN in internal/assets/install-singbox.sh, which
// TestLocalSingboxBinDirMatchesScript checks.
const localSingboxBinDir = "/usr/local/bin"

// upgradeLocalSingBox runs the same script on the panel's own machine.
func upgradeLocalSingBox(ctx context.Context) (string, error) {
	if !runtimeIsLinux() {
		return "", errLinuxOnly
	}
	systemdRun, _ := exec.LookPath("systemd-run")
	argv := localInstallArgv(dirReadOnly(localSingboxBinDir), systemdRun)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(assets.InstallScript())
	out, err := cmd.CombinedOutput()
	if err != nil {
		if hint := localInstallHint(argv, string(out)); hint != "" {
			err = fmt.Errorf("%w（%s）", err, hint)
		}
	}
	return string(out), err
}

// localInstallHint explains a failed local install when the reason is the
// panel's own sandbox rather than anything the script did.
//
// Both cases leave an operator with a message that points away from the truth —
// an EROFS on a machine whose disk is plainly writable, or a systemd-run error
// with no sign of the install it was supposed to run — so each says what
// happened and how to install by hand regardless.
//
// It must stay silent on every other failure. A script that died on its own
// terms (no network, not root, no such directory) already says so, and a wrong
// "只读" on top of that is the exact kind of misdirection this exists to end.
func localInstallHint(argv []string, out string) string {
	// Wrapping only happens after the probe hit a genuine EROFS, so on that path
	// the sandbox is the reason we are here whatever the failure looks like —
	// including systemd-run failing before the script ever ran, which is the one
	// failure that carries no trace of the install underneath it.
	viaSystemdRun := argv[0] != "bash"
	if !viaSystemdRun && !strings.Contains(out, "Read-only file system") {
		return ""
	}
	manually := "请登录服务器手动执行：curl -fsSL <面板地址>/install-singbox.sh | bash -s -- --force"
	if viaSystemdRun {
		return "安装路径对面板进程只读，已改由 systemd-run 代跑，但仍失败。" + manually
	}
	return "安装路径对面板进程只读 —— 面板 systemd 单元的 ProtectSystem 把 /usr 挡在了外面。" + manually
}

// localInstallArgv is how the install script gets run on the panel's own
// machine: directly under bash, or wrapped in systemd-run.
//
// Direct is the obvious choice and stays the default, but it is wrong on the
// panel's own recommended deployment. deploy/qingzhou.service (and the unit
// install.sh writes) sets ProtectSystem=full, which remounts /usr read-only
// inside the service's mount namespace — and a forked child inherits that
// namespace. The script installs to /usr/local/bin/sing-box, so clicking 重装
// for 面板本机 could only ever fail with EROFS, while the very same script run
// from a root shell on that machine succeeds. Nothing about the error said so:
// `install` reports "Read-only file system" for a disk that is writable.
//
// systemd-run asks PID1 to start a transient unit, which is created fresh from
// PID1's namespace and so sees /usr writable again. --pipe passes our stdin
// (the script) in and the output back, --wait blocks until it exits and
// propagates its status, --collect reaps the unit even when it fails. All four
// flags predate systemd 236; --service-type=exec is deliberately not used, as
// it needs 240.
//
// The wrapper is used only where the direct call is already doomed, so an
// unsandboxed host keeps the plain, dependency-free path and a host with no
// systemd-run at all still gets its attempt (and the EROFS hint above).
//
// The transient unit is named rather than anonymous, and capped: killing
// systemd-run — which is what the job's timeout does — does not stop the unit
// it asked PID1 to start. A fixed name makes the next click refuse to start a
// second install over the top of the first (the same collision claimUpgrade
// prevents in-process), and RuntimeMaxSec stops an orphan outliving the job
// that is supposed to own it. --collect frees the name again either way.
func localInstallArgv(binDirReadOnly bool, systemdRun string) []string {
	direct := []string{"bash", "-s", "--", "--force"}
	if !binDirReadOnly || systemdRun == "" {
		return direct
	}
	return append([]string{
		systemdRun, "--pipe", "--wait", "--collect", "--quiet",
		"--unit=qingzhou-singbox-install", "-p", "RuntimeMaxSec=600", "--",
	}, direct...)
}

// dirReadOnly reports whether dir sits on a mount this process cannot write to
// at all — the panel's own sandbox — as opposed to merely being out of reach
// for this user or missing entirely.
//
// It has to be answered by trying: the mount is read-only for this namespace,
// not for this user, so permission bits say nothing about it. And the distinction
// matters, because it is what the caller escalates to systemd-run over: a panel
// running as non-root, or a host with no /usr/local/bin, is better served by the
// script's own diagnosis ("请用 root 运行") than by a sandbox workaround that
// cannot help it and a message blaming a read-only disk.
func dirReadOnly(dir string) bool {
	f, err := os.CreateTemp(dir, ".qz-writable-")
	if err != nil {
		return errors.Is(err, syscall.EROFS)
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return false
}

// trimOutput bounds what a node's script can push into an API response.
func trimOutput(s string) string {
	s = strings.TrimSpace(s)
	const max = 8 << 10
	if len(s) > max {
		return "…" + s[len(s)-max:]
	}
	return s
}
