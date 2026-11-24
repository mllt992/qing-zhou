// Package acmesh drives the acme.sh shell client to issue and install real
// Let's Encrypt certificates, mirroring what mature panels (3x-ui / s-ui) do:
// rather than embedding a Go ACME stack, it shells out to acme.sh, installs the
// cert to a fixed path, and lets acme.sh's own cron handle renewal. sing-box is
// pointed at the installed file paths (tls.certificate_path / key_path), so a
// renewal is picked up on the next service reload with no panel involvement.
//
// Execution is abstracted behind Runner so the same logic works on the local
// host (os/exec) or, later, a remote server (SSH).
package acmesh

import (
	"context"
	"fmt"
	"path"
	"strings"
)

// acmeBin is the standard acme.sh install location (per-user, root here).
const acmeBin = "$HOME/.acme.sh/acme.sh"

// AcmeVersion is the acme.sh git tag installed by Install. Pinned so the
// bootstrap can't be repointed at new code by whoever controls the download
// endpoint on the day a panel happens to issue its first certificate.
const AcmeVersion = "3.0.7"

// Runner executes a shell command on a host and returns combined stdout+stderr.
type Runner interface {
	Run(ctx context.Context, cmd string) (out string, err error)
}

// EnvRunner is a Runner that can also pass environment variables to the child
// without putting them on its command line. Secrets (the Cloudflare token) go
// through this: anything interpolated into the command string is visible in
// /proc/<pid>/cmdline to every local user for as long as the command runs.
// A Runner that doesn't implement it still works — runEnv falls back to the
// in-command export, which is what the code did unconditionally before.
type EnvRunner interface {
	Runner
	RunEnv(ctx context.Context, cmd string, env map[string]string) (out string, err error)
}

// runEnv dispatches to RunEnv when available, otherwise folds env into the
// command string.
func runEnv(ctx context.Context, r Runner, cmd string, env map[string]string) (string, error) {
	if len(env) == 0 {
		return r.Run(ctx, cmd)
	}
	if er, ok := r.(EnvRunner); ok {
		return er.RunEnv(ctx, cmd, env)
	}
	prefix := ""
	for k, v := range env {
		prefix += "export " + k + "=" + shellQuote(v) + "; "
	}
	return r.Run(ctx, prefix+cmd)
}

// Method is the ACME challenge method.
type Method string

const (
	MethodHTTP01  Method = "http-01" // standalone listener on :80
	MethodWebroot Method = "webroot" // drop challenge into an existing web root (nginx/etc.)
	MethodCFDNS   Method = "dns-cf"  // Cloudflare DNS-01 (supports wildcard)
)

// IssueOpts describes a certificate request.
type IssueOpts struct {
	Domain         string // e.g. proxy.example.com or *.example.com (dns-cf only)
	Method         Method
	CFToken        string // Cloudflare API token, required for MethodCFDNS
	Webroot        string // web root that already serves the domain on :80, required for MethodWebroot
	Email          string // ACME account email (optional but recommended)
	CertDir        string // where the installed cert/key land; default /etc/sing-box/certs
	ReloadCmd      string // run by acme.sh after issue and on every renewal
	KeyLength      string // empty keeps legacy acme.sh behaviour; 2048 or ec-256 when policy-managed
	PreferredChain string // empty follows the CA; currently only ISRG Root X1 is policy-managed
}

// ACME profiles are stored by the panel, while their concrete settings are
// resolved here. This keeps records stable when the recommended compatibility
// policy evolves in a future release. An empty profile is deliberately legacy:
// upgraded certificates keep the exact acme.sh behaviour they had before this
// feature instead of silently changing key algorithm during renewal.
const (
	ProfileAuto       = "auto"
	ProfileCompatible = "compatible"
	ProfileModern     = "modern"
)

// OptionsForProfile resolves a stored policy into acme.sh parameters.
func OptionsForProfile(profile string) (keyLength, preferredChain string, err error) {
	switch strings.TrimSpace(profile) {
	case "":
		return "", "", nil
	case ProfileAuto:
		// Current conservative recommendation. Unlike ProfileCompatible, this
		// mapping may evolve as older client trust stores disappear.
		return "2048", "ISRG Root X1", nil
	case ProfileCompatible:
		return "2048", "ISRG Root X1", nil
	case ProfileModern:
		return "ec-256", "", nil
	default:
		return "", "", fmt.Errorf("unsupported ACME profile %q", profile)
	}
}

func validateCryptoOptions(o IssueOpts) error {
	switch strings.TrimSpace(o.KeyLength) {
	case "", "2048", "ec-256":
	default:
		return fmt.Errorf("unsupported key length %q", o.KeyLength)
	}
	switch strings.TrimSpace(o.PreferredChain) {
	case "", "ISRG Root X1":
	default:
		return fmt.Errorf("unsupported preferred chain %q", o.PreferredChain)
	}
	return nil
}

