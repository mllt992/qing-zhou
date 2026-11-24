package acmesh

import (
	"context"
	"strings"
	"testing"
)

func TestBuildIssueCmd_HTTP01(t *testing.T) {
	cmd, err := buildIssueCmd(IssueOpts{Domain: "proxy.example.com", Method: MethodHTTP01})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	for _, want := range []string{"--issue", "-d 'proxy.example.com'", "--standalone --httpport 80", "--server letsencrypt"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("cmd %q missing %q", cmd, want)
		}
	}
	if strings.Contains(cmd, "CF_Token") {
		t.Errorf("http-01 cmd should not export CF_Token: %q", cmd)
	}
}

func TestOptionsForProfile(t *testing.T) {
	for _, tc := range []struct {
		profile, key, chain string
	}{
		{"", "", ""}, // upgraded rows retain legacy behaviour
		{ProfileAuto, "2048", "ISRG Root X1"},
		{ProfileCompatible, "2048", "ISRG Root X1"},
		{ProfileModern, "ec-256", ""},
	} {
		key, chain, err := OptionsForProfile(tc.profile)
		if err != nil || key != tc.key || chain != tc.chain {
			t.Errorf("OptionsForProfile(%q) = %q, %q, %v", tc.profile, key, chain, err)
		}
	}
	if _, _, err := OptionsForProfile("surprise"); err == nil {
		t.Error("unknown profile was accepted")
	}
}

func TestBuildIssueCmd_CompatibilityOptions(t *testing.T) {
	cmd, err := buildIssueCmd(IssueOpts{
		Domain: "proxy.example.com", Method: MethodHTTP01,
		KeyLength: "2048", PreferredChain: "ISRG Root X1",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--keylength '2048'", "--preferred-chain 'ISRG Root X1'"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("cmd %q missing %q", cmd, want)
		}
	}
	if _, err := buildIssueCmd(IssueOpts{Domain: "a.example.com", Method: MethodHTTP01, KeyLength: "4096; touch /tmp/pwn"}); err == nil {
		t.Error("unsupported key option was accepted")
	}
}

func TestBuildIssueCmd_HTTP01RejectsWildcard(t *testing.T) {
	if _, err := buildIssueCmd(IssueOpts{Domain: "*.example.com", Method: MethodHTTP01}); err == nil {
		t.Error("expected wildcard to be rejected for http-01")
	}
}

