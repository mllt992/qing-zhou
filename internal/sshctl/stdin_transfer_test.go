package sshctl

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestConfigTransferStdinPreservesBytesPermissionsAndShellLiterals(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config with quote ' .json")
	// Larger than Linux's per-argument limit, with former heredoc delimiters.
	payload := []byte(strings.Repeat("literal `echo wrong` $(echo wrong)\n__SSHCTL_EOF_8f3a__\n", 24000))
	command := configWriteCommand(file)
	if len(command) > 1024 || strings.Contains(command, "echo wrong") {
		t.Fatal("configuration entered command arguments")
	}
	cmd := exec.Command("sh", "-c", command)
	cmd.Stdin = bytes.NewReader(remoteBytes(payload))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("write failed: %v: %s", err, output)
	}
	actual, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, remoteBytes(payload)) {
		t.Fatal("stdin bytes changed")
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
}

func TestLargeApplyConfigUsesStdinAndSecondApplyIsNoop(t *testing.T) {
	node := newFakeNode()
	host, port := startApplyTestSSHServerWithInput(t, func(command, input string) (string, uint32) {
		if strings.HasPrefix(command, "umask 077; cat > ") && (len(command) > 1024 || len(input) < 1<<20) {
			t.Error("large config not transferred on stdin")
		}
		return node.handleWithInput(command, input)
	})
	cfg := ServerConfig{ID: 1, Host: host, Port: port, SSHUser: "root", SSHPassword: "fixture", ConfigPath: "/etc/sing-box/config.json", SystemdUnit: "sing-box"}
	data, err := json.Marshal(map[string]any{"log": map[string]string{"level": "info"}, "note": strings.Repeat("private-fixture-", 80000)})
	if err != nil {
		t.Fatal(err)
	}
	manager := New(WithTimeout(5 * time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		restarted, err := manager.ApplyConfig(ctx, &cfg, data)
		if err != nil {
			t.Fatal(err)
		}
		if restarted != (i == 0) {
			t.Fatalf("pass %d restart %v", i, restarted)
		}
	}
	if node.file(cfg.ConfigPath) != string(remoteBytes(data)) || node.restartCount() != 1 {
		t.Fatal("large apply lost bytes or restarted unchanged node")
	}
}

func TestPinnedHostKeyNegotiatesItsExactAlgorithm(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	cfg := ServerConfig{SSHUser: "root", SSHPassword: "fixture", HostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))}
	manager := New()
	client, err := manager.buildClientConfig(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(client.HostKeyAlgorithms) != 1 || client.HostKeyAlgorithms[0] != ssh.KeyAlgoED25519 {
		t.Fatal("pinned key type not negotiated")
	}
	_, wrong, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongSigner, _ := ssh.NewSignerFromKey(wrong)
	if client.HostKeyCallback("fixture", nil, wrongSigner.PublicKey()) == nil {
		t.Fatal("wrong host key accepted")
	}
	cfg.HostKey = "invalid fixture"
	if _, err := manager.buildClientConfig(&cfg); err == nil {
		t.Fatal("invalid pinned key accepted")
	}
}

// Exercise writeFile itself through SSH and a real shell. This fails with E2BIG
// when the payload is embedded in the exec command, independent of fake parsing.
func TestWriteFileOverSSHTransfersLargePayloadOnStdin(t *testing.T) {
	file := filepath.Join(t.TempDir(), "large config ' .json")
	data := []byte(strings.Repeat("literal $(echo unused)\n__SSHCTL_EOF_8f3a__\n", 40000))
	host, port := startApplyTestSSHServerWithInput(t, func(command, input string) (string, uint32) {
		cmd := exec.Command("sh", "-c", command)
		cmd.Stdin = strings.NewReader(input)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return "shell execution failed", 1
		}
		return string(output), 0
	})
	cfg := ServerConfig{Host: host, Port: port, SSHUser: "fixture", SSHPassword: "fixture"}
	manager := New(WithTimeout(5 * time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := manager.dial(ctx, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := manager.writeFile(ctx, client, file, data); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(written, remoteBytes(data)) {
		t.Fatal("large SSH transfer changed payload")
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("SSH transfer permissions")
	}
}