func appendCryptoOptions(cmd string, o IssueOpts) (string, error) {
	if err := validateCryptoOptions(o); err != nil {
		return "", err
	}
	if v := strings.TrimSpace(o.KeyLength); v != "" {
		cmd += " --keylength " + shellQuote(v)
	}
	if v := strings.TrimSpace(o.PreferredChain); v != "" {
		cmd += " --preferred-chain " + shellQuote(v)
	}
	return cmd, nil
}

// IssueResult is the on-disk location of the installed certificate.
type IssueResult struct {
	CertPath string // fullchain PEM
	KeyPath  string // private key PEM
}

// shellQuote wraps s in single quotes, escaping any embedded single quotes, so
// it is safe to interpolate into a /bin/sh command line.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Installed reports whether acme.sh is present and executable on the host.
func Installed(ctx context.Context, r Runner) bool {
	out, err := r.Run(ctx, `[ -x `+acmeBin+` ] && echo __yes__ || echo __no__`)
	return err == nil && strings.Contains(out, "__yes__")
}

// Install fetches and installs acme.sh (idempotent — acme.sh's installer is safe
// to re-run) and pins Let's Encrypt as the default CA. A curl or wget must be
// present on the host.
func Install(ctx context.Context, r Runner, email string) error {
	acct := ""
	if e := strings.TrimSpace(email); e != "" {
		acct = " --accountemail " + shellQuote(e)
	}
	// Pinned to a tagged revision instead of https://get.acme.sh, which serves
	// whatever is current. This runs as root on the panel host, so an unpinned
	// `curl | sh` means whoever controls that endpoint at any future moment owns
	// the box — a second supply-chain entry point beside the self-updater.
	// Bump AcmeVersion deliberately; the URL is a git tag, so a given version
	// always resolves to the same bytes.
	src := "https://raw.githubusercontent.com/acmesh-official/acme.sh/" + AcmeVersion + "/acme.sh"
	// Prefer curl, fall back to wget — same probe the probe installer uses.
	get := `if command -v curl >/dev/null 2>&1; then curl -fsSL ` + shellQuote(src) + ` | sh -s -- --install ` +
		strings.TrimSpace(acct) + `; elif command -v wget >/dev/null 2>&1; then wget -qO- ` + shellQuote(src) + ` | sh -s -- --install ` +
		strings.TrimSpace(acct) + `; else echo "need curl or wget" >&2; exit 1; fi`
	if out, err := r.Run(ctx, get); err != nil {
		return fmt.Errorf("install acme.sh: %v: %s", err, out)
	}
	// Pin CA to Let's Encrypt (avoids ZeroSSL's mandatory registration).
	if out, err := r.Run(ctx, acmeBin+` --set-default-ca --server letsencrypt`); err != nil {
		return fmt.Errorf("set default CA: %v: %s", err, out)
	}
	return nil
}

// buildIssueCmd constructs the `acme.sh --issue` command for the given options.
// Exposed (lowercase, but unit-tested in-package) so the exact command line is
// verifiable without a live host.
func buildIssueCmd(o IssueOpts) (string, error) {
	domain := strings.TrimSpace(o.Domain)
	if domain == "" {
		return "", fmt.Errorf("domain is required")
	}
	base := acmeBin + " --issue -d " + shellQuote(domain) + " --server letsencrypt"
	var err error
	base, err = appendCryptoOptions(base, o)
	if err != nil {
		return "", err
	}
	switch o.Method {
	case MethodHTTP01:
		if strings.HasPrefix(domain, "*.") {
			return "", fmt.Errorf("wildcard domains require the Cloudflare DNS method")
		}
		return base + " --standalone --httpport 80", nil
	case MethodWebroot:
		if strings.HasPrefix(domain, "*.") {
			return "", fmt.Errorf("wildcard domains require the Cloudflare DNS method")
		}
		if strings.TrimSpace(o.Webroot) == "" {
			return "", fmt.Errorf("webroot path is required for the webroot method")
		}
		// acme.sh writes the challenge under <webroot>/.well-known/acme-challenge/,
		// which the existing web server (e.g. nginx) serves — no port binding.
		return base + " -w " + shellQuote(strings.TrimSpace(o.Webroot)), nil
	case MethodCFDNS:
		if strings.TrimSpace(o.CFToken) == "" {
			return "", fmt.Errorf("Cloudflare API token is required for the DNS method")
		}
		// CF_Token is consumed by acme.sh's dns_cf hook from the environment;
		// Issue passes it through runEnv so it never reaches a command line.
		return base + " --dns dns_cf", nil
	default:
		return "", fmt.Errorf("unsupported method %q", o.Method)
	}
}

