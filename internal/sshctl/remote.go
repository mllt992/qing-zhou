// Package sshctl provides SSH-based remote management for sing-box
// configuration deployment across multiple VPS nodes.
package sshctl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// parsePrivateKey parses a PEM-encoded private key, using the passphrase only
// when one is set. Passing a passphrase to an unencrypted OpenSSH key fails
// with "key is not password protected", so empty-passphrase keys must use the
// plain parser.
func parsePrivateKey(pem, passphrase string) (ssh.Signer, error) {
	if passphrase != "" {
		return ssh.ParsePrivateKeyWithPassphrase([]byte(pem), []byte(passphrase))
	}
	return ssh.ParsePrivateKey([]byte(pem))
}

// Fingerprint renders a pinned host key (an authorized_keys line, as stored on
// the server row) as the SHA256 form OpenSSH prints — so an admin can compare it
// against `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` on the machine
// itself. Returns "" when the stored value isn't a parseable key.
func Fingerprint(authorizedKey string) string {
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(authorizedKey))
	if err != nil {
		return ""
	}
	return ssh.FingerprintSHA256(pk)
}

// tunnelConn is a remote TCP connection carried over SSH. Closing it also closes
// the SSH client that carries it, so a caller that only holds the net.Conn can't
// leak the session underneath.
type tunnelConn struct {
	net.Conn
	client *ssh.Client
}

func (t *tunnelConn) Close() error {
	err := t.Conn.Close()
	if cerr := t.client.Close(); err == nil {
		err = cerr
	}
	return err
}

// DialTunnel opens a TCP connection to addr *on the remote host*, tunnelled over
// SSH. It exists so the panel can reach a service the remote box only binds on
// loopback — notably sing-box's v2ray stats API, whose default listen address is
// 127.0.0.1:18080 and is therefore unreachable directly.
//
// Each call establishes its own SSH client, which the returned conn owns.
func (m *RemoteManager) DialTunnel(ctx context.Context, cfg *ServerConfig, addr string) (net.Conn, error) {
	client, err := m.dial(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh connect %s: %w", cfg.Host, err)
	}
	conn, err := client.DialContext(ctx, "tcp", addr)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("tunnel to %s on %s: %w", addr, cfg.Host, err)
	}
	return &tunnelConn{Conn: conn, client: client}, nil
}

// ServerConfig holds the SSH connection details and sing-box paths
// for a single remote server.
type ServerConfig struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	SSHUser     string `json:"ssh_user"`
	SSHKey      string `json:"ssh_key"`      // PEM private key CONTENT (not a path)
	SSHKeyPass  string `json:"ssh_key_pass"` // passphrase for encrypted key
	SSHPassword string `json:"ssh_password"` // password auth fallback
	ConfigPath  string `json:"config_path"`  // e.g. /etc/sing-box/config.json
	SystemdUnit string `json:"systemd_unit"` // e.g. sing-box
	SingBoxBin  string `json:"sing_box_bin"` // e.g. /usr/local/bin/sing-box
	V2rayListen string `json:"v2ray_listen"` // e.g. 127.0.0.1
	HostKey     string `json:"-"`            // pinned SSH host key (authorized_keys line); "" = pin on first use

	// UseSudo prefixes every privileged command with sudo. Set when SSHUser is
	// not root: writing the config, installing it and restarting the unit all
	// need root, so without this the whole deploy path fails on a normal account.
	UseSudo bool `json:"use_sudo"`
	// SudoPassword feeds `sudo -S` over the session's stdin. Empty means the
	// account has NOPASSWD and `sudo -n` is used instead.
	SudoPassword string `json:"sudo_password"`
	// SSHKeyPath is the NAME of a key file in the panel's key directory (never a
	// path — see keyfile.go). Set, it takes precedence over the pasted SSHKey and
	// the key never has to travel through the browser or sit in the database.
	SSHKeyPath string `json:"ssh_key_path"`
}

// RemoteManager coordinates SSH operations against multiple remote servers.
type RemoteManager struct {
	timeout time.Duration

	// keyDir is where file-based private keys live on the panel host. Empty
	// disables them, so a row naming a key file fails loudly rather than falling
	// back to some other credential.
	keyDir string

	// persistHostKey pins a server's SSH host key on first successful connect
	// (TOFU). nil disables persistence (verification against an already-pinned
	// key still applies).
	persistHostKey func(serverID int64, hostKey string) error

	mu sync.Mutex

	// restartPending marks servers whose config landed but whose service restart
	// then failed. It gates the "node already has these exact bytes" shortcut:
	// matching bytes prove the file arrived, NOT that sing-box came up on them,
	// so without this a node left down by a failed restart would be reported
	// healthy on every later pass until an unrelated edit changed the config.
	restartPending map[int64]bool

	// binCache holds the resolved absolute path of each node's sing-box, so the
	// candidate scan costs one round trip per server rather than one per command.
	// The connection identity and configured path travel with the entry: a server
	// row can keep its ID while being pointed at a different host or binary.
	binCache map[int64]binCacheEntry
}

type binCacheEntry struct {
	host       string
	port       int
	configured string
	resolved   string
}

func (e binCacheEntry) matches(cfg *ServerConfig) bool {
	return e.resolved != "" && e.host == cfg.Host && e.port == cfg.Port &&
		e.configured == strings.TrimSpace(cfg.SingBoxBin)
}

// Option configures a RemoteManager.
type Option func(*RemoteManager)

// WithTimeout sets the dial/command timeout (default 30s).
func WithTimeout(d time.Duration) Option {
	return func(m *RemoteManager) { m.timeout = d }
}

// WithKeyDir sets the directory that file-based private keys are read from.
func WithKeyDir(dir string) Option {
	return func(m *RemoteManager) { m.keyDir = dir }
}

