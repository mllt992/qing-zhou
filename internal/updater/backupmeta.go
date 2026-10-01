package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// minBackupBytes is a floor below which a "binary" is certainly damaged. The
// real one is tens of megabytes; this only has to be far enough above zero to
// catch an empty or barely-started copy on installs whose backup predates the
// metadata sidecar and therefore has no hash to check.
const minBackupBytes = 1 << 20 // 1 MiB

// elfMagic is the first four bytes of every ELF executable. Cheap way to notice
// that the backup is not a Linux binary at all.
var elfMagic = []byte{0x7f, 'E', 'L', 'F'}

// backupMeta describes the kept binary. Written next to it at backup time.
type backupMeta struct {
	Version    string `json:"version"`
	SnapshotID string `json:"snapshot_id,omitempty"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
}

func backupMetaPath(exePath string) string { return backupPath(exePath) + ".meta" }

// fileSHA256 hashes a file without reading it all into memory.
func fileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// writeBackupMeta remains a best-effort compatibility helper for legacy callers.
func writeBackupMeta(exePath, ver string) {
	if err := writeBackupMetaWithSnapshot(exePath, ver, ""); err != nil {
		clearBackupMeta(exePath)
	}
}

// stageBackupMeta writes and syncs metadata for a staged binary before either
// existing rollback file is replaced. The caller must remove the returned file.
func stageBackupMeta(exePath, binaryPath, ver, snapshotID string) (string, error) {
	if st, err := os.Lstat(backupMetaPath(exePath)); err == nil {
		if !st.Mode().IsRegular() {
			return "", errors.New("回滚元数据路径不是普通文件")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	ver = strings.TrimSpace(ver)
	if ver == "" {
		ver = "unknown"
	}
	sum, size, err := fileSHA256(binaryPath)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(backupMeta{Version: ver, SHA256: sum, Size: size, SnapshotID: snapshotID})
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(exePath), ".qz-meta-*")
	if err != nil {
		return "", err
	}
	path := f.Name()
	complete := false
	defer func() {
		if !complete {
			f.Close()
			os.Remove(path)
		}
	}()
	if _, err := f.Write(body); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	complete = true
	return path, nil
}

// New updates require this durable link before replacing the live executable.
func writeBackupMetaWithSnapshot(exePath, ver, snapshotID string) error {
	path, err := stageBackupMeta(exePath, backupPath(exePath), ver, snapshotID)
	if err != nil {
		return err
	}
	defer os.Remove(path)
	if err := os.Rename(path, backupMetaPath(exePath)); err != nil {
		return err
	}
	return syncDir(filepath.Dir(exePath))
}

// readBackupMeta returns the sidecar, or nil when there is none (an install
// whose backup was taken before this metadata existed).
func readBackupMeta(exePath string) *backupMeta {
	b, err := os.ReadFile(backupMetaPath(exePath))
	if err != nil || len(b) > 4<<10 {
		return nil
	}
	var m backupMeta
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	if len(m.Version) > 64 {
		m.Version = ""
	}
	return &m
}

// readBackupMetaForRetention is intentionally strict: a damaged sidecar is
// not a legacy install. Treating it as absent would lose the rollback snapshot
// association and let retention delete the only matching database copy.
func readBackupMetaForRetention(exePath string) (*backupMeta, error) {
	path := backupMetaPath(exePath)
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() <= 0 || st.Size() > 4<<10 {
		return nil, errors.New("回滚元数据不是有效的普通文件，保留全部快照并停止更新")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(st, actual) {
		return nil, errors.New("读取期间回滚元数据发生变化")
	}
	body, err := io.ReadAll(io.LimitReader(f, (4<<10)+1))
	if err != nil {
		return nil, err
	}
	var meta backupMeta
	if len(body) > 4<<10 || json.Unmarshal(body, &meta) != nil || meta.Size <= 0 ||
		strings.TrimSpace(meta.Version) == "" || len(meta.Version) > 64 {
		return nil, errors.New("回滚元数据损坏，保留全部快照并停止更新")
	}
	digest, err := hex.DecodeString(meta.SHA256)
	if err != nil || len(digest) != sha256.Size || (meta.SnapshotID != "" && !snapshotName.MatchString(meta.SnapshotID)) {
		return nil, errors.New("回滚元数据校验信息无效，保留全部快照并停止更新")
	}
	return &meta, nil
}

// clearBackupMeta drops the sidecar, e.g. when the backup it describes is gone.
func clearBackupMeta(exePath string) { _ = os.Remove(backupMetaPath(exePath)) }

// checkBackupShallow is the cheap sanity check: run on every status poll, so it
// must not hash tens of megabytes. Catches the cases that matter for deciding
// whether to *offer* the button — missing, empty, truncated early, or not a
// Linux binary at all.
func checkBackupShallow(exePath string) error {
	prev := backupPath(exePath)
	st, err := os.Stat(prev)
	if err != nil {
		return errors.New("本机没有保留上一个版本（面板尚未通过「在线更新」升级过）")
	}
	if !st.Mode().IsRegular() {
		return errors.New("保留的上一个版本不是普通文件，已忽略")
	}
	if st.Size() < minBackupBytes {
		return fmt.Errorf("保留的上一个版本只有 %d 字节，明显不完整，拒绝用于回滚", st.Size())
	}
	if meta := readBackupMeta(exePath); meta != nil && meta.Size > 0 && meta.Size != st.Size() {
		return errors.New("保留的上一个版本大小与备份记录不符，可能已损坏，拒绝用于回滚")
	}
	if err := checkELF(prev); err != nil {
		return err
	}
	return nil
}

func checkELF(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return errors.New("无法读取保留的上一个版本: " + err.Error())
	}
	defer f.Close()
	head := make([]byte, len(elfMagic))
	if _, err := io.ReadFull(f, head); err != nil {
		return errors.New("保留的上一个版本无法读取，拒绝用于回滚")
	}
	for i := range elfMagic {
		if head[i] != elfMagic[i] {
			return errors.New("保留的上一个版本不是可执行文件，拒绝用于回滚")
		}
	}
	return nil
}

// verifyBackupContent is the thorough check, run once immediately before the
// swap rather than on every poll. With a recorded digest it proves the bytes
// are exactly what was backed up; without one — a backup kept by a build that
// predates this sidecar — it falls back to the shallow checks and says so.
func verifyBackupContent(exePath string) error {
	if err := checkBackupShallow(exePath); err != nil {
		return err
	}
	meta := readBackupMeta(exePath)
	if meta == nil || meta.SHA256 == "" {
		// Transitional: nothing to compare against. The shallow checks already
		// ruled out the damage modes that are actually plausible here.
		return nil
	}
	sum, _, err := fileSHA256(backupPath(exePath))
	if err != nil {
		return errors.New("校验保留版本失败: " + err.Error())
	}
	if !strings.EqualFold(sum, meta.SHA256) {
		return errors.New("保留的上一个版本校验不通过（内容与备份时不一致），拒绝用于回滚")
	}
	return nil
}

// copyFileAtomic copies src to dst via a sibling temp file and a rename, so dst
// either does not change or appears complete. Plain copyFile writes into dst
// incrementally and can leave it truncated.
func copyFileAtomic(src, dst string) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".qz-bak-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	if err := copyFile(src, tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}