// buildInstallCmd constructs the `acme.sh --install-cert` command that copies the
// issued cert/key to stable paths and records the reload command for renewals.
func buildInstallCmd(o IssueOpts, certPath, keyPath string) string {
	cmd := acmeBin + " --install-cert -d " + shellQuote(strings.TrimSpace(o.Domain)) +
		" --key-file " + shellQuote(keyPath) +
		" --fullchain-file " + shellQuote(certPath)
	if strings.TrimSpace(o.KeyLength) == "ec-256" {
		cmd += " --ecc"
	}
	if rc := strings.TrimSpace(o.ReloadCmd); rc != "" {
		cmd += " --reloadcmd " + shellQuote(rc)
	}
	return cmd
}

// certPaths returns the stable install paths for a domain under CertDir. A
// wildcard's leading "*." is normalized to "wildcard." for a valid filename.
func certPaths(certDir, domain string) (certPath, keyPath string) {
	dir := strings.TrimSpace(certDir)
	if dir == "" {
		dir = "/etc/sing-box/certs"
	}
	fn := strings.TrimSpace(domain)
	if strings.HasPrefix(fn, "*.") {
		fn = "wildcard." + strings.TrimPrefix(fn, "*.")
	}
	return path.Join(dir, fn+".crt"), path.Join(dir, fn+".key")
}

// Issue installs acme.sh if needed, requests the certificate, installs it to a
// stable path, and returns those paths. The caller stores the paths in the TLS
// profile (tls.certificate_path / key_path); renewal is handled by acme.sh's
// cron via the recorded reload command.
func Issue(ctx context.Context, r Runner, o IssueOpts) (*IssueResult, error) {
	issueCmd, err := buildIssueCmd(o)
	if err != nil {
		return nil, err
	}
	if !Installed(ctx, r) {
		if err := Install(ctx, r, o.Email); err != nil {
			return nil, err
		}
	}
	certPath, keyPath := certPaths(o.CertDir, o.Domain)
	// 0700: the directory holds a TLS private key. mkdir -p previously left it
	// at the umask default (0755 in a typical 022 umask).
	if out, err := r.Run(ctx, "mkdir -p "+shellQuote(path.Dir(certPath))+" && chmod 700 "+shellQuote(path.Dir(certPath))); err != nil {
		return nil, fmt.Errorf("create cert dir: %v: %s", err, out)
	}

	env := map[string]string{}
	if o.Method == MethodCFDNS {
		env["CF_Token"] = strings.TrimSpace(o.CFToken)
	}
	if out, err := runEnv(ctx, r, issueCmd, env); err != nil {
		// acme.sh exits non-zero when a valid cert already exists and isn't near
		// renewal; treat only that as success.
		//
		// Matched against acme.sh's own status lines, not a bare substring of the
		// combined output — that output echoes the admin-supplied domain, so a
		// domain containing "Skipping" used to turn a genuine failure into a
		// success and fall through to --install-cert.
		if !alreadyUpToDate(out) {
			// Common, self-inflicted failure: :80 is held by a web server (nginx).
			// Point the operator at the methods that don't need to bind :80.
			if strings.Contains(out, "port 80 is already used") || strings.Contains(out, "Please stop it") {
				return nil, fmt.Errorf("80 端口已被占用（通常是 nginx/网站服务）——请改用「Cloudflare DNS」或「webroot（网站根目录）」方式，无需占用端口。原始输出：%s", out)
			}
			return nil, fmt.Errorf("acme.sh issue failed: %v: %s", err, out)
		}
	}
	if out, err := r.Run(ctx, buildInstallCmd(o, certPath, keyPath)); err != nil {
		return nil, fmt.Errorf("acme.sh install-cert failed: %v: %s", err, out)
	}
	// The key is written by acme.sh under the panel's umask — typically 0644,
	// i.e. a world-readable TLS private key. Clamp it.
	if out, err := r.Run(ctx, "chmod 600 "+shellQuote(keyPath)); err != nil {
		return nil, fmt.Errorf("secure key file: %v: %s", err, out)
	}
	return &IssueResult{CertPath: certPath, KeyPath: keyPath}, nil
}

// ReadPEM returns the contents of the installed fullchain and key files via the
// runner. 轻舟 stores these bytes in the DB (encrypted) rather than pointing
// sing-box at the files, so a certificate issued on the panel host can be pushed
// to any node — including a remote one that never ran acme.sh.
func ReadPEM(ctx context.Context, r Runner, res *IssueResult) (certPEM, keyPEM string, err error) {
	certPEM, err = r.Run(ctx, "cat "+shellQuote(res.CertPath))
	if err != nil {
		return "", "", fmt.Errorf("read cert file: %v: %s", err, certPEM)
	}
	keyPEM, err = r.Run(ctx, "cat "+shellQuote(res.KeyPath))
	if err != nil {
		return "", "", fmt.Errorf("read key file: %v: %s", err, keyPEM)
	}
	return certPEM, keyPEM, nil
}