// New creates a RemoteManager with the given options.
func New(opts ...Option) *RemoteManager {
	m := &RemoteManager{
		timeout: 30 * time.Second,
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// SetHostKeyPersister registers a callback used to pin a server's SSH host key
// on first successful connection (trust-on-first-use).
func (m *RemoteManager) SetHostKeyPersister(fn func(serverID int64, hostKey string) error) {
	m.persistHostKey = fn
}

// ──────────────────────────────────────────────────────────────
// SSH client construction
// ──────────────────────────────────────────────────────────────

func (m *RemoteManager) buildClientConfig(cfg *ServerConfig) (*ssh.ClientConfig, error) {
	authMethods, err := m.buildAuthMethods(cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh auth: %w", err)
	}
	if len(authMethods) == 0 {
		return nil, errors.New("ssh: no authentication method configured (set password or key)")
	}

	var hostAlgorithms []string
	if cfg.HostKey != "" {
		key, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(cfg.HostKey))
		if err != nil || len(bytes.TrimSpace(rest)) != 0 {
			return nil, errors.New("ssh: invalid pinned host key")
		}
		// Negotiate the pinned key type, rather than another valid host key
		// offered by the same server. The callback still checks exact key bytes.
		hostAlgorithms = []string{key.Type()}
		switch key.Type() {
		case ssh.KeyAlgoRSA:
			hostAlgorithms = []string{ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSA}
		case ssh.CertAlgoRSAv01:
			hostAlgorithms = []string{ssh.CertAlgoRSASHA256v01, ssh.CertAlgoRSASHA512v01, ssh.CertAlgoRSAv01}
		}
	}
	return &ssh.ClientConfig{
		HostKeyAlgorithms: hostAlgorithms,
		User:              cfg.SSHUser,
		Auth:              authMethods,
		HostKeyCallback:   m.hostKeyCallback(cfg),
		Timeout:           m.timeout,
	}, nil
}

// hostKeyCallback verifies the remote host key. If the server already has a
// pinned key, the presented key must match it (mismatch = possible MITM →
// refuse). Otherwise the key is trusted on first use and pinned via the
// persister, so every later connection is verified. This replaces the previous
// InsecureIgnoreHostKey, which let any MITM impersonate a landing server and
// harvest SSH credentials / push an attacker-chosen config (root RCE).
func (m *RemoteManager) hostKeyCallback(cfg *ServerConfig) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		presented := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
		if cfg.HostKey != "" {
			if subtle.ConstantTimeCompare([]byte(cfg.HostKey), []byte(presented)) == 1 {
				return nil
			}
			return fmt.Errorf("ssh host key mismatch for %s: refusing (possible MITM; clear the pinned key to re-trust)", cfg.Host)
		}
		// Trust on first use: pin the key so subsequent connections are verified.
		if m.persistHostKey != nil && cfg.ID != 0 {
			if err := m.persistHostKey(cfg.ID, presented); err != nil {
				return fmt.Errorf("pin host key: %w", err)
			}
		}
		cfg.HostKey = presented
		return nil
	}
}

