package sbctl

// Optional extension keeps existing integrations/fakes compatible. Readiness
// is acknowledged only by the successful apply path, never by desired hashes.
type relayMeteringStore interface {
	RelayMeteringEnabled() bool
	PrepareRelayMetering() error
	RelayMeteringProgress() (string, error)
	RecordRelayConfigApplied(int64, []byte) error
}

// Each successful pass can make the next upstream level ready. A DAG with N
// machines needs at most N downstream levels plus registration/confirmation.
// Stop immediately when no persisted progress is made, instead of busy retrying
// an offline or incompatible downstream. The next normal reconcile can resume.
func (c *Controller) rebuildUntilStable(periodic, force bool) error {
	metering, ok := c.st.(relayMeteringStore)
	if !ok || !metering.RelayMeteringEnabled() {
		return c.rebuild(periodic, force)
	}
	servers, err := c.st.ListServers()
	if err != nil {
		return err
	}
	var last error
	for i := 0; i < len(servers)+3; i++ {
		before, err := metering.RelayMeteringProgress()
		if err != nil {
			return err
		}
		last = c.rebuild(periodic, force)
		after, err := metering.RelayMeteringProgress()
		if err != nil {
			return err
		}
		if before == after {
			return last
		}
	}
	return last
}
