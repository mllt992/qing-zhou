package store

import (
	"os"
	"path/filepath"
	"testing"
)

// The snapshot must contain data that is still only in the WAL — that is the
// whole reason this exists rather than copying the file.
func TestBackupCapturesUncheckpointedWrites(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	st, err := Open(src)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := st.SetSetting("canary", "written-into-wal"); err != nil {
		t.Fatalf("write: %v", err)
	}

	dst := filepath.Join(dir, "snap", "backup.db")
	if err := st.BackupTo(dst); err != nil {
		t.Fatalf("backup: %v", err)
	}

	// A real backup is one file. Sidecars would mean the caller has to ship
	// three files and get their consistency right themselves.
	for _, side := range []string{dst + "-wal", dst + "-shm"} {
		if _, err := os.Stat(side); err == nil {
			t.Errorf("snapshot left a sidecar behind: %s", side)
		}
	}

	restored, err := Open(dst)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer restored.Close()
	got, err := restored.GetSetting("canary")
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if got != "written-into-wal" {
		t.Errorf("canary = %q, want %q — the snapshot missed committed data", got, "written-into-wal")
	}
}

// Writes after the snapshot must not appear in it, and must not corrupt it.
func TestBackupIsAPointInTime(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "src.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	_ = st.SetSetting("k", "before")

	dst := filepath.Join(dir, "backup.db")
	if err := st.BackupTo(dst); err != nil {
		t.Fatalf("backup: %v", err)
	}
	_ = st.SetSetting("k", "after")

	restored, err := Open(dst)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer restored.Close()
	if got, _ := restored.GetSetting("k"); got != "before" {
		t.Errorf("snapshot value = %q, want %q", got, "before")
	}
	if got, _ := st.GetSetting("k"); got != "after" {
		t.Errorf("live db value = %q, want %q — backup disturbed the source", got, "after")
	}
}

func TestBackupRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "src.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	dst := filepath.Join(dir, "taken.db")
	if err := os.WriteFile(dst, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.BackupTo(dst); err == nil {
		t.Fatal("overwrote an existing file")
	}
	if b, _ := os.ReadFile(dst); string(b) != "precious" {
		t.Error("existing file was clobbered anyway")
	}
	if err := st.BackupTo(""); err == nil {
		t.Error("empty destination accepted")
	}
}

func TestBackupPrivatePermissionsAndNoTemporaryFiles(t *testing.T) {
	st := openMigrated(t)
	dir := t.TempDir()
	dst := filepath.Join(dir, "a 'quoted snapshot.db")
	if err := st.BackupTo(dst); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("staging left behind: %v %v", entries, err)
	}
}

func TestBackupFailureLeavesNoPartialSnapshot(t *testing.T) {
	st := openMigrated(t)
	st.Close()
	dir := t.TempDir()
	dst := filepath.Join(dir, "failed.db")
	if err := st.BackupTo(dst); err == nil {
		t.Fatal("closed DB accepted")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("partial files: %v %v", entries, err)
	}
}

func TestBackupRefusesDanglingSymlink(t *testing.T) {
	st := openMigrated(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "untouched")
	dst := filepath.Join(dir, "backup.db")
	if err := os.Symlink(target, dst); err != nil {
		t.Fatal(err)
	}
	if err := st.BackupTo(dst); err == nil {
		t.Fatal("accepted existing symlink")
	}
	if _, err := os.Lstat(dst); err != nil {
		t.Fatal("removed someone else's symlink")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("wrote symlink target: %v", err)
	}
}

func TestBackupConcurrentSameDestinationDoesNotOverwrite(t *testing.T) {
	st := openMigrated(t)
	dst := filepath.Join(t.TempDir(), "snapshot.db")
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { results <- st.BackupTo(dst) }()
	}
	successes := 0
	for i := 0; i < 2; i++ {
		if <-results == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly one atomic publication, got %d", successes)
	}
	restored, err := Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var check string
	if err := restored.db.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
		t.Fatalf("snapshot integrity %s: %v", check, err)
	}
}
