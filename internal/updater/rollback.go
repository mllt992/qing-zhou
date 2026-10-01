package updater

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"qingzhou/internal/version"
)

// backupVersion is the recorded version of the kept binary, or "" when the
// backup predates the metadata sidecar.
func backupVersion(exePath string) string {
	if m := readBackupMeta(exePath); m != nil {
		return m.Version
	}
	return ""
}

// RollbackState describes whether the one-step local rollback is usable, and to
// what. Reason is populated only when Available is false, and is written to be
// shown to the admin as-is.
type RollbackState struct {
	Available bool          `json:"available"`
	Snapshot  *SnapshotInfo `json:"snapshot,omitempty"`
	Version   string        `json:"version"`
	Reason    string        `json:"reason,omitempty"`
	Size      int64         `json:"size,omitempty"`
	SavedAt   int64         `json:"saved_at,omitempty"`
}

// currentExe resolves the running binary, following symlinks so the backup ends
// up beside the real file rather than beside a link to it.
func currentExe() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, rerr := filepath.EvalSymlinks(p); rerr == nil {
		p = resolved
	}
	return p, nil
}

// Rollback reports whether the previous binary is on disk and ready to swap in.
func (m *Manager) RollbackState() RollbackState {
	if runtime.GOOS != "linux" {
		return RollbackState{Reason: "回滚仅支持 Linux 部署"}
	}
	exePath, err := currentExe()
	if err != nil {
		return RollbackState{Reason: "无法定位当前程序路径: " + err.Error()}
	}
	// Shallow, not the full digest: this runs on every status poll, and hashing
	// tens of megabytes to render a button would be absurd. The digest is
	// verified once, immediately before the swap.
	if err := checkBackupShallow(exePath); err != nil {
		return RollbackState{Reason: err.Error()}
	}
	st, err := os.Stat(backupPath(exePath))
	if err != nil {
		return RollbackState{Reason: "读取保留版本失败: " + err.Error()}
	}
	var snapshot *SnapshotInfo
	if meta := readBackupMeta(exePath); meta != nil && meta.SnapshotID != "" {
		if info, err := m.snapshotInfo(meta.SnapshotID); err == nil && info.SourceVersion == meta.Version {
			snapshot = info
		}
	}
	return RollbackState{
		Snapshot:  snapshot,
		Available: true,
		Version:   backupVersion(exePath),
		Size:      st.Size(),
		SavedAt:   st.ModTime().Unix(),
	}
}

// Rollback swaps the previously-installed binary back in and re-execs.
//
// This is the path that has to work when nothing else does. A release that
// starts but misbehaves can be fixed by installing an older tag from GitHub;
// this exists for the case where the panel is barely usable, GitHub is
// unreachable, or the operator simply wants the previous build back in one
// click and one restart with no download.
//
// The swap is a rotation, not a one-way restore: the version being left behind
// becomes the new rollback target, so a mistaken rollback is itself reversible
// without the network.
func (m *Manager) Rollback(nowUnix int64) error {
	if runtime.GOOS != "linux" {
		return errors.New("回滚仅支持 Linux 部署；请在服务器上手动替换二进制")
	}
	rs := m.RollbackState()
	if !rs.Available {
		return errors.New(rs.Reason)
	}
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return errors.New("已有更新任务在进行中")
	}
	m.running = true
	m.state = State{Status: StatusInstalling, Message: "正在回滚…", Percent: 100, StartedAt: nowUnix, TargetVersion: rs.Version}
	m.mu.Unlock()

	go m.runRollback()
	return nil
}

// runRollback performs the swap. Every step is ordered so that a failure at any
// point leaves a working binary at exePath — losing the ability to roll back is
// recoverable, losing the panel is not.
func (m *Manager) runRollback() {
	exePath, err := currentExe()
	if err != nil {
		m.fail("无法定位当前程序路径: " + err.Error())
		return
	}
	prev := backupPath(exePath)
	prevVer := backupVersion(exePath)
	curVer := version.Current()
	label := prevVer
	if label == "" {
		label = "上一个版本"
	}
	m.setState(StatusVerifying, "校验保留的版本…", 100, prevVer)

	// The full digest check, once, right before anything irreversible. The
	// button was offered on the cheap checks alone; installing a binary is not
	// something to do on "probably fine".
	if err := verifyBackupContent(exePath); err != nil {
		m.fail(err.Error())
		return
	}
	snapshot, err := m.createUpgradeSnapshot(exePath, prevVer)
	if err != nil {
		m.fail("回滚前快照失败，程序未替换: " + err.Error())
		return
	}
	m.setState(StatusInstalling, "正在回滚到 "+label+"…", 100, prevVer)

	// 1. Stage a copy of the target. Copy rather than rename: until the swap
	//    actually happens, both the running binary and the backup must survive
	//    untouched, so an error here changes nothing at all.
	staging := exePath + ".rb"
	_ = os.Remove(staging)
	if err := copyFile(prev, staging); err != nil {
		_ = os.Remove(staging)
		m.fail("准备回滚文件失败: " + err.Error())
		return
	}
	if err := os.Chmod(staging, 0o755); err != nil {
		_ = os.Remove(staging)
		m.fail("设置可执行权限失败: " + err.Error())
		return
	}

	// 2. Preserve the version we are leaving, so the rollback is reversible
	//    offline. Still a copy, for the same reason as step 1.
	keep := exePath + ".fwd"
	_ = os.Remove(keep)
	if err := copyFile(exePath, keep); err != nil {
		_ = os.Remove(staging)
		_ = os.Remove(keep)
		m.fail("备份当前版本失败，已取消回滚: " + err.Error())
		return
	}

	// 3. The swap. rename() over the running binary is safe on Linux (only
	//    *writing* a busy text file fails), and both paths are siblings so the
	//    rename cannot cross a filesystem.
	if err := os.Rename(staging, exePath); err != nil {
		_ = os.Remove(staging)
		_ = os.Remove(keep)
		m.fail("替换二进制失败: " + err.Error())
		return
	}

	// 4. Rotate the backup. Past the point of no return: exePath already holds
	//    the rolled-back binary, so a failure here costs the *next* rollback,
	//    not this one. Do not abort.
	if err := os.Rename(keep, prev); err == nil {
		if err := writeBackupMetaWithSnapshot(exePath, curVer, snapshot.ID); err != nil {
			clearBackupMeta(exePath)
		}
	} else {
		// prev still holds the bytes now running as exePath. Leaving it would
		// advertise a rollback that swaps the live binary for an identical copy
		// — a pointless service restart dressed up as a recovery action. Remove
		// it so the button honestly reports that there is nothing to go back to.
		_ = os.Remove(keep)
		_ = os.Remove(prev)
		clearBackupMeta(exePath)
	}

	m.setState(StatusRestarting, "回滚完成，正在重启服务…", 100, prevVer)
	// Let the in-flight status poll flush before the process image is replaced.
	time.Sleep(600 * time.Millisecond)
	if err := restartSelf(exePath); err != nil {
		m.fail(fmt.Sprintf("已回滚到 %s，但重启失败: %v；请手动重启服务", label, err))
		return
	}
}
