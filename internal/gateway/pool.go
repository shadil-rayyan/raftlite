package gateway

import (
	"sync"
	"time"
)

type RequestMeta struct {
	IPv4        uint32
	TimestampNs uint64
	RouteID     uint32
	LatencyNs   uint64
}

var metaPool = sync.Pool{
	New: func() any {
		return &RequestMeta{}
	},
}

func AcquireMeta() *RequestMeta {
	return metaPool.Get().(*RequestMeta)
}

func ReleaseMeta(m *RequestMeta) {
	*m = RequestMeta{}
	metaPool.Put(m)
}

func nowUnixNs() uint64 {
	return uint64(time.Now().UnixNano())
}
