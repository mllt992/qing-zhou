package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"qingzhou/internal/assets"
	"qingzhou/internal/store"
)

func newNodeUpgradeAPI(t *testing.T) (*API, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	return New(st, []byte("secret"), nil), st
}

func upgradeReq(id int64) *http.Request {
	path := "/api/admin/nodes/" + strconv.FormatInt(id, 10) + "/singbox/upgrade"
	req := httptest.NewRequest("POST", path, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", strconv.FormatInt(id, 10))
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// TestNodeSingboxUpgradeReturnsBeforeInstallFinishes is the regression test for
// the reason this handler is asynchronous.
//
// The install pushes a ~60MB sing-box onto the node, which routinely runs past
// the server's 30s WriteTimeout (main.go). A synchronous handler therefore had
// its response torn off the wire while the install was still going, and the
// panel reported 安装失败 for a node that had in fact installed correctly. So
// what matters is not merely that the endpoint works, but that it ANSWERS
// promptly and leaves the work running behind it.
func TestNodeSingboxUpgradeReturnsBeforeInstallFinishes(t *testing.T) {
	a, st := newNodeUpgradeAPI(t)
	// Hold a real local TCP connection before the SSH banner. Reserved Internet
	// addresses may reject immediately (sandbox / CI routing), so they cannot
	// deterministically establish that a job is still in flight.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	accepted := make(chan struct{})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		close(accepted)
		<-release
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	var serverID int64
	t.Cleanup(func() {
		close(release)
		listener.Close()
		select {
		case <-serverDone:
		case <-time.After(5 * time.Second):
			t.Error("SSH fixture goroutine did not exit")
		}
		deadline := time.Now().Add(5 * time.Second)
		for serverID > 0 {
			job, exists := a.upgradeSnapshot()[serverID]
			if !exists || !job.Running {
				break
			}
			if time.Now().After(deadline) {
				t.Error("upgrade goroutine did not exit after fixture closed")
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	id, err := st.CreateServer(store.Server{
		Name: "landing", Host: "127.0.0.1", Port: port, SSHUser: "root",
		SSHPassword: "hunter2", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	serverID = id
	w := httptest.NewRecorder()
	start := time.Now()
	handlerDone := make(chan struct{})
	go func() { defer close(handlerDone); a.handleAdminNodeSingboxUpgrade(w, upgradeReq(id)) }()
	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("handler waited for install instead of responding asynchronously")
	}
	elapsed := time.Since(start)
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("upgrade never connected to the SSH fixture")
	}

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	// Generous: a correct handler returns in microseconds. Anything near the
	// 30s WriteTimeout means the work is being awaited inline again.
	if elapsed > 2*time.Second {
		t.Fatalf("handler blocked for %v; the install must run in the background", elapsed)
	}
	var body struct {
		Data struct {
			Started bool `json:"started"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	if !body.Data.Started {
		t.Fatalf("expected started=true, got %s", w.Body.String())
	}

	// The job is visible while it runs, so the node list the UI polls can report
	// "安装中" instead of looking idle.
	if j, ok := a.upgradeSnapshot()[id]; !ok || !j.Running {
		t.Fatalf("expected a running job for server %d, got %+v", id, j)
	}

	// A second click while the first is in flight must be refused: two runs of
	// the install script on one machine would race over the same binary and
	// systemd unit.
	w2 := httptest.NewRecorder()
	a.handleAdminNodeSingboxUpgrade(w2, upgradeReq(id))
	if w2.Code != http.StatusConflict {
		t.Fatalf("concurrent upgrade: status = %d, want 409; body %s", w2.Code, w2.Body.String())
	}
}

// A node the panel has no way into must be refused up front rather than
// becoming a background job that fails a minute later somewhere the operator
// has to go looking for it.
func TestNodeSingboxUpgradeRejectsUnreachableConfig(t *testing.T) {
	a, st := newNodeUpgradeAPI(t)
	noCreds, err := st.CreateServer(store.Server{
		Name: "no-creds", Host: "192.0.2.2", Port: 22, SSHUser: "root", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		id   int64
	}{
		{"no ssh credentials", noCreds},
		{"unknown server", noCreds + 999},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			a.handleAdminNodeSingboxUpgrade(w, upgradeReq(tc.id))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %s", w.Code, w.Body.String())
			}
			if _, ok := a.upgradeSnapshot()[tc.id]; ok {
				t.Fatal("a rejected request must not leave a job behind")
			}
		})
	}
}

// TestLocalInstallEscapesProtectSystem covers why the local reinstall is not
// simply `bash`.
//
// The panel's own systemd unit sets ProtectSystem=full, which remounts /usr
// read-only for the service and every child it forks. The install script writes
// /usr/local/bin/sing-box, so 重装 on 面板本机 failed with EROFS every single
// time — on a machine where the identical script run from a root shell worked.
// systemd-run hands the script to PID1 instead, which starts it in a fresh
// namespace where /usr is writable again.
func TestLocalInstallEscapesProtectSystem(t *testing.T) {
	const sr = "/usr/bin/systemd-run"
	direct := []string{"bash", "-s", "--", "--force"}
	wrapped := []string{sr, "--pipe", "--wait", "--collect", "--quiet",
		"--unit=qingzhou-singbox-install", "-p", "RuntimeMaxSec=600", "--",
		"bash", "-s", "--", "--force"}
	for _, tc := range []struct {
		name     string
		readOnly bool
		sr       string
		want     []string
	}{{
		// The common case: no sandbox in the way, so no extra moving part.
		name: "writable bin dir runs bash directly", readOnly: false, sr: sr,
		want: direct,
	}, {
		name: "read-only bin dir goes through systemd-run", readOnly: true, sr: sr,
		want: wrapped,
	}, {
		// Nothing to escape with: still try, and let the script's own error
		// (plus the hint upgradeLocalSingBox adds) explain the failure.
		name: "no systemd-run falls back to bash", readOnly: true, sr: "",
		want: direct,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got := localInstallArgv(tc.readOnly, tc.sr)
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Fatalf("argv = %q, want %q", got, tc.want)
			}
		})
	}
}

// A local install that fails because of the panel's own sandbox must say so.
// Left alone, both failures point the operator somewhere useless: an EROFS on a
// machine whose disk is writable, or a systemd-run error with no trace of the
// install underneath it.
func TestLocalInstallHint(t *testing.T) {
	direct := []string{"bash", "-s", "--", "--force"}
	wrapped := []string{"/usr/bin/systemd-run", "--pipe", "--", "bash"}

	if h := localInstallHint(direct, "install: Read-only file system"); !strings.Contains(h, "ProtectSystem") {
		t.Fatalf("EROFS without systemd-run should explain the sandbox, got %q", h)
	}
	// systemd-run failing before the script ever ran is the one failure whose
	// output carries no trace of the install — but the wrapper is only used
	// after a real EROFS, so the sandbox is known to be the reason regardless.
	if h := localInstallHint(wrapped, "Failed to start transient service"); !strings.Contains(h, "手动执行") {
		t.Fatalf("a failed systemd-run should still offer the manual command, got %q", h)
	}
	// A script that failed on its own terms (no network, not root, no such
	// directory) must not be blamed on the sandbox: a wrong 只读 on top of a
	// correct diagnosis is the misdirection this whole change exists to end.
	for _, out := range []string{
		"curl: (7) Failed to connect",
		"✗ 请用 root 运行（sudo bash）",
		"install: cannot create regular file '/usr/local/bin/sing-box': No such file or directory",
	} {
		if h := localInstallHint(direct, out); h != "" {
			t.Fatalf("unrelated failure %q got a sandbox hint: %q", out, h)
		}
	}
}

// dirReadOnly must answer by trying — on the panel's own host the directory is
// root-owned and mode 755, so permission bits report writable while the mount
// underneath is read-only — and it must answer only about the mount. A missing
// directory is not a sandbox, and escalating it to systemd-run would replace
// the script's accurate complaint with an irrelevant one.
func TestDirReadOnly(t *testing.T) {
	if dirReadOnly(t.TempDir()) {
		t.Fatal("a fresh temp dir must not be reported read-only")
	}
	if dirReadOnly(filepath.Join(t.TempDir(), "no-such-dir")) {
		t.Fatal("a missing directory is not a read-only mount")
	}
}

// The constant the read-only probe checks has to be the directory the script
// actually installs into, or the probe silently tests the wrong mount.
func TestLocalSingboxBinDirMatchesScript(t *testing.T) {
	want := "BIN=" + localSingboxBinDir + "/sing-box"
	if !strings.Contains(assets.InstallScript(), want) {
		t.Fatalf("install-singbox.sh no longer sets %q; localSingboxBinDir must follow it", want)
	}
}

// finishUpgrade is what the node list reads to decide between 安装中, a result,
// and a failure — including carrying the script's output, which is the whole
// diagnosis when an install goes wrong.
func TestUpgradeJobLifecycle(t *testing.T) {
	a, _ := newNodeUpgradeAPI(t)
	const id int64 = 7

	if !a.claimUpgrade(id) {
		t.Fatal("first claim must succeed")
	}
	if a.claimUpgrade(id) {
		t.Fatal("second claim while running must fail")
	}

	a.finishUpgrade(id, "downloading...\ndone", nil)
	j := a.upgradeSnapshot()[id]
	if j.Running {
		t.Fatal("job should be finished")
	}
	if j.Error != "" {
		t.Fatalf("unexpected error %q", j.Error)
	}
	if j.Output != "downloading...\ndone" {
		t.Fatalf("output = %q", j.Output)
	}
	if j.FinishedAt == 0 {
		t.Fatal("FinishedAt should be stamped")
	}

	// Once finished, the node can be reinstalled again.
	if !a.claimUpgrade(id) {
		t.Fatal("claim after completion must succeed")
	}
	a.finishUpgrade(id, "curl: (7) failed to connect", context.DeadlineExceeded)
	j = a.upgradeSnapshot()[id]
	if j.Error == "" {
		t.Fatal("expected an error to be recorded")
	}
	// The failure message must carry the script's own output, not just the Go
	// error: "exit status 1" alone gives the operator nothing to act on.
	if !strings.Contains(j.Error, "curl: (7)") {
		t.Fatalf("error %q should include the script output", j.Error)
	}
}