func (m *RemoteManager) buildAuthMethods(cfg *ServerConfig) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	// 1) SSH private key, from a file on the panel host if the row names one and
	// otherwise from SSHKey, which holds the PEM *content* pasted in the admin UI
	// and stored encrypted.
	//
	// The file wins deliberately: a row that names one is saying "do not use what
	// is in the database", and silently falling back to a stale pasted key would
	// authenticate with a credential the admin believes they stopped using.
	pem := cfg.SSHKey
	if cfg.SSHKeyPath != "" {
		data, err := ReadKeyFile(m.keyDir, cfg.SSHKeyPath)
		if err != nil {
			return nil, err
		}
		pem = string(data)
	}
	if pem != "" {
		signer, err := parsePrivateKey(pem, cfg.SSHKeyPass)
		if err != nil {
			return nil, fmt.Errorf("parse key: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}

	// 2) Password
	if cfg.SSHPassword != "" {
		methods = append(methods, ssh.Password(cfg.SSHPassword))
	}

	return methods, nil
}

func (m *RemoteManager) dial(ctx context.Context, cfg *ServerConfig) (*ssh.Client, error) {
	cc, err := m.buildClientConfig(cfg)
	if err != nil {
		return nil, err
	}
	addr := netAddr(cfg.Host, cfg.Port)

	// The x/crypto/ssh client does not accept context directly, so we
	// wrap the dial with a goroutine+select for context cancellation.
	type result struct {
		client *ssh.Client
		err    error
	}
	ch := make(chan result, 1)
	go func() {
		c, e := ssh.Dial("tcp", addr, cc)
		ch <- result{c, e}
	}()

	select {
	case <-ctx.Done():
		// The dial goroutine outlives this return. Hand the result off to a
		// reaper instead of abandoning it: a dial that lands after the deadline
		// would otherwise leave a live *ssh.Client in the buffered channel with
		// nobody to Close it, leaking the TCP connection and the library's
		// handshake/mux goroutines. Rebuild runs every minute, so a node that is
		// slow-but-reachable leaks one of each per cycle.
		go func() {
			if r := <-ch; r.client != nil {
				r.client.Close()
			}
		}()
		return nil, ctx.Err()
	case r := <-ch:
		return r.client, r.err
	}
}

// ──────────────────────────────────────────────────────────────
// Public API
// ──────────────────────────────────────────────────────────────

// ApplyConfig writes configJSON to the remote server's ConfigPath,
// validates it, and restarts the sing-box service.
// Skips the write+restart when the remote config is byte-identical and the
// service is active, preventing needless restarts without mistaking a stopped
// node for a healthy one.
//
// The bool reports whether the node was actually touched, i.e. whether every
// connection on it was just cut by the restart. The caller logs that: a restart
// is the single most user-visible thing the panel does, and without a record of
// it a bug that restarts a healthy node on every pass looks exactly like a
// successful deploy from the outside.
func (m *RemoteManager) ApplyConfig(ctx context.Context, cfg *ServerConfig, configJSON []byte) (bool, error) {
	return m.ApplyConfigForce(ctx, cfg, configJSON, false)
}

// ApplyConfigForce can deliberately restart matching bytes to establish a
// verified legacy statistics namespace after an interrupted earlier apply.
func (m *RemoteManager) ApplyConfigForce(ctx context.Context, cfg *ServerConfig, configJSON []byte, force bool) (bool, error) {
	// Do not skip the remote check from an in-memory hash. The node can reboot or
	// sing-box can crash while the panel process (and such a cache) stays alive;
	// treating the old hash as proof of health would then report a dead node as
	// successfully synced forever. nodeAlreadyHas verifies both the remote bytes
	// and systemd state without rewriting or restarting a healthy node.
	client, err := m.dial(ctx, cfg)
	if err != nil {
		return false, fmt.Errorf("ssh connect %s: %w", cfg.Host, err)
	}
	defer client.Close()

	// Nothing changed on the node either? Then this is a genuine no-op.
	//
	// The in-memory cache above only knows about applies this process made, and
	// it is empty after every restart — so without this check, restarting the
	// panel rewrites the config on every enabled node and restarts sing-box on
	// all of them, dropping every user's connection for no reason. applyLocal has
	// compared the on-disk bytes for exactly this reason all along; the remote
	// path never did.
	same, err := m.nodeAlreadyHas(ctx, client, cfg, configJSON)
	if err != nil {
		// A periodic pass that cannot prove the current state must fail closed.
		// Treating a transient SSH/sudo/hash error as "different" turns a harmless
		// observation failure into a disruptive config rewrite and restart.
		return false, fmt.Errorf("verify current config: %w", err)
	}
	if same && !force {
		return false, nil
	}

	// Write to a staging file first, validate it, and only then move it into
	// place. This keeps the live config intact if the new one is broken or
	// the transfer is interrupted, so a bad config never takes the node down.
	// Per-server staging name: Rebuild fans out one goroutine per enabled server,
	// so a shared ".qz-new" lets two rows targeting the same path interleave
	// write/validate/mv and publish each other's config.
	stage, err := m.createStageFile(ctx, client, cfg)
	if err != nil {
		return false, err
	}
	defer func() {
		// Only the /tmp staging file needs sweeping; the root path stages next to
		// the destination and the mv below consumes it.
		if cfg.UseSudo {
			cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _ = m.run(cleanup, client, "rm -f "+shellQuote(stage))
		}
	}()
	if err := m.writeFile(ctx, client, stage, configJSON); err != nil {
		return false, fmt.Errorf("write config: %w", err)
	}
	if err := m.validateConfigPath(ctx, client, cfg, stage); err != nil {
		if !cfg.UseSudo {
			_, _ = m.run(ctx, client, "rm -f "+shellQuote(stage))
		}
		return false, fmt.Errorf("validate config: %w", err)
	}
	if err := m.installConfig(ctx, client, cfg, stage); err != nil {
		return false, err
	}

	if err := m.restartService(ctx, client, cfg); err != nil {
		// The config is already in place but the service is not running on it.
		// Remember that, or the "node already has these bytes" check above would
		// call the next pass a no-op and report a node that is down as healthy.
		m.mu.Lock()
		if m.restartPending == nil {
			m.restartPending = make(map[int64]bool)
		}
		m.restartPending[cfg.ID] = true
		m.mu.Unlock()
		return true, fmt.Errorf("restart service: %w", err)
	}
	m.mu.Lock()
	delete(m.restartPending, cfg.ID)
	m.mu.Unlock()

	return true, nil
}

// rootStagePath is where a root login writes the new config before installing
// it. It stays beside the destination so the final rename is atomic.
//
// When the panel can write the destination directory itself — which is to say
// the SSH user is root — the staging file goes right next to the live config,
// exactly as it always has. A same-directory rename is atomic, and the config,
// which embeds the Reality private key and every user's credentials, never
// leaves the directory it belongs in.
func rootStagePath(cfg *ServerConfig) string {
	return fmt.Sprintf("%s.qz-new-%d", cfg.ConfigPath, cfg.ID)
}

// createStageFile returns a safe staging path for this apply.
//
// A fixed name in /tmp is not safe: another local account can pre-create it as
// a symlink to an attacker-owned, writable file. The login user then follows the
// link while writing the generated config, leaking every credential embedded in
// it before the later chmod fails. mktemp atomically creates a 0600 file owned by
// the SSH user; /tmp's sticky bit then prevents other users from replacing it.
func (m *RemoteManager) createStageFile(ctx context.Context, client *ssh.Client, cfg *ServerConfig) (string, error) {
	if !cfg.UseSudo {
		return rootStagePath(cfg), nil
	}
	return m.createTempFile(ctx, client, ".qz-cfg.XXXXXXXXXX")
}

// createTempFile atomically creates a private file in /tmp and returns its
// absolute path. template is always an internal constant, never user input.
func (m *RemoteManager) createTempFile(ctx context.Context, client *ssh.Client, template string) (string, error) {
	out, err := m.run(ctx, client, tempFileCommand(template))
	if err != nil {
		return "", fmt.Errorf("create remote temp file: %w", err)
	}
	remotePath := strings.TrimSpace(out)
	if !validTempFilePath(remotePath, template) {
		return "", fmt.Errorf("mktemp returned unexpected path %q", remotePath)
	}
	return remotePath, nil
}

func tempFileCommand(template string) string {
	return "umask 077; mktemp " + shellQuote("/tmp/"+template)
}

func validTempFilePath(remotePath, template string) bool {
	prefix := strings.TrimSuffix(template, "XXXXXXXXXX")
	randomLen := len(template) - len(prefix)
	base := path.Base(remotePath)
	return path.Dir(remotePath) == "/tmp" &&
		strings.HasPrefix(base, prefix) &&
		randomLen > 0 && len(base) == len(prefix)+randomLen &&
		!strings.ContainsAny(remotePath, "\r\n")
}

// installConfig moves the staged config into place.
//
// The root path is a plain rename, byte-for-byte what shipped before sudo
// support existed. The sudo path cannot rename across filesystems (/tmp is
// usually tmpfs), so it installs into a sibling of the destination first and
// renames from there — the rename has to stay atomic, or a half-written config
// becomes reachable exactly when the file is being replaced.
func (m *RemoteManager) installConfig(ctx context.Context, client *ssh.Client, cfg *ServerConfig, stage string) error {
	if !cfg.UseSudo {
		if _, err := m.run(ctx, client, fmt.Sprintf("mv -f %s %s", shellQuote(stage), shellQuote(cfg.ConfigPath))); err != nil {
			return fmt.Errorf("install config: %w", err)
		}
		return nil
	}
	sibling := fmt.Sprintf("%s.qz-new-%d", cfg.ConfigPath, cfg.ID)
	if _, err := m.runElevated(ctx, client, cfg, "mkdir -p "+shellQuote(path.Dir(cfg.ConfigPath))); err != nil {
		return fmt.Errorf("mkdir config dir: %w", err)
	}
	if _, err := m.runElevated(ctx, client, cfg,
		fmt.Sprintf("install -m600 -o root -g root %s %s", shellQuote(stage), shellQuote(sibling))); err != nil {
		return fmt.Errorf("install config: %w", err)
	}
	if _, err := m.runElevated(ctx, client, cfg,
		fmt.Sprintf("mv -f %s %s", shellQuote(sibling), shellQuote(cfg.ConfigPath))); err != nil {
		return fmt.Errorf("install config: %w", err)
	}
	return nil
}

// nodeAlreadyHas reports whether the node's live config is byte-identical to the
// one we are about to send AND its service is actually running on it.
//
// Both halves are required. Matching bytes only prove the file landed — the
// config is swapped before systemctl runs — so a node left down by a failed
// restart would otherwise look settled and get reported healthy forever. Asking
// systemd directly is one extra word on a command we are already sending, and it
// closes that hole rather than tracking it in memory that a restart wipes.
//
// A valid inactive/missing state returns false so the apply can repair it. An
// observation error is returned to the caller instead: turning an unknown state
// into a rewrite/restart is exactly the failure mode this comparison prevents.
func (m *RemoteManager) nodeAlreadyHas(ctx context.Context, client *ssh.Client, cfg *ServerConfig, configJSON []byte) (bool, error) {
	m.mu.Lock()
	pending := m.restartPending[cfg.ID]
	m.mu.Unlock()
	if pending {
		return false, nil
	}

	unit := cfg.SystemdUnit
	if unit == "" {
		unit = "sing-box"
	}
	// One round trip for both answers. sha256sum needs root for a 0600 config;
	// systemctl is-active does not, but it rides along.
	// systemctl returns 3 for a known but inactive unit. That is a valid answer
	// (and means the config should be applied/started), not an observation error.
	// Other failures stay non-zero so ApplyConfig fails closed.
	cmd := fmt.Sprintf("sha256sum %s 2>/dev/null | cut -d%s -f1; systemctl is-active %s 2>/dev/null; rc=$?; [ \"$rc\" -eq 0 ] || [ \"$rc\" -eq 3 ]",
		shellQuote(cfg.ConfigPath), shellQuote(" "), shellQuote(unit))
	out, err := m.runElevated(ctx, client, cfg, cmd)
	if err != nil {
		return false, err
	}
	fields := strings.Fields(out)
	if len(fields) < 2 {
		return false, nil
	}
	return fields[0] == configHash(remoteBytes(configJSON)) && fields[1] == "active", nil
}

// RunCommand dials the server and runs one shell command, returning its combined
// stdout+stderr. For ad-hoc, non-mutating diagnostics that must originate from
// the node itself — e.g. testing a proxy egress so an IP-whitelisted upstream
// sees the node's address, not the panel's. The caller is responsible for a
// bounded ctx; the command string must already be shell-safe.
func (m *RemoteManager) RunCommand(ctx context.Context, cfg *ServerConfig, cmd string) (string, error) {
	client, err := m.dial(ctx, cfg)
	if err != nil {
		return "", fmt.Errorf("ssh connect %s: %w", cfg.Host, err)
	}
	defer client.Close()
	return m.run(ctx, client, cmd)
}

// TestConnection verifies SSH connectivity and returns the remote
// sing-box version string on success.
func (m *RemoteManager) TestConnection(ctx context.Context, cfg *ServerConfig) (string, error) {
	client, err := m.dial(ctx, cfg)
	if err != nil {
		return "", fmt.Errorf("ssh connect %s: %w", cfg.Host, err)
	}
	defer client.Close()

	// Prove elevation works before anything depends on it. Without this the first
	// evidence that sudo is misconfigured is a config deploy dying on systemctl,
	// hours later and nowhere near the settings that caused it.
	if cfg.UseSudo {
		if _, err := m.runElevated(ctx, client, cfg, "true"); err != nil {
			return "", fmt.Errorf("提权失败：%w\n"+
				"请给 %s 配置免密 sudo（visudo：%s ALL=(ALL) NOPASSWD:ALL），或在面板填写 sudo 密码",
				err, cfg.SSHUser, cfg.SSHUser)
		}
	}

	bin, err := m.resolveBin(ctx, client, cfg)
	if err != nil {
		return "", err
	}
	// No `2>/dev/null || echo unknown` here. That turned every failure into the
	// literal string "unknown" with a nil error, which the caller could not tell
	// from a real answer — and SupportsStatsAPI reads this output to decide
	// whether to meter the node's traffic at all. A version this cannot obtain is
	// an error, not a value.
	out, err := m.run(ctx, client, shellQuote(bin)+" version")
	if err != nil {
		return "", fmt.Errorf("run version on %s: %w", bin, err)
	}
	return strings.TrimSpace(out), nil
}

// SupportsStatsAPI reports whether the remote sing-box was built with the
// v2ray_api plugin, which the panel needs to read per-user traffic off that node.
//
// `sing-box version` prints its build tags, so this is a definitive answer
// rather than an inference from a failed config load — and it has to be checked:
// emitting an experimental.v2ray_api block into a config for a binary that lacks
// the plugin makes `sing-box check` fail, and the panel would then refuse to
// deploy ANY further config to that node.
// Returns the raw version output alongside the verdict so a "not supported"
// answer can be reported with the evidence behind it — otherwise an operator has
// no way to tell a genuinely feature-less build from a probe that resolved the
// wrong binary.
func (m *RemoteManager) SupportsStatsAPI(ctx context.Context, cfg *ServerConfig) (bool, string, error) {
	out, err := m.TestConnection(ctx, cfg)
	if err != nil {
		return false, "", err
	}
	return strings.Contains(out, "with_v2ray_api"), out, nil
}

// RestartService restarts the systemd unit on the remote server.
func (m *RemoteManager) RestartService(ctx context.Context, cfg *ServerConfig) error {
	client, err := m.dial(ctx, cfg)
	if err != nil {
		return fmt.Errorf("ssh connect %s: %w", cfg.Host, err)
	}
	defer client.Close()
	return m.restartService(ctx, client, cfg)
}

// ConfigFileInfo describes a .json file found on the remote server.
type ConfigFileInfo struct {
	Path string `json:"path"`
	Size int64  `json:"size"` // bytes
}

// ListRemoteConfigFiles scans common sing-box config directories on the
// remote server and returns all .json files found, sorted by modification
// time (newest first).  Also includes the systemd ExecStart -c path and
// the stored ConfigPath if they point to unique locations.
func (m *RemoteManager) ListRemoteConfigFiles(ctx context.Context, cfg *ServerConfig) ([]ConfigFileInfo, error) {
	client, err := m.dial(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh connect %s: %w", cfg.Host, err)
	}
	defer client.Close()

	unit := cfg.SystemdUnit
	if unit == "" {
		unit = "sing-box"
	}

	// 1) Get systemd ExecStart -c path
	detectCmd := fmt.Sprintf(
		`systemctl cat %s 2>/dev/null | grep -oP '(?<=-c\s)\S+' | head -1`,
		shellQuote(unit),
	)
	detectedPath, _ := m.runElevated(ctx, client, cfg, detectCmd)
	detectedPath = strings.TrimSpace(detectedPath)

	// 2) Scan common directories for .json files
	// find returns: "size\tmtime\tfilepath" lines, sorted by mtime desc
	scanCmd := `find /etc/s-box /etc/sing-box /usr/local/etc/sing-box /etc/x-ui ` +
		`-maxdepth 2 -name '*.json' -type f ` +
		`-printf '%s\t%T@\t%p\n' 2>/dev/null | sort -t$'\t' -k2 -rn | head -20`
	scanOut, _ := m.runElevated(ctx, client, cfg, scanCmd)

	seen := map[string]bool{}
	var files []ConfigFileInfo

	// Helper to add if unique
	addPath := func(p string, sizeHint int64) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		files = append(files, ConfigFileInfo{Path: p, Size: sizeHint})
	}

	// Parse find output
	for _, line := range strings.Split(scanOut, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		var sz int64
		fmt.Sscanf(parts[0], "%d", &sz)
		addPath(parts[2], sz)
	}

	// Ensure detected and stored paths are in the list
	if detectedPath != "" {
		addPath(detectedPath, 0)
	}
	if cfg.ConfigPath != "" {
		addPath(cfg.ConfigPath, 0)
	}

	// Always include standard sing-box config paths
	addPath("/etc/sing-box/config.json", 0)
	addPath("/etc/s-box/sb.json", 0)

	return files, nil
}

// ReadRemoteConfigAtPath reads a specific config file from a remote server
// by path. No auto-detection — the caller provides the exact path.
func (m *RemoteManager) ReadRemoteConfigAtPath(ctx context.Context, cfg *ServerConfig, configPath string) ([]byte, error) {
	client, err := m.dial(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh connect %s: %w", cfg.Host, err)
	}
	defer client.Close()

	raw, err := m.runElevated(ctx, client, cfg, "cat "+shellQuote(configPath)+" 2>/dev/null")
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", configPath, err)
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("文件 %s 为空", configPath)
	}
	return []byte(raw), nil
}

