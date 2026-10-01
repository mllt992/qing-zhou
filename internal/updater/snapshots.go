package updater

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"time"

	"qingzhou/internal/version"
)

// SnapshotRetention is a count, not an age limit. The snapshot associated with
// the retained rollback binary is always protected within this budget.
const SnapshotRetention = 5

// SnapshotSource is implemented by store.Store. BackupTo must use SQLite's
// consistent backup facility, rather than copying a live WAL-mode database.
type SnapshotSource interface {
	Path() string
	BackupTo(string) error
}

type SnapshotInfo struct {
	ID             string `json:"id"`
	SourceVersion  string `json:"source_version"`
	TargetVersion  string `json:"target_version"`
	SourceRevision string `json:"source_revision"`
	CreatedAt      int64  `json:"created_at"`
	Size           int64  `json:"size"`
	SHA256         string `json:"sha256"`
}

var pendingSnapshotName = regexp.MustCompile(`^\.pending-[0-9]+$`)

var snapshotName = regexp.MustCompile(`^snapshot-[0-9]{8}T[0-9]{6}\.[0-9]{9}Z-[0-9a-f]{16}$`)

func sourceRevision() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		var revision string
		var dirty bool
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				revision = setting.Value
			}
			if setting.Key == "vcs.modified" {
				dirty = setting.Value == "true"
			}
		}
		if revision != "" {
			if dirty {
				revision += "+modified"
			}
			return revision
		}
	}
	return "unknown"
}

func (m *Manager) snapshotDir() (string, error) {
	if m.snapshotSource == nil || m.snapshotSource.Path() == "" || m.snapshotSource.Path() == ":memory:" {
		return "", errors.New("数据库快照服务未配置，拒绝替换程序")
	}
	path, err := filepath.Abs(m.snapshotSource.Path())
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), "upgrade-snapshots"), nil
}

// Validate directories/files without following symlinks: snapshots must not
// become an arbitrary-file download endpoint even if a directory is damaged.
func privateSnapshotDir(path string, create bool) error {
	if create {
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return errors.New("快照目录不是普通目录")
	}
	if st.Mode().Perm() != 0o700 {
		return fmt.Errorf("快照目录权限必须为 0700: %s", path)
	}
	return nil
}

func openSnapshotFile(path string) (*os.File, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 {
		return nil, errors.New("快照文件必须是权限为 0600 的普通文件")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	actual, err := f.Stat()
	if err != nil || !os.SameFile(st, actual) {
		f.Close()
		return nil, errors.New("读取期间快照文件发生变化")
	}
	return f, nil
}

func readSnapshot(dir, id string) (SnapshotInfo, error) {
	var info SnapshotInfo
	if !snapshotName.MatchString(id) {
		return info, os.ErrNotExist
	}
	base := filepath.Join(dir, id)
	if err := privateSnapshotDir(base, false); err != nil {
		return info, err
	}
	f, err := openSnapshotFile(filepath.Join(base, "metadata.json"))
	if err != nil {
		return info, err
	}
	body, err := io.ReadAll(io.LimitReader(f, 8193))
	f.Close()
	if err != nil {
		return info, err
	}
	if len(body) > 8192 || json.Unmarshal(body, &info) != nil || info.ID != id || info.Size <= 0 || len(info.SHA256) != 64 {
		return SnapshotInfo{}, errors.New("快照元数据损坏")
	}
	db, err := openSnapshotFile(filepath.Join(base, "database.db"))
	if err != nil {
		return SnapshotInfo{}, err
	}
	st, err := db.Stat()
	db.Close()
	if err != nil || st.Size() != info.Size {
		return SnapshotInfo{}, errors.New("快照大小与记录不符")
	}
	return info, nil
}

func listSnapshots(dir string) ([]SnapshotInfo, error) {
	list := make([]SnapshotInfo, 0)
	if err := privateSnapshotDir(dir, false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return list, nil
		}
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !snapshotName.MatchString(entry.Name()) {
			continue
		}
		info, err := readSnapshot(dir, entry.Name())
		// A damaged snapshot must be visible as an error, never downloadable or
		// silently discarded to satisfy retention.
		if err != nil {
			return nil, fmt.Errorf("读取快照 %s: %w", entry.Name(), err)
		}
		list = append(list, info)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID > list[j].ID })
	return list, nil
}

func (m *Manager) ListSnapshots() ([]SnapshotInfo, error) {
	m.snapshotMu.Lock()
	defer m.snapshotMu.Unlock()
	dir, err := m.snapshotDir()
	if err != nil {
		return nil, err
	}
	return listSnapshots(dir)
}

