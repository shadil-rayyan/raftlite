// Package policy defines the shared policy model and the backend contract
// used identically by both the Traditional server and the RaftLite adapter.
//
// Backend contract (identical response shape for both systems):
//
//	GET /v1/policies
//	→ {"policies": [...], "revision": N}
//
// Keeping this shape identical is deliberate: the gateway code that talks
// to a backend is literally the same in both environments, so any measured
// difference comes from the policy-management architecture, not from
// gateway logic.
package policy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Policy is one security policy record.
type Policy struct {
	ID     string `json:"id"`
	IP     string `json:"ip"`     // exact IP ("192.168.1.50") or CIDR ("10.0.0.0/24")
	Action string `json:"action"` // "BLOCK" or "ALLOW"
}

// ListResponse is the uniform backend response.
type ListResponse struct {
	Policies []Policy `json:"policies"`
	Revision uint64   `json:"revision"`
}

// NormalizeAction maps the vocabulary of each backend to BLOCK/ALLOW.
// RaftLite represents blocks as action "deny"; the Traditional server
// stores BLOCK directly. Unknown values are left as-is.
func NormalizeAction(a string) string {
	switch strings.ToLower(a) {
	case "block", "deny":
		return "BLOCK"
	case "allow", "permit":
		return "ALLOW"
	}
	return strings.ToUpper(a)
}

// SortByID orders a list deterministically so comparisons are stable.
func SortByID(p []Policy) {
	sort.Slice(p, func(i, j int) bool { return p[i].ID < p[j].ID })
}

// Diff returns the IDs present in current but not in previous, and the IDs
// present in previous but not in current. It is how a gateway discovers
// which policies are new since its last poll.
func Diff(previous, current []Policy) (added, removed []string) {
	prev := make(map[string]bool, len(previous))
	for _, p := range previous {
		prev[p.ID] = true
	}
	cur := make(map[string]bool, len(current))
	for _, p := range current {
		cur[p.ID] = true
	}
	for _, p := range current {
		if !prev[p.ID] {
			added = append(added, p.ID)
		}
	}
	for _, p := range previous {
		if !cur[p.ID] {
			removed = append(removed, p.ID)
		}
	}
	return added, removed
}

// Dataset is a generated policy sequence used for experiments. Both
// systems receive the identical dataset in the identical order.
type Dataset struct {
	Name     string   `json:"name"`
	Policies []Policy `json:"policies"`
}

// LoadDataset reads a dataset JSON file ({"name": ..., "policies": [...]}).
func LoadDataset(path string) (*Dataset, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ds Dataset
	if err := json.Unmarshal(b, &ds); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if ds.Name == "" {
		ds.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	return &ds, nil
}