// ReadRemoteConfig reads the sing-box config JSON from a remote server.
// It auto-detects the actual config path by trying multiple candidates
// and returning the first one that contains a non-empty inbounds array.
func (m *RemoteManager) ReadRemoteConfig(ctx context.Context, cfg *ServerConfig) ([]byte, string, error) {
	client, err := m.dial(ctx, cfg)
	if err != nil {
		return nil, "", fmt.Errorf("ssh connect %s: %w", cfg.Host, err)
	}
	defer client.Close()

	unit := cfg.SystemdUnit
	if unit == "" {
		unit = "sing-box"
	}

	// Strategy: read the systemd unit file to find the real -c path.
	// This handles cases where config_path in the DB is wrong (e.g. /etc/sing-box/config.json
	// but the actual service uses /etc/s-box/sb.json).
	detectCmd := fmt.Sprintf(
		`unit=$(systemctl cat %s 2>/dev/null | grep -oP '(?<=-c\s)\S+' | head -1); `+
			`if [ -n "$unit" ]; then echo "$unit"; else echo ""; fi`,
		shellQuote(unit),
	)
	detectedPath, _ := m.runElevated(ctx, client, cfg, detectCmd)
	detectedPath = strings.TrimSpace(detectedPath)

	// Build candidate paths: detected > stored > common defaults
	var candidates []string
	if detectedPath != "" {
		candidates = append(candidates, detectedPath)
	}
	if cfg.ConfigPath != "" {
		candidates = append(candidates, cfg.ConfigPath)
	}
	// Common defaults (avoid duplicates)
	for _, p := range []string{"/etc/s-box/sb.json", "/etc/sing-box/config.json", "/usr/local/etc/sing-box/config.json"} {
		if p != detectedPath && p != cfg.ConfigPath {
			candidates = append(candidates, p)
		}
	}

	for _, path := range candidates {
		raw, err := m.runElevated(ctx, client, cfg, "cat "+shellQuote(path)+" 2>/dev/null")
		if err != nil || strings.TrimSpace(raw) == "" {
			continue
		}
		// Must contain "inbounds" with a non-empty array — skip files
		// that only have "inbounds": [] or no inbounds at all.
		if !strings.Contains(raw, "inbounds") {
			continue
		}
		// Quick check: there must be at least one opening brace after "inbounds"
		// to confirm a non-empty array.  "inbounds": [  {  counts.
		idx := strings.Index(raw, "inbounds")
		after := raw[idx:]
		if !strings.Contains(after[:min(len(after), 500)], "{") {
			continue
		}
		return []byte(strings.TrimSpace(raw)), path, nil
	}

	return nil, "", fmt.Errorf("未找到有效的 sing-box 配置文件（已尝试路径: %v）", candidates)
}

