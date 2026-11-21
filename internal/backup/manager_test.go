package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"qingzhou/internal/store"
)

type fakeObjectStore struct {
	mu          sync.Mutex
	putStarted  chan struct{}
	putContinue chan struct{}
	putErr      error
	objects     map[string][]byte
	deleted     []string
}

func (f *fakeObjectStore) PutFile(_ context.Context, key, filename, _ string) (int64, error) {
	if f.putStarted != nil {
		select {
		case f.putStarted <- struct{}{}:
		default:
		}
	}
	if f.putContinue != nil {
		<-f.putContinue
	}
	if f.putErr != nil {
		return 0, f.putErr
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.objects == nil {
		f.objects = make(map[string][]byte)
	}
	f.objects[key] = data
	return int64(len(data)), nil
}

func (f *fakeObjectStore) HeadBucket(context.Context) error { return nil }

func (f *fakeObjectStore) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
	f.deleted = append(f.deleted, key)
	return nil
}

func (f *fakeObjectStore) PresignURL(context.Context, string, time.Duration) (string, error) {
	return "https://example.test/download", nil
}

func newBackupTestManager(t *testing.T) (*Manager, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "qingzhou.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Migrate(); err != nil {
		st.Close()
		t.Fatalf("migrate store: %v", err)
	}
	st.SetSecretKey([]byte("backup-test-key"))
	return New(st), st
}

func validConfig() Config {
	return Config{
		Endpoint:        "https://account.r2.cloudflarestorage.com",
		Region:          "auto",
		Bucket:          "backups",
		AccessKeyID:     "access-key",
		SecretAccessKey: "secret-key",
		Prefix:          "qingzhou",
	}
}

func saveValidConfig(t *testing.T, manager *Manager) {
	t.Helper()
	if _, err := manager.SaveConfig(validConfig()); err != nil {
		t.Fatalf("save config: %v", err)
	}
}

