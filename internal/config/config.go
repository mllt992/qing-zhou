package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Config holds runtime configuration. Everything has a sensible default so the
// binary runs out of the box; override via QZ_* environment variables.
type Config struct {
	ListenAddr    string
	DBPath        string
	AdminUsername string
	AdminPassword string
	// SSHKeyDir is where server rows may keep SSH private keys as files instead
	// of pasting them into the panel. Derived from the database's directory so it
	// lands inside the volume an install already persists — /data/ssh-keys under
	// Docker, <install dir>/ssh-keys for the one-click script — rather than
	// needing its own mount before the feature works at all.
	SSHKeyDir string
}

// LegacyComposeKey is recognized only to reject or migrate the public example.
const LegacyComposeKey = "please-change-me-openssl-rand-hex-32"

func ConfiguredSecretKey() (string, error) {
	key := os.Getenv("QZ_SECRET_KEY")
	if key != "" && (strings.TrimSpace(key) == "" || strings.TrimSpace(key) == LegacyComposeKey) {
		return "", errors.New("QZ_SECRET_KEY 不能使用空白或旧版公开示例值；请备份数据库并配置独立随机密钥")
	}
	return key, nil
}

func Load() *Config {
	return &Config{
		// Reachable by default. A loopback default reads as the safer choice, but
		// this is a panel whose whole job is to be opened from somewhere else: it
		// made "装好了却打不开" the standard first experience, and the fix for it
		// was always to change this value anyway. Deployments behind nginx set
		// QZ_LISTEN explicitly — the one-click installer writes it either way, and
		// on upgrade it pins the old behaviour rather than letting this default
		// widen the reach of an install that is already running.
		ListenAddr:    env("QZ_LISTEN", "0.0.0.0:8081"),
		DBPath:        env("QZ_DB", "qingzhou.db"),
		AdminUsername: env("QZ_ADMIN_USER", "mllt992"),
		// Empty means: generate a random password on first-run seed and log it.
		AdminPassword: os.Getenv("QZ_ADMIN_PASS"),
		SSHKeyDir:     env("QZ_SSH_KEY_DIR", defaultSSHKeyDir(env("QZ_DB", "qingzhou.db"))),
	}
}

func defaultSSHKeyDir(dbPath string) string {
	dir := filepath.Dir(dbPath)
	if dir == "" || dir == "." {
		return "ssh-keys"
	}
	return filepath.Join(dir, "ssh-keys")
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
