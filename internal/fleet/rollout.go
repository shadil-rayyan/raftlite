package fleet

import (
	"context"
	"time"
)

// startRollout creates a rollout, advances desired revisions, schedules
// delivery, and marks anything it supersedes. Callers pre-filter targets.
func (m *Manager) startRollout(rev uint64, policyID, trigger string, targets []string, snap ConfigSnapshot) *Rollout {
	now := time.Now().UTC()
	m.mu.Lock()
	if m.lastRev >= rev && len(m.order) > 0 {
		// Idempotent per revision: a duplicate OnRevision returns the
		// existing rollout instead of double-delivering.
		for i := len(m.order) - 1; i >= 0; i-- {
			if r := m.rollouts[m.order[i]]; r.Revision == rev && trigger == "auto" {
				m.mu.Unlock()
				return r
			}
		}
	}
	ro := &Rollout{
		ID:          "ro-" + uintToHex(rev) + "-" + uintToHex(uint64(now.UnixNano())),
		Revision:    rev,
		PolicyID:    policyID,
		CreatedAt:   now,
		TriggerType: trigger,
		Results:     make(map[string]*RolloutResult, len(targets)),
	}
	for _, id := range targets {
		gw, ok := m.gateways[id]
		if !ok {
			continue
		}
		gw.DesiredRevision = rev
		gw.LastUpdateAt = now
		res := &RolloutResult{GatewayID: id, Status: RollPending, Revision: rev}
		if gw.ConnectionStatus == StatusOffline {
			res.Status = RollOffline
			fin := now
			res.FinishedAt = &fin
		}
		ro.Results[id] = res
		// A newer desired revision supersedes any still-pending older result.
		for _, oid := range m.order {
			old := m.rollouts[oid]
			if old.Revision >= rev {
				continue
			}
			if or := old.Results[id]; or != nil && (or.Status == RollPending || or.Status == RollQueued || or.Status == RollSending) {
				or.Status = RollSuperseded
				fin := now
				or.FinishedAt = &fin
			}
		}
	}
	m.rollouts[ro.ID] = ro
	m.order = append(m.order, ro.ID)
	m.lastRev = rev
	m.lastSnap = &snap
	m.mu.Unlock()

	m.events.add(Event{Timestamp: now, Type: EvPolicyPublished, PolicyID: policyID, Revision: rev, RolloutID: ro.ID, Result: trigger})
	m.events.add(Event{Timestamp: now, Type: EvRolloutCreated, Revision: rev, RolloutID: ro.ID, Result: trigger})

	// Schedule delivery for online targets only; offline targets stay
	// offline in this rollout and are covered by reconciliation on reconnect.
	m.mu.Lock()
	var sched []string
	for id, res := range ro.Results {
		if res.Status != RollPending {
			continue
		}
		res.Status = RollSending
		sched = append(sched, id)
		gw := m.gateways[id]
		gw.LastUpdateAt = now
		t := time.AfterFunc(m.timeout, func() { m.onTimeout(id, rev, ro.ID) })
		m.timers[id] = t
	}
	m.mu.Unlock()
	for _, id := range sched {
		m.events.add(Event{Timestamp: now, GatewayID: id, Revision: rev, RolloutID: ro.ID, Type: EvDeliveryScheduled, Result: RollSending})
		go m.adapter.Publish(context.Background(), id, rev, snap)
	}
	return ro
}

// onTimeout classifies a delivery as timed out. A timeout is NOT a rejection
// and says nothing about whether the gateway applied the configuration.
func (m *Manager) onTimeout(gatewayID string, rev uint64, rolloutID string) {
	now := time.Now().UTC()
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return
	}
	delete(m.timers, gatewayID)
	if _, ok := m.gateways[gatewayID]; !ok {
		m.mu.Unlock()
		return
	}
	ro := m.rollouts[rolloutID]
	if ro == nil {
		m.mu.Unlock()
		return
	}
	res := ro.Results[gatewayID]
	if res == nil || res.Status != RollSending {
		m.mu.Unlock()
		return // already resolved or superseded
	}
	res.Status = RollTimeout
	fin := now
	res.FinishedAt = &fin
	m.mu.Unlock()

	m.events.add(Event{Timestamp: now, GatewayID: gatewayID, Revision: rev, RolloutID: rolloutID,
		Type: EvDeliveryTimeout, Result: RollTimeout,
		Metadata: map[string]string{"note": "no result received within the delivery window"}})
}

