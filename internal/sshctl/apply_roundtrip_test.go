package sshctl

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestApplyConfigDoesNotRestartANodeThatAlreadyHasTheConfig is the regression
// test for a once-a-minute disconnect on every SSH-managed node.
//
// ApplyConfig decides "nothing changed here" by hashing the file on the node and
// comparing it with the config it is about to send. The two sides have to agree
// on what a write leaves behind: the config comes out of json.MarshalIndent
// without a trailing newline, while the heredoc that transfers it terminates its
// body with one. Hashing the untransformed config compares N bytes with the N+1
// on disk, so the node never looks up to date — and the periodic sync pass
// rewrites an identical config and restarts sing-box every interval, cutting
// every live connection.
//
// Which is why the fake node here stores what it is actually sent and hashes
// that, instead of being told in advance which hash to report. A test that
// answers with configHash(configJSON) passes with the bug in place.
func TestApplyConfigDoesNotRestartANodeThatAlreadyHasTheConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  ServerConfig
	}{
		{
			name: "root",
			cfg: ServerConfig{
				ID: 1, SSHUser: "root", SSHPassword: "fixture",
				ConfigPath: "/etc/sing-box/config.json", SystemdUnit: "sing-box",
			},
		},
		{
			name: "sudo",
			cfg: ServerConfig{
				ID: 2, SSHUser: "ubuntu", SSHPassword: "fixture",
				UseSudo: true, SudoPassword: "fixture",
				ConfigPath: "/etc/sing-box/config.json", SystemdUnit: "sing-box",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Marshalled the way the real config is built, so the trailing-newline
			// mismatch this test exists for is present in the input.
			configJSON, err := json.MarshalIndent(map[string]any{
				"log":      map[string]any{"level": "info"},
				"inbounds": []any{map[string]any{"type": "vless", "tag": "in-1"}},
			}, "", "  ")
			if err != nil {
				t.Fatal(err)
			}

			node := newFakeNode()
			host, port := startApplyTestSSHServerWithInput(t, node.handleWithInput)
			cfg := tc.cfg
			cfg.Host, cfg.Port = host, port

			m := New(WithTimeout(2 * time.Second))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			// First pass: the node has nothing, so it gets the config and a restart.
			if restarted, err := m.ApplyConfig(ctx, &cfg, configJSON); err != nil {
				t.Fatalf("first apply: %v", err)
			} else if !restarted {
				t.Fatal("installing a config on a bare node was not reported as a restart")
			}
			if got := node.restartCount(); got != 1 {
				t.Fatalf("first apply restarted %d time(s), want 1", got)
			}
			if got, want := node.file(cfg.ConfigPath), string(remoteBytes(configJSON)); got != want {
				t.Fatalf("installed config = %q, want %q", got, want)
			}

			// Second pass with the identical config: the node is already serving
			// these bytes and its unit is up, so it must not be touched at all.
			if restarted, err := m.ApplyConfig(ctx, &cfg, configJSON); err != nil {
				t.Fatalf("second apply: %v", err)
			} else if restarted {
				t.Fatal("an unchanged config was reported as a restart")
			}
			if got := node.restartCount(); got != 1 {
				t.Fatalf("unchanged config restarted the node %d time(s), want 1", got)
			}

			// And a real change still lands.
			changed := append([]byte(nil), configJSON...)
			changed = append(changed, ' ')
			if _, err := m.ApplyConfig(ctx, &cfg, changed); err != nil {
				t.Fatalf("apply after change: %v", err)
			}
			if got := node.restartCount(); got != 2 {
				t.Fatalf("changed config restarted the node %d time(s), want 2", got)
			}
		})
	}
}

// fakeNode is a minimal stand-in for a managed node: a filesystem, a unit, and
// enough shell literacy to run the handful of commands ApplyConfig sends.
type fakeNode struct {
	mu       sync.Mutex
	files    map[string]string
	active   bool
	restarts int
	tempSeq  int
}

