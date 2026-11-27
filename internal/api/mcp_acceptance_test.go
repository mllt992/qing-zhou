package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"qingzhou/internal/auth"
	"qingzhou/internal/sbver"
	"qingzhou/internal/store"
)

// Acceptance coverage for the built-in Remote MCP shipped in #136–#139
// (issue #135): protocol flow, auth, secret redaction, confirm/immutable
// write safety, audit logs, REST permission isolation, and diagnose_panel.

const mcpProtocolVersion = "2025-11-25"

const mcpInitializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"qz-test","version":"0"}}}`

const (
	secretSSHPass    = "SSH-PASSWORD-SENTINEL-9f3a"
	secretSSHKey     = "SSH-PRIVATE-KEY-SENTINEL-9f3a"
	secretSSHKeyPass = "SSH-KEY-PASS-SENTINEL-9f3a"
	secretSudoPass   = "SUDO-PASSWORD-SENTINEL-9f3a"
	secretProbeToken = "PROBE-TOKEN-SENTINEL-9f3a"
	secretRealityKey = "REALITY-PRIVATE-KEY-SENTINEL-9f3a"
	secretTLSKey     = "TLS-PRIVATE-KEY-SENTINEL-9f3a"
	secretCertKey    = "CERT-KEY-PEM-SENTINEL-9f3a"
	secretSMTP       = "SMTP-PASSWORD-SENTINEL-9f3a"
	secretTelegram   = "123456:TG-BOT-TOKEN-SENTINEL-9f3a"
	secretCFToken    = "CF-API-TOKEN-SENTINEL-9f3a"
	secretEgressPass = "EGRESS-PASSWORD-SENTINEL-9f3a"
	secretJWT        = "JWT-SECRET-SENTINEL-9f3a"
	secretOAuth      = "OAUTH-CLIENT-SECRET-SENTINEL-9f3a"
	secretUserPass   = "User-Login-Pass-9f3a"
)

func mcpSecrets() []string {
	return []string{
		secretSSHPass, secretSSHKey, secretSSHKeyPass, secretSudoPass, secretProbeToken,
		secretRealityKey, secretTLSKey, secretCertKey, secretSMTP, secretTelegram,
		secretCFToken, secretEgressPass, secretJWT, secretOAuth, secretUserPass,
	}
}

type mcpHarness struct {
	a       *API
	st      *store.Store
	adminID int64
	base    string
}

func newMCPHarness(t *testing.T) *mcpHarness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "mcp.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatal(err)
	}
	st.SetSecretKey([]byte("mcp-acceptance-secret-key-32b!!"))
	id, err := st.CreateUser(store.NewUser{
		Username: "mcp-admin", Role: "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	a := New(st, []byte("test-secret-32-bytes-pad-pad-pad!"), nil)
	t.Cleanup(a.Close)
	srv := httptest.NewServer(a.Router())
	t.Cleanup(srv.Close)
	return &mcpHarness{a: a, st: st, adminID: id, base: srv.URL}
}

func (h *mcpHarness) token(t *testing.T, name string, scopes []string) (string, *store.APIToken) {
	t.Helper()
	plain, tok, err := h.st.CreateAPIToken(name, scopes, h.adminID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return plain, tok
}

func (h *mcpHarness) preset(t *testing.T, name, preset string) string {
	t.Helper()
	scopes, ok := store.ResolveAPITokenPreset(preset)
	if !ok {
		t.Fatalf("unknown preset %s", preset)
	}
	plain, _ := h.token(t, name, scopes)
	return plain
}

type bearerTripper struct {
	base  http.RoundTripper
	token string
}

func (b bearerTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	base := b.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(r)
}

func (h *mcpHarness) session(t *testing.T, token string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	client := mcp.NewClient(&mcp.Implementation{Name: "qz-mcp-test", Version: "v0"}, nil)
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             h.base + "/mcp",
		HTTPClient:           &http.Client{Transport: bearerTripper{token: token}},
		DisableStandaloneSSE: true,
	}, &mcp.ClientSessionOptions{ProtocolVersion: mcpProtocolVersion})
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func toolText(res *mcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func callMCP(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, *mcp.CallToolResult) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if args == nil {
		args = map[string]any{}
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return toolText(res), res
}

func mustCall(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	text, res := callMCP(t, cs, name, args)
	if res.IsError {
		t.Fatalf("%s failed: %s", name, text)
	}
	return text
}

func mustDeny(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	text, res := callMCP(t, cs, name, args)
	if res == nil || !res.IsError {
		t.Fatalf("%s succeeded, want permission error: %s", name, text)
	}
	if !strings.Contains(text, "权限不足") {
		t.Fatalf("%s error = %s, want 权限不足", name, text)
	}
	return text
}

func decodeTool(t *testing.T, raw string, dest any) {
	t.Helper()
	if err := json.Unmarshal([]byte(raw), dest); err != nil {
		t.Fatalf("decode tool json: %v\n%s", err, raw)
	}
}

func mustNotLeak(t *testing.T, label, body string, secrets []string) {
	t.Helper()
	for _, s := range secrets {
		if s != "" && strings.Contains(body, s) {
			t.Fatalf("%s leaked %q\n%s", label, s, body)
		}
	}
}

func (h *mcpHarness) do(t *testing.T, method, path, token, body string) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, h.base+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

func (h *mcpHarness) adminJWT(t *testing.T) string {
	t.Helper()
	jti := "mcp-jwt-" + strings.ReplaceAll(t.Name(), "/", "-")
	if err := h.st.CreateSession(h.adminID, jti, "127.0.0.1", "mcp-test"); err != nil {
		t.Fatal(err)
	}
	tok, err := auth.Issue(h.a.secret, h.adminID, "admin", jti, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestSummarizeMCPArgsRedactsSecrets(t *testing.T) {
	summary := summarizeMCPArgs(mcpUpdateSettingsIn{Settings: map[string]string{
		"site_name":          "轻舟",
		"smtp_pass":          secretSMTP,
		"telegram_bot_token": secretTelegram,
		"jwt_secret":         secretJWT,
		"oauth2_config":      `{"client_secret":"` + secretOAuth + `"}`,
		"cf_api_token":       secretCFToken,
	}})
	if !strings.Contains(summary, "轻舟") {
		t.Fatalf("non-secret setting dropped: %s", summary)
	}
	mustNotLeak(t, "settings summary", summary, mcpSecrets())
	if !strings.Contains(summary, `"smtp_pass":"***"`) || !strings.Contains(summary, `"telegram_bot_token":"***"`) {
		t.Fatalf("secret settings not redacted: %s", summary)
	}

	server := summarizeMCPArgs(mcpUpsertServerIn{
		Name: "edge", Host: "203.0.113.10",
		SSHPassword: secretSSHPass, SSHKey: secretSSHKey, SSHKeyPass: secretSSHKeyPass,
		SudoPassword: secretSudoPass, SSHKeyPath: "edge.key",
	})
	mustNotLeak(t, "server summary", server, mcpSecrets())
	if !strings.Contains(server, `"ssh_key_path":"edge.key"`) {
		t.Fatalf("ssh_key_path should stay visible: %s", server)
	}
	if !strings.Contains(server, `"ssh_password":"***"`) || !strings.Contains(server, `"ssh_key":"***"`) {
		t.Fatalf("ssh secrets not redacted: %s", server)
	}

	egress := summarizeMCPArgs(mcpManageEgressIn{
		Action: "upsert", Name: "exit", Type: "socks", Host: "198.51.100.2", Port: 1080,
		Password: secretEgressPass,
	})
	mustNotLeak(t, "egress summary", egress, mcpSecrets())
	if !strings.Contains(egress, `"password":"***"`) {
		t.Fatalf("egress password not redacted: %s", egress)
	}
}

func TestMCP_ProtocolFlow(t *testing.T) {
	h := newMCPHarness(t)
	plain := h.preset(t, "admin", "mcp-admin")

	code, body := h.do(t, http.MethodPost, "/mcp", plain, mcpInitializeBody)
	if code != http.StatusOK {
		t.Fatalf("initialize status %d body %s", code, body)
	}
	if !strings.Contains(body, `"name":"qingzhou-panel"`) || !strings.Contains(body, mcpProtocolVersion) {
		t.Fatalf("initialize result missing server info: %s", body)
	}

	cs := h.session(t, plain)
	init := cs.InitializeResult()
	if init == nil || init.ServerInfo == nil || init.ServerInfo.Name != "qingzhou-panel" {
		t.Fatalf("initialize result = %+v", init)
	}
	if init.ProtocolVersion != mcpProtocolVersion {
		t.Fatalf("protocol %q", init.ProtocolVersion)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{
		"diagnose_panel", "get_overview_stats", "get_settings", "list_servers",
		"quick_setup_node", "update_settings", "delete_server",
		"provision_user_access", "upsert_package", "delete_user", "retire_package",
	} {
		if !names[want] {
			t.Fatalf("tools/list missing %s", want)
		}
	}

	stats := mustCall(t, cs, "get_overview_stats", map[string]any{"range": "7d"})
	var ov struct {
		RangeDays int64 `json:"range_days"`
	}
	decodeTool(t, stats, &ov)
	if ov.RangeDays != 7 {
		t.Fatalf("overview range = %d, body %s", ov.RangeDays, stats)
	}

	resources, err := cs.ListResources(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	uris := map[string]bool{}
	for _, r := range resources.Resources {
		uris[r.URI] = true
	}
	if !uris["qingzhou://health"] || !uris["qingzhou://topology"] {
		t.Fatalf("resources = %+v", resources.Resources)
	}
	health, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: "qingzhou://health"})
	if err != nil {
		t.Fatal(err)
	}
	if len(health.Contents) == 0 || !strings.Contains(health.Contents[0].Text, `"issue_count"`) {
		t.Fatalf("health resource = %+v", health.Contents)
	}
	if health.Contents[0].URI != "qingzhou://health" {
		t.Fatalf("health uri %q", health.Contents[0].URI)
	}
}

func TestMCP_AuthRejected(t *testing.T) {
	h := newMCPHarness(t)

	code, body := h.do(t, http.MethodPost, "/mcp", "", mcpInitializeBody)
	if code != http.StatusUnauthorized || !strings.Contains(body, "Bearer") {
		t.Fatalf("missing token: %d %s", code, body)
	}

	code, body = h.do(t, http.MethodPost, "/mcp", "not-a-token", mcpInitializeBody)
	if code != http.StatusUnauthorized {
		t.Fatalf("garbage token: %d %s", code, body)
	}

	expired, _, err := h.st.CreateAPIToken("expired", []string{"mcp:read"}, h.adminID, time.Now().Add(-time.Hour).Unix())
	if err != nil {
		t.Fatal(err)
	}
	code, _ = h.do(t, http.MethodPost, "/mcp", expired, mcpInitializeBody)
	if code != http.StatusUnauthorized {
		t.Fatalf("expired token: %d", code)
	}

	revoked, tok := h.token(t, "revoked", []string{"mcp:read"})
	if err := h.st.RevokeAPIToken(tok.ID); err != nil {
		t.Fatal(err)
	}
	code, body = h.do(t, http.MethodPost, "/mcp", revoked, mcpInitializeBody)
	if code != http.StatusUnauthorized || !strings.Contains(body, "吊销") {
		t.Fatalf("revoked token: %d %s", code, body)
	}

	restOnly, _ := h.token(t, "stats", []string{"stats:read", "users:read", "servers:read"})
	code, body = h.do(t, http.MethodPost, "/mcp", restOnly, mcpInitializeBody)
	if code != http.StatusForbidden || !strings.Contains(body, "mcp:") {
		t.Fatalf("REST token on /mcp: %d %s", code, body)
	}

	jwt := h.adminJWT(t)
	code, body = h.do(t, http.MethodPost, "/mcp", jwt, mcpInitializeBody)
	if code != http.StatusUnauthorized {
		t.Fatalf("admin JWT on /mcp: %d %s", code, body)
	}

	read := h.preset(t, "readonly", "mcp-readonly")
	cs := h.session(t, read)
	mustDeny(t, cs, "update_settings", map[string]any{"settings": map[string]any{"site_name": "nope"}})
	mustDeny(t, cs, "delete_server", map[string]any{"id": 1, "confirm": false})
	mustDeny(t, cs, "upsert_package", map[string]any{"name": "x", "traffic_gb": 1})
	mustDeny(t, cs, "delete_user", map[string]any{"id": h.adminID, "confirm": true})

	nodesOnly, _ := h.token(t, "nodes", []string{"mcp:nodes"})
	ns := h.session(t, nodesOnly)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = ns.ReadResource(ctx, &mcp.ReadResourceParams{URI: "qingzhou://health"})
	if err == nil || !strings.Contains(err.Error(), "权限不足") {
		t.Fatalf("health without mcp:read: %v", err)
	}
}

func TestMCP_ReadToolsRedactSecrets(t *testing.T) {
	h := newMCPHarness(t)
	if _, err := h.st.CreateServer(store.Server{
		Name: "edge", Host: "203.0.113.10", Enabled: true, Port: 22, SSHUser: "root",
		SSHPassword: secretSSHPass, SSHKey: secretSSHKey, SSHKeyPass: secretSSHKeyPass,
		SudoPassword: secretSudoPass, ProbeEnabled: true, ProbeToken: secretProbeToken,
	}); err != nil {
		t.Fatal(err)
	}
	realitySrv := `{"enabled":true,"server_name":"www.microsoft.com","reality":{"enabled":true,"private_key":"` + secretRealityKey + `","short_id":["abcd"],"handshake":{"server":"www.microsoft.com","server_port":443}}}`
	realityCli := `{"server_name":"www.microsoft.com","reality":{"public_key":"REALITY-PUBLIC-KEY-OK"},"utls":{"enabled":true,"fingerprint":"chrome"}}`
	if _, err := h.st.SaveSbTls(&store.SbTls{Name: "reality", Mode: "reality", ServerJSON: realitySrv, ClientJSON: realityCli}); err != nil {
		t.Fatal(err)
	}
	tlsSrv := `{"enabled":true,"server_name":"tls.example","certificate":"not-a-secret-cert","key":"` + secretTLSKey + `"}`
	if _, err := h.st.SaveSbTls(&store.SbTls{Name: "inline-tls", Mode: "tls", ServerJSON: tlsSrv, ClientJSON: `{"insecure":true}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.SaveCert(&store.Cert{
		Name: "managed", Domain: "cert.example", Source: "paste",
		CertPEM: "-----BEGIN CERTIFICATE-----\nnot-real\n-----END CERTIFICATE-----",
		KeyPEM:  secretCertKey,
	}); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"smtp_pass":          secretSMTP,
		"telegram_bot_token": secretTelegram,
		"cf_api_token":       secretCFToken,
		"site_name":          "轻舟测试",
	} {
		if err := h.st.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.st.SaveSbEgress(&store.SbEgress{
		Name: "exit", Type: "socks", Host: "198.51.100.8", Port: 1080,
		Username: "proxyuser", Password: secretEgressPass,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.SaveSbInbound(&store.SbInbound{
		Type: "vless", Tag: "vless-in", ListenPort: 443, Enabled: true, Options: "{}",
	}); err != nil {
		t.Fatal(err)
	}

	cs := h.session(t, h.preset(t, "reader", "mcp-readonly"))
	out := map[string]string{}
	var blobs []string
	for _, spec := range []struct {
		name string
		args map[string]any
	}{
		{"diagnose_panel", nil},
		{"get_overview_stats", nil},
		{"get_monitor_status", nil},
		{"get_traffic_report", map[string]any{"range": "7d"}},
		{"list_alerts", nil},
		{"list_servers", nil},
		{"list_tls_and_certs", nil},
		{"list_inbounds", nil},
		{"list_nodes_and_groups", nil},
		{"list_packages", nil},
		{"list_users", map[string]any{"query": "mcp"}},
		{"get_settings", nil},
		{"check_singbox_config", map[string]any{"server_id": 0}},
	} {
		text := mustCall(t, cs, spec.name, spec.args)
		out[spec.name] = text
		blobs = append(blobs, text)
	}

	// detect_node_host dials public echo services. Bound the call so a blocked
	// network cannot stall the suite; a timeout is not a redaction failure.
	dctx, dcancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer dcancel()
	if res, err := cs.CallTool(dctx, &mcp.CallToolParams{Name: "detect_node_host", Arguments: map[string]any{}}); err != nil {
		blobs = append(blobs, err.Error())
	} else {
		blobs = append(blobs, toolText(res))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, uri := range []string{"qingzhou://health", "qingzhou://topology"} {
		res, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
		if err != nil {
			t.Fatalf("read %s: %v", uri, err)
		}
		if len(res.Contents) == 0 {
			t.Fatalf("%s empty", uri)
		}
		blobs = append(blobs, res.Contents[0].Text)
	}

	joined := strings.Join(blobs, "\n")
	mustNotLeak(t, "read tools", joined, mcpSecrets())

	var servers struct {
		Servers []struct {
			Server struct {
				Name        string `json:"name"`
				SSHPassword string `json:"ssh_password"`
				SSHKey      string `json:"ssh_key"`
				SSHKeyPass  string `json:"ssh_key_pass"`
				SudoPass    string `json:"sudo_password"`
				ProbeToken  string `json:"probe_token"`
			} `json:"server"`
		} `json:"servers"`
	}
	decodeTool(t, out["list_servers"], &servers)
	if len(servers.Servers) != 1 || servers.Servers[0].Server.Name != "edge" {
		t.Fatalf("servers = %+v", servers.Servers)
	}
	sv := servers.Servers[0].Server
	if sv.SSHPassword != "***" || sv.SSHKey != "***" || sv.SSHKeyPass != "***" || sv.SudoPass != "***" || sv.ProbeToken != "" {
		t.Fatalf("server secrets not masked: %+v", sv)
	}

	if !strings.Contains(out["list_tls_and_certs"], "REALITY-PUBLIC-KEY-OK") {
		t.Fatalf("reality public key missing from tls list: %s", out["list_tls_and_certs"])
	}
	var settings struct {
		Settings map[string]string `json:"settings"`
	}
	decodeTool(t, out["get_settings"], &settings)
	for _, k := range []string{"smtp_pass", "telegram_bot_token", "cf_api_token"} {
		if settings.Settings[k] != "***" {
			t.Fatalf("setting %s = %q", k, settings.Settings[k])
		}
	}
	if settings.Settings["site_name"] != "轻舟测试" {
		t.Fatalf("site_name = %q", settings.Settings["site_name"])
	}
	var inbounds struct {
		Egresses []struct {
			Name     string `json:"name"`
			Password string `json:"password"`
		} `json:"egresses"`
	}
	decodeTool(t, out["list_inbounds"], &inbounds)
	if len(inbounds.Egresses) != 1 || inbounds.Egresses[0].Password != "***" || inbounds.Egresses[0].Name != "exit" {
		t.Fatalf("egresses = %+v", inbounds.Egresses)
	}
}

func TestMCP_ConfirmAndImmutableSettings(t *testing.T) {
	h := newMCPHarness(t)
	sid, err := h.st.CreateServer(store.Server{Name: "doomed", Host: "198.51.100.20", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	ibID, err := h.st.SaveSbInbound(&store.SbInbound{
		ServerID: sid, Type: "vless", Tag: "doomed-in", ListenPort: 8443, Enabled: true, Options: "{}",
	})
	if err != nil {
		t.Fatal(err)
	}
	tlsID, err := h.st.SaveSbTls(&store.SbTls{Name: "unused", Mode: "reality", ServerJSON: `{"enabled":true}`, ClientJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	nid, err := h.st.CreateNode(store.Node{Type: "external", Name: "ext", ShareLink: "vless://example", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	gid, err := h.st.CreateGroup(store.NodeGroup{Name: "keep-me"})
	if err != nil {
		t.Fatal(err)
	}
	egID, err := h.st.SaveSbEgress(&store.SbEgress{Name: "keep-eg", Type: "http", Host: "198.51.100.30", Port: 8080})
	if err != nil {
		t.Fatal(err)
	}
	srcID, err := h.st.CreateSource(store.NodeSource{Name: "airport", URL: "https://example.invalid/sub", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	uid, err := h.st.CreateUser(store.NewUser{Username: "victim"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetUserClient(uid, 7, "victim", "uuid-before-reset", "marker-before-reset"); err != nil {
		t.Fatal(err)
	}
	pkgID, err := h.st.CreatePackage(store.Package{
		Type: "plan", Name: "still-on", TrafficBytes: 1 << 30, DurationDays: 30, Stock: -1, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetSetting("update_repo", "qingzhou/official"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetSetting("jwt_secret", "original-jwt"); err != nil {
		t.Fatal(err)
	}

	cs := h.session(t, h.preset(t, "admin", "mcp-admin"))
	dry := mustCall(t, cs, "delete_server", map[string]any{"id": sid, "confirm": false})
	var preview struct {
		DryRun       bool `json:"dry_run"`
		InboundCount int  `json:"inbound_count"`
	}
	decodeTool(t, dry, &preview)
	if !preview.DryRun || preview.InboundCount != 1 {
		t.Fatalf("delete_server preview = %+v body %s", preview, dry)
	}
	mustCall(t, cs, "delete_inbound", map[string]any{"id": ibID, "confirm": false})
	mustCall(t, cs, "delete_tls", map[string]any{"id": tlsID, "confirm": false})
	mustCall(t, cs, "delete_node", map[string]any{"id": nid, "confirm": false})
	mustCall(t, cs, "delete_node_group", map[string]any{"id": gid, "confirm": false})
	mustCall(t, cs, "manage_egress", map[string]any{"action": "delete", "id": egID, "confirm": false})
	mustCall(t, cs, "manage_node_sources", map[string]any{"action": "delete", "id": srcID, "confirm": false})
	mustCall(t, cs, "delete_user", map[string]any{"id": uid, "confirm": false})
	mustCall(t, cs, "reset_user_node_creds", map[string]any{"id": uid, "confirm": false})
	retire := mustCall(t, cs, "retire_package", map[string]any{"id": pkgID, "confirm": false})
	if !strings.Contains(retire, `"dry_run": true`) {
		t.Fatalf("retire preview: %s", retire)
	}

	if sv, _ := h.st.GetServer(sid); sv == nil || sv.Name != "doomed" {
		t.Fatal("delete_server confirm=false removed the server")
	}
	if ib, _ := h.st.GetSbInbound(ibID); ib == nil || ib.Tag != "doomed-in" {
		t.Fatal("delete_inbound confirm=false removed the inbound")
	}
	if tls, _ := h.st.GetSbTls(tlsID); tls == nil {
		t.Fatal("delete_tls confirm=false removed the profile")
	}
	if n, _ := h.st.GetNode(nid); n == nil {
		t.Fatal("delete_node confirm=false removed the node")
	}
	groups, _ := h.st.ListGroups()
	if len(groups) != 1 || groups[0].ID != gid {
		t.Fatalf("group changed: %+v", groups)
	}
	if eg, _ := h.st.GetSbEgress(egID); eg == nil {
		t.Fatal("delete egress confirm=false removed the egress")
	}
	if src, _ := h.st.GetSource(srcID); src == nil {
		t.Fatal("delete source confirm=false removed the source")
	}
	u, _ := h.st.UserByID(uid)
	if u == nil || !u.ClientUUID.Valid || u.ClientUUID.String != "uuid-before-reset" {
		t.Fatalf("user creds changed: %+v", u)
	}
	pkg, _ := h.st.GetPackage(pkgID)
	if pkg == nil || !pkg.Enabled {
		t.Fatalf("package retired without confirm: %+v", pkg)
	}

	_, denied := callMCP(t, cs, "delete_user", map[string]any{"id": h.adminID, "confirm": true})
	if denied == nil || !denied.IsError || !strings.Contains(toolText(denied), "管理员") {
		t.Fatalf("admin delete = %s", toolText(denied))
	}
	if admin, _ := h.st.UserByID(h.adminID); admin == nil || admin.Role != "admin" {
		t.Fatal("admin account was removed")
	}

	updated := mustCall(t, cs, "update_settings", map[string]any{"settings": map[string]any{
		"site_name":           "MCP 安全",
		"update_repo":         "evil/repo",
		"jwt_secret":          secretJWT,
		"oauth2_config":       `{"client_secret":"` + secretOAuth + `"}`,
		"relay_link_metering": "true",
		"smtp_pass":           secretSMTP,
	}})
	mustNotLeak(t, "update_settings result", updated, mcpSecrets())
	if !strings.Contains(updated, "禁止修改") {
		t.Fatalf("immutable keys not rejected: %s", updated)
	}
	if got, _ := h.st.GetSetting("site_name"); got != "MCP 安全" {
		t.Fatalf("site_name = %q", got)
	}
	if got, _ := h.st.GetSetting("update_repo"); got != "qingzhou/official" {
		t.Fatalf("update_repo = %q", got)
	}
	if got, _ := h.st.GetSetting("jwt_secret"); got != "original-jwt" {
		t.Fatalf("jwt_secret = %q", got)
	}
	if got, _ := h.st.GetSetting("oauth2_config"); got != "" {
		t.Fatalf("oauth2_config written: %q", got)
	}
	if got, _ := h.st.GetSetting("relay_link_metering"); got != "" {
		t.Fatalf("relay_link_metering written: %q", got)
	}
	if got, _ := h.st.GetSetting("smtp_pass"); got != secretSMTP {
		t.Fatalf("smtp_pass not stored (redaction must not block a real write): %q", got)
	}
}

func TestMCP_AuditLogs(t *testing.T) {
	h := newMCPHarness(t)
	admin := h.preset(t, "audit-admin", "mcp-admin")
	cs := h.session(t, admin)
	falseBool := false
	mustCall(t, cs, "upsert_server", map[string]any{
		"name": "audited", "host": "203.0.113.40",
		"ssh_password": secretSSHPass, "ssh_key": secretSSHKey, "sudo_password": secretSudoPass,
	})
	mustCall(t, cs, "update_settings", map[string]any{"settings": map[string]any{
		"site_name":          "audit",
		"smtp_pass":          secretSMTP,
		"telegram_bot_token": secretTelegram,
		"jwt_secret":         secretJWT,
	}})
	mustCall(t, cs, "setup_telegram", map[string]any{
		"bot_token": secretTelegram, "test_connection": falseBool,
	})
	mustCall(t, cs, "manage_egress", map[string]any{
		"action": "upsert", "name": "aud-eg", "type": "socks",
		"host": "198.51.100.40", "port": 1080, "password": secretEgressPass,
	})

	reader := h.preset(t, "audit-reader", "mcp-readonly")
	rs := h.session(t, reader)
	mustDeny(t, rs, "delete_server", map[string]any{"id": 1, "confirm": true})

	rows, total, err := h.st.ListMCPAuditLogs(50, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if total < 5 || len(rows) < 5 {
		t.Fatalf("audit rows = %d total %d", len(rows), total)
	}
	byTool := map[string]*store.MCPAuditLog{}
	for _, row := range rows {
		byTool[row.ToolName] = row
		mustNotLeak(t, "audit "+row.ToolName, row.ArgsSummary+" "+row.ErrorMsg, mcpSecrets())
	}
	for _, name := range []string{"upsert_server", "update_settings", "setup_telegram", "manage_egress"} {
		row := byTool[name]
		if row == nil || row.Status != "ok" || row.TokenName != "audit-admin" {
			t.Fatalf("%s audit = %+v", name, row)
		}
		if row.ArgsSummary == "" || !strings.Contains(row.ArgsSummary, "***") {
			t.Fatalf("%s args not redacted: %q", name, row.ArgsSummary)
		}
	}
	denied := byTool["delete_server"]
	if denied == nil || denied.Status != "denied" || denied.TokenName != "audit-reader" {
		t.Fatalf("denied audit = %+v", denied)
	}
	if !strings.Contains(denied.ErrorMsg, "mcp:servers") {
		t.Fatalf("denied error %q", denied.ErrorMsg)
	}

	jwt := h.adminJWT(t)
	code, body := h.do(t, http.MethodGet, "/api/admin/mcp/audit-logs?limit=20", jwt, "")
	if code != http.StatusOK || !strings.Contains(body, `"tool_name":"upsert_server"`) {
		t.Fatalf("jwt audit list: %d %s", code, body)
	}
	mustNotLeak(t, "audit http", body, mcpSecrets())
	var env struct {
		Data struct {
			Rows  []store.MCPAuditLog `json:"rows"`
			Total int                 `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.Total < 5 || len(env.Data.Rows) == 0 {
		t.Fatalf("audit envelope %+v", env.Data)
	}

	code, body = h.do(t, http.MethodGet, "/api/admin/mcp/audit-logs?tool=setup_telegram", reader, "")
	if code != http.StatusOK || !strings.Contains(body, "setup_telegram") {
		t.Fatalf("mcp:read audit list: %d %s", code, body)
	}
	mustNotLeak(t, "mcp:read audit", body, mcpSecrets())

	nodesOnly, _ := h.token(t, "nodes-audit", []string{"mcp:nodes"})
	code, _ = h.do(t, http.MethodGet, "/api/admin/mcp/audit-logs", nodesOnly, "")
	if code != http.StatusForbidden {
		t.Fatalf("mcp:nodes audit list: %d", code)
	}
	statsOnly, _ := h.token(t, "stats-audit", []string{"stats:read"})
	code, _ = h.do(t, http.MethodGet, "/api/admin/mcp/audit-logs", statsOnly, "")
	if code != http.StatusForbidden {
		t.Fatalf("stats:read audit list: %d", code)
	}
}

func TestMCP_ScopesDoNotEscalateREST(t *testing.T) {
	h := newMCPHarness(t)
	admin := h.preset(t, "full-mcp", "mcp-admin")
	for _, path := range []string{
		"/api/admin/stats/overview",
		"/api/admin/users",
		"/api/admin/servers",
		"/api/admin/nodes",
		"/api/admin/backup",
		"/api/admin/tokens",
		"/api/admin/update/status",
		"/api/user/dashboard",
	} {
		code, body := h.do(t, http.MethodGet, path, admin, "")
		if code != http.StatusForbidden {
			t.Fatalf("GET %s with mcp-admin: %d %s", path, code, body)
		}
	}
	code, body := h.do(t, http.MethodPut, "/api/admin/settings", admin, `{"site_name":"hacked"}`)
	if code != http.StatusForbidden {
		t.Fatalf("PUT settings with mcp-admin: %d %s", code, body)
	}
	if got, _ := h.st.GetSetting("site_name"); got == "hacked" {
		t.Fatal("mcp token wrote settings through REST")
	}

	stats, _ := h.token(t, "stats", []string{"stats:read"})
	code, body = h.do(t, http.MethodGet, "/api/admin/stats/overview", stats, "")
	if code != http.StatusOK {
		t.Fatalf("stats:read overview: %d %s", code, body)
	}
	code, _ = h.do(t, http.MethodPost, "/mcp", stats, mcpInitializeBody)
	if code != http.StatusForbidden {
		t.Fatalf("stats:read /mcp: %d", code)
	}
	code, _ = h.do(t, http.MethodGet, "/api/admin/users", stats, "")
	if code != http.StatusForbidden {
		t.Fatalf("stats:read users: %d", code)
	}
	code, _ = h.do(t, http.MethodGet, "/api/admin/tokens", stats, "")
	if code != http.StatusForbidden {
		t.Fatalf("stats:read tokens: %d", code)
	}

	// A browser admin still reaches REST routes an MCP token cannot.
	jwt := h.adminJWT(t)
	code, body = h.do(t, http.MethodGet, "/api/admin/stats/overview", jwt, "")
	if code != http.StatusOK {
		t.Fatalf("jwt overview: %d %s", code, body)
	}
}

func TestMCP_ScopeIsolation(t *testing.T) {
	h := newMCPHarness(t)
	sid, err := h.st.CreateServer(store.Server{Name: "scope-box", Host: "203.0.113.80", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	nid, err := h.st.CreateNode(store.Node{Type: "external", Name: "scope-node", ShareLink: "vless://x", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	read := h.session(t, h.preset(t, "ro", "mcp-readonly"))
	mustCall(t, read, "diagnose_panel", nil)
	mustCall(t, read, "get_settings", nil)
	mustDeny(t, read, "update_settings", map[string]any{"settings": map[string]any{"site_name": "x"}})
	mustDeny(t, read, "delete_node", map[string]any{"id": nid, "confirm": false})
	mustDeny(t, read, "delete_server", map[string]any{"id": sid, "confirm": false})
	mustDeny(t, read, "create_user", map[string]any{"username": "nope01"})

	nodesTok, _ := h.token(t, "nodes", []string{"mcp:nodes"})
	nodes := h.session(t, nodesTok)
	mustCall(t, nodes, "delete_node", map[string]any{"id": nid, "confirm": false})
	mustDeny(t, nodes, "get_overview_stats", nil)
	mustDeny(t, nodes, "update_settings", map[string]any{"settings": map[string]any{"site_name": "x"}})
	mustDeny(t, nodes, "delete_server", map[string]any{"id": sid, "confirm": false})

	serversTok, _ := h.token(t, "servers", []string{"mcp:servers"})
	servers := h.session(t, serversTok)
	mustCall(t, servers, "delete_server", map[string]any{"id": sid, "confirm": false})
	mustDeny(t, servers, "upsert_package", map[string]any{"name": "p", "traffic_gb": 1})
	mustDeny(t, servers, "diagnose_panel", nil)

	usersTok, _ := h.token(t, "users", []string{"mcp:users"})
	users := h.session(t, usersTok)
	codes := mustCall(t, users, "manage_reg_codes", map[string]any{"action": "list"})
	if !strings.Contains(codes, "reg_codes") {
		t.Fatalf("reg codes: %s", codes)
	}
	mustDeny(t, users, "list_servers", nil)
	mustDeny(t, users, "update_settings", map[string]any{"settings": map[string]any{"site_name": "x"}})

	settingsTok, _ := h.token(t, "settings", []string{"mcp:settings"})
	settings := h.session(t, settingsTok)
	mustCall(t, settings, "update_settings", map[string]any{"settings": map[string]any{"site_name": "scoped"}})
	mustDeny(t, settings, "delete_user", map[string]any{"id": h.adminID, "confirm": false})
	mustDeny(t, settings, "get_settings", nil)
	if got, _ := h.st.GetSetting("site_name"); got != "scoped" {
		t.Fatalf("site_name = %q", got)
	}
	if n, _ := h.st.GetNode(nid); n == nil {
		t.Fatal("scope test deleted the node")
	}
	if sv, _ := h.st.GetServer(sid); sv == nil {
		t.Fatal("scope test deleted the server")
	}
}

func issueCodes(t *testing.T, raw string) map[string]string {
	t.Helper()
	var doc struct {
		Issues []struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
			Title  string `json:"title"`
		} `json:"issues"`
	}
	decodeTool(t, raw, &doc)
	out := map[string]string{}
	for _, iss := range doc.Issues {
		out[iss.Code] = iss.Title + " " + iss.Detail
	}
	return out
}

func TestMCP_DiagnosePanelFaults(t *testing.T) {
	h := newMCPHarness(t)
	if _, err := h.st.SaveSbInbound(&store.SbInbound{
		Type: "vless", Tag: "orphan-vless", ListenPort: 443, Enabled: true, Options: "{}",
	}); err != nil {
		t.Fatal(err)
	}
	cs := h.session(t, h.preset(t, "diag", "mcp-readonly"))
	local := issueCodes(t, mustCall(t, cs, "diagnose_panel", nil))
	for _, code := range []string{"missing_node_host", "orphan_inbounds", "missing_https_public_base", "singbox_unprobed"} {
		if _, ok := local[code]; !ok {
			t.Fatalf("local diagnose missing %s: %#v", code, local)
		}
	}
	if !strings.Contains(local["missing_node_host"], "node_host_override") {
		t.Fatalf("missing_node_host detail: %s", local["missing_node_host"])
	}
	if !strings.Contains(local["orphan_inbounds"], "orphan-vless") {
		t.Fatalf("orphan detail: %s", local["orphan_inbounds"])
	}
	if _, ok := local["missing_v2ray_api"]; ok {
		t.Fatalf("unprobed local machine reported missing_v2ray_api: %#v", local)
	}

	if err := h.st.SetSetting("node_host_override", "203.0.113.9"); err != nil {
		t.Fatal(err)
	}
	fixed := issueCodes(t, mustCall(t, cs, "diagnose_panel", nil))
	if _, ok := fixed["missing_node_host"]; ok {
		t.Fatalf("node_host_override still reported missing: %#v", fixed)
	}
	if _, ok := fixed["orphan_inbounds"]; !ok {
		t.Fatal("setting node host cleared the orphan inbound finding")
	}

	h2 := newMCPHarness(t)
	oldID, err := h2.st.CreateServer(store.Server{Name: "old-box", Host: "203.0.113.10", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := h2.st.SetNodeSingbox(oldID, sbver.Info{Version: "1.11.0", HasV2RayAPI: false, Raw: "sing-box version 1.11.0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h2.st.CreateServer(store.Server{Name: "unprobed-box", Host: "203.0.113.11", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := h2.st.SaveSbInbound(&store.SbInbound{
		ServerID: oldID, Type: "vmess", Tag: "unbound-vmess", ListenPort: 10086, Enabled: true, Options: "{}",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h2.st.CreateNode(store.Node{
		Type: "self_built", Name: "loose-node", InboundTag: "unbound-vmess", Enabled: true, RouteUpstreamBroken: true,
	}); err != nil {
		t.Fatal(err)
	}
	gid, err := h2.st.CreateGroup(store.NodeGroup{Name: "vacant"})
	if err != nil {
		t.Fatal(err)
	}
	pkgID, err := h2.st.CreatePackage(store.Package{
		Type: "plan", Name: "empty-plan", TrafficBytes: 1 << 30, DurationDays: 30, Stock: -1, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h2.st.SetPlanGroups(pkgID, []int64{gid}); err != nil {
		t.Fatal(err)
	}

	cs2 := h2.session(t, h2.preset(t, "diag2", "mcp-readonly"))
	faults := issueCodes(t, mustCall(t, cs2, "diagnose_panel", nil))
	for _, code := range []string{"missing_v2ray_api", "singbox_too_old", "singbox_unprobed", "ungrouped_nodes", "broken_relay_routes", "empty_group_packages"} {
		if _, ok := faults[code]; !ok {
			t.Fatalf("topology diagnose missing %s: %#v", code, faults)
		}
	}
	if _, ok := faults["missing_node_host"]; ok {
		t.Fatalf("enabled server host should satisfy node host: %#v", faults)
	}
	if !strings.Contains(faults["missing_v2ray_api"], "old-box") || !strings.Contains(faults["singbox_too_old"], "1.11.0") {
		t.Fatalf("version findings: v2ray=%s old=%s", faults["missing_v2ray_api"], faults["singbox_too_old"])
	}
	if !strings.Contains(faults["singbox_unprobed"], "unprobed-box") {
		t.Fatalf("unprobed: %s", faults["singbox_unprobed"])
	}
	if !strings.Contains(faults["ungrouped_nodes"], "loose-node") || !strings.Contains(faults["broken_relay_routes"], "loose-node") {
		t.Fatalf("node findings: ungrouped=%s broken=%s", faults["ungrouped_nodes"], faults["broken_relay_routes"])
	}
	if !strings.Contains(faults["empty_group_packages"], "empty-plan") {
		t.Fatalf("empty package: %s", faults["empty_group_packages"])
	}
	// The vmess inbound is bound to loose-node, so it is not an orphan.
	if _, ok := faults["orphan_inbounds"]; ok {
		t.Fatalf("bound inbound reported orphan: %s", faults["orphan_inbounds"])
	}
}

func TestMCP_QuickSetupAndProvision(t *testing.T) {
	h := newMCPHarness(t)
	cs := h.session(t, h.preset(t, "ops", "mcp-admin"))
	mustCall(t, cs, "update_settings", map[string]any{"settings": map[string]any{
		"public_base": "https://panel.example.test",
		"site_name":   "一键",
	}})
	raw := mustCall(t, cs, "quick_setup_node", map[string]any{
		"protocol":  "vless",
		"name":      "测试节点",
		"node_host": "203.0.113.50",
		"sni":       "www.microsoft.com",
		"port":      8443,
		"tag":       "vless-reality",
	})
	var setup struct {
		GroupID           int64  `json:"group_id"`
		EffectiveNodeHost string `json:"effective_node_host"`
		NodeHostAutoSet   string `json:"node_host_auto_set"`
	}
	decodeTool(t, raw, &setup)
	if setup.GroupID == 0 || setup.EffectiveNodeHost != "203.0.113.50" || setup.NodeHostAutoSet != "203.0.113.50" {
		t.Fatalf("quick setup = %+v\n%s", setup, raw)
	}
	if got, _ := h.st.GetSetting("node_host_override"); got != "203.0.113.50" {
		t.Fatalf("node_host_override = %q", got)
	}
	profiles, err := h.st.ListSbTls()
	if err != nil || len(profiles) != 1 {
		t.Fatalf("tls profiles: %v %+v", err, profiles)
	}
	var reality struct {
		Reality struct {
			PrivateKey string `json:"private_key"`
		} `json:"reality"`
	}
	if err := json.Unmarshal([]byte(profiles[0].ServerJSON), &reality); err != nil || reality.Reality.PrivateKey == "" {
		t.Fatalf("reality key missing from store: %v %s", err, profiles[0].ServerJSON)
	}
	if strings.Contains(raw, reality.Reality.PrivateKey) {
		t.Fatal("quick_setup_node echoed the Reality private key")
	}

	pkgRaw := mustCall(t, cs, "upsert_package", map[string]any{
		"name": "月付套餐", "traffic_gb": 50, "duration_days": 30, "price_points": 0,
		"group_ids": []any{setup.GroupID},
	})
	var pkg store.Package
	decodeTool(t, pkgRaw, &pkg)
	if pkg.ID == 0 || len(pkg.GroupIDs) != 1 || pkg.GroupIDs[0] != setup.GroupID {
		t.Fatalf("package = %+v\n%s", pkg, pkgRaw)
	}

	provRaw := mustCall(t, cs, "provision_user_access", map[string]any{
		"username": "alice01", "password": secretUserPass, "package_id": pkg.ID,
	})
	mustNotLeak(t, "provision result", provRaw, []string{secretUserPass, reality.Reality.PrivateKey})
	var prov struct {
		VisibleNodeCount int      `json:"visible_node_count"`
		SubscriptionURL  string   `json:"subscription_url"`
		Warnings         []string `json:"warnings"`
	}
	decodeTool(t, provRaw, &prov)
	if prov.VisibleNodeCount < 1 {
		t.Fatalf("visible nodes = %d warnings %v\n%s", prov.VisibleNodeCount, prov.Warnings, provRaw)
	}
	if !strings.Contains(prov.SubscriptionURL, "/sub/") || !strings.Contains(prov.SubscriptionURL, "https://panel.example.test") {
		t.Fatalf("subscription url %q", prov.SubscriptionURL)
	}

	listed := mustCall(t, cs, "list_tls_and_certs", nil)
	healthCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	health, err := cs.ReadResource(healthCtx, &mcp.ReadResourceParams{URI: "qingzhou://health"})
	if err != nil {
		t.Fatal(err)
	}
	combined := listed + health.Contents[0].Text
	if strings.Contains(combined, reality.Reality.PrivateKey) {
		t.Fatal("read tools leaked the generated Reality private key")
	}
	codes := issueCodes(t, health.Contents[0].Text)
	for _, gone := range []string{"missing_node_host", "orphan_inbounds", "empty_group_packages", "ungrouped_nodes"} {
		if _, ok := codes[gone]; ok {
			t.Fatalf("healthy chain still reports %s: %#v", gone, codes)
		}
	}

	logs, _, err := h.st.ListMCPAuditLogs(20, 0, "provision_user_access")
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || strings.Contains(logs[0].ArgsSummary, secretUserPass) {
		t.Fatalf("provision audit = %+v", logs)
	}
}
