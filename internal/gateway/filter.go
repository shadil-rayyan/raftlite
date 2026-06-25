package gateway

import (
	"net/http"
	"strconv"
	"strings"
)

type BlocklistChecker interface {
	IsBlocked(ip string) bool
}

func Middleware(checker BlocklistChecker, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := extractIP(r)
		if checker.IsBlocked(ip) {
			http.Error(w, `{"error":"too many requests"}`, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func extractIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	host := r.RemoteAddr
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		host = host[:idx]
	}
	return host
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