func newFakeNode() *fakeNode {
	return &fakeNode{files: map[string]string{}}
}

func (n *fakeNode) restartCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.restarts
}

func (n *fakeNode) file(path string) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.files[path]
}

func (n *fakeNode) handle(command string) (string, uint32) {
	return n.handleWithInput(command, "")
}

func (n *fakeNode) handleWithInput(command, input string) (string, uint32) {
	n.mu.Lock()
	defer n.mu.Unlock()

	// A sudo row wraps every privileged command; the node runs what is inside.
	command = strings.TrimPrefix(command, "sudo -S -p '' -- ")
	command = strings.TrimPrefix(command, "sudo -n -- ")

	switch {
	case strings.HasPrefix(command, "umask 077; cat > "):
		file, rest, ok := cutQuoted(strings.TrimPrefix(command, "umask 077; cat > "))
		if !ok || rest != " && chmod 600 "+shellQuote(file) {
			return "invalid stdin write command", 1
		}
		n.files[file] = input
		return "", 0

	case strings.HasPrefix(command, "umask 077; mktemp "):
		n.tempSeq++
		// Must satisfy validTempFilePath: /tmp, the template's prefix, and exactly
		// as many characters as the template had X's.
		return fmt.Sprintf("/tmp/.qz-cfg.t%09d\n", n.tempSeq), 0

	case strings.HasPrefix(command, "mkdir -p "):
		return "", 0

	case strings.HasPrefix(command, "for c in "):
		return "/usr/local/bin/sing-box\n", 0

	case strings.Contains(command, "sha256sum "):
		// sha256sum PATH | cut -d' ' -f1; systemctl is-active UNIT
		args := quotedArgs(command)
		if len(args) < 3 {
			return "", 1
		}
		out := ""
		if content, ok := n.files[args[0]]; ok {
			out += configHash([]byte(content)) + "\n"
		}
		if n.active {
			out += "active\n"
		} else {
			out += "inactive\n"
		}
		return out, 0

	case strings.Contains(command, " check -c "):
		args := quotedArgs(command)
		if len(args) < 2 {
			return "", 1
		}
		content, ok := n.files[args[1]]
		if !ok {
			return "config not found", 1
		}
		if !json.Valid([]byte(content)) {
			return "invalid config", 1
		}
		return "", 0

	case strings.HasPrefix(command, "install -m600 "):
		args := quotedArgs(command)
		if len(args) < 2 {
			return "", 1
		}
		content, ok := n.files[args[0]]
		if !ok {
			return "no such file", 1
		}
		n.files[args[1]] = content
		return "", 0

	case strings.HasPrefix(command, "mv -f "):
		args := quotedArgs(command)
		if len(args) < 2 {
			return "", 1
		}
		content, ok := n.files[args[0]]
		if !ok {
			return "no such file", 1
		}
		delete(n.files, args[0])
		n.files[args[1]] = content
		return "", 0

	case strings.HasPrefix(command, "rm -f "):
		for _, arg := range quotedArgs(command) {
			delete(n.files, arg)
		}
		return "", 0

	case strings.HasPrefix(command, "systemctl restart "):
		n.restarts++
		n.active = true
		return "", 0

	default:
		return "", 0
	}
}

// cutQuoted reads one shellQuote'd argument off the front of s.
func cutQuoted(s string) (string, string, bool) {
	if !strings.HasPrefix(s, "'") {
		return "", s, false
	}
	end := strings.Index(s[1:], "'")
	if end < 0 {
		return "", s, false
	}
	return s[1 : 1+end], s[end+2:], true
}

// quotedArgs returns every single-quoted argument in a command, in order.
func quotedArgs(command string) []string {
	var out []string
	for {
		start := strings.Index(command, "'")
		if start < 0 {
			return out
		}
		rest := command[start+1:]
		end := strings.Index(rest, "'")
		if end < 0 {
			return out
		}
		out = append(out, rest[:end])
		command = rest[end+1:]
	}
}
