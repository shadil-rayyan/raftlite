package gateway

import (
	"net/http"
	"net/http/httptest"
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

func TestIPToUint32(t *testing.T) {
	n := ipToUint32("192.168.1.1")
	if n != 0xC0A80101 {
		t.Fatalf("expected 0xC0A80101, got 0x%08X", n)
	}
}
