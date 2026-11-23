// Package sbproc manages the external sing-box process (B2 integration model):
// it writes the generated config, validates it with `sing-box check`, and only
// then atomically swaps the live config and reloads sing-box. An invalid config
// is NEVER written to the live path or reloaded — a generation bug must not take
// every user offline.
package sbproc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Manager owns the live sing-box config file and the reload mechanism.
type Manager struct {
	bin        string // sing-box binary path (for the default checker)
	configPath string // live config.json path
	check      func(path string) error
	reload     func() error
	// escalate writes the config from outside this process's mount namespace,
	// for the one case where the panel's own sandbox owns the config directory.
	// nil disables the escape (tests, and any caller that wants the raw error).
	escalate func(configPath string, config []byte) error
	mu       sync.Mutex

	// reloadFailed records that the last reload attempt errored. The config file
	// is swapped BEFORE the reload runs, so without this the no-op fast path
	// (which compares against the file on disk) would see the desired bytes
	// already in place on the next tick and return success forever, leaving
	// sing-box down until something happened to change the config. Recovery
	// otherwise required an unrelated user/inbound edit.
	reloadFailed bool
}

// New builds a Manager. reload is how sing-box is (re)started after a config
// swap — e.g. `systemctl restart sing-box`. If reload is nil, Apply only writes
// the validated config (useful for dry runs / tests).
func New(bin, configPath string, reload func() error) *Manager {
	m := &Manager{bin: bin, configPath: configPath, reload: reload}
	m.check = m.defaultCheck
	m.escalate = systemdRunSwap
	return m
}

