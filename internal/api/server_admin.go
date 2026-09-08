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

	"qingzhou/internal/sbctl"
	"qingzhou/internal/sbver"
	"qingzhou/internal/sshctl"
	"qingzhou/internal/store"
)

// ---- server management (multi-server sing-box orchestration) ----

// maskServerSecrets replaces stored SSH credentials with a "***" sentinel
// before returning a server to the client. The update handler preserves any
// field still equal to "***", so secrets never round-trip through the browser.
func maskServerSecrets(sv *store.Server) {
	if sv == nil {
		return
	}
	if sv.SSHKey != "" {
		sv.SSHKey = "***"
	}
	if sv.SSHKeyPass != "" {
		sv.SSHKeyPass = "***"
	}
	if sv.SSHPassword != "" {
		sv.SSHPassword = "***"
	}
	if sv.SudoPassword != "" {
		sv.SudoPassword = "***"
	}
}

func (a *API) handleAdminListServers(w http.ResponseWriter, r *http.Request) {
	servers, err := a.st.ListServers()
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取服务器失败")
		return
	}
	machineToken, _ := r.Context().Value(ctxAuthKind).(string)
	for _, sv := range servers {
		maskServerSecrets(sv)
		// The probe token is a write credential for /api/monitor/report. A
		// servers:read machine token must never receive it, otherwise read-only
		// access can be exchanged for forged metrics and server status updates.
		// Browser admins still need the token to copy the probe install command.
		if machineToken == "api_token" {
			sv.ProbeToken = ""
		}
	}
	ok(w, servers)
}

func (a *API) handleAdminCreateServer(w http.ResponseWriter, r *http.Request) {
	var sv store.Server
	if err := json.NewDecoder(r.Body).Decode(&sv); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	sv.Name = strings.TrimSpace(sv.Name)
	sv.Host = strings.TrimSpace(sv.Host)
	if sv.Name == "" || sv.Host == "" {
		fail(w, http.StatusBadRequest, "名称和主机不能为空")
		return
	}
	if sv.Port == 0 {
		sv.Port = 22
	}
	sv.SSHKeyPath = strings.TrimSpace(sv.SSHKeyPath)
	if err := a.checkServerKeyFile(&sv); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	sv.Enabled = true
	sv.Status = "unknown"
	id, err := a.st.CreateServer(sv)
	if err != nil {
		fail(w, http.StatusInternalServerError, "创建服务器失败")
		return
	}
	created, _ := a.st.GetServer(id)
	maskServerSecrets(created)
	ok(w, created)
}

func (a *API) handleAdminReorderServers(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.IDs) == 0 {
		fail(w, http.StatusBadRequest, "服务器顺序不能为空")
		return
	}
	if err := a.st.ReorderServers(body.IDs); err != nil {
		fail(w, http.StatusInternalServerError, "保存服务器顺序失败")
		return
	}
	ok(w, nil)
}

