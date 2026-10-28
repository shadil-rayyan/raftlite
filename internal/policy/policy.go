// Package policy implements RaftLite's authoritative policy model.
//
// Determinism contract (SRS FR-004, NFR-015): Apply must be a pure function of
// the command bytes. All decision data — IDs, timestamps, expiration — is
// decided once by the proposer and carried inside the replicated command.
// Nodes never read wall-clock time inside Apply; expiration is evaluated at
// lookup time against the committed expires_at timestamp (ADR-0003).
package policy

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"time"
)

// Type identifies the kind of a policy.
type Type string

const (
	TypeIPBlock Type = "ip_block"
	TypeCIDR    Type = "cidr_block"
)

// Status is the lifecycle state of a policy.
type Status string

const (
	StatusActive  Status = "active"
	StatusRevoked Status = "revoked"
)

// Action is the enforceable action of a policy. The MVP supports deny only.
const ActionDeny = "deny"

// Policy is a single authoritative security policy (SRS §7.1).
type Policy struct {
	ID        string     `json:"id"`
	Name      string     `json:"name,omitempty"`
	Type      Type       `json:"type"`
	Target    string     `json:"target"`
	Action    string     `json:"action"`
	Status    Status     `json:"status"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Reason    string     `json:"reason,omitempty"`
	CreatedBy string     `json:"created_by,omitempty"`
	Version   uint64     `json:"version"`

	// parsed target; not serialized
	prefix netip.Prefix
}

// Expired reports whether the policy is expired at the given instant.
// Expiration compares against the committed timestamp only (ADR-0003).
func (p *Policy) Expired(now time.Time) bool {
	return p.ExpiresAt != nil && !now.Before(*p.ExpiresAt)
}

// Effective reports whether the policy is enforceable at the given instant.
func (p *Policy) Effective(now time.Time) bool {
	return p.Status == StatusActive && !p.Expired(now)
}

// Command is a replicated state-machine command. The leader fills every field
// (including ID and timestamps) before proposing; followers apply verbatim.
type Command struct {
	Op        string     `json:"op"` // create | revoke
	ID        string     `json:"id,omitempty"`
	Name      string     `json:"name,omitempty"`
	Type      Type       `json:"type,omitempty"`
	Target    string     `json:"target,omitempty"`
	Reason    string     `json:"reason,omitempty"`
	CreatedBy string     `json:"created_by,omitempty"`
	CreatedAt time.Time  `json:"created_at,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	IdemKey   string     `json:"idem_key,omitempty"`
}

const (
	OpCreate = "create"
	OpRevoke = "revoke"
)

// Errors returned by the engine. All are deterministic across replicas.
var (
	ErrUnknownOp     = errors.New("unknown command op")
	ErrMissingID     = errors.New("missing policy id")
	ErrInvalidTarget = errors.New("invalid policy target")
	ErrBadRevoke     = errors.New("revoke requires policy id")
)

// Validate checks a command's static fields without applying it.
func (c Command) Validate() error {
	switch c.Op {
	case OpCreate:
		if c.ID == "" {
			return ErrMissingID
		}
		if _, err := netip.ParsePrefix(c.Target); err != nil {
			if _, err2 := netip.ParseAddr(c.Target); err2 != nil {
				return fmt.Errorf("%w: %q", ErrInvalidTarget, c.Target)
			}
			return nil
		}
		return nil
	case OpRevoke:
		if c.ID == "" {
			return ErrBadRevoke
		}
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrUnknownOp, c.Op)
	}
}

// IdemResult records the replicated outcome of a previously seen idempotency
// key (SRS FR-016). Stored in policy state so retries survive failover.
type IdemResult struct {
	Op       string `json:"op"`
	PolicyID string `json:"policy_id"`
}

// State is the full serializable engine state (snapshot payload).
type State struct {
	Revision   uint64                  `json:"revision"`
	Policies   map[string]*Policy      `json:"policies"`
	Idemotency map[string]IdemResult   `json:"idemotency,omitempty"`
}

// Decision is the result of a lookup.
type Decision struct {
	Blocked bool
	Policy  *Policy
}

// Engine is the deterministic policy state machine.
type Engine struct {
	revision   uint64
	policies   map[string]*Policy
	idemotency map[string]IdemResult
}

// NewEngine returns an empty engine.
func NewEngine() *Engine {
	return &Engine{
		policies:   make(map[string]*Policy),
		idemotency: make(map[string]IdemResult),
	}
}

// NewID generates a random policy identifier. Called only on the proposer;
// the resulting ID is carried in the replicated command.
func NewID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "pol-" + hex.EncodeToString(b[:]), nil
}

