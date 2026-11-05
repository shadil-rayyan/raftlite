// Enforcement verification for pull-based (envoy-sim) gateways.
//
// After a gateway reports accepting a revision, the control plane probes
// its DATA PATH — real HTTP requests through the actual proxy — to answer
// "is the policy achieved, is it working?". Probe outcomes are the ONLY
// thing that advances a gateway's verification state; simulated acceptance
// never does (docs/gateway-simulation.md, FR-018/FR-021).
//
// The probe carries the policy's target as the client address via
// X-Forwarded-For. That header is honored only because the gateway is
// explicitly configured to trust the control plane's source as a proxy hop
// (SEC-007) — nothing else can forge source addresses.
package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"time"

	"github.com/shadil/raftlite/internal/consensus"
	"github.com/shadil/raftlite/internal/fleet"
	"github.com/shadil/raftlite/internal/policy"
)

// StartFleetVerifier runs the probe loop on every node; only the leader
// does work (fleet state is leader-owned). Stops with the given channel.
func (a *API) StartFleetVerifier(stop <-chan struct{}) {
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				a.verifyFleetOnce(context.Background())
			}
		}
	}()
}

// verifyFleetOnce probes every online envoy-sim gateway that has applied
// the current revision. Exported to tests via the method; tests call it
// directly instead of waiting on the ticker.
func (a *API) verifyFleetOnce(ctx context.Context) {
	if a.fleet == nil || a.node.State() != consensus.Leader {
		return
	}
	rev := a.fleet.CurrentRevision()
	if rev == 0 {
		return
	}
	snap := a.fleet.CurrentSnapshot()
	now := time.Now()
	for _, gw := range a.fleet.List() {
		if gw.AdapterType != fleet.AdapterEnvoySim || gw.ConnectionStatus != fleet.StatusOnline {
			continue
		}
		if gw.ProxyURL == "" {
			continue
		}
		// Only gateways that applied the revision being asked about are
		// probed; staleness is its own (already-reported) question.
		if gw.DesiredRevision != rev || gw.ReportedAcceptedRevision != rev {
			continue
		}
		a.verifyGateway(ctx, gw, snap, rev, now)
	}
}

func (a *API) verifyGateway(ctx context.Context, gw fleet.Gateway, snap fleet.ConfigSnapshot, rev uint64, now time.Time) {
	// Negative control first: an address no policy covers must get through.
	// If it does not, the probe result is untrustworthy (the gateway is not
	// proxying as configured) and no per-policy conclusion is safe.
	if ctrl, ok := controlAddress(snap, now); ok {
		code, err := a.probeDataPath(ctx, gw.ProxyURL, ctrl)
		if err != nil || code != http.StatusOK {
			a.fleet.MarkGatewayVerification(gw.ID, rev, fleet.VerifFailed, &fleet.EventError{
				Code:    "PROBE_FAILED",
				Message: fmt.Sprintf("control request from %s via %s returned %d (%v); want 200", ctrl, gw.ProxyURL, code, err),
			})
			return
		}
	}
	for _, p := range snap.Policies {
		if p.Action != policy.ActionDeny || p.Status != string(policy.StatusActive) {
			continue
		}
		if p.Expires != nil && *p.Expires <= now.Unix() {
			continue
		}
		target, ok := probeAddress(p)
		if !ok {
			continue
		}
		code, err := a.probeDataPath(ctx, gw.ProxyURL, target)
		if err == nil && code == http.StatusTooManyRequests {
			a.fleet.MarkVerification(gw.ID, p.ID, rev, fleet.VerifVerified, nil)
			continue
		}
		a.fleet.MarkVerification(gw.ID, p.ID, rev, fleet.VerifFailed, &fleet.EventError{
			Code:    "ENFORCEMENT_MISSING",
			Message: fmt.Sprintf("request from %s via %s returned %d (%v); want 429", target, gw.ProxyURL, code, err),
		})
	}
}

// probeDataPath sends one request through a gateway's data path with the
// given client address carried in X-Forwarded-For.
func (a *API) probeDataPath(ctx context.Context, baseURL, clientIP string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-Forwarded-For", clientIP)
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)) // drain for reuse
	return resp.StatusCode, nil
}

// probeControlCandidates are TEST-NET addresses used as negative controls;
// the first one no policy covers is chosen.
var probeControlCandidates = []string{"203.0.113.200", "198.51.100.200", "192.0.2.200", "2001:db8::ff"}

func controlAddress(snap fleet.ConfigSnapshot, now time.Time) (string, bool) {
	for _, cand := range probeControlCandidates {
		if !snapshotBlocks(snap, cand, now) {
			return cand, true
		}
	}
	return "", false
}

// snapshotBlocks evaluates a client address against a config snapshot the
// same way a gateway's local policy set does (FR-019): exact match for
// ip_block, prefix containment for cidr_block, expired entries ignored.
func snapshotBlocks(snap fleet.ConfigSnapshot, ip string, now time.Time) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	for _, p := range snap.Policies {
		if p.Action != policy.ActionDeny || p.Status != string(policy.StatusActive) {
			continue
		}
		if p.Expires != nil && *p.Expires <= now.Unix() {
			continue
		}
		switch p.Type {
		case string(policy.TypeIPBlock):
			if t, err := netip.ParseAddr(p.Target); err == nil && t == addr {
				return true
			}
		case string(policy.TypeCIDR):
			if pre, err := netip.ParsePrefix(p.Target); err == nil && pre.Contains(addr) {
				return true
			}
		}
	}
	return false
}

// probeAddress picks the address a policy's probe should carry: the target
// itself for ip_block, the network address for cidr_block.
func probeAddress(p fleet.PolicyView) (string, bool) {
	switch p.Type {
	case string(policy.TypeIPBlock):
		return p.Target, true
	case string(policy.TypeCIDR):
		pre, err := netip.ParsePrefix(p.Target)
		if err != nil {
			return "", false
		}
		return pre.Masked().Addr().String(), true
	}
	return "", false
}
