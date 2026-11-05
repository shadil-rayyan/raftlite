// Command gen generates the shared policy datasets used by both systems.
// Output is deterministic: policies-100.json, policies-1000.json and
// policies-10000.json are nested prefixes of one master sequence, so a
// smaller dataset is always an exact prefix of the larger one.
//
// Datasets are BLOCK-only exact IPs: RaftLite's policy model is a block
// list, so a shared ALLOW workload would not be representable on both
// sides. (ALLOW remains supported by the Traditional server for
// single-system demos, but is out of scope for the comparison.)
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shadil-rayyan/traditional-vs-raftlite-evaluation/internal/policy"
)

func main() {
	out := flag.String("out", "datasets", "output directory")
	maxPolicies := flag.Int("max", 10000, "master sequence length")
	flag.Parse()

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fatal(err)
	}

	master := generate(*maxPolicies)
	for _, size := range []int{100, 1000, 10000} {
		if size > len(master) {
			continue
		}
		ds := policy.Dataset{
			Name:     fmt.Sprintf("policies-%d", size),
			Policies: master[:size],
		}
		path := filepath.Join(*out, fmt.Sprintf("policies-%d.json", size))
		b, err := json.MarshalIndent(ds, "", "  ")
		if err != nil {
			fatal(err)
		}
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			fatal(err)
		}
		fmt.Printf("wrote %s (%d policies)\n", path, size)
	}
}

// generate builds a deterministic, duplicate-free sequence of exact
// IPv4 BLOCK policies spread across five documentation prefixes.
func generate(n int) []policy.Policy {
	prefixes := [][2]int{
		{192, 168}, {10, 0}, {172, 16}, {203, 0}, {198, 51},
	}
	suffix := [][2]int{
		{113, 0}, // 203.0.113.0/24  (TEST-NET-3)
		{100, 0}, // 198.51.100.0/24 (TEST-NET-2)
	}
	out := make([]policy.Policy, 0, n)
	for i := 0; i < n; i++ {
		p := prefixes[i%len(prefixes)]
		idx := i / len(prefixes)
		var ip string
		if p[0] == 203 || p[0] == 198 {
			// TEST-NET ranges keep their fixed second/third octet base
			// and extend deterministically: 203.0.113.x, 203.0.114.x, …
			s := suffix[0]
			if p[0] == 198 {
				s = suffix[1]
			}
			ip = fmt.Sprintf("%d.%d.%d.%d", p[0], p[1], s[0]+idx/256, idx%256)
		} else {
			ip = fmt.Sprintf("%d.%d.%d.%d", p[0], p[1], idx/256, idx%256)
		}
		out = append(out, policy.Policy{
			ID:     fmt.Sprintf("P%05d", i),
			IP:     ip,
			Action: "BLOCK",
		})
	}
	return out
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "gen:", err)
	os.Exit(1)
}