// ──────────────────────────────────────────────────────────────
// Low-level helpers
// ──────────────────────────────────────────────────────────────

// writeFile uploads data to the remote path via SFTP-like behaviour
// using a shell pipe (avoids needing a sftp sub-dependency).
func (m *RemoteManager) writeFile(ctx context.Context, client *ssh.Client, remotePath string, data []byte) error {
	// Ensure remote directory exists.
	dir := path.Dir(remotePath)
	if _, err := m.run(ctx, client, "mkdir -p "+shellQuote(dir)); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	// Config secrets travel only over SSH stdin. A fixed-size command avoids
	// both shell argument limits and exposing authentication data in argv.
	// Keep the historical normalized bytes so unchanged nodes remain no-ops.
	script := configWriteCommand(remotePath)
	if _, err := m.runInput(ctx, client, script, bytes.NewReader(remoteBytes(data))); err != nil {
		return fmt.Errorf("stdin write: %w", err)
	}
	return nil
}

func configWriteCommand(remotePath string) string {
	return fmt.Sprintf("umask 077; cat > %s && chmod 600 %s", shellQuote(remotePath), shellQuote(remotePath))
}

// validateConfigPath runs "sing-box check" against the given config path on
// the remote server.
func (m *RemoteManager) validateConfigPath(ctx context.Context, client *ssh.Client, cfg *ServerConfig, path string) error {
	bin, err := m.resolveBin(ctx, client, cfg)
	if err != nil {
		return err
	}
	// Elevated even though the staged file is ours: a config may still reference
	// a certificate_path from before the certificate centre inlined them, and
	// those files are root-only. Checking unprivileged would pass here and fail
	// on whoever kept a hand-written path.
	cmd := fmt.Sprintf("%s check -c %s 2>&1", shellQuote(bin), shellQuote(path))
	out, err := m.runElevated(ctx, client, cfg, cmd)
	if err != nil {
		return fmt.Errorf("sing-box check failed: %s: %w", out, err)
	}
	return nil
}

