// Package gateway provides in-process request enforcement: the blocklist
// middleware decides whether a request's source address is blocked.
package gateway

import (
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
)

type BlocklistChecker interface {
	IsBlocked(ip string) bool
}

// Middleware enforces the blocklist using the untrusted default: the client
// address is taken from RemoteAddr only (SEC-007). Request headers are never
// consulted unless a trusted-proxy set is configured via MiddlewareTrusted.
func Middleware(checker BlocklistChecker, next http.Handler) http.Handler {
	return MiddlewareTrusted(checker, nil, next)
}

// MiddlewareTrusted enforces the blocklist with an explicit trust boundary.
// X-Forwarded-For / X-Real-IP are honored ONLY when the direct peer
// (RemoteAddr) is inside trustedProxies; otherwise those headers are
// attacker-controlled and ignored. trustedProxies is a set of CIDR prefixes
// (e.g. 127.0.0.1/32, 10.0.0.0/8) — empty means "trust nobody".
func MiddlewareTrusted(checker BlocklistChecker, trustedProxies []netip.Prefix, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := extractIPTrusted(r, trustedProxies)
		if checker.IsBlocked(ip) {
			http.Error(w, `{"error":"too many requests"}`, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// extractIP returns the request's source address, trusting only the TCP peer.
// Retained for tests and single-hop deployments; it never reads forwarded
// headers, so a client cannot forge its way out of an IP block.
func extractIP(r *http.Request) string {
	return extractIPTrusted(r, nil)
}

// extractIPTrusted applies the trusted-proxy rule (SEC-007).
func extractIPTrusted(r *http.Request, trustedProxies []netip.Prefix) string {
	remote := hostOnly(r.RemoteAddr)

	if !isTrustedProxy(remote, trustedProxies) {
		// Direct peer is not a configured proxy: headers are untrusted.
		return remote
	}
	// From here on the direct peer is a proxy. X-Forwarded-For appends the
	// client address to the right; entries to the left of the trusted hops
	// are client-supplied. Walk from the right past trusted proxies to find
	// the first untrusted hop — that is the real client.
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			cand := strings.TrimSpace(parts[i])
			if !isTrustedProxy(cand, trustedProxies) {
				return cand
			}
		}
		// Entire chain trusted: leftmost entry is the originating client.
		return strings.TrimSpace(parts[0])
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	return remote
}

func isTrustedProxy(ip string, trusted []netip.Prefix) bool {
	if len(trusted) == 0 {
		return false
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	for _, p := range trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// hostOnly strips a v4/v6 "ip:port" or "[ip]:port" address down to the IP.
func hostOnly(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

func ipToUint32(ip string) uint32 {
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return 0
	}
	var n uint32
	for i, p := range parts {
		v, _ := strconv.Atoi(p)
		n |= uint32(v) << (8 * (3 - i))
	}
	return n
}

func routeID(r *http.Request) uint32 {
	path := r.URL.Path
	var h uint32
	for i := 0; i < len(path); i++ {
		h = h*31 + uint32(path[i])
	}
	return h
}
