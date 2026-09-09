package sbctl

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// AllTarget is the SyncStatus key for a full (every-server) rebuild. Real server
// rows use their positive id; the local panel instance uses 0.
const AllTarget int64 = -1

// SyncStatus is the outcome of the most recent config apply to one target, so an
// async (non-blocking) rebuild can still report to the admin UI whether it
// actually landed on each machine.
type SyncStatus struct {
	State string `json:"state"` // pending | running | ok | failed
	Error string `json:"error,omitempty"`
	At    int64  `json:"at"` // unix seconds of the last state change
	// seq is the controller-wide revision of this write (see Controller.statusSeq).
	// Internal bookkeeping, never serialized.
	seq uint64
}

// ScheduleRebuild queues a full rebuild (all servers) and returns immediately.
func (c *Controller) ScheduleRebuild() { c.schedule(AllTarget) }

// ScheduleRebuildServer queues a rebuild of one server (0 = local panel) and
// returns immediately.
func (c *Controller) ScheduleRebuildServer(serverID int64) { c.schedule(serverID) }

// schedule marks a target dirty and ensures exactly one drain goroutine is
// running. Repeated calls while a pass is in flight coalesce into a single
// follow-up pass, so a burst of admin edits doesn't reload each node N times.
func (c *Controller) schedule(target int64) {
	c.schedMu.Lock()
	if target == AllTarget {
		c.pendingAll = true
	} else {
		c.pendingServer[target] = true
	}
	c.putStatusLocked(target, SyncStatus{State: "pending", At: time.Now().Unix()})
	if c.schedRunning {
		c.schedMu.Unlock()
		return
	}
	c.schedRunning = true
	c.schedMu.Unlock()
	go c.drain()
}

// drain runs queued rebuilds until nothing is pending, then exits. A full
// rebuild subsumes any individually-queued servers, so they inherit its outcome
// rather than being reloaded a second time.
func (c *Controller) drain() {
	for {
		c.schedMu.Lock()
		all := c.pendingAll
		c.pendingAll = false
		var servers []int64
		for id := range c.pendingServer {
			servers = append(servers, id)
			delete(c.pendingServer, id)
		}
		if !all && len(servers) == 0 {
			c.schedRunning = false
			c.schedMu.Unlock()
			return
		}
		c.schedMu.Unlock()

		if all {
			// Rebuild reports each machine's own outcome as it goes, so remember
			// where every subsumed server stood before the pass and only fall back
			// to the aggregate result for those it never reached (a disabled server,
			// or a failure that aborted the loop early). Copying unconditionally
			// would replace "SSH 下发失败: ..." on the machine that actually failed
			// with the same generic line on every machine.
			before := c.statusSeqs(servers)
			c.runOne(AllTarget, c.Rebuild)
			st := c.status(AllTarget)
			for _, id := range servers {
				c.setStatusFromIfUntouched(id, st, before[id])
			}
			continue
		}
		for _, id := range servers {
			id := id
			c.runOne(id, func() error { return c.RebuildServer(id) })
		}
	}
}

func (c *Controller) runOne(target int64, fn func() error) {
	c.setStatus(target, "running", "")
	if err := fn(); err != nil {
		c.setStatus(target, "failed", err.Error())
		return
	}
	c.setStatus(target, "ok", "")
}

func (c *Controller) setStatus(id int64, state, errMsg string) {
	c.schedMu.Lock()
	c.putStatusLocked(id, SyncStatus{State: state, Error: errMsg, At: time.Now().Unix()})
	c.schedMu.Unlock()
}

// putStatusLocked stores st under id with the next revision. Caller holds schedMu.
func (c *Controller) putStatusLocked(id int64, st SyncStatus) {
	c.statusSeq++
	st.seq = c.statusSeq
	c.syncStatus[id] = st
}

// statusSeqs snapshots the current revision of each id (0 when unknown).
func (c *Controller) statusSeqs(ids []int64) map[int64]uint64 {
	c.schedMu.Lock()
	defer c.schedMu.Unlock()
	out := make(map[int64]uint64, len(ids))
	for _, id := range ids {
		out[id] = c.syncStatus[id].seq
	}
	return out
}

// setStatusFromIfUntouched copies st onto id only if id's status has not been
// rewritten since the given revision — i.e. nobody reported on that machine
// specifically in the meantime.
func (c *Controller) setStatusFromIfUntouched(id int64, st SyncStatus, since uint64) {
	c.schedMu.Lock()
	defer c.schedMu.Unlock()
	if c.syncStatus[id].seq != since {
		return
	}
	c.putStatusLocked(id, st)
}

func (c *Controller) status(id int64) SyncStatus {
	c.schedMu.Lock()
	defer c.schedMu.Unlock()
	return c.syncStatus[id]
}

// SyncStatuses returns a snapshot of every target's last apply outcome, keyed by
// server id (0 = local panel, -1 = full rebuild).
func (c *Controller) SyncStatuses() map[int64]SyncStatus {
	c.schedMu.Lock()
	defer c.schedMu.Unlock()
	out := make(map[int64]SyncStatus, len(c.syncStatus))
	for k, v := range c.syncStatus {
		out[k] = v
	}
	return out
}

// RunOnServer runs one shell command on the given server (0 = local panel host)
// and returns its combined output. Local servers run via the local shell;
// remote servers run over SSH. Used for node-side diagnostics such as testing a
// proxy egress from the machine that will actually route through it. The command
// must already be shell-safe; ctx bounds the whole operation.
func (c *Controller) RunOnServer(ctx context.Context, serverID int64, cmd string) (string, error) {
	if serverID != 0 {
		sv, err := c.st.GetServer(serverID)
		if err != nil {
			return "", fmt.Errorf("get server %d: %w", serverID, err)
		}
		if sv == nil {
			return "", fmt.Errorf("服务器 %d 不存在（可能已被删除）", serverID)
		}
		if !isLocalHostContext(ctx, sv.Host) {
			if c.remoteMgr == nil {
				return "", fmt.Errorf("remote manager not configured")
			}
			if err := c.acquireRemote(ctx); err != nil {
				return "", err
			}
			defer c.releaseRemote()
			return c.remoteMgr.RunCommand(ctx, SSHConfigFor(sv), cmd)
		}
	}
	ec := exec.CommandContext(ctx, "sh", "-c", cmd)
	// Without this, ctx does not actually bound the call. CommandContext kills
	// the shell on expiry, but CombinedOutput goes on waiting until every holder
	// of the output pipe closes it — and the shell's own children (curl here, and
	// anything a diagnostic script backgrounds) inherit that pipe and are not
	// killed with it. So a deadline of 25s could still return at 40s, past the
	// server's 30s WriteTimeout, and the panel would show a torn connection
	// instead of the "不通" the script had already determined.
	//
	// WaitDelay closes the pipes and gives up shortly after the kill, so the
	// caller gets whatever output was produced plus the context error.
	ec.WaitDelay = 2 * time.Second
	out, err := ec.CombinedOutput()
	return string(out), err
}
