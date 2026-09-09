package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"qingzhou/internal/api"
	"qingzhou/internal/config"
	"qingzhou/internal/intervalcfg"
	"qingzhou/internal/mailer"
	"qingzhou/internal/sbctl"
	"qingzhou/internal/sbproc"
	"qingzhou/internal/sbstats"
	"qingzhou/internal/singbox"
	"qingzhou/internal/sshctl"
	"qingzhou/internal/store"
)

func main() {
	cfg := config.Load()

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer st.Close()

	if err := st.Migrate(); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	seed, err := st.Seed(cfg)
	if err != nil {
		log.Fatalf("seed: %v", err)
	}
	if seed.AdminCreated {
		if seed.AdminGenerated {
			log.Printf("seed: created admin %q with generated password: %s", seed.AdminUsername, seed.AdminPassword)
			log.Printf("seed: ^ save this and change it after first login (or set QZ_ADMIN_PASS before first run)")
		} else {
			log.Printf("seed: created admin %q (please change the password after first login)", seed.AdminUsername)
		}
	}

	secret, err := st.GetSetting("jwt_secret")
	if err != nil || secret == "" {
		log.Fatalf("jwt secret missing after seed")
	}

	// Key for encrypting secret settings (SMTP/sing-box passwords) at rest.
	// Prefer QZ_SECRET_KEY (kept outside the DB) for real protection.
	encKey := os.Getenv("QZ_SECRET_KEY")
	if encKey == "" {
		encKey = secret
		log.Printf("WARNING: QZ_SECRET_KEY not set — encrypting secret settings with jwt_secret, which lives in the same DB. A DB leak then exposes both ciphertext and key. Set QZ_SECRET_KEY (openssl rand -hex 32) for real at-rest protection.")
	}
	st.SetSecretKey([]byte(encKey))

	// Self-check: if any at-rest secret can't be decrypted with the current key
	// (typically QZ_SECRET_KEY changed after first boot), fail loudly. The config
	// builder now refuses to emit affected inbounds rather than downgrade them to
	// plaintext, so without this warning the symptom would be silent node outages.
	if n := st.CountUndecryptableSecrets(); n > 0 {
		log.Printf("WARNING: %d encrypted secret(s) (sing-box TLS/Reality key(s) and/or SMTP password) cannot be decrypted with the current key. If you recently set or changed QZ_SECRET_KEY, restore the previous value — affected TLS inbounds will refuse to deploy (never downgraded to plaintext) until the key matches.", n)
	}

	mail := buildMailer(st)
	if mail != nil {
		log.Printf("SMTP mailer enabled (%s)", mail.Host)
	} else {
		log.Printf("SMTP not configured; verification/reset links will be logged instead of emailed")
	}

	app := api.New(st, []byte(secret), mail)
	defer app.Close()
	app.SetSSHKeyDir(cfg.SSHKeyDir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Native sing-box controller (B2): always active; config/listen/unit
	// are overridable via env or DB settings.
	ctrl := buildSbController(st, app, cfg.SSHKeyDir)
	log.Printf("native sing-box enabled (controller managing config + stats)")
	// Track the controller loop so shutdown can wait for its in-flight rebuild to
	// finish before the deferred st.Close() runs — otherwise a stats/rebuild query
	// can race a closed DB ("database is closed").
	var bgWG sync.WaitGroup
	bgWG.Add(1)
	go func() {
		defer bgWG.Done()
		ctrl.Run(ctx, func() (time.Duration, time.Duration) {
			return intervalcfg.Controller(st)
		}, func(err error) { log.Printf("sing-box controller: %v", err) })
	}()

	app.StartSourceSync(ctx, time.Hour, &bgWG)
	app.StartMaintenance(ctx, time.Hour)
	app.StartMonitorTasks(ctx)
	app.StartHostedProbeSync(ctx)
	app.StartCertRenew(ctx, 12*time.Hour)
	// Tracked in bgWG for the same reason the controller is: it opens write
	// transactions, so shutdown must let an in-flight sweep finish before the
	// deferred st.Close() runs.
	app.StartQueueAdvance(ctx, 2*time.Minute, &bgWG)
	app.StartTelegram(ctx)

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      app.Router(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: api.ServerWriteTimeout,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("qingzhou listening on http://%s", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Println("shutting down...")
	cancel()
	shctx, shcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shcancel()
	_ = srv.Shutdown(shctx)

	// Drain the controller loop (bounded) before returning, so the deferred
	// st.Close() doesn't fire while a rebuild is still querying the DB.
	drained := make(chan struct{})
	go func() { bgWG.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(10 * time.Second):
		log.Println("shutdown: controller drain timed out, closing anyway")
	}
}

// buildMailer constructs the SMTP mailer from settings, with QZ_SMTP_* env
// overrides. Returns nil when no host is configured.
func buildMailer(st *store.Store) *mailer.Mailer {
	get := func(envKey, settingKey string) string {
		if v := os.Getenv(envKey); v != "" {
			return v
		}
		v, _ := st.GetSetting(settingKey)
		return v
	}
	host := get("QZ_SMTP_HOST", "smtp_host")
	if host == "" {
		return nil
	}
	port := firstNonEmpty(get("QZ_SMTP_PORT", "smtp_port"), "587")
	from := firstNonEmpty(get("QZ_SMTP_FROM", "smtp_from"), get("QZ_SMTP_USER", "smtp_user"))
	return &mailer.Mailer{
		Host:     host,
		Port:     port,
		User:     get("QZ_SMTP_USER", "smtp_user"),
		Pass:     get("QZ_SMTP_PASS", "smtp_pass"),
		From:     from,
		FromName: firstNonEmpty(get("QZ_SMTP_FROM_NAME", "smtp_from_name"), "轻舟"),
		Security: get("QZ_SMTP_SECURITY", "smtp_security"),
	}
}

// buildSbController wires the native sing-box orchestrator (B2). Always enabled.
// Config/listen/unit are overridable
// via env or settings; the base template falls back to singbox.DefaultBaseConfig.
func buildSbController(st *store.Store, app *api.API, sshKeyDir string) *sbctl.Controller {
	get := func(envKey, settingKey, def string) string {
		if v := os.Getenv(envKey); v != "" {
			return v
		}
		if v, _ := st.GetSetting(settingKey); v != "" {
			return v
		}
		return def
	}
	configPath := get("QZ_SINGBOX_CONFIG", "sb_config_path", "/etc/sing-box/config.json")
	v2rayListen := get("QZ_SINGBOX_V2RAY", "sb_v2ray_listen", "127.0.0.1:18080")
	unit := get("QZ_SINGBOX_UNIT", "sb_systemd_unit", "sing-box")
	base := get("", "sb_base_config", singbox.DefaultBaseConfig)

	// Local process manager (for this host). Auto-detect the sing-box binary
	// path: env var → common install paths → PATH lookup.
	bin := sbproc.FindSingBoxBin()
	if bin == "" {
		bin = "sing-box" // last resort: rely on PATH at exec time
	}
	mgr := sbproc.New(bin, configPath, sbproc.SystemdReload(unit))
	stats := sbstats.New(v2rayListen)

	// Remote manager for SSH-based servers. Pin each server's SSH host key on
	// first connect (TOFU) so later connections are verified against it.
	remoteMgr := sshctl.New(sshctl.WithKeyDir(sshKeyDir))
	remoteMgr.SetHostKeyPersister(func(id int64, key string) error { return st.SetServerHostKey(id, key) })

	ctrl := sbctl.New(st, mgr, stats, base, v2rayListen)
	ctrl.SetRemoteManager(remoteMgr)
	app.SetSbController(ctrl)
	// Restarts caused by the periodic pass — the ones nobody asked for — feed the
	// restart-loop watcher.
	ctrl.SetRestartObserver(app.NodeRestarted)
	ctrl.SetRestartCircuit(app.RestartCircuitPolicy, app.RestartCircuitOpen, app.NodeCircuitChanged)
	return ctrl
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
