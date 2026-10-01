package updater

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type snapshotSourceStub struct {
	path   string
	backup func(string) error
}

func (s snapshotSourceStub) Path() string               { return s.path }
func (s snapshotSourceStub) BackupTo(path string) error { return s.backup(path) }

func snapshotManager(t *testing.T) (*Manager, string) {
	t.Helper()
	exe := setup(t, "current-binary", "previous-binary")
	return New(nil, nil, snapshotSourceStub{
		path:   filepath.Join(filepath.Dir(exe), "qingzhou.db"),
		backup: func(path string) error { return os.WriteFile(path, []byte("consistent SQLite snapshot"), 0o600) },
	}), exe
}

func TestUpgradeSnapshotMetadataPermissionsAndDownload(t *testing.T) {
	m, exe := snapshotManager(t)
	info, err := m.createUpgradeSnapshot(exe, "v9.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotName.MatchString(info.ID) || strings.Contains(info.ID, "v9.1.0") {
		t.Fatalf("unsafe snapshot name %q", info.ID)
	}
	if info.TargetVersion != "v9.1.0" || info.SourceVersion == "" || info.SourceRevision == "" || info.CreatedAt <= 0 || len(info.SHA256) != 64 {
		t.Fatalf("metadata: %+v", info)
	}
	dir, _ := m.snapshotDir()
	for _, path := range []string{dir, filepath.Join(dir, info.ID)} {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o700 {
			t.Fatalf("%s mode: %o", path, st.Mode().Perm())
		}
	}
	for _, name := range []string{"database.db", "metadata.json"} {
		st, err := os.Stat(filepath.Join(dir, info.ID, name))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode: %o", name, st.Mode().Perm())
		}
	}
	f, got, err := m.OpenSnapshot(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got != info {
		t.Fatalf("download metadata: %+v, want %+v", got, info)
	}
	// The version is metadata, never interpolated into filesystem paths.
	unusual, err := m.createUpgradeSnapshot(exe, "../../bad/version")
	if err != nil || !snapshotName.MatchString(unusual.ID) {
		t.Fatalf("safe naming: %+v %v", unusual, err)
	}
}

func TestSnapshotFailuresPreventEveryBinarySwap(t *testing.T) {
	for _, tc := range []struct {
		name   string
		backup func(string) error
	}{
		{"disk-full", func(path string) error { _ = os.WriteFile(path, []byte("partial"), 0o600); return syscall.ENOSPC }},
		{"permission-denied", func(string) error { return os.ErrPermission }},
		{"empty-output", func(path string) error { return os.WriteFile(path, nil, 0o600) }},
		{"public-output", func(path string) error { return os.WriteFile(path, []byte("private data"), 0o644) }},
		{"metadata-failure", func(path string) error {
			if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(filepath.Dir(path), "metadata.json"), []byte("occupied"), 0o600)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, exe := snapshotManager(t)
			m.snapshotSource = snapshotSourceStub{path: filepath.Join(filepath.Dir(exe), "qingzhou.db"), backup: tc.backup}
			tmp := exe + ".new"
			if err := os.WriteFile(tmp, []byte("new-binary"), 0o755); err != nil {
				t.Fatal(err)
			}
			writeBackupMeta(exe, "v0.0.1")
			oldMeta := read(t, backupMetaPath(exe))
			err := m.installVerifiedBinary(exe, tmp, "v9.1.0")
			if err == nil {
				t.Fatal("failed snapshot did not abort installation")
			}
			if tc.name == "disk-full" && !errors.Is(err, syscall.ENOSPC) {
				t.Fatalf("disk-full cause lost: %v", err)
			}
			if got := read(t, exe); got != "current-binary" {
				t.Fatalf("live binary changed: %q", got)
			}
			if got := read(t, backupPath(exe)); got != "previous-binary" {
				t.Fatalf("rollback binary changed: %q", got)
			}
			if got := read(t, backupMetaPath(exe)); got != oldMeta {
				t.Fatal("rollback metadata changed")
			}
			dir, _ := m.snapshotDir()
			files, err := os.ReadDir(dir)
			if err != nil || len(files) != 0 {
				t.Fatalf("partial snapshot survived: %v %v", files, err)
			}
		})
	}
}