// restartService restarts (or starts) a systemd unit.
func (m *RemoteManager) restartService(ctx context.Context, client *ssh.Client, cfg *ServerConfig) error {
	unit := cfg.SystemdUnit
	if unit == "" {
		unit = "sing-box"
	}
	q := shellQuote(unit)
	// Preserve the historical root command byte-for-byte. Existing root rows are
	// the common case, and sudo support must not change what they execute.
	if !cfg.UseSudo {
		cmd := fmt.Sprintf("systemctl restart %s 2>&1 || systemctl start %s 2>&1", q, q)
		out, err := m.run(ctx, client, cmd)
		if err != nil {
			return fmt.Errorf("systemctl: %s: %w", out, err)
		}
		return nil
	}

	// For sudo rows, restart and start must be separate elevated calls: prefixing
	// `sudo` to `restart || start` only elevates the command to the left of ||
	// because the login shell parses the operator first.
	restartOut, restartErr := m.runElevated(ctx, client, cfg, fmt.Sprintf("systemctl restart %s 2>&1", q))
	if restartErr == nil {
		return nil
	}
	startOut, startErr := m.runElevated(ctx, client, cfg, fmt.Sprintf("systemctl start %s 2>&1", q))
	if startErr != nil {
		return fmt.Errorf("systemctl restart failed: %s: %v; start failed: %s: %w", restartOut, restartErr, startOut, startErr)
	}
	return nil
}

// singBoxCandidates is where a sing-box can be, most-preferred first. It mirrors
// sbproc.FindSingBoxBin so a remote node and the panel's own host resolve the
// same way.
var singBoxCandidates = []string{
	"/opt/qingzhou/sing-box",
	"/usr/local/bin/sing-box",
	"/usr/bin/sing-box",
}

// ResolveSingBoxBin returns the absolute path of the node's sing-box binary.
//
// It replaces a `[ -x "$BIN" ] || BIN=sing-box` fallback that leaned on PATH.
// That fallback breaks as soon as anything runs under sudo: sudo replaces PATH
// with sudoers' secure_path, and the RHEL family's default does not contain
// /usr/local/bin — which is exactly where the installer puts the binary.
//
// Worse than breaking, it broke quietly. The version probe ended in
// `2>/dev/null || echo unknown`, so a missing binary came back as the literal
// string "unknown" with a nil error: the connection test reported success, and
// SupportsStatsAPI read "unknown" as "this build has no v2ray_api" and stopped
// emitting the stats block for that node. Traffic there was then never metered
// and quotas never fired, with nothing but one log line to say so.
//
// The scan is deliberately unprivileged. The installer uses `install -m755`, so
// the login user can test the file; keeping it out of sudo means a locked-down
// sudoers never has to allow a shell loop.
func (m *RemoteManager) ResolveSingBoxBin(ctx context.Context, cfg *ServerConfig) (string, error) {
	client, err := m.dial(ctx, cfg)
	if err != nil {
		return "", fmt.Errorf("ssh connect %s: %w", cfg.Host, err)
	}
	defer client.Close()
	return m.resolveBin(ctx, client, cfg)
}

func (m *RemoteManager) resolveBin(ctx context.Context, client *ssh.Client, cfg *ServerConfig) (string, error) {
	if cfg.ID != 0 {
		m.mu.Lock()
		cached := m.binCache[cfg.ID]
		m.mu.Unlock()
		if cached.matches(cfg) {
			return cached.resolved, nil
		}
	}

	var quoted []string
	// The stored path first: an admin who points a row at a non-standard build
	// means it, and must not be silently overridden by a stock one elsewhere.
	if b := strings.TrimSpace(cfg.SingBoxBin); b != "" {
		quoted = append(quoted, shellQuote(b))
	}
	for _, c := range singBoxCandidates {
		quoted = append(quoted, shellQuote(c))
	}
	// PATH last and never alone — under sudo it is secure_path, not the login
	// shell's PATH, which is the whole reason the old fallback failed.
	quoted = append(quoted, `"$(command -v sing-box 2>/dev/null)"`)

	cmd := fmt.Sprintf(
		`for c in %s; do [ -n "$c" ] && [ -x "$c" ] && { printf '%%s\n' "$c"; exit 0; }; done; exit 1`,
		strings.Join(quoted, " "),
	)
	out, err := m.run(ctx, client, cmd)
	bin := strings.TrimSpace(out)
	if err != nil || bin == "" {
		return "", fmt.Errorf("在 %s 上找不到 sing-box（已试：%s 及 PATH）；"+
			"请确认已安装，或在服务器设置里把「sing-box 路径」填成实际路径",
			cfg.Host, strings.Join(append([]string{cfg.SingBoxBin}, singBoxCandidates...), " "))
	}

	if cfg.ID != 0 {
		m.mu.Lock()
		if m.binCache == nil {
			m.binCache = make(map[int64]binCacheEntry)
		}
		m.binCache[cfg.ID] = binCacheEntry{
			host:       cfg.Host,
			port:       cfg.Port,
			configured: strings.TrimSpace(cfg.SingBoxBin),
			resolved:   bin,
		}
		m.mu.Unlock()
	}
	return bin, nil
}

