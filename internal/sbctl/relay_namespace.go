package sbctl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"time"

	"qingzhou/internal/sbproc"
	"qingzhou/internal/sshctl"
	"qingzhou/internal/store"
)

var legacyNumericClient = regexp.MustCompile(`^relay_[1-9][0-9]*$`)

func configNeedsRelayNamespaceProof(raw []byte) bool {
	var cfg struct {
		Inbounds []struct {
			Type  string           `json:"type"`
			Users []map[string]any `json:"users"`
		} `json:"inbounds"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return false
	}
	for _, ib := range cfg.Inbounds {
		if ib.Type != "mixed" {
			continue
		}
		for _, u := range ib.Users {
			if name, ok := u["username"].(string); ok && legacyNumericClient.MatchString(name) {
				return true
			}
		}
	}
	return false
}

// Namespace proof is created only around a controlled successful restart. A
// matching disk file with an old active core is deliberately insufficient.
func (c *Controller) applyWithRelayNamespace(ctx context.Context, serverID int64, sv *store.Server, raw []byte, apply func(bool) (bool, error)) (bool, error) {
	if err := c.verifyCoreTopologySnapshot(serverID); err != nil {
		return false, err
	}
	requirements, needsCore := c.coreRequirements[serverID]
	if inspector, ok := c.st.(interface {
		RelayCoreRequirementsForConfig(int64, []byte) (store.RelayCoreRequirements, error)
	}); ok {
		actual, err := inspector.RelayCoreRequirementsForConfig(serverID, raw)
		if err != nil {
			return false, fmt.Errorf("无法确认待下发配置的内核能力：%w", err)
		}
		requirements.VisionFraming = requirements.VisionFraming || actual.VisionFraming
		requirements.TransportReadBuffer = requirements.TransportReadBuffer || actual.TransportReadBuffer
		requirements.TrojanHandshake = requirements.TrojanHandshake || actual.TrojanHandshake
		needsCore = requirements.VisionFraming || requirements.TransportReadBuffer || requirements.TrojanHandshake
		if needsCore {
			// Remote callers already hold their apply slot. Reacquiring here
			// could deadlock when all slots are occupied by guarded applies.
			if err := c.verifyCoreCapability(ctx, serverID, sv, true, requirements, true); err != nil {
				return false, err
			}
		}
	}
	restarted, err := c.applyWithRelayNamespaceRaw(ctx, serverID, sv, raw, apply)
	if err == nil {
		if err = c.verifyCoreTopologySnapshot(serverID); err != nil {
			return restarted, err
		}
		if needsCore {
			// Recheck the SAME raw-derived capability after restart, regardless
			// of a concurrently changed feature flag or desired topology.
			err = c.verifyCoreCapability(ctx, serverID, sv, false, requirements, true)
		} else if _, legacyVision := c.visionRequired[serverID]; legacyVision {
			err = c.verifyVisionCapability(ctx, serverID, sv, false)
		}
	}
	return restarted, err
}

func (c *Controller) applyWithRelayNamespaceRaw(ctx context.Context, serverID int64, sv *store.Server, raw []byte, apply func(bool) (bool, error)) (bool, error) {
	if !configNeedsRelayNamespaceProof(raw) {
		return apply(false)
	}
	proofs, ok := c.st.(interface {
		RelayNamespaceProofKnown(int64, string, []byte) (bool, error)
		RecordRelayNamespaceEpoch(int64, string, []byte) error
	})
	if !ok {
		return apply(false)
	}
	before, inspectErr := c.inspectRelayNamespace(ctx, sv)
	known := false
	if inspectErr == nil {
		var err error
		known, err = proofs.RelayNamespaceProofKnown(serverID, before.Epoch, raw)
		if err != nil {
			return false, err
		}
	}
	// Only a verifiable managed process can be forced. Unsupported deployments
	// continue their normal applies without entering an endless restart loop.
	restarted, err := apply(inspectErr == nil && !known)
	if err != nil {
		return restarted, err
	}
	after, afterErr := c.inspectRelayNamespace(ctx, sv)
	expected := namespaceConfigHash(raw)
	if sv != nil && (sv.Host == "" || !isLocalHostContext(ctx, sv.Host)) {
		expected = sshctl.InstalledConfigHash(raw)
	}
	verified := inspectErr == nil && afterErr == nil && after.ConfigHash == expected
	if verified && !restarted && known && before.Epoch == after.Epoch {
		return restarted, nil
	}
	if verified && restarted && before.Epoch != after.Epoch {
		if err = proofs.RecordRelayNamespaceEpoch(serverID, after.Epoch, raw); err == nil {
			return restarted, nil
		}
	}
	c.recordTrafficFailure(serverID, "legacy_identity_unverified")
	log.Printf("sbctl: server %d legacy numeric account metering unavailable: requires a verified systemd restart using the managed single config path; ordinary accounts continue", serverID)
	return restarted, nil
}

func namespaceConfigHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (c *Controller) inspectRelayNamespace(ctx context.Context, sv *store.Server) (sbproc.ManagedProcess, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if sv != nil && (sv.Host == "" || !isLocalHostContext(ctx, sv.Host)) {
		inspector, ok := c.remoteMgr.(interface {
			InspectManagedProcess(context.Context, *sshctl.ServerConfig) (sbproc.ManagedProcess, error)
		})
		if !ok {
			return sbproc.ManagedProcess{}, fmt.Errorf("remote managed-process verification unavailable")
		}
		return inspector.InspectManagedProcess(ctx, SSHConfigFor(sv))
	}
	path := c.localConfigPath()
	if sv != nil {
		path = serverConfigPath(sv)
	}
	return sbproc.InspectManagedProcess(ctx, c.trafficUnit(sv), path)
}

func (c *Controller) applyRemoteConfig(ctx context.Context, sv *store.Server, raw []byte) (bool, error) {
	return c.applyWithRelayNamespace(ctx, sv.ID, sv, raw, func(force bool) (bool, error) {
		if manager, ok := c.remoteMgr.(interface {
			ApplyConfigForce(context.Context, *sshctl.ServerConfig, []byte, bool) (bool, error)
		}); ok {
			return manager.ApplyConfigForce(ctx, SSHConfigFor(sv), raw, force)
		}
		return c.remoteMgr.ApplyConfig(ctx, SSHConfigFor(sv), raw)
	})
}