// OpenSnapshot returns an already-open file so concurrent retention cannot
// invalidate a download. The caller owns Close; paths are never client input.
func (m *Manager) OpenSnapshot(id string) (*os.File, SnapshotInfo, error) {
	m.snapshotMu.Lock()
	defer m.snapshotMu.Unlock()
	var info SnapshotInfo
	if !snapshotName.MatchString(id) {
		return nil, info, os.ErrNotExist
	}
	dir, err := m.snapshotDir()
	if err != nil {
		return nil, info, err
	}
	if err := privateSnapshotDir(dir, false); err != nil {
		return nil, info, err
	}
	info, err = readSnapshot(dir, id)
	if err != nil {
		return nil, info, err
	}
	f, err := openSnapshotFile(filepath.Join(dir, id, "database.db"))
	if err != nil {
		return nil, info, err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		f.Close()
		return nil, info, err
	}
	if hex.EncodeToString(h.Sum(nil)) != info.SHA256 {
		f.Close()
		return nil, info, errors.New("快照 SHA-256 与记录不符")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, info, err
	}
	return f, info, nil
}

// Status polling reads metadata/size only; downloading verifies the full hash.
func (m *Manager) snapshotInfo(id string) (*SnapshotInfo, error) {
	m.snapshotMu.Lock()
	defer m.snapshotMu.Unlock()
	dir, err := m.snapshotDir()
	if err != nil {
		return nil, err
	}
	if err := privateSnapshotDir(dir, false); err != nil {
		return nil, err
	}
	info, err := readSnapshot(dir, id)
	return &info, err
}

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func writePrivateFile(path string, body []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Make room BEFORE allocating a full database copy. Never delete the snapshot
// tied to .prev, including when repeated attempts fail before the binary swap.
func pruneSnapshots(dir, protected string, keep int) error {
	list, err := listSnapshots(dir)
	if err != nil {
		return err
	}
	remaining := len(list)
	for i := len(list) - 1; i >= 0 && remaining > keep; i-- {
		if list[i].ID == protected {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, list[i].ID)); err != nil {
			return err
		}
		remaining--
	}
	if remaining > keep {
		return errors.New("无法在保留回滚快照的同时释放快照空间")
	}
	return syncDir(dir)
}

func (m *Manager) createUpgradeSnapshot(exePath, target string) (SnapshotInfo, error) {
	m.snapshotMu.Lock()
	defer m.snapshotMu.Unlock()
	var info SnapshotInfo
	dir, err := m.snapshotDir()
	if err != nil {
		return info, err
	}
	if err := privateSnapshotDir(dir, true); err != nil {
		return info, err
	}
	if err := syncDir(filepath.Dir(dir)); err != nil {
		return info, err
	}
	// A killed previous updater can leave a hidden unpublished snapshot. No
	// reader can use it, and retaining it indefinitely would evade the budget.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return info, err
	}
	for _, entry := range entries {
		if pendingSnapshotName.MatchString(entry.Name()) {
			path := filepath.Join(dir, entry.Name())
			if err := privateSnapshotDir(path, false); err != nil {
				return info, err
			}
			if err := os.RemoveAll(path); err != nil {
				return info, err
			}
		}
	}
	protected := ""
	if meta := readBackupMeta(exePath); meta != nil {
		protected = meta.SnapshotID
	}
	if err := pruneSnapshots(dir, protected, SnapshotRetention-1); err != nil {
		return info, err
	}
	stage, err := os.MkdirTemp(dir, ".pending-")
	if err != nil {
		return info, err
	}
	defer os.RemoveAll(stage)
	m.setState(StatusSnapshotting, "创建升级前数据库一致性快照…", 100, target)
	dbPath := filepath.Join(stage, "database.db")
	if err := m.snapshotSource.BackupTo(dbPath); err != nil {
		return info, fmt.Errorf("数据库一致性快照失败: %w", err)
	}
	// Reassert the public contract at the boundary, then ensure the data reaches
	// disk before publishing either the snapshot or the new executable.
	f, err := openSnapshotFile(dbPath)
	if err != nil {
		return info, err
	}
	err = f.Sync()
	closeErr := f.Close()
	if err != nil {
		return info, err
	}
	if closeErr != nil {
		return info, closeErr
	}
	sum, size, err := fileSHA256(dbPath)
	if err != nil {
		return info, err
	}
	if size == 0 {
		return info, errors.New("数据库快照为空")
	}
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return info, err
	}
	now := time.Now().UTC()
	info = SnapshotInfo{
		ID:            "snapshot-" + now.Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(random),
		SourceVersion: version.Current(), TargetVersion: target, SourceRevision: sourceRevision(),
		CreatedAt: now.Unix(), Size: size, SHA256: sum,
	}
	body, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return info, err
	}
	if err := writePrivateFile(filepath.Join(stage, "metadata.json"), body); err != nil {
		return info, err
	}
	if err := syncDir(stage); err != nil {
		return info, err
	}
	final := filepath.Join(dir, info.ID)
	if err := os.Rename(stage, final); err != nil {
		return info, err
	}
	if err := syncDir(dir); err != nil {
		_ = os.RemoveAll(final)
		return info, err
	}
	return info, nil
}