// ForgetSingBoxBin drops the cached binary location for a server, so the next
// operation re-scans. Called after a reinstall, which can move the binary.
func (m *RemoteManager) ForgetSingBoxBin(serverID int64) {
	m.mu.Lock()
	delete(m.binCache, serverID)
	m.mu.Unlock()
}

// elevate wraps one command so it runs as root.
//
// A root row returns the command unchanged, so what ships to an existing
// installation is byte-identical to what it was before sudo support existed.
//
// The command is NOT wrapped in `sh -c`: every caller passes a single simple
// command, and putting a shell in between would mean quoting the whole thing —
// including, on the write path, a config that embeds Reality private keys and
// every user's credentials. Fewer layers of shell parsing, fewer ways to break.
func elevate(cfg *ServerConfig, cmd string) string {
	if !cfg.UseSudo {
		return cmd
	}
	if cfg.SudoPassword != "" {
		// -p '' suppresses the prompt, which would otherwise land in the combined
		// output we hand back to the admin.
		return "sudo -S -p '' -- " + cmd
	}
	return "sudo -n -- " + cmd
}

// runElevated runs cmd with root privileges on the remote host.
func (m *RemoteManager) runElevated(ctx context.Context, client *ssh.Client, cfg *ServerConfig, cmd string) (string, error) {
	stdin := ""
	if cfg.UseSudo && cfg.SudoPassword != "" {
		stdin = cfg.SudoPassword + "\n"
	}
	out, err := m.runStdin(ctx, client, elevate(cfg, cmd), stdin)
	if err != nil && cfg.UseSudo {
		return out, annotateSudoError(err, out)
	}
	return out, err
}

// annotateSudoError turns sudo's own refusals into something an admin can act
// on. "exit status 1" with a line about a tty is otherwise a dead end.
func annotateSudoError(err error, out string) error {
	switch {
	case strings.Contains(out, "must have a tty"), strings.Contains(out, "no tty present"):
		return fmt.Errorf("%w（sudoers 里的 requiretty 挡住了非交互 sudo，请去掉该项）", err)
	case strings.Contains(out, "password is required"), strings.Contains(out, "a password is required"):
		return fmt.Errorf("%w（该账号的 sudo 需要密码：请在面板填写 sudo 密码，或为它配置 NOPASSWD）", err)
	case strings.Contains(out, "incorrect password"), strings.Contains(out, "Sorry, try again"):
		return fmt.Errorf("%w（sudo 密码不对）", err)
	case strings.Contains(out, "not in the sudoers"):
		return fmt.Errorf("%w（该账号不在 sudoers 里，无法提权）", err)
	case strings.Contains(out, "sudo: command not found"), strings.Contains(out, "sudo: not found"):
		return fmt.Errorf("%w（这台机器没装 sudo）", err)
	}
	return err
}

// run executes a shell command on the remote host and returns combined output.
func (m *RemoteManager) run(ctx context.Context, client *ssh.Client, cmd string) (string, error) {
	return m.runStdin(ctx, client, cmd, "")
}

// runStdin is run with something written to the command's standard input.
//
// It exists for `sudo -S`, which reads the password from stdin. Passing it that
// way rather than as `echo pw | sudo -S` is the whole point: an argument is
// visible to every local user on the node through ps and /proc/<pid>/cmdline,
// and this is a password that opens a root shell there.
func (m *RemoteManager) runStdin(ctx context.Context, client *ssh.Client, cmd, stdin string) (string, error) {
	return m.runInput(ctx, client, cmd, strings.NewReader(stdin))
}

// runInput is the binary-safe counterpart of runStdin. Keeping the probe bytes
// on SSH stdin avoids shell argument limits and heredoc corruption (an ELF is
// neither UTF-8 text nor line-oriented).
func (m *RemoteManager) runInput(ctx context.Context, client *ssh.Client, cmd string, stdin io.Reader) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("new session: %w", err)
	}
	defer session.Close()

	var stdout, stderr strings.Builder
	session.Stdout = &stdout
	session.Stderr = &stderr
	if stdin != nil {
		session.Stdin = stdin
	}

	if err := session.Start(cmd); err != nil {
		return "", fmt.Errorf("start: %w", err)
	}

	done := make(chan error, 1)
	go func() { done <- session.Wait() }()

	select {
	case <-ctx.Done():
		_ = session.Close()
		return "", ctx.Err()
	case err := <-done:
		combined := strings.TrimSpace(stdout.String() + " " + stderr.String())
		if err != nil {
			return combined, fmt.Errorf("exit: %w: %s", err, combined)
		}
		return combined, nil
	}
}

// writeBinary streams an opaque file through SSH stdin to an already-created
// private temporary path. Unlike writeFile it never embeds bytes in a shell
// command, so NULs and arbitrary ELF contents round-trip exactly.
func (m *RemoteManager) writeBinary(ctx context.Context, client *ssh.Client, remotePath string, data []byte) error {
	cmd := fmt.Sprintf("umask 077; cat > %s; chmod 700 %s", shellQuote(remotePath), shellQuote(remotePath))
	if _, err := m.runInput(ctx, client, cmd, bytes.NewReader(data)); err != nil {
		return fmt.Errorf("binary upload: %w", err)
	}
	return nil
}