// consumeResults drains adapter results and applies them with the stale
// guard. Runs until Stop.
func (m *Manager) consumeResults() {
	for {
		select {
		case <-m.stopCh:
			return
		case res := <-m.adapter.Results():
			m.handleResult(res)
		}
	}
}

// handleResult applies one adapter result. Guards:
//   - unknown gateway (deleted) → ignore;
//   - stale result (revision < current desired) → superseded, gateway untouched;
//   - late result after a timeout classification → gateway state still
//     advances (the gateway did apply it), the rollout result stays timeout.
func (m *Manager) handleResult(res Result) {
	now := time.Now().UTC()
	m.mu.Lock()
	gw, ok := m.gateways[res.GatewayID]
	if !ok {
		m.mu.Unlock()
		return
	}
	ro := m.rolloutForRevision(res.GatewayID, res.Revision)
	stale := res.Revision < gw.DesiredRevision
	var resPtr *RolloutResult
	if ro != nil {
		resPtr = ro.Results[res.GatewayID]
	}
	alreadyDone := resPtr != nil && resPtr.Status != RollSending && resPtr.Status != RollPending && resPtr.Status != RollQueued

	switch {
	case stale:
		if resPtr != nil && !alreadyDone {
			resPtr.Status = RollSuperseded
			fin := now
			resPtr.FinishedAt = &fin
		}
		m.mu.Unlock()
		m.events.add(Event{Timestamp: now, GatewayID: res.GatewayID, Revision: res.Revision,
			RolloutID: rolloutIDOr(ro), Type: EvSuperseded, Result: RollSuperseded,
			Metadata: map[string]string{"desired_revision": uintToHex(gw.DesiredRevision)}})
		return
	}

	evType := EvConfigAccepted
	switch res.Status {
	case RollAccepted:
		gw.ReportedAcceptedRevision = res.Revision
		gw.LastError = nil
		gw.LastHeartbeatAt = now
		gw.LastUpdateAt = now
	case RollRejected:
		evType = EvConfigRejected
		rej := res.Revision
		gw.LastRejectedRevision = &rej
		gw.LastError = res.Err
		gw.LastUpdateAt = now
	default:
		m.mu.Unlock()
		return
	}
	if resPtr != nil && !alreadyDone {
		resPtr.Status = res.Status
		fin := now
		resPtr.FinishedAt = &fin
		resPtr.Error = res.Err
	}
	m.mu.Unlock()

	m.events.add(Event{Timestamp: now, GatewayID: res.GatewayID, Revision: res.Revision,
		RolloutID: rolloutIDOr(ro), Type: evType, Result: res.Status, Error: res.Err})
}

// rolloutForRevision finds the newest rollout targeting this gateway at this
// revision with an unresolved result.
func (m *Manager) rolloutForRevision(gatewayID string, rev uint64) *Rollout {
	for i := len(m.order) - 1; i >= 0; i-- {
		ro := m.rollouts[m.order[i]]
		if ro.Revision != rev {
			continue
		}
		if r := ro.Results[gatewayID]; r != nil {
			return ro
		}
	}
	return nil
}

func rolloutIDOr(ro *Rollout) string {
	if ro == nil {
		return ""
	}
	return ro.ID
}

// heartbeatLoop keeps last_heartbeat_at fresh for online gateways.
// ponytail: operator-driven offline only; automatic connectivity-loss
// detection from heartbeat age is the documented upgrade path.
func (m *Manager) heartbeatLoop() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-t.C:
			now := time.Now().UTC()
			m.mu.Lock()
			for _, gw := range m.gateways {
				if gw.ConnectionStatus == StatusOnline {
					gw.LastHeartbeatAt = now
				}
			}
			m.mu.Unlock()
		}
	}
}