// parseTarget accepts either a bare IP (treated as /32 or /128) or a CIDR.
func parseTarget(s string) (netip.Prefix, error) {
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// Apply executes a command. Identical command sequences must produce
// identical state on every replica (FR-004).
func (e *Engine) Apply(cmd Command, revision uint64) (idem *IdemResult, err error) {
	if err := cmd.Validate(); err != nil {
		return nil, err
	}
	// Idempotency: a seen key returns the original outcome without re-applying.
	if cmd.IdemKey != "" {
		if prev, ok := e.idemotency[cmd.IdemKey]; ok {
			p := prev
			return &p, nil
		}
	}
	defer func() {
		if err == nil && cmd.IdemKey != "" {
			res := IdemResult{Op: cmd.Op, PolicyID: cmd.ID}
			if idem != nil {
				res = *idem
			}
			e.idemotency[cmd.IdemKey] = res
		}
	}()

	switch cmd.Op {
	case OpCreate:
		prefix, perr := parseTarget(cmd.Target)
		if perr != nil {
			return nil, fmt.Errorf("%w: %q", ErrInvalidTarget, cmd.Target)
		}
		typ := TypeIPBlock
		if prefix.Bits() != prefix.Addr().BitLen() {
			typ = TypeCIDR
		}
		p := &Policy{
			ID:        cmd.ID,
			Name:      cmd.Name,
			Type:      typ,
			Target:    cmd.Target,
			Action:    ActionDeny,
			Status:    StatusActive,
			CreatedAt: cmd.CreatedAt,
			ExpiresAt: cmd.ExpiresAt,
			Reason:    cmd.Reason,
			CreatedBy: cmd.CreatedBy,
			Version:   revision,
			prefix:    prefix,
		}
		e.policies[cmd.ID] = p
		e.revision = revision
		return &IdemResult{Op: OpCreate, PolicyID: cmd.ID}, nil

	case OpRevoke:
		p, ok := e.policies[cmd.ID]
		if !ok {
			// Revoking an unknown id is a deterministic no-op; the API layer
			// decides how to surface it. Never corrupts state.
			e.revision = revision
			return &IdemResult{Op: OpRevoke, PolicyID: cmd.ID}, nil
		}
		p.Status = StatusRevoked
		p.Version = revision
		e.revision = revision
		return &IdemResult{Op: OpRevoke, PolicyID: cmd.ID}, nil

	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownOp, cmd.Op)
	}
}

// Lookup evaluates a client address at the given instant.
//
// Deterministic precedence (SRS §7.2), independent of map iteration order:
//  1. exact-IP policies before CIDR policies;
//  2. longer CIDR prefixes before shorter ones;
//  3. lexicographic policy ID as the final tie-break.
func (e *Engine) Lookup(addr netip.Addr, now time.Time) Decision {
	if !addr.IsValid() {
		return Decision{}
	}

	candidates := make([]*Policy, 0, 8)
	for _, p := range e.policies {
		if !p.Effective(now) {
			continue
		}
		if p.prefix.Contains(addr) {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) == 0 {
		return Decision{}
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.prefix.Bits() != b.prefix.Bits() {
			return a.prefix.Bits() > b.prefix.Bits()
		}
		return a.ID < b.ID
	})
	best := candidates[0]
	return Decision{Blocked: true, Policy: best}
}

// LookupString parses the address and evaluates it.
func (e *Engine) LookupString(s string, now time.Time) Decision {
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return Decision{}
	}
	return e.Lookup(addr, now)
}

// Get returns a policy by id regardless of status.
func (e *Engine) Get(id string) (*Policy, bool) {
	p, ok := e.policies[id]
	return p, ok
}

// List returns policies visible at the given instant, sorted by ID.
// includeInactive adds revoked and expired policies to the result.
func (e *Engine) List(now time.Time, includeInactive bool) []*Policy {
	out := make([]*Policy, 0, len(e.policies))
	for _, p := range e.policies {
		if includeInactive || p.Effective(now) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Revision is the last applied revision.
func (e *Engine) Revision() uint64 { return e.revision }

// CountActive returns the number of enforceable policies at the instant.
func (e *Engine) CountActive(now time.Time) int {
	n := 0
	for _, p := range e.policies {
		if p.Effective(now) {
			n++
		}
	}
	return n
}

// Snapshot serializes the full engine state.
func (e *Engine) Snapshot() ([]byte, error) {
	return json.Marshal(State{
		Revision:   e.revision,
		Policies:   e.policies,
		Idemotency: e.idemotency,
	})
}

// Restore replaces the engine state from a snapshot. Policies are re-parsed
// from their targets; a corrupt target skips the policy rather than failing
// the whole restore (the command log remains the source of truth).
func (e *Engine) Restore(data []byte) error {
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return err
	}
	if st.Policies == nil {
		st.Policies = make(map[string]*Policy)
	}
	if st.Idemotency == nil {
		st.Idemotency = make(map[string]IdemResult)
	}
	for _, p := range st.Policies {
		prefix, err := parseTarget(p.Target)
		if err != nil {
			continue
		}
		p.prefix = prefix
	}
	e.revision = st.Revision
	e.policies = st.Policies
	e.idemotency = st.Idemotency
	return nil
}

// LegacyCommand supports the prototype's "add:x.y.z.w" / "del:x.y.z.w"
// command strings so existing logs and tests keep applying. New writes must
// use structured Commands.
func LegacyCommand(s string) (Command, bool) {
	if len(s) > 4 && s[:4] == "add:" {
		target := s[4:]
		id := "pol-legacy-" + target
		return Command{Op: OpCreate, ID: id, Target: target, Reason: "legacy command"}, true
	}
	if len(s) > 4 && s[:4] == "del:" {
		target := s[4:]
		id := "pol-legacy-" + target
		return Command{Op: OpRevoke, ID: id}, true
	}
	return Command{}, false
}
