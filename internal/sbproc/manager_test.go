package sbproc

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func newTestManager(t *testing.T, reload func() error) (*Manager, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	m := &Manager{configPath: path, reload: reload}
	m.check = func(string) error { return nil } // no sing-box binary in tests
	return m, path
}

// The config file is swapped BEFORE the reload runs, and the no-op fast path
// compares against that file. So a failed reload used to be unrecoverable: the
// next tick saw the desired bytes already on disk and returned success without
// retrying, leaving sing-box down while Rebuild reported healthy every minute.
// Only an unrelated user/inbound edit could break the loop.
func TestApply_RetriesAfterFailedReload(t *testing.T) {
	var reloads int
	fail := true
	m, path := newTestManager(t, func() error {
		reloads++
		if fail {
			return errors.New("systemctl restart failed")
		}
		return nil
	})

	cfg := []byte(`{"a":1}`)
	if err := m.Apply(cfg); err == nil {
		t.Fatal("Apply should surface the reload failure")
	}
	if reloads != 1 {
		t.Fatalf("reloads = %d, want 1", reloads)
	}
	// The config did land on disk — that is exactly what used to poison the
	// fast path.
	if b, _ := os.ReadFile(path); string(b) != string(cfg) {
		t.Fatalf("config on disk = %q, want %q", b, cfg)
	}

	// Same bytes again: must retry the reload rather than short-circuit.
	if err := m.Apply(cfg); err == nil {
		t.Fatal("second Apply should still surface the failure")
	}
	if reloads != 2 {
		t.Errorf("reloads = %d after retry, want 2 — the failure was short-circuited away", reloads)
	}

	// Once the reload succeeds, the no-op fast path comes back.
	fail = false
	if err := m.Apply(cfg); err != nil {
		t.Fatalf("third Apply: %v", err)
	}
	if reloads != 3 {
		t.Fatalf("reloads = %d, want 3", reloads)
	}
	if err := m.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	if reloads != 3 {
		t.Errorf("reloads = %d — unchanged config should not restart sing-box again", reloads)
	}
}

// The fast path is the reason sing-box isn't restarted every tick; it must stay
// intact on the healthy path.
func TestApply_NoOpOnUnchangedConfig(t *testing.T) {
	var reloads int
	m, _ := newTestManager(t, func() error { reloads++; return nil })

	cfg := []byte(`{"a":1}`)
	for i := 0; i < 3; i++ {
		if err := m.Apply(cfg); err != nil {
			t.Fatal(err)
		}
	}
	if reloads != 1 {
		t.Errorf("reloads = %d, want 1 — identical config restarted sing-box repeatedly", reloads)
	}
}

// A changed config must always reload, and an invalid one must never reach the
// live path.
func TestApply_ChangedConfigReloads_InvalidNeverLands(t *testing.T) {
	var reloads int
	m, path := newTestManager(t, func() error { reloads++; return nil })

	if err := m.Apply([]byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := m.Apply([]byte(`{"a":2}`)); err != nil {
		t.Fatal(err)
	}
	if reloads != 2 {
		t.Errorf("reloads = %d, want 2", reloads)
	}

	m.check = func(string) error { return errors.New("bad config") }
	if err := m.Apply([]byte(`{"a":3}`)); err == nil {
		t.Fatal("invalid config should be rejected")
	}
	if b, _ := os.ReadFile(path); string(b) != `{"a":2}` {
		t.Errorf("live config = %q, want the last good one — an invalid config was installed", b)
	}
	if reloads != 2 {
		t.Errorf("reloads = %d — a rejected config must not reload", reloads)
	}
}

// An EROFS on the config directory is the panel's own systemd sandbox, not a
// broken disk — the operator's next move is a ReadWritePaths drop-in, and
// nothing in the raw error says so. The wrapped error must keep the original
// (callers and errors.Is still see it) and name the directory that is blocked.
func TestSandboxHint_ExplainsEROFSAndKeepsCause(t *testing.T) {
	cause := &os.PathError{Op: "open", Path: "/etc/sing-box/.qz-sbcfg-1.tmp", Err: syscall.EROFS}
	got := sandboxHint("/etc/sing-box", cause)
	if !errors.Is(got, syscall.EROFS) {
		t.Fatalf("wrapped error lost the EROFS cause: %v", got)
	}
	for _, want := range []string{"ProtectSystem", "ReadWritePaths=-/etc/sing-box"} {
		if !strings.Contains(got.Error(), want) {
			t.Errorf("hint does not mention %q:\n%s", want, got)
		}
	}
}

// Every other write failure already explains itself. A full disk or a missing
// directory blamed on "沙箱" is the same misdirection in the other direction.
func TestSandboxHint_LeavesOtherErrorsAlone(t *testing.T) {
	cause := &os.PathError{Op: "open", Path: "/etc/sing-box/x", Err: syscall.ENOSPC}
	if got := sandboxHint("/etc/sing-box", cause); got != error(cause) {
		t.Errorf("non-EROFS error was rewritten: %v", got)
	}
}

// The config path comes from a setting an operator can edit, so it must reach
// the transient unit as a positional argument — never spliced into the shell
// script, where a quote in the path would become syntax. The flags must stay the
// ones that predate systemd 236 (same set localInstallArgv settled on).
func TestSystemdRunArgv_PathIsArgumentNotScript(t *testing.T) {
	path := `/etc/sing-box/o'ddly named.json`
	argv := systemdRunArgv("/usr/bin/systemd-run", path, 42)

	if argv[len(argv)-2] != path {
		t.Errorf("config path is not the $1 argument: %q", argv)
	}
	if argv[len(argv)-1] != "42" {
		t.Errorf("config length is not the $2 argument: %q", argv)
	}
	for _, a := range argv {
		if strings.Contains(a, "cat >") && strings.Contains(a, path) {
			t.Errorf("path was interpolated into the shell script: %q", a)
		}
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"--pipe", "--wait", "--collect"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s in %q", want, joined)
		}
	}
	if strings.Contains(joined, "--service-type=exec") {
		t.Error("--service-type=exec needs systemd 240; the other flags are the 236 set")
	}
	// A fixed unit name would collide: one Rebuild escalates once for the panel's
	// own config and again per local `servers` row, and systemd rejects a name it
	// has not finished collecting. Let it generate one.
	if strings.Contains(joined, "--unit=") {
		t.Errorf("fixed unit name invites 'unit already exists' between back-to-back writes: %q", joined)
	}
}