// defaultCheck runs `sing-box check -c <path>` and returns the tool's output on
// failure so the operator sees exactly what's wrong.
func (m *Manager) defaultCheck(path string) error {
	if m.bin == "" {
		return fmt.Errorf("sing-box binary path not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, m.bin, "check", "-c", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("sing-box check failed: %v: %s", err, out)
	}
	return nil
}

// sandboxHint names the one cause behind an EROFS on the config directory that
// the error itself points away from: the directory is read-only *for this process
// only*. The panel's own unit (install.sh, deploy/qingzhou.service) sets
// ProtectSystem=full, which remounts /usr AND /etc read-only inside the service's
// mount namespace — so `touch /etc/sing-box/x` from a root shell on the very same
// machine succeeds, and the operator is left staring at a writable disk reporting
// read-only. Same trap as the install path's localInstallHint (nodever_admin.go),
// one directory over: that one is /usr for the binary, this is /etc for the config.
//
// Silent on every other error: a genuinely full disk or a missing directory says
// so already, and a wrong "沙箱" on top of that is the misdirection this exists to end.
func sandboxHint(dir string, err error) error {
	if !errors.Is(err, syscall.EROFS) {
		return err
	}
	return fmt.Errorf("%w —— %s 对面板进程只读，宿主机上能写不代表服务里能写："+
		"面板 systemd 单元的 ProtectSystem 把它挡在了外面。放行后重启面板："+
		"mkdir -p /etc/systemd/system/qingzhou.service.d && printf '[Service]\\nReadWritePaths=-%s\\n' "+
		"> /etc/systemd/system/qingzhou.service.d/10-singbox-rw.conf && systemctl daemon-reload && systemctl restart qingzhou",
		err, dir, dir)
}

// swap replaces the live config with config, atomically, and escapes the panel's
// own sandbox if that is what stands in the way.
//
// 一键安装 puts sing-box on the panel's own machine, and from that moment the
// panel has to write /etc/sing-box/config.json. But the panel's unit sets
// ProtectSystem=full, which mounts /etc read-only inside the service's mount
// namespace — so every 下发 fails with EROFS on a machine where the same write
// from a root shell succeeds. This is the second half of the problem 0ead49e
// fixed for the binary: that one escaped /usr to *install* sing-box, this one
// escapes /etc to *configure* it. Leaving it to the operator means editing
// systemd by hand on a box they may have no shell on, for a machine the panel
// itself set up.
func (m *Manager) swap(config []byte) error {
	return writeConfig(m.configPath, config, writeDirect, m.escalate)
}

// WriteConfig atomically installs config at path, escaping the panel's own mount
// namespace if that directory is read-only for this process.
//
// Exported for sbctl.applyLocal, which writes the same file for a `servers` row
// that happens to be this machine — a second copy of this write that would hit
// the identical wall.
func WriteConfig(path string, config []byte) error {
	return writeConfig(path, config, writeDirect, systemdRunSwap)
}

// writeConfig takes both writers as parameters because the interesting behaviour
// — escalate on EROFS and nothing else, then prove what landed — cannot be
// reached from a test otherwise: no portable way to conjure a read-only mount.
// escalate nil disables the escape and surfaces the raw (hinted) error.
func writeConfig(path string, config []byte, direct, escalate func(string, []byte) error) error {
	err := direct(path, config)
	if err == nil || !errors.Is(err, syscall.EROFS) {
		return err // unsandboxed hosts never leave this line
	}
	dir := filepath.Dir(path)
	if escalate == nil {
		return sandboxHint(dir, err)
	}
	if e := escalate(path, config); e != nil {
		return fmt.Errorf("%w（已试过用 systemd-run 绕开沙箱，也失败了：%v）", sandboxHint(dir, err), e)
	}
	// Guard against an escalator that exits 0 without landing the expected bytes
	// (mid-transfer truncation when this panel dies is caught inside the helper
	// script itself via its byte-count check before mv). Reading is never what
	// the read-only mount forbids, so verifying the landed file is cheap and exact.
	got, e := os.ReadFile(path)
	if e != nil {
		return fmt.Errorf("绕过沙箱写入后读回失败，无法确认 %s 的内容: %w", path, e)
	}
	if !bytes.Equal(got, config) {
		return fmt.Errorf("绕过沙箱写入后内容不符：%s 落盘 %d 字节，应为 %d 字节（写到一半被打断），"+
			"sing-box 下次重启会起不来，请检查该文件", path, len(got), len(config))
	}
	return nil
}

// writeDirect is the plain path: write a sibling temp file, then rename over the
// live config. Same directory, so the rename is atomic — sing-box never sees a
// half-written file.
func writeDirect(path string, config []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".qz-sbcfg-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(config); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// systemdRunArgv is how the write gets out of the sandbox: systemd-run asks PID1
// for a transient unit, which is built from PID1's namespace and so sees /etc
// writable again. Flags match localInstallArgv (nodever_admin.go) — --pipe to
// feed the config in on stdin, --wait to block and propagate status, --collect
// to reap the unit even when it fails, all of them older than systemd 236.
//
// The config travels on stdin and the path as a positional argument, never
// interpolated into the shell script: the path comes from a setting an operator
// can edit, and a quote in it must not become shell syntax.
//
// Deliberately NOT named with --unit, unlike localInstallArgv. A fixed name is
// right there, where it stops a second 重装 piling onto a running one; here it
// would only invite collisions — one Rebuild escalates once for the panel's own
// config and again for every `servers` row on this machine, and systemd refuses
// a name it has not finished collecting yet. These writes are milliseconds long
// and already serialized by their callers, so there is nothing to pile up.
func systemdRunArgv(systemdRun, configPath string, configLen int) []string {
	// Same atomic swap as writeDirect, expressed for sh: sibling temp, byte-count
	// check, then mv. umask matches os.CreateTemp's 0600 so the escalated path
	// doesn't quietly widen the permissions on a file holding every user's
	// credentials. Checking byte count inside the transient unit stops a
	// mid-write kill of the panel (which closes the pipe with a clean EOF) from
	// installing a truncated config. The trap clears the temp on any failure.
	const script = `set -e; umask 077; trap 'rm -f "$1.qz-tmp"' EXIT; cat > "$1.qz-tmp"; [ $(wc -c < "$1.qz-tmp") -eq "$2" ]; mv "$1.qz-tmp" "$1"`
	return []string{
		systemdRun, "--pipe", "--wait", "--collect", "--quiet",
		"-p", "RuntimeMaxSec=15", "--",
		"/bin/sh", "-c", script, "sh", configPath, strconv.Itoa(configLen),
	}
}

// systemdRunSwap runs systemdRunArgv, feeding the config in on stdin.
//
// The timeout matters more than it looks: 「重建配置」 runs Rebuild inline in its
// HTTP handler, and the server's WriteTimeout is 30s — a minute spent waiting on
// a wedged systemd-run would surface to the operator as a dead request rather
// than an error. Writing a few KB through a transient unit is a sub-second job,
// so 15s is already far past generous, and RuntimeMaxSec caps the unit itself in
// case our own kill leaves it behind.
func systemdRunSwap(configPath string, config []byte) error {
	bin, err := exec.LookPath("systemd-run")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	argv := systemdRunArgv(bin, configPath, len(config))
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = bytes.NewReader(config)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Validate writes config to a temp file and runs the checker against it without
// touching the live config.
func (m *Manager) Validate(config []byte) error {
	tmp, err := os.CreateTemp("", "qz-sbcheck-*.json")
	if err != nil {
		return err
	}
	path := tmp.Name()
	defer os.Remove(path)
	if _, err := tmp.Write(config); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return m.check(path)
}

// Apply validates the config, then (only on success) atomically replaces the
// live config file and reloads sing-box. Serialized so concurrent applies can't
// interleave a half-written file with a reload.
// ConfigPath is the file Apply installs to. The controller reads it to catch a
// server row that points at the very same file (see sbctl.panelPathConflict).
func (m *Manager) ConfigPath() string { return m.configPath }

// Apply installs config and reloads sing-box, unless the file already holds
// exactly these bytes and the last reload succeeded.
func (m *Manager) Apply(config []byte) error {
	_, err := m.ApplyChanged(config)
	return err
}

// ApplyChanged is Apply, reporting whether sing-box was actually reloaded —
// which is to say whether every connection on this machine was just cut. The
// controller counts those to notice a node stuck in a restart loop.
func (m *Manager) ApplyChanged(config []byte) (bool, error) {
	return m.ApplyChangedForce(config, false)
}

// ApplyChangedForce establishes a controlled restart boundary for a legacy
// numeric statistics namespace even when a crashed prior apply wrote the file.
func (m *Manager) ApplyChangedForce(config []byte, force bool) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// No-op when the config is byte-identical to what's already live. The
	// controller calls Apply on a timer; without this it would `sing-box check`
	// and restart sing-box every tick, dropping every user's connection each
	// minute. Generated config is deterministic so equal input → equal bytes.
	//
	// Skipped while a reload is outstanding: matching bytes on disk only means
	// the file was swapped, not that sing-box picked it up.
	if !m.reloadFailed && !force {
		if cur, err := os.ReadFile(m.configPath); err == nil && bytes.Equal(cur, config) {
			return false, nil
		}
	}

	if err := m.Validate(config); err != nil {
		return false, err // invalid: live config untouched, no reload
	}

	if err := m.swap(config); err != nil {
		return false, err
	}

	// The reload below cuts every connection on this machine. It only happens
	// when the config genuinely changed, and saying so is what makes an
	// unexpected reload every minute visible instead of invisible.
	log.Printf("sbproc: 本机 sing-box 配置有变化，已写入 %s 并重载（本机连接会断一次）", m.configPath)

	if m.reload != nil {
		if err := m.reload(); err != nil {
			// Leave the flag set so the next Apply retries the reload instead of
			// short-circuiting on the already-swapped file.
			m.reloadFailed = true
			return true, err
		}
	}
	m.reloadFailed = false
	return true, nil
}

// FindSingBoxBin auto-detects the sing-box binary path. It checks the
// QZ_SINGBOX_BIN env var first, then probes common installation paths, and
// finally falls back to `exec.LookPath("sing-box")`. Returns "" if not found.
func FindSingBoxBin() string {
	if v := os.Getenv("QZ_SINGBOX_BIN"); v != "" {
		if _, err := os.Stat(v); err == nil {
			return v
		}
	}
	for _, p := range []string{
		"/opt/qingzhou/sing-box",
		"/usr/local/bin/sing-box",
		"/usr/bin/sing-box",
	} {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	if p, err := exec.LookPath("sing-box"); err == nil {
		return p
	}
	return ""
}

// SystemdReload returns a reload func that restarts a systemd unit.
func SystemdReload(unit string) func() error {
	return func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "systemctl", "restart", unit).CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl restart %s: %v: %s", unit, err, out)
		}
		return nil
	}
}

// Version runs `sing-box version` on the local binary.
//
// The panel manages a sing-box on its own machine as well as on the remote
// nodes, but only the remote ones get probed over SSH — so without this the one
// node an operator is most likely to have installed by hand is the one the
// panel can say the least about.
//
// Returns the raw output; interpreting it is sbver's job.
func (m *Manager) Version(ctx context.Context) (string, error) {
	if m.bin == "" {
		return "", errors.New("未配置 sing-box 可执行文件路径")
	}
	out, err := exec.CommandContext(ctx, m.bin, "version").CombinedOutput()
	if err != nil {
		// The output carries the real reason (missing file, permission denied),
		// which is far more useful than "exit status 1".
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