func TestBuildIssueCmd_Webroot(t *testing.T) {
	cmd, err := buildIssueCmd(IssueOpts{Domain: "a.example.com", Method: MethodWebroot, Webroot: "/var/www/html"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	for _, want := range []string{"--issue", "-d 'a.example.com'", "-w '/var/www/html'"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("cmd %q missing %q", cmd, want)
		}
	}
	if strings.Contains(cmd, "--standalone") {
		t.Errorf("webroot cmd should not use standalone: %q", cmd)
	}
}

func TestBuildIssueCmd_WebrootNeedsPath(t *testing.T) {
	if _, err := buildIssueCmd(IssueOpts{Domain: "a.example.com", Method: MethodWebroot}); err == nil {
		t.Error("expected error when webroot path is missing")
	}
}

func TestBuildIssueCmd_CFDNS(t *testing.T) {
	cmd, err := buildIssueCmd(IssueOpts{Domain: "*.example.com", Method: MethodCFDNS, CFToken: "tok123"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	for _, want := range []string{"-d '*.example.com'", "--dns dns_cf"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("cmd %q missing %q", cmd, want)
		}
	}
	// The token must NOT be in the command string: everything here becomes an
	// argv element of the `sh` process and is world-readable via
	// /proc/<pid>/cmdline for as long as issuance runs — minutes, for dns_cf.
	// Issue passes it through the child's environment instead.
	if strings.Contains(cmd, "tok123") || strings.Contains(cmd, "CF_Token") {
		t.Errorf("Cloudflare token leaked into the command line: %q", cmd)
	}
}

// runEnv must hand secrets to an EnvRunner rather than folding them into the
// command, and must still work for a plain Runner.
func TestRunEnv_PrefersEnvRunner(t *testing.T) {
	var gotCmd string
	var gotEnv map[string]string
	er := envRunnerStub{fn: func(cmd string, env map[string]string) { gotCmd, gotEnv = cmd, env }}
	if _, err := runEnv(context.Background(), er, "acme.sh --issue", map[string]string{"CF_Token": "secret"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(gotCmd, "secret") {
		t.Errorf("secret reached the command string: %q", gotCmd)
	}
	if gotEnv["CF_Token"] != "secret" {
		t.Errorf("env = %v, want CF_Token=secret", gotEnv)
	}

	// A Runner without RunEnv still gets the variable, via the old in-command
	// export — degraded but functional.
	var plainCmd string
	pr := plainRunnerStub{fn: func(cmd string) { plainCmd = cmd }}
	if _, err := runEnv(context.Background(), pr, "acme.sh --issue", map[string]string{"CF_Token": "secret"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plainCmd, "export CF_Token='secret';") {
		t.Errorf("fallback did not export the variable: %q", plainCmd)
	}
}

type envRunnerStub struct{ fn func(string, map[string]string) }

func (e envRunnerStub) Run(_ context.Context, cmd string) (string, error) {
	e.fn(cmd, nil)
	return "", nil
}
func (e envRunnerStub) RunEnv(_ context.Context, cmd string, env map[string]string) (string, error) {
	e.fn(cmd, env)
	return "", nil
}

type plainRunnerStub struct{ fn func(string) }

func (p plainRunnerStub) Run(_ context.Context, cmd string) (string, error) {
	p.fn(cmd)
	return "", nil
}

// alreadyUpToDate must key off acme.sh's own status lines, not a bare substring
// of output that echoes the admin-supplied domain — otherwise a domain
// containing "Skipping" turns a real issuance failure into a success.
func TestAlreadyUpToDate_NotFooledByDomainEcho(t *testing.T) {
	failure := "[Mon Jul 19] Registering account\n" +
		"[Mon Jul 19] Create new order error for main domain: skipping-tests.example.com\n" +
		"[Mon Jul 19] Please add '--debug' to check more details.\n"
	if alreadyUpToDate(failure) {
		t.Errorf("a real failure was read as up-to-date because the domain contained the marker")
	}

	for _, ok := range []string{
		"[Mon Jul 19] Skipping. Next renewal time is: 2026-09-01\n",
		"[Mon Jul 19] Domains not changed.\n",
		"[Mon Jul 19] Next renewal time is: 2026-09-01\n",
	} {
		if !alreadyUpToDate(ok) {
			t.Errorf("genuine up-to-date output not recognised: %q", ok)
		}
	}
}

func TestBuildIssueCmd_CFDNSNeedsToken(t *testing.T) {
	if _, err := buildIssueCmd(IssueOpts{Domain: "a.example.com", Method: MethodCFDNS}); err == nil {
		t.Error("expected error when CF token is missing")
	}
}

func TestBuildIssueCmd_Validation(t *testing.T) {
	if _, err := buildIssueCmd(IssueOpts{Domain: "", Method: MethodHTTP01}); err == nil {
		t.Error("expected error for empty domain")
	}
	if _, err := buildIssueCmd(IssueOpts{Domain: "a.com", Method: "bogus"}); err == nil {
		t.Error("expected error for unknown method")
	}
}

func TestBuildInstallCmd(t *testing.T) {
	cmd := buildInstallCmd(IssueOpts{Domain: "a.example.com", ReloadCmd: "systemctl restart sing-box"},
		"/etc/sing-box/certs/a.example.com.crt", "/etc/sing-box/certs/a.example.com.key")
	for _, want := range []string{
		"--install-cert", "-d 'a.example.com'",
		"--key-file '/etc/sing-box/certs/a.example.com.key'",
		"--fullchain-file '/etc/sing-box/certs/a.example.com.crt'",
		"--reloadcmd 'systemctl restart sing-box'",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("cmd %q missing %q", cmd, want)
		}
	}
}

func TestBuildInstallCmd_ECCSelectsECCStore(t *testing.T) {
	cmd := buildInstallCmd(IssueOpts{Domain: "a.example.com", KeyLength: "ec-256"}, "/tmp/a.crt", "/tmp/a.key")
	if !strings.Contains(cmd, " --ecc") {
		t.Errorf("ECC install command did not select the ECC cert: %q", cmd)
	}
}

func TestRenewAppliesStoredCryptoPolicy(t *testing.T) {
	var got string
	r := plainRunnerStub{fn: func(cmd string) { got = cmd }}
	if _, err := Renew(context.Background(), r, IssueOpts{
		Domain: "a.example.com", Method: MethodHTTP01, PreferredChain: "ISRG Root X1",
	}, true); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--renew", "--preferred-chain 'ISRG Root X1'", "--force"} {
		if !strings.Contains(got, want) {
			t.Errorf("renew command %q missing %q", got, want)
		}
	}

	if _, err := Renew(context.Background(), r, IssueOpts{
		Domain: "a.example.com", Method: MethodHTTP01, KeyLength: "ec-256",
	}, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, " --ecc") {
		t.Errorf("modern renewal did not select ECC storage: %q", got)
	}
}

type scriptRunnerStub struct {
	cmds    []string
	handler func(cmd string) (string, error)
}

func (s *scriptRunnerStub) Run(_ context.Context, cmd string) (string, error) {
	s.cmds = append(s.cmds, cmd)
	return s.handler(cmd)
}

func TestRenewFallsBackToNonECCOrReissueWhenDomainNotIssued(t *testing.T) {
	notIssued := "[Tue Nov 24 10:29:50 PM CST 2026] Renew: 'ssl.mllt.vip'\n[Tue Nov 24 10:29:50 PM CST 2026] 'ssl.mllt.vip' is not an issued domain, skipping.\n"

	// Case 1: --renew --ecc fails with "not an issued domain", retry without --ecc succeeds.
	r1 := &scriptRunnerStub{
		handler: func(cmd string) (string, error) {
			if strings.Contains(cmd, "--renew") && strings.Contains(cmd, "--ecc") {
				return notIssued, context.DeadlineExceeded
			}
			return "ok", nil
		},
	}
	if _, err := Renew(context.Background(), r1, IssueOpts{
		Domain: "ssl.mllt.vip", Method: MethodCFDNS, CFToken: "tok", KeyLength: "ec-256",
	}, true); err != nil {
		t.Fatalf("expected fallback without --ecc to succeed, got: %v", err)
	}
	if len(r1.cmds) != 2 {
		t.Fatalf("expected 2 commands (--ecc then non-ecc), got %d: %v", len(r1.cmds), r1.cmds)
	}

	// Case 2: both --renew attempts fail with "not an issued domain" (e.g. container recreated),
	// fallback to Issue (--issue + --install-cert) succeeds.
	r2 := &scriptRunnerStub{
		handler: func(cmd string) (string, error) {
			if strings.Contains(cmd, "[ -x ") {
				return "__yes__", nil
			}
			if strings.Contains(cmd, "--renew") {
				return notIssued, context.DeadlineExceeded
			}
			return "ok", nil
		},
	}
	res, err := Renew(context.Background(), r2, IssueOpts{
		Domain: "ssl.mllt.vip", Method: MethodCFDNS, CFToken: "tok", KeyLength: "ec-256",
	}, true)
	if err != nil {
		t.Fatalf("expected fallback to Issue to succeed, got: %v", err)
	}
	if res == nil || res.CertPath == "" {
		t.Fatalf("expected non-empty IssueResult, got: %+v", res)
	}
	var sawIssue, sawInstall bool
	for _, c := range r2.cmds {
		if strings.Contains(c, "--issue") {
			sawIssue = true
		}
		if strings.Contains(c, "--install-cert") {
			sawInstall = true
		}
	}
	if !sawIssue || !sawInstall {
		t.Errorf("expected --issue and --install-cert in fallback commands, got: %v", r2.cmds)
	}
}

func TestCertPaths(t *testing.T) {
	c, k := certPaths("", "a.example.com")
	if c != "/etc/sing-box/certs/a.example.com.crt" || k != "/etc/sing-box/certs/a.example.com.key" {
		t.Errorf("default paths wrong: %q %q", c, k)
	}
	c, k = certPaths("/opt/certs", "*.example.com")
	if c != "/opt/certs/wildcard.example.com.crt" || k != "/opt/certs/wildcard.example.com.key" {
		t.Errorf("wildcard paths wrong: %q %q", c, k)
	}
}

func TestShellQuoteEscaping(t *testing.T) {
	// A token containing a single quote must be escaped so injection is impossible.
	got := shellQuote("a'b")
	if got != `'a'\''b'` {
		t.Errorf("shellQuote = %q", got)
	}
}