func (a *API) handleAdminUpdateServer(w http.ResponseWriter, r *http.Request) {
	id := atoi(chi.URLParam(r, "id"))

	// Decode ONTO the stored row, not into a zero value. UpdateServer writes all
	// 22 columns, while the edit form only posts nine — so decoding into a fresh
	// struct silently blanked everything it doesn't carry: the SSH key/password,
	// the probe token (breaking the running agent and flipping probe_enabled
	// off), and every field owned by the 监控管理 page (expiry_date, provider,
	// location, spec, price, notes). Renaming a server or changing its port was
	// enough to destroy its credentials irrecoverably.
	//
	// json.Decode leaves fields absent from the payload untouched, so this keeps
	// stored values for anything the caller didn't send.
	sv, err := a.st.GetServer(id)
	if err != nil || sv == nil {
		fail(w, http.StatusNotFound, "服务器不存在")
		return
	}
	if err := json.NewDecoder(r.Body).Decode(sv); err != nil {
		fail(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	sv.ID = id
	sv.Name = strings.TrimSpace(sv.Name)
	sv.Host = strings.TrimSpace(sv.Host)
	if sv.Name == "" || sv.Host == "" {
		fail(w, http.StatusBadRequest, "名称和主机不能为空")
		return
	}
	if sv.Port == 0 {
		sv.Port = 22
	}
	sv.SSHKeyPath = strings.TrimSpace(sv.SSHKeyPath)
	if err := a.checkServerKeyFile(sv); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	// The list response masks secrets as "***"; a client that echoes the masked
	// value back must not store the literal. sv already holds the stored value
	// (it was decoded onto the stored row), so this just undoes the echo.
	stored, _ := a.st.GetServer(id)
	if stored != nil {
		if sv.SSHKey == "***" {
			sv.SSHKey = stored.SSHKey
		}
		if sv.SSHKeyPass == "***" {
			sv.SSHKeyPass = stored.SSHKeyPass
		}
		if sv.SSHPassword == "***" {
			sv.SSHPassword = stored.SSHPassword
		}
		if sv.ProbeToken == "***" {
			sv.ProbeToken = stored.ProbeToken
		}
		if sv.SudoPassword == "***" {
			sv.SudoPassword = stored.SudoPassword
		}
	}
	if err := a.st.UpdateServer(*sv); err != nil {
		fail(w, http.StatusInternalServerError, "更新服务器失败")
		return
	}
	// Point the row at a different host and the pinned key stops being about the
	// machine we are dialling: it belongs to the old one, so every later connect
	// fails as "host key mismatch (possible MITM)" for what is really just a
	// different machine. OpenSSH indexes known_hosts BY host for the same reason;
	// storing the pin on the server row is what makes the host field mutable
	// underneath it. Drop the pin so the next connect re-pins by TOFU.
	//
	// Deliberately NOT done for a credential change: a new SSH password says
	// nothing about the machine's identity, and clearing on it would hand anyone
	// who can talk an admin into a password rotation a free way to make the panel
	// forget who it is talking to.
	if stored != nil && stored.Host != sv.Host && stored.HostKey != "" {
		if err := a.st.SetServerHostKey(id, ""); err != nil {
			log.Printf("admin: clear pinned host key after host change on server %d: %v", id, err)
		} else {
			log.Printf("admin: server %d host changed %s -> %s, pinned SSH host key dropped", id, stored.Host, sv.Host)
		}
	}
	saved, _ := a.st.GetServer(id)
	maskServerSecrets(saved)
	ok(w, saved)
}

func (a *API) handleAdminDeleteServer(w http.ResponseWriter, r *http.Request) {
	if err := a.st.DeleteServer(atoi(chi.URLParam(r, "id"))); err != nil {
		if errors.Is(err, store.ErrInUse) {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		fail(w, http.StatusInternalServerError, "删除服务器失败")
		return
	}
	ok(w, nil)
}

// checkServerKeyFile validates a row that names a key file, at save time.
//
// Deliberately at save rather than at first use: the row is being edited by
// someone who is looking at the panel right now, and a typo'd name or a wrong
// passphrase discovered here costs a second. Discovered on the next deploy it
// costs however long it takes someone to notice that one node stopped updating.
//
// The passphrase may still be masked at this point (the form echoes "***" back
// for secrets it did not retype), in which case only the file itself is checked.
func (a *API) checkServerKeyFile(sv *store.Server) error {
	if sv.SSHKeyPath == "" {
		return nil
	}
	pass := sv.SSHKeyPass
	if pass == "***" {
		pass = ""
		if stored, _ := a.st.GetServer(sv.ID); stored != nil {
			pass = stored.SSHKeyPass
		}
	}
	return sshctl.ValidateKeyFile(a.sshKeyDir, sv.SSHKeyPath, pass)
}

// serverHasCredentials reports whether a row has any way to authenticate.
//
// Single definition because it gates five different flows, and a key held as a
// FILE counts: without SSHKeyPath here, every row using one is reported as
// "no credentials configured" and shown as un-upgradable.
func serverHasCredentials(sv *store.Server) bool {
	return sv != nil && (sv.SSHKey != "" || sv.SSHKeyPath != "" || sv.SSHPassword != "")
}

// GET /api/admin/ssh-keys — list the private key files the panel can offer, so
// the server form can present a choice instead of asking for a pasted PEM.
//
// Names and readability only; never contents. A key that reaches the browser has
// already lost the property this whole feature exists to provide.
func (a *API) handleAdminListSSHKeys(w http.ResponseWriter, r *http.Request) {
	files, err := sshctl.ListKeyFiles(a.sshKeyDir)
	if err != nil {
		if errors.Is(err, sshctl.ErrKeyDirUnset) {
			ok(w, J{"dir": "", "configured": false, "files": []sshctl.KeyFile{}})
			return
		}
		fail(w, http.StatusInternalServerError, "读取私钥目录失败: "+err.Error())
		return
	}
	if files == nil {
		files = []sshctl.KeyFile{}
	}
	ok(w, J{"dir": a.sshKeyDir, "configured": true, "files": files})
}

// newRemoteManager builds an SSH manager that verifies host keys and pins them
// on first successful connect (trust-on-first-use), for all remote SSH flows.
func (a *API) newRemoteManager(timeout time.Duration) *sshctl.RemoteManager {
	rm := sshctl.New(sshctl.WithTimeout(timeout), sshctl.WithKeyDir(a.sshKeyDir))
	rm.SetHostKeyPersister(func(id int64, key string) error { return a.st.SetServerHostKey(id, key) })
	return rm
}

// POST /api/admin/servers/{id}/test — attempt an SSH connection and report
// success or failure. The SSH key/pass from DB is used (not from request body).
func (a *API) handleAdminTestServer(w http.ResponseWriter, r *http.Request) {
	id := atoi(chi.URLParam(r, "id"))
	sv, err := a.st.GetServer(id)
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取服务器失败")
		return
	}
	if sv == nil {
		fail(w, http.StatusNotFound, "服务器不存在")
		return
	}

	if !serverHasCredentials(sv) {
		_ = a.st.UpdateServerStatus(id, "error")
		fail(w, http.StatusBadRequest, "未配置 SSH 密钥或密码，无法连接")
		return
	}

	// Exercise the exact auth + connection path used for real config
	// deployment, so a passing test guarantees deployment can connect too.
	// This is also the natural place to pin the host key (trust-on-first-use).
	rm := a.newRemoteManager(10 * time.Second)
	cfg := sbctl.SSHConfigFor(sv)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	version, err := rm.TestConnection(ctx, cfg)
	if err != nil {
		_ = a.st.UpdateServerStatus(id, "error")
		fail(w, http.StatusBadGateway, "SSH 连接失败: "+err.Error())
		return
	}
	_ = a.st.UpdateServerStatus(id, "online")
	// TestConnection runs `sing-box version`; record it rather than showing it
	// once and forgetting, so the node version list is fresh after a test too.
	_ = a.st.SetNodeSingbox(sv.ID, sbver.Parse(version))
	// And record WHERE it found the binary. sing_box_bin used to be allowed to be
	// wrong or empty because every command fell back to `sing-box` from PATH —
	// a fallback that breaks under sudo, since sudo replaces PATH with sudoers'
	// secure_path. Writing the resolved absolute path back makes the column
	// self-healing instead, so nothing downstream has to guess again.
	if bin, err := rm.ResolveSingBoxBin(ctx, cfg); err == nil && bin != "" && bin != sv.SingBoxBin {
		if err := a.st.SetServerSingBoxBin(id, bin); err != nil {
			log.Printf("admin: record resolved sing-box path for server %d: %v", id, err)
		} else {
			log.Printf("admin: server %d sing-box resolved to %s (was %q)", id, bin, sv.SingBoxBin)
		}
	}
	ok(w, J{"status": "online", "message": "SSH 连接成功", "version": version})
}

// POST /api/admin/servers/{id}/clear-host-key — drop the pinned SSH host key and
// re-pin whatever the machine presents now.
//
// The pin is what stops an attacker who can answer for this IP from harvesting
// the root SSH credentials we would otherwise hand over. But it also refuses to
// connect when the key legitimately changed — a reinstalled VPS regenerates
// /etc/ssh/ssh_host_*, and reusing a server row for a replacement machine has
// the same effect. Until now that left the panel wedged with no way out of the
// UI: host_key is never sent to the client and editing the server doesn't touch
// it, so recovering meant editing the database by hand.
//
// Reconnecting right away is the point: it re-pins in the same click and returns
// the new fingerprint, so the admin can check it against the machine instead of
// trusting silently. The client side gates this behind a warning — clearing a
// pin that changed for reasons you can't explain is accepting a MITM.
func (a *API) handleAdminClearServerHostKey(w http.ResponseWriter, r *http.Request) {
	id := atoi(chi.URLParam(r, "id"))
	sv, err := a.st.GetServer(id)
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取服务器失败")
		return
	}
	if sv == nil {
		fail(w, http.StatusNotFound, "服务器不存在")
		return
	}
	if err := a.st.SetServerHostKey(id, ""); err != nil {
		fail(w, http.StatusInternalServerError, "清除失败")
		return
	}
	// Root-level trust decision: leave a trace even when it works, since the
	// panel keeps no other record of a pin being reset.
	log.Printf("admin: cleared pinned SSH host key for server %d (%s)", id, sv.Host)

	if !serverHasCredentials(sv) {
		ok(w, J{"message": "已清除固定的主机密钥；该服务器未配置 SSH 密钥或密码，无法立即重新连接"})
		return
	}
	// Dial with the pin cleared so the callback trusts-on-first-use and persists
	// the new key. sv still holds the OLD key in memory — zero it, or we would
	// verify against exactly the key we just removed.
	sv.HostKey = ""
	rm := a.newRemoteManager(10 * time.Second)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if _, err := rm.TestConnection(ctx, sbctl.SSHConfigFor(sv)); err != nil {
		_ = a.st.UpdateServerStatus(id, "error")
		fail(w, http.StatusBadGateway, "已清除固定的主机密钥，但重新连接失败："+err.Error())
		return
	}
	_ = a.st.UpdateServerStatus(id, "online")
	var fp string
	if cur, _ := a.st.GetServer(id); cur != nil {
		fp = sshctl.Fingerprint(cur.HostKey)
	}
	ok(w, J{"message": "已重新信任这台机器", "fingerprint": fp})
}

// POST /api/admin/servers/{id}/rebuild — rebuild sing-box config for a single
// server (server_id=0 means the local panel server).
func (a *API) handleAdminRebuildServer(w http.ResponseWriter, r *http.Request) {
	id := atoi(chi.URLParam(r, "id"))
	if id < 0 {
		fail(w, http.StatusBadRequest, "无效的服务器 ID")
		return
	}
	if a.sbctl == nil {
		fail(w, http.StatusServiceUnavailable, "sing-box 控制器未初始化")
		return
	}
	if err := a.sbctl.RebuildServer(int64(id)); err != nil {
		fail(w, http.StatusBadGateway, "重建失败: "+err.Error())
		return
	}
	ok(w, J{"message": "已重建"})
}
