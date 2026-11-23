//go:build unix

package sbproc

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Execute the exact /bin/sh helper from systemdRunArgv against a real POSIX
// shell to verify quoting, umask, byte-count truncation guard, trap cleanup,
// and non-zero exit propagation.
func TestSystemdRunArgv_ShellHelperSemantics(t *testing.T) {
	shCmd := func(t *testing.T, path string, wantLen int, stdin []byte) ([]byte, error) {
		t.Helper()
		argv := systemdRunArgv("/usr/bin/systemd-run", path, wantLen)
		idx := -1
		for i, a := range argv {
			if a == "/bin/sh" {
				idx = i
				break
			}
		}
		if idx < 0 {
			t.Fatalf("missing /bin/sh in argv: %q", argv)
		}
		cmd := exec.Command(argv[idx], argv[idx+1:]...)
		cmd.Stdin = bytes.NewReader(stdin)
		return cmd.CombinedOutput()
	}

	t.Run("spaces_and_quotes_atomic_0600", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, `o'ddly named config.json`)
		cfg := []byte(`{"log":{"level":"info"}}` + "\n")

		if out, err := shCmd(t, target, len(cfg), cfg); err != nil {
			t.Fatalf("helper failed: %v (%s)", err, out)
		}
		got, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, cfg) {
			t.Fatalf("got %q, want %q", got, cfg)
		}
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("mode = %o, want 0600", perm)
		}
		if _, err := os.Stat(target + ".qz-tmp"); !os.IsNotExist(err) {
			t.Errorf("temp file left behind: err=%v", err)
		}
	})

	t.Run("truncated_stdin_rejected_and_cleaned_up", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "config.json")
		prev := []byte(`{"good":true}`)
		if err := os.WriteFile(target, prev, 0o600); err != nil {
			t.Fatal(err)
		}

		// Simulate panel killed mid-pipe: helper expects 50 bytes, gets 10.
		if _, err := shCmd(t, target, 50, []byte(`{"short":1`)); err == nil {
			t.Fatal("truncated write should exit non-zero")
		}
		got, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, prev) {
			t.Fatalf("live config overwritten on truncated input: got %q, want %q", got, prev)
		}
		if _, err := os.Stat(target + ".qz-tmp"); !os.IsNotExist(err) {
			t.Errorf("trap did not clean up temp file after truncation failure: err=%v", err)
		}
	})
}