// The sandbox escape is the whole point on 面板本机 — a refactor that leaves
// escalate nil would silently put every such panel back to "重建配置失败" with
// no way out but a shell.
func TestNew_WiresTheSandboxEscape(t *testing.T) {
	if New("/usr/local/bin/sing-box", "/etc/sing-box/config.json", nil).escalate == nil {
		t.Fatal("New left escalate nil: the config write can no longer escape ProtectSystem")
	}
}

// erofs is the failure that means "this process's mount namespace", the only one
// worth escalating over.
func erofs(path string) error {
	return &os.PathError{Op: "open", Path: path, Err: syscall.EROFS}
}

// On a normal host nothing escalates: spawning a transient unit per config write
// would be a needless dependency on systemd-run for every panel out there.
func TestWriteConfig_NoEscalationWhenTheDirectWriteWorks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	escalated := false
	err := writeConfig(path, []byte(`{"a":1}`), writeDirect,
		func(string, []byte) error { escalated = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if escalated {
		t.Error("escalated despite the direct write succeeding")
	}
	if b, _ := os.ReadFile(path); string(b) != `{"a":1}` {
		t.Errorf("config on disk = %q", b)
	}
}

// Only EROFS escalates. A full disk or a missing directory is a real problem
// with the machine, and quietly re-running it through systemd-run would just
// fail again with a stranger message.
func TestWriteConfig_OnlyEROFSEscalates(t *testing.T) {
	escalated := false
	err := writeConfig("/nowhere/config.json", []byte(`{}`),
		func(string, []byte) error { return &os.PathError{Op: "open", Err: syscall.ENOSPC} },
		func(string, []byte) error { escalated = true; return nil })
	if escalated {
		t.Error("escalated on a non-EROFS failure")
	}
	if !errors.Is(err, syscall.ENOSPC) {
		t.Errorf("err = %v, want the original ENOSPC", err)
	}
}

// The escalated write hands bytes to another process down a pipe, where a
// truncated transfer ends in a clean EOF and installs a short config that only
// surfaces at the next sing-box restart. Whatever the escape reports, what
// actually landed is what counts.
func TestWriteConfig_EscalatedWriteIsVerified(t *testing.T) {
	dir := t.TempDir()
	cfg := []byte(`{"inbounds":[1,2,3]}`)

	full := filepath.Join(dir, "full.json")
	if err := writeConfig(full, cfg,
		func(p string, _ []byte) error { return erofs(p) },
		func(p string, c []byte) error { return os.WriteFile(p, c, 0o600) }); err != nil {
		t.Fatalf("a complete escalated write should succeed: %v", err)
	}

	short := filepath.Join(dir, "short.json")
	err := writeConfig(short, cfg,
		func(p string, _ []byte) error { return erofs(p) },
		func(p string, c []byte) error { return os.WriteFile(p, c[:5], 0o600) })
	if err == nil {
		t.Fatal("a truncated escalated write was accepted — sing-box would fail to start on it later")
	}

	// And when the escape itself is unavailable, the operator still gets the
	// sandbox explanation rather than a bare EROFS.
	err = writeConfig(filepath.Join(dir, "x.json"), cfg,
		func(p string, _ []byte) error { return erofs(p) }, nil)
	if !strings.Contains(err.Error(), "ProtectSystem") {
		t.Errorf("no escape and no explanation either: %v", err)
	}
}