func TestMissingSnapshotSourceFailsClosed(t *testing.T) {
	exe := setup(t, "current", "previous")
	m := New(nil, nil)
	if err := m.installVerifiedBinary(exe, exe+".new", "v1"); err == nil {
		t.Fatal("installed without snapshot source")
	}
	if read(t, exe) != "current" || read(t, backupPath(exe)) != "previous" {
		t.Fatal("binaries changed")
	}
}

func TestVerifiedInstallAssociatesRollbackSnapshot(t *testing.T) {
	m, exe := snapshotManager(t)
	tmp := exe + ".new"
	if err := os.WriteFile(tmp, []byte("new-binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.installVerifiedBinary(exe, tmp, "v9.1.0"); err != nil {
		t.Fatal(err)
	}
	if read(t, exe) != "new-binary" || read(t, backupPath(exe)) != "current-binary" {
		t.Fatal("unexpected binary swap")
	}
	meta := readBackupMeta(exe)
	if meta == nil || meta.SnapshotID == "" {
		t.Fatal("no linked snapshot")
	}
	f, info, err := m.OpenSnapshot(meta.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if info.SourceVersion != meta.Version || info.TargetVersion != "v9.1.0" {
		t.Fatalf("snapshot link: %+v %+v", meta, info)
	}
}

func TestSnapshotRetentionPreservesOldRollbackSnapshot(t *testing.T) {
	m, exe := snapshotManager(t)
	first, err := m.createUpgradeSnapshot(exe, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeBackupMetaWithSnapshot(exe, "old", first.ID); err != nil {
		t.Fatal(err)
	}
	var newest SnapshotInfo
	for i := 0; i < SnapshotRetention+3; i++ {
		newest, err = m.createUpgradeSnapshot(exe, "v2")
		if err != nil {
			t.Fatal(err)
		}
	}
	list, err := m.ListSnapshots()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != SnapshotRetention {
		t.Fatalf("retained %d, want %d", len(list), SnapshotRetention)
	}
	found := map[string]bool{}
	for _, s := range list {
		found[s.ID] = true
	}
	if !found[first.ID] || !found[newest.ID] {
		t.Fatalf("lost protected or newest snapshot: %+v", list)
	}
}

func TestSnapshotDownloadRejectsTraversalAndSymlinks(t *testing.T) {
	m, exe := snapshotManager(t)
	for _, id := range []string{"", "..", "../qingzhou.db", "/etc/passwd", "snapshot-../database.db"} {
		if f, _, err := m.OpenSnapshot(id); err == nil {
			f.Close()
			t.Fatalf("accepted %q", id)
		}
	}
	info, err := m.createUpgradeSnapshot(exe, "v2")
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := m.snapshotDir()
	path := filepath.Join(dir, info.ID, "database.db")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, path); err != nil {
		t.Fatal(err)
	}
	if f, _, err := m.OpenSnapshot(info.ID); err == nil {
		f.Close()
		t.Fatal("followed snapshot symlink")
	}
}

func TestSnapshotDirectoryPermissionsAndSymlink(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		m, exe := snapshotManager(t)
		dir, _ := m.snapshotDir()
		var err error
		if symlink {
			err = os.Symlink(t.TempDir(), dir)
		} else {
			err = os.Mkdir(dir, 0o755)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.createUpgradeSnapshot(exe, "v2"); err == nil {
			t.Fatalf("accepted unsafe directory (symlink=%t)", symlink)
		}
	}
}

func TestSnapshotDownloadDetectsSameSizeCorruption(t *testing.T) {
	m, exe := snapshotManager(t)
	info, err := m.createUpgradeSnapshot(exe, "v2")
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := m.snapshotDir()
	path := filepath.Join(dir, info.ID, "database.db")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body[0] ^= 0x20
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if f, _, err := m.OpenSnapshot(info.ID); err == nil {
		f.Close()
		t.Fatal("downloaded corrupt snapshot")
	}
}

func TestSnapshotCreationCleansAbandonedStaging(t *testing.T) {
	m, exe := snapshotManager(t)
	dir, _ := m.snapshotDir()
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, ".pending-12345")
	if err := os.Mkdir(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "database.db"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.createUpgradeSnapshot(exe, "v2"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging survived: %v", err)
	}
}

func TestInstallMetadataFailurePreservesPreviousBinaryAndSnapshotLink(t *testing.T) {
	m, exe := snapshotManager(t)
	oldSnapshot, err := m.createUpgradeSnapshot(exe, "old-target")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeBackupMetaWithSnapshot(exe, "old-version", oldSnapshot.ID); err != nil {
		t.Fatal(err)
	}
	oldMeta := read(t, backupMetaPath(exe))
	// An obstruction where the atomic metadata rename must publish. It must be
	// discovered before .prev is replaced, rather than downgrading to no metadata.
	if err := os.Remove(backupMetaPath(exe)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(backupMetaPath(exe), 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(backupMetaPath(exe), "original.meta")
	if err := os.WriteFile(marker, []byte(oldMeta), 0o600); err != nil {
		t.Fatal(err)
	}
	tmp := exe + ".new"
	if err := os.WriteFile(tmp, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := m.installVerifiedBinary(exe, tmp, "v2"); err == nil {
		t.Fatal("metadata obstruction allowed update")
	}
	if read(t, exe) != "current-binary" || read(t, backupPath(exe)) != "previous-binary" {
		t.Fatal("metadata failure consumed original binaries")
	}
	if read(t, marker) != oldMeta {
		t.Fatal("original metadata obstruction modified")
	}
	if f, _, err := m.OpenSnapshot(oldSnapshot.ID); err != nil {
		t.Fatal(err)
	} else {
		f.Close()
	}
}

func TestFailedLiveRenameRestoresPreviousBinaryAndMetadata(t *testing.T) {
	for _, hasPrevious := range []bool{true, false} {
		m, exe := snapshotManager(t)
		oldSnapshot, err := m.createUpgradeSnapshot(exe, "old-target")
		if err != nil {
			t.Fatal(err)
		}
		if err := writeBackupMetaWithSnapshot(exe, "old-version", oldSnapshot.ID); err != nil {
			t.Fatal(err)
		}
		oldMeta := read(t, backupMetaPath(exe))
		if !hasPrevious {
			if err := os.Remove(backupPath(exe)); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(backupMetaPath(exe)); err != nil {
				t.Fatal(err)
			}
		}
		// Chmod succeeds, but a directory cannot replace the regular live binary.
		tmp := exe + ".new"
		if err := os.Mkdir(tmp, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := m.installVerifiedBinary(exe, tmp, "v2"); err == nil {
			t.Fatal("invalid final rename succeeded")
		}
		if read(t, exe) != "current-binary" {
			t.Fatal("live binary changed")
		}
		if hasPrevious {
			if read(t, backupPath(exe)) != "previous-binary" || read(t, backupMetaPath(exe)) != oldMeta {
				t.Fatal("failed final swap did not restore old rollback pair")
			}
		} else {
			for _, path := range []string{backupPath(exe), backupMetaPath(exe)} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("created rollback file survived failed swap: %s %v", path, err)
				}
			}
		}
		matches, err := filepath.Glob(filepath.Join(filepath.Dir(exe), ".qz-rollback-*"))
		if err != nil || len(matches) != 0 {
			t.Fatalf("recovery temp files survived: %v %v", matches, err)
		}
	}
}

func TestInvalidRollbackMetadataAtRetentionCapacityPreservesAllSnapshots(t *testing.T) {
	for _, kind := range []string{"directory", "malformed", "empty-object", "invalid-hash", "invalid-id", "oversized", "symlink", "unreadable"} {
		t.Run(kind, func(t *testing.T) {
			m, exe := snapshotManager(t)
			first, err := m.createUpgradeSnapshot(exe, "old")
			if err != nil {
				t.Fatal(err)
			}
			if err := writeBackupMetaWithSnapshot(exe, "v1", first.ID); err != nil {
				t.Fatal(err)
			}
			for i := 1; i < SnapshotRetention; i++ {
				if _, err := m.createUpgradeSnapshot(exe, "later"); err != nil {
					t.Fatal(err)
				}
			}
			original := read(t, backupMetaPath(exe))
			path := backupMetaPath(exe)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "directory":
				err = os.Mkdir(path, 0o700)
			case "malformed":
				err = os.WriteFile(path, []byte("{broken"), 0o600)
			case "empty-object":
				err = os.WriteFile(path, []byte("{}"), 0o600)
			case "invalid-hash":
				err = os.WriteFile(path, []byte(`{"version":"v1","size":1,"sha256":"invalid"}`), 0o600)
			case "invalid-id":
				err = os.WriteFile(path, []byte(strings.Replace(original, first.ID, "../invalid", 1)), 0o600)
			case "oversized":
				err = os.WriteFile(path, []byte(strings.Repeat("x", 4097)), 0o600)
			case "symlink":
				target := path + ".real"
				if err = os.WriteFile(target, []byte(original), 0o600); err == nil {
					err = os.Symlink(target, path)
				}
			case "unreadable":
				err = os.WriteFile(path, []byte(original), 0o000)
			}
			if err != nil {
				t.Fatal(err)
			}
			if kind == "unreadable" && os.Geteuid() == 0 {
				t.Skip("root can read mode000; other invalid-sidecar cases still run")
			}
			tmp := exe + ".new"
			if err := os.WriteFile(tmp, []byte("new"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := m.installVerifiedBinary(exe, tmp, "v2"); err == nil {
				t.Fatal("invalid metadata allowed update")
			}
			list, err := m.ListSnapshots()
			if err != nil || len(list) != SnapshotRetention {
				t.Fatalf("retention mutated: %d %v", len(list), err)
			}
			f, _, err := m.OpenSnapshot(first.ID)
			if err != nil {
				t.Fatalf("associated oldest snapshot lost: %v", err)
			}
			f.Close()
			if read(t, exe) != "current-binary" || read(t, backupPath(exe)) != "previous-binary" {
				t.Fatal("failed update changed binaries")
			}
		})
	}
}

func TestRetentionAcceptsGenuinelyMissingOrLegacyMetadata(t *testing.T) {
	m, exe := snapshotManager(t)
	if _, err := m.createUpgradeSnapshot(exe, "v2"); err != nil {
		t.Fatalf("missing sidecar: %v", err)
	}
	if err := writeBackupMetaWithSnapshot(exe, "legacy", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.createUpgradeSnapshot(exe, "v2"); err != nil {
		t.Fatalf("legacy metadata without snapshot id: %v", err)
	}
}

func TestRollbackUsesFailClosedMetadataGateBeforeRotatingBinaries(t *testing.T) {
	exe := exeWithBackup(t)
	oldLive, oldPrev := read(t, exe), read(t, backupPath(exe))
	writeBackupMeta(exe, "v1")
	oldMeta := read(t, backupMetaPath(exe))
	m := New(nil, nil, snapshotSourceStub{path: filepath.Join(filepath.Dir(exe), "db.sqlite"), backup: func(path string) error {
		if err := os.WriteFile(path, []byte("snapshot"), 0o600); err != nil {
			return err
		}
		// Inject a metadata-write failure after the snapshot has been prepared.
		// The former rollback path had already swapped both binaries at this point.
		if err := os.Remove(backupMetaPath(exe)); err != nil {
			return err
		}
		if err := os.Mkdir(backupMetaPath(exe), 0o700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(backupMetaPath(exe), "original"), []byte(oldMeta), 0o600)
	}})
	if err := m.restorePreviousBinary(exe, "v1"); err == nil {
		t.Fatal("metadata failure allowed rollback")
	}
	if read(t, exe) != oldLive || read(t, backupPath(exe)) != oldPrev {
		t.Fatal("metadata failure changed rollback pair")
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".qz-rb-*"))
	if len(matches) != 0 {
		t.Fatal("rollback staging survived failure")
	}
}

func TestRollbackRotatesAndRetainsVerifiedSnapshotAssociation(t *testing.T) {
	exe := exeWithBackup(t)
	live, prev := read(t, exe), read(t, backupPath(exe))
	writeBackupMeta(exe, "v1")
	m := New(nil, nil, snapshotSourceStub{path: filepath.Join(filepath.Dir(exe), "db.sqlite"), backup: func(path string) error { return os.WriteFile(path, []byte("snapshot"), 0o600) }})
	if err := m.restorePreviousBinary(exe, "v1"); err != nil {
		t.Fatal(err)
	}
	if read(t, exe) != prev || read(t, backupPath(exe)) != live {
		t.Fatal("rollback did not rotate both binaries")
	}
	meta, err := readBackupMetaForRetention(exe)
	if err != nil || meta == nil || meta.SnapshotID == "" {
		t.Fatalf("missing recovery association: %+v %v", meta, err)
	}
	f, _, err := m.OpenSnapshot(meta.SnapshotID)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := verifyBackupContent(exe); err != nil {
		t.Fatal(err)
	}
	if err := m.restorePreviousBinary(exe, meta.Version); err != nil {
		t.Fatal(err)
	}
	if read(t, exe) != live || read(t, backupPath(exe)) != prev {
		t.Fatal("rollback not reversible")
	}
}
