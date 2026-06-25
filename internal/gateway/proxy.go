package gateway

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

type Proxy struct {
	handler      http.Handler
	upstream     *url.URL
	reverseProxy *httputil.ReverseProxy
	unixSockPath string
}

func NewProxy(checker BlocklistChecker, upstreamURL string, unixSockPath string) (*Proxy, error) {
	u, err := url.Parse(upstreamURL)
	if err != nil {
		return nil, err
	}

	p := &Proxy{
		upstream:     u,
		unixSockPath: unixSockPath,
	}
	p.reverseProxy = httputil.NewSingleHostReverseProxy(u)
	p.handler = Middleware(checker, http.HandlerFunc(p.serveReverseProxy))
	return p, nil
}

func (p *Proxy) Handler() http.Handler {
	return p.handler
}

func (p *Proxy) serveReverseProxy(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	meta := AcquireMeta()
	meta.IPv4 = ipToUint32(extractIP(r))
	meta.TimestampNs = nowUnixNs()
	meta.RouteID = routeID(r)

	p.writeTelemetry(meta)
	ReleaseMeta(meta)

	p.reverseProxy.ServeHTTP(w, r)

	meta2 := AcquireMeta()
	meta2.LatencyNs = uint64(time.Since(start).Nanoseconds())
	p.writeTelemetry(meta2)
	ReleaseMeta(meta2)
}

func (p *Proxy) writeTelemetry(meta *RequestMeta) {
	if p.unixSockPath == "" {
		return
	}
	// ponytail: Unix socket write to Rust drain, implement when Rust side exists
	_ = meta
}