// installVerifiedBinary is the only live-binary swap in the online updater.
// Keep the fail-closed snapshot gate in this function so it cannot be bypassed
// by future changes to network/signature handling.
func (m *Manager) installVerifiedBinary(exePath, tmpPath, target string) (err error) {
	snapshot, err := m.createUpgradeSnapshot(exePath, target)
	if err != nil {
		return fmt.Errorf("升级前快照失败，已取消更新（程序未替换）: %w", err)
	}
	m.setState(StatusInstalling, "安装新版本…", 100, target)
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return err
	}
	prev := backupPath(exePath)
	stage := prev + ".new"
	_ = os.Remove(stage)
	if err := os.Link(exePath, stage); err != nil {
		if err := copyFileAtomic(exePath, stage); err != nil {
			return fmt.Errorf("备份当前版本失败: %w", err)
		}
	}
	defer os.Remove(stage)
	metadata, err := stageBackupMeta(exePath, stage, version.Current(), snapshot.ID)
	if err != nil {
		return fmt.Errorf("记录回滚版本与快照关联失败: %w", err)
	}
	defer os.Remove(metadata)

	// Preserve the old pair before changing either name. A metadata failure or
	// failed final rename must not consume the only known-good rollback target.
	var saved []savedRollbackFile
	changed := false
	defer func() {
		for _, file := range saved {
			if err != nil && changed {
				if restoreErr := file.restore(); restoreErr != nil {
					err = errors.Join(err, fmt.Errorf("恢复原回滚文件 %s 失败（保留在 %s）: %w", file.original, file.saved, restoreErr))
					continue // Keep the recovery copy if restoring it failed.
				}
			}
			if file.saved != "" {
				_ = os.Remove(file.saved)
			}
		}
	}()
	for _, path := range []string{prev, backupMetaPath(exePath)} {
		file, keepErr := preserveRollbackFile(path)
		if keepErr != nil {
			return keepErr
		}
		saved = append(saved, file)
	}
	if err := os.Rename(stage, prev); err != nil {
		return err
	}
	changed = true
	if err := os.Rename(metadata, backupMetaPath(exePath)); err != nil {
		return err
	}
	if err := syncDir(filepath.Dir(exePath)); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, exePath); err != nil {
		return fmt.Errorf("替换二进制失败: %w", err)
	}
	return nil
}

type savedRollbackFile struct {
	original string
	saved    string // Empty means no original existed; restore by removing the new file.
}

func (file savedRollbackFile) restore() error {
	if file.saved != "" {
		return os.Rename(file.saved, file.original)
	}
	if err := os.Remove(file.original); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func preserveRollbackFile(path string) (savedRollbackFile, error) {
	saved := savedRollbackFile{original: path}
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return saved, nil
	}
	if err != nil {
		return saved, err
	}
	if !st.Mode().IsRegular() {
		return saved, errors.New("原回滚文件不是普通文件")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".qz-rollback-*")
	if err != nil {
		return saved, err
	}
	temp := f.Name()
	f.Close()
	// A same-directory hard link needs no extra binary-sized allocation. Fall
	// back to a private, synced copy on filesystems that do not support links.
	_ = os.Remove(temp)
	if err := os.Link(path, temp); err == nil {
		saved.saved = temp
		return saved, nil
	}
	f, err = os.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, st.Mode().Perm())
	if err != nil {
		return saved, err
	}
	complete := false
	defer func() {
		f.Close()
		if !complete {
			os.Remove(temp)
		}
	}()
	in, err := os.Open(path)
	if err != nil {
		return saved, err
	}
	defer in.Close()
	if _, err := io.Copy(f, in); err != nil {
		return saved, err
	}
	if err := f.Sync(); err != nil {
		return saved, err
	}
	if err := f.Close(); err != nil {
		return saved, err
	}
	complete = true
	saved.saved = temp
	return saved, nil
}