func waitForRecord(t *testing.T, manager *Manager, id string) Record {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		records, err := manager.List()
		if err != nil {
			t.Fatalf("list records: %v", err)
		}
		for _, record := range records {
			if record.ID == id && record.Status != "pending" {
				return record
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("backup %s did not finish", id)
	return Record{}
}

func TestConfigIsEncryptedAndLoadConfigIsRedacted(t *testing.T) {
	manager, st := newBackupTestManager(t)
	defer st.Close()

	saved, err := manager.SaveConfig(validConfig())
	if err != nil {
		t.Fatalf("save config: %v", err)
	}
	if saved.SecretAccessKey != "" {
		t.Fatal("SaveConfig returned the secret")
	}
	loaded, configured, err := manager.LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !configured || loaded.SecretAccessKey != "" {
		t.Fatalf("loaded config = %+v, configured = %v", loaded, configured)
	}
	var raw string
	if err := st.DB().QueryRow(`SELECT value FROM settings WHERE key='backup_s3_config'`).Scan(&raw); err != nil {
		t.Fatalf("read raw config: %v", err)
	}
	if raw == "" || raw == "secret-key" || !contains(raw, "enc:v1:") {
		t.Fatalf("secret was not encrypted in raw setting: %q", raw)
	}
}

func TestR2EndpointRejectsBucketPath(t *testing.T) {
	manager, st := newBackupTestManager(t)
	defer st.Close()
	cfg := validConfig()
	cfg.Endpoint += "/" + cfg.Bucket
	if _, err := manager.SaveConfig(cfg); err == nil {
		t.Fatal("R2 Endpoint with a bucket path was accepted")
	}
}

func TestSaveScheduleValidatesCron(t *testing.T) {
	manager, st := newBackupTestManager(t)
	defer st.Close()
	if _, err := manager.SaveSchedule(context.Background(), Schedule{CronExpr: "not cron"}); err == nil {
		t.Fatal("invalid cron was accepted")
	}
	if _, err := manager.SaveSchedule(context.Background(), Schedule{CronExpr: "0 3 * * *", RetainDays: -1}); err == nil {
		t.Fatal("negative retention was accepted")
	}
}

func TestStartBackupUploadsSnapshotAndRecordsDigest(t *testing.T) {
	manager, st := newBackupTestManager(t)
	defer st.Close()
	saveValidConfig(t, manager)
	fake := &fakeObjectStore{}
	manager.factory = func(context.Context, Config) (objectStore, error) { return fake, nil }

	record, err := manager.StartBackup(context.Background(), "manual")
	if err != nil {
		t.Fatalf("start backup: %v", err)
	}
	finished := waitForRecord(t, manager, record.ID)
	if finished.Status != "completed" || finished.SizeBytes == 0 || len(finished.SHA256) != 64 {
		t.Fatalf("finished record = %+v", finished)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.objects) != 1 || fake.objects[finished.ObjectKey] == nil {
		t.Fatalf("uploaded objects = %v", fake.objects)
	}
}

func TestStartBackupBuildsRecoveryArchiveWhenManifestIsConfigured(t *testing.T) {
	manager, st := newBackupTestManager(t)
	defer st.Close()
	saveValidConfig(t, manager)
	runtimeDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(runtimeDir, "qingzhou.env")
	unitFile := filepath.Join(runtimeDir, "qingzhou.service")
	configDir := filepath.Join(runtimeDir, "sing-box")
	if err := os.Mkdir(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	for filename, content := range map[string]string{
		envFile:                                 "QZ_SECRET_KEY=kept-in-archive\n",
		unitFile:                                "[Service]\nExecStart=/opt/qingzhou/qingzhou\n",
		filepath.Join(configDir, "config.json"): "{\"inbounds\":[]}",
	} {
		if err := os.WriteFile(filename, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	specFile := filepath.Join(runtimeDir, "recovery.json")
	spec := RecoverySpec{Repository: "https://github.com/example/qingzhou", SingBoxVersion: "1.0.0", Sources: []RecoverySource{{Path: envFile}, {Path: unitFile}, {Path: configDir}, {Path: filepath.Join(runtimeDir, "missing"), Optional: true}}}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specFile, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QZ_BACKUP_MANIFEST", specFile)
	fake := &fakeObjectStore{}
	manager.factory = func(context.Context, Config) (objectStore, error) { return fake, nil }
	record, err := manager.StartBackup(context.Background(), "manual")
	if err != nil {
		t.Fatal(err)
	}
	finished := waitForRecord(t, manager, record.ID)
	if finished.Status != "completed" || finished.Format != "tar.gz" || filepath.Ext(finished.FileName) != ".gz" {
		t.Fatalf("record = %+v", finished)
	}
	fake.mu.Lock()
	archiveData := append([]byte(nil), fake.objects[finished.ObjectKey]...)
	fake.mu.Unlock()
	reader, err := gzip.NewReader(bytes.NewReader(archiveData))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	entries := map[string][]byte{}
	tarReader := tar.NewReader(reader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		payload, err := io.ReadAll(tarReader)
		if err != nil {
			t.Fatal(err)
		}
		entries[header.Name] = payload
	}
	for _, name := range []string{"database/qingzhou.db", "manifest.json", "RESTORE.md", "files" + filepath.ToSlash(envFile), "files" + filepath.ToSlash(unitFile), "files" + filepath.ToSlash(filepath.Join(configDir, "config.json"))} {
		if _, ok := entries[name]; !ok {
			t.Errorf("recovery archive missing %s", name)
		}
	}
	if !bytes.Contains(entries["manifest.json"], []byte("missing")) {
		t.Error("manifest did not record missing optional source")
	}
	var manifest recoveryManifest
	if err := json.Unmarshal(entries["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Encrypted || len(manifest.Files) < 4 || manifest.Files[0].SHA256 == "" {
		t.Fatalf("unexpected recovery manifest: %+v", manifest)
	}
	if !bytes.Contains(entries["RESTORE.md"], []byte("SQLite integrity_check")) {
		t.Error("recovery instructions do not require integrity check")
	}
}

func TestRecoveryArchiveRejectsMissingRequiredSource(t *testing.T) {
	manager, st := newBackupTestManager(t)
	defer st.Close()
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if err := st.BackupTo(snapshot); err != nil {
		t.Fatal(err)
	}
	err := manager.createRecoveryArchive(context.Background(), &RecoverySpec{Sources: []RecoverySource{{Path: filepath.Join(t.TempDir(), "missing")}}}, snapshot, filepath.Join(t.TempDir(), "recovery.tar.gz"))
	if err == nil || !os.IsNotExist(err) {
		t.Fatalf("missing required source error = %v", err)
	}
}

func TestRecoveryArchiveRejectsSymlinkSource(t *testing.T) {
	manager, st := newBackupTestManager(t)
	defer st.Close()
	runtimeDir := t.TempDir()
	snapshot := filepath.Join(runtimeDir, "snapshot.db")
	if err := st.BackupTo(snapshot); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(runtimeDir, "target.env")
	if err := os.WriteFile(target, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(runtimeDir, "linked.env")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	err := manager.createRecoveryArchive(context.Background(), &RecoverySpec{Sources: []RecoverySource{{Path: link}}}, snapshot, filepath.Join(runtimeDir, "recovery.tar.gz"))
	if err == nil {
		t.Fatal("symlink source was accepted")
	}
}

func TestStartBackupRecordsUploadFailureAndRejectsConcurrentRun(t *testing.T) {
	manager, st := newBackupTestManager(t)
	defer st.Close()
	saveValidConfig(t, manager)
	fake := &fakeObjectStore{putStarted: make(chan struct{}, 1), putContinue: make(chan struct{}), putErr: errors.New("upload down")}
	manager.factory = func(context.Context, Config) (objectStore, error) { return fake, nil }

	first, err := manager.StartBackup(context.Background(), "manual")
	if err != nil {
		t.Fatalf("start first backup: %v", err)
	}
	<-fake.putStarted
	if _, err := manager.StartBackup(context.Background(), "manual"); !errors.Is(err, ErrInProgress) {
		t.Fatalf("concurrent backup error = %v, want ErrInProgress", err)
	}
	close(fake.putContinue)
	finished := waitForRecord(t, manager, first.ID)
	if finished.Status != "failed" || finished.Error == "" {
		t.Fatalf("failed record = %+v", finished)
	}
}

func TestRetentionDeletesOldObjects(t *testing.T) {
	manager, st := newBackupTestManager(t)
	defer st.Close()
	saveValidConfig(t, manager)
	if _, err := manager.SaveSchedule(context.Background(), Schedule{CronExpr: defaultCron, RetainCount: 1}); err != nil {
		t.Fatalf("save schedule: %v", err)
	}
	fake := &fakeObjectStore{}
	manager.factory = func(context.Context, Config) (objectStore, error) { return fake, nil }
	for i := 0; i < 2; i++ {
		record, err := manager.StartBackup(context.Background(), "manual")
		if err != nil {
			t.Fatalf("start backup %d: %v", i, err)
		}
		waitForRecord(t, manager, record.ID)
		if i == 0 {
			time.Sleep(time.Second)
		}
	}
	// Retention cleanup runs after the record is already saved as completed,
	// so waitForRecord can return before it; poll instead of racing it.
	var deleted []string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		fake.mu.Lock()
		deleted = append([]string(nil), fake.deleted...)
		fake.mu.Unlock()
		if len(deleted) >= 1 {
			break
		}
	}
	time.Sleep(50 * time.Millisecond) // a second, wrong deletion would land here
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.deleted) != 1 {
		t.Fatalf("deleted keys = %v, want one", fake.deleted)
	}
}

func contains(value, needle string) bool {
	for i := 0; i+len(needle) <= len(value); i++ {
		if value[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
