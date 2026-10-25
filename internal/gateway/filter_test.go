package gateway

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

type mockChecker struct {
	blocked bool
}

func (m *mockChecker) IsBlocked(ip string) bool { return m.blocked }

func TestMiddlewareAllows(t *testing.T) {
	checker := &mockChecker{blocked: false}
	handler := Middleware(checker, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestMiddlewareBlocks(t *testing.T) {
	checker := &mockChecker{blocked: true}
	handler := Middleware(checker, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
}

func TestExtractIP(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:54321"
	ip := extractIP(req)
	if ip != "10.0.0.1" {
		t.Fatalf("expected 10.0.0.1, got %s", ip)
	}
}

// SEC-007 regression: a direct client must not be able to spoof its address
// with X-Forwarded-For. The forged header is ignored when the TCP peer is
// not a configured trusted proxy.
func TestExtractIPIgnoresForgedXFFFromUntrustedPeer(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "203.0.113.66:4444"
	req.Header.Set("X-Forwarded-For", "10.0.0.1")
	req.Header.Set("X-Real-IP", "10.0.0.2")
	ip := extractIP(req)
	if ip != "203.0.113.66" {
		t.Fatalf("forged forwarded headers were trusted: got %s, want 203.0.113.66", ip)
	}
}

// With no trusted proxies configured (the default), forwarded headers are
// never honored — even from loopback.
func TestMiddlewareTrustedDefaultsToRemoteAddr(t *testing.T) {
	var seen string
	checker := &captureChecker{seen: &seen}
	handler := MiddlewareTrusted(checker, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "127.0.0.1:9999"
	req.Header.Set("X-Forwarded-For", "8.8.8.8")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if seen != "127.0.0.1" {
		t.Fatalf("expected RemoteAddr to win with empty trust set, got %s", seen)
	}
}

// X-Forwarded-For is honored only from a trusted proxy, and the rightmost
// untrusted hop is used — entries the client appended itself are skipped.
func TestMiddlewareTrustedHonorsXFFFromProxy(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}

	var seen string
	checker := &captureChecker{seen: &seen}
	handler := MiddlewareTrusted(checker, trusted, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Proxy on loopback forwards a client chain; leftmost entries are
	// client-supplied junk, the rightmost untrusted hop is the client.
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 10.0.0.7")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if seen != "10.0.0.7" {
		t.Fatalf("expected rightmost untrusted hop 10.0.0.7, got %s", seen)
	}
}

// An untrusted peer cannot smuggle a blocked address out of a block by
// pairing a forged XFF with a non-proxy source address: the middleware
// evaluates the real peer.
func TestMiddlewareBlocksForgedSource(t *testing.T) {
	checker := &staticChecker{blocked: map[string]bool{"198.51.100.10": true}}
	handler := Middleware(checker, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "198.51.100.10:5555"
	req.Header.Set("X-Forwarded-For", "9.9.9.9")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("blocked peer escaped enforcement: got %d, want 429", rec.Code)
	}
}

type captureChecker struct{ seen *string }

func (c *captureChecker) IsBlocked(ip string) bool { *c.seen = ip; return false }

type staticChecker struct{ blocked map[string]bool }

func (c *staticChecker) IsBlocked(ip string) bool { return c.blocked[ip] }

func TestIPToUint32(t *testing.T) {
	n := ipToUint32("192.168.1.1")
	if n != 0xC0A80101 {
		t.Fatalf("expected 0xC0A80101, got 0x%08X", n)
	}
}