// Renew renews an already-issued domain and returns the install paths so the
// caller can re-read the refreshed PEM. force=true passes --force (manual "renew
// now"); force=false lets acme.sh's own threshold decide (scheduled sweep).
// acme.sh re-runs the recorded --install-cert on a successful renewal, so the
// files at the returned paths are refreshed in place.
func Renew(ctx context.Context, r Runner, o IssueOpts, force bool) (*IssueResult, error) {
	domain := strings.TrimSpace(o.Domain)
	if domain == "" {
		return nil, fmt.Errorf("domain is required")
	}
	cmd := acmeBin + " --renew -d " + shellQuote(domain)
	if err := validateCryptoOptions(o); err != nil {
		return nil, err
	}
	if strings.TrimSpace(o.KeyLength) == "ec-256" {
		cmd += " --ecc"
	}
	if v := strings.TrimSpace(o.PreferredChain); v != "" {
		cmd += " --preferred-chain " + shellQuote(v)
	}
	if force {
		cmd += " --force"
	}
	env := map[string]string{}
	if o.Method == MethodCFDNS && strings.TrimSpace(o.CFToken) != "" {
		env["CF_Token"] = strings.TrimSpace(o.CFToken)
	}
	if out, err := runEnv(ctx, r, cmd, env); err != nil {
		if !alreadyUpToDate(out) {
			if notIssuedDomain(out) {
				// 1. Legacy certificates issued before the policy update (or with a
				// different acme.sh default key type) may live in the non-ECC directory
				// even when ActualKeyType is ECDSA, or vice versa. Try toggling --ecc
				// before falling back to a full re-issue.
				altCmd := buildRenewCmd(domain, o, force, strings.TrimSpace(o.KeyLength) != "ec-256")
				if altOut, altErr := runEnv(ctx, r, altCmd, env); altErr == nil || alreadyUpToDate(altOut) {
					certPath, keyPath := certPaths(o.CertDir, domain)
					return &IssueResult{CertPath: certPath, KeyPath: keyPath}, nil
				}
				// 2. When ~/.acme.sh was wiped (e.g. Docker container recreated or host
				// migrated) and the challenge can run unattended (dns-cf orhttp-01 or
				// webroot with path), fall back to Issue so renewal self-heals.
				if canReissue(o) {
					return Issue(ctx, r, o)
				}
			}
			return nil, fmt.Errorf("acme.sh renew failed: %v: %s", err, out)
		}
	}
	certPath, keyPath := certPaths(o.CertDir, domain)
	return &IssueResult{CertPath: certPath, KeyPath: keyPath}, nil
}

func buildRenewCmd(domain string, o IssueOpts, force, ecc bool) string {
	cmd := acmeBin + " --renew -d " + shellQuote(domain)
	if ecc {
		cmd += " --ecc"
	}
	if v := strings.TrimSpace(o.PreferredChain); v != "" {
		cmd += " --preferred-chain " + shellQuote(v)
	}
	if force {
		cmd += " --force"
	}
	return cmd
}

func canReissue(o IssueOpts) bool {
	switch o.Method {
	case MethodCFDNS:
		return strings.TrimSpace(o.CFToken) != ""
	case MethodHTTP01:
		return !strings.HasPrefix(strings.TrimSpace(o.Domain), "*.")
	case MethodWebroot:
		return !strings.HasPrefix(strings.TrimSpace(o.Domain), "*.") && strings.TrimSpace(o.Webroot) != ""
	default:
		return false
	}
}

// notIssuedDomain reports whether acme.sh rejected --renew because its local
// state directory has no record of the domain ("'<domain>' is not an issued
// domain, skipping.").
func notIssuedDomain(out string) bool {
	return strings.Contains(out, "is not an issued domain")
}

// alreadyUpToDate reports whether acme.sh declined to reissue because the
// existing certificate is still valid. acme.sh emits these as whole lines
// prefixed with a timestamp, so the match is anchored to line starts after the
// "[time] " prefix rather than run against the whole blob — which includes the
// domain the admin typed.
func alreadyUpToDate(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		l := strings.TrimSpace(line)
		if i := strings.Index(l, "] "); i >= 0 && strings.HasPrefix(l, "[") {
			l = l[i+2:]
		}
		if strings.HasPrefix(l, "Skipping") || strings.HasPrefix(l, "Domains not changed") ||
			strings.HasPrefix(l, "Next renewal time is") {
			return true
		}
	}
	return false
}
