package api

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"qingzhou/internal/compliance"
	"qingzhou/internal/config"
)

// 合规检测 (#99): rule-based, read-only check of every machine against the
// common provider rules (no unrestricted public HTTP/SOCKS proxy, no
// unrestricted web service on 80/443/8080/8443). It inspects the sing-box
// config 轻舟 generates for each machine and, where the panel can already run
// commands there, the sockets actually listening. Nothing is changed.

type complianceServer struct {
	ServerID    int64                `json:"server_id"`
	Name        string               `json:"name"`
	Host        string               `json:"host"`
	Findings    []compliance.Finding `json:"findings"`
	LiveChecked bool                 `json:"live_checked"`
	LiveNote    string               `json:"live_note,omitempty"`
	ConfigNote  string               `json:"config_note,omitempty"`
}

// complianceRunner runs the read-only socket listing on a machine. Overridable
// in tests; production uses the existing sing-box controller channel
// (local shell for the panel host, SSH for remote servers).
func (a *API) complianceRunner() func(ctx context.Context, serverID int64, cmd string) (string, error) {
	if a.complianceRun != nil {
		return a.complianceRun
	}
	if a.sbctl == nil {
		return nil
	}
	return a.sbctl.RunOnServer
}

func panelListen() string {
	return config.Load().ListenAddr
}

// POST /api/admin/compliance/check
func (a *API) handleAdminComplianceCheck(w http.ResponseWriter, r *http.Request) {
	servers, err := a.st.ListServers()
	if err != nil {
		fail(w, http.StatusInternalServerError, "读取服务器列表失败，请稍后再试")
		return
	}
	listen := panelListen()
	_, pp, _ := net.SplitHostPort(listen)
	panelPort, _ := strconv.Atoi(pp)

	targets := []complianceServer{{ServerID: 0, Name: "面板主机", Host: "本机"}}
	for _, sv := range servers {
		if !sv.Enabled {
			continue
		}
		targets = append(targets, complianceServer{ServerID: sv.ID, Name: sv.Name, Host: sv.Host})
	}

	// Sized under the HTTP server's 30s write timeout, including RunOnServer's
	// own WaitDelay.
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	run := a.complianceRunner()
	var wg sync.WaitGroup
	for i := range targets {
		wg.Add(1)
		go func(t *complianceServer) {
			defer wg.Done()
			a.checkCompliance(ctx, t, run, listen, panelPort)
		}(&targets[i])
	}
	wg.Wait()

	counts := map[string]int{}
	for _, t := range targets {
		for _, f := range t.Findings {
			counts[f.Severity]++
		}
	}
	ok(w, J{"servers": targets, "counts": counts, "checked_at": nowUnix()})
}

func (a *API) checkCompliance(ctx context.Context, t *complianceServer, run func(context.Context, int64, string) (string, error), listen string, panelPort int) {
	var owned map[int]string
	if cfg, err := a.buildPreviewConfig(t.ServerID); err != nil {
		t.ConfigNote = "没能生成这台机器的配置，配置项未检查"
	} else if fs, o, err := compliance.AnalyzeConfig(cfg); err != nil {
		t.ConfigNote = "配置无法解析，配置项未检查"
	} else {
		t.Findings, owned = append(t.Findings, fs...), o
	}
	pport := 0
	if t.ServerID == 0 {
		t.Findings = append(t.Findings, compliance.AnalyzePanel(listen)...)
		pport = panelPort
	}
	if run == nil {
		t.LiveNote = "未启用服务器管理，没有检查实际监听的端口"
	} else {
		out, err := run(ctx, t.ServerID, compliance.ListenScript)
		ls := compliance.ParseListeners(out)
		switch {
		case len(ls) > 0:
			t.LiveChecked = true
			t.Findings = append(t.Findings, compliance.AnalyzeListeners(ls, owned, pport)...)
		case ctx.Err() != nil:
			t.LiveNote = "连接服务器超时，没有检查实际监听的端口"
		case err != nil && strings.TrimSpace(out) == "":
			t.LiveNote = "无法连接服务器，没有检查实际监听的端口"
		default:
			t.LiveNote = "服务器上缺少 ss / netstat 命令，没有检查实际监听的端口"
		}
	}
	if t.Findings == nil {
		t.Findings = []compliance.Finding{}
	}
	compliance.Sort(t.Findings)
}