// InstallProbe uploads the panel-bundled probe, installs its environment and
// hardened systemd unit, then starts it. The remote machine never has to reach
// back to the panel to download a binary; only its normal report connection is
// required after startup.
func (m *RemoteManager) InstallProbe(ctx context.Context, cfg *ServerConfig, binary []byte, panelURL, token string) (string, error) {
	if len(binary) == 0 {
		return "", errors.New("探针二进制为空")
	}
	if strings.ContainsAny(panelURL+token, "\r\n") {
		return "", errors.New("探针配置包含非法换行")
	}
	client, err := m.dial(ctx, cfg)
	if err != nil {
		return "", err
	}
	defer client.Close()

	type stagedFile struct {
		path string
	}
	var staged []stagedFile
	stage := func(template string) (string, error) {
		p, err := m.createTempFile(ctx, client, template)
		if err == nil {
			staged = append(staged, stagedFile{path: p})
		}
		return p, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, f := range staged {
			_, _ = m.run(cleanup, client, "rm -f "+shellQuote(f.path))
		}
	}()

	binPath, err := stage(".qz-probe-bin.XXXXXXXXXX")
	if err != nil {
		return "", err
	}
	envPath, err := stage(".qz-probe-env.XXXXXXXXXX")
	if err != nil {
		return "", err
	}
	unitPath, err := stage(".qz-probe-unit.XXXXXXXXXX")
	if err != nil {
		return "", err
	}

	if err := m.writeBinary(ctx, client, binPath, binary); err != nil {
		return "", fmt.Errorf("上传探针失败: %w", err)
	}
	wantHash := sha256.Sum256(binary)
	gotHash, err := m.run(ctx, client, "sha256sum "+shellQuote(binPath)+" | cut -d' ' -f1")
	if err != nil || strings.TrimSpace(gotHash) != hex.EncodeToString(wantHash[:]) {
		return gotHash, errors.New("上传后的探针校验失败")
	}
	envBody := "QZ_PROBE_SERVER=" + panelURL + "\nQZ_PROBE_TOKEN=" + token + "\n"
	unitBody := `[Unit]
Description=Qingzhou Monitor Probe
After=network.target

[Service]
Type=simple
EnvironmentFile=/etc/qingzhou-probe.env
ExecStart=/usr/local/bin/qingzhou-probe
Restart=always
RestartSec=10
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadOnlyPaths=/proc /sys

[Install]
WantedBy=multi-user.target
`
	if err := m.writeFile(ctx, client, envPath, []byte(envBody)); err != nil {
		return "", fmt.Errorf("上传探针配置失败: %w", err)
	}
	if err := m.writeFile(ctx, client, unitPath, []byte(unitBody)); err != nil {
		return "", fmt.Errorf("上传探针服务失败: %w", err)
	}

	// Each elevated call is one simple command. runElevated deliberately avoids
	// `sh -c`, so joining these with && would elevate only the first command for
	// sudo-based server rows.
	steps := []string{
		"install -m 755 " + shellQuote(binPath) + " /usr/local/bin/qingzhou-probe.new",
		"mv -f /usr/local/bin/qingzhou-probe.new /usr/local/bin/qingzhou-probe",
		"install -m 600 " + shellQuote(envPath) + " /etc/qingzhou-probe.env",
		"install -m 644 " + shellQuote(unitPath) + " /etc/systemd/system/qingzhou-probe.service",
		"systemctl daemon-reload",
		"systemctl enable qingzhou-probe",
		"systemctl restart qingzhou-probe",
		"systemctl is-active --quiet qingzhou-probe",
	}
	for _, cmd := range steps {
		out, err := m.runElevated(ctx, client, cfg, cmd)
		if err != nil {
			return out, fmt.Errorf("安装探针失败（%s）: %w", cmd, err)
		}
	}
	ver, err := m.run(ctx, client, "/usr/local/bin/qingzhou-probe -version")
	return probeInstallSummary(ver, err), nil
}

// probeInstallSummary is the install-job message after systemd already reported
// the unit active. Version is only a log nicety: old panel-hosted binaries
// (pre -version flag) exit 2 here with "flag provided but not defined", which
// used to mark a successful install as failed. Metrics reports carry
// probe_version once a newer binary is in QZ_PROBE_DIR.
func probeInstallSummary(ver string, err error) string {
	if err == nil {
		if s := strings.TrimSpace(ver); s != "" {
			return "探针安装完成：" + s
		}
		return "探针安装完成"
	}
	return "探针安装完成（二进制已启动，版本将在首次上报后显示）"
}

// ──────────────────────────────────────────────────────────────
// Utilities
// ──────────────────────────────────────────────────────────────

func netAddr(host string, port int) string {
	if port <= 0 {
		port = 22
	}
	// net.JoinHostPort brackets IPv6 literals correctly.
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// shellQuote wraps a path in single quotes for safe shell usage.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// configHash returns a hex-encoded SHA-256 of the config bytes.
func configHash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// remoteBytes preserves the existing on-disk representation: exactly one
// trailing newline. Both writes and no-op hash comparisons use these bytes.
func remoteBytes(data []byte) []byte {
	body := bytes.TrimRight(data, "\n")
	out := make([]byte, len(body)+1)
	copy(out, body)
	out[len(body)] = '\n'
	return out
}

// UpgradeSingBox reinstalls sing-box on the remote node by running the panel's
// own install script there with --force.
//
// The script is pushed over the existing SSH session rather than fetched by the
// node from a URL. Two reasons: the node does not have to be able to reach the
// panel (a landing box may well have no route back), and the bytes that run are
// the ones embedded in this binary rather than whatever an intercepted URL
// would have served.
//
// Reusing the script instead of reimplementing the download is deliberate — it
// already knows to prefer the panel's own sing-box build (the official one is
// built without with_v2ray_api, so installing it silently ends traffic
// accounting on that node), and to re-apply the tuning and unit file.
//
// Returns the script's combined output, which is what the operator needs to see
// when it does not work.
func (m *RemoteManager) UpgradeSingBox(ctx context.Context, cfg *ServerConfig, script string) (string, error) {
	client, err := m.dial(ctx, cfg)
	if err != nil {
		return "", err
	}
	defer client.Close()

	// Atomically create the script path. A guessed/pre-created /tmp symlink would
	// otherwise be executed as root below after the unprivileged upload.
	remote, err := m.createTempFile(ctx, client, ".qz-install-singbox.XXXXXXXXXX")
	if err != nil {
		return "", err
	}
	// Removed whatever happens: it is a script the panel put on someone else's
	// machine, and leaving copies of it lying around in /tmp is impolite at best.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = m.run(cleanup, client, "rm -f "+shellQuote(remote))
	}()
	if err := m.writeFile(ctx, client, remote, []byte(script)); err != nil {
		return "", fmt.Errorf("上传安装脚本失败: %w", err)
	}

	// install-singbox.sh dies on `[ "$(id -u)" = 0 ]`, so on a non-root account
	// this is the difference between reinstalling and printing "请用 root 运行".
	out, err := m.runElevated(ctx, client, cfg, "bash "+shellQuote(remote)+" --force")
	if err != nil {
		return out, fmt.Errorf("安装脚本执行失败: %w", err)
	}
	return out, nil
}
