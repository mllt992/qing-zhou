package updater

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"
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
	prevVer := backupVersion(exePath)
	label := prevVer
	if label == "" {
		label = "上一个版本"
	}
	m.setState(StatusVerifying, "校验保留的版本…", 100, prevVer)

	if err := m.restorePreviousBinary(exePath, prevVer); err != nil {
		m.fail("回滚已取消（程序未替换）: " + err.Error())
		return
	}

	m.setState(StatusRestarting, "回滚完成，正在重启服务…", 100, prevVer)
	// Let the in-flight status poll flush before the process image is replaced.
	time.Sleep(600 * time.Millisecond)
	if err := restartSelf(exePath); err != nil {
		m.fail(fmt.Sprintf("已回滚到 %s，但重启失败: %v；请手动重启服务", label, err))
		return
	}
}

// restorePreviousBinary stages the existing target and then uses the SAME
// fail-closed snapshot/metadata/swap ordering as an online update. In particular,
// failed metadata persistence must never rotate binaries or restart the process.
func (m *Manager) restorePreviousBinary(exePath, target string) error {
	if err := verifyBackupContent(exePath); err != nil {
		return err
	}
	in, err := os.Open(backupPath(exePath))
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(exePath), ".qz-rb-*")
	if err != nil {
		return err
	}
	staging := out.Name()
	defer os.Remove(staging)
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return m.installVerifiedBinary(exePath, staging, target)
}
