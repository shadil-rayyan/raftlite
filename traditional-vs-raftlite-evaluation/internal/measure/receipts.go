package measure

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/shadil-rayyan/traditional-vs-raftlite-evaluation/internal/gateway"
)

// ReceiptHub receives receipt POSTs from gateways and fans them out to
// active experiment subscriptions and to the SSE stream.
type ReceiptHub struct {
	mu     sync.Mutex
	nextID int
	subs   map[int]chan gateway.Receipt
}

// NewReceiptHub creates an empty hub.
func NewReceiptHub() *ReceiptHub {
	return &ReceiptHub{subs: map[int]chan gateway.Receipt{}}
}

// HandlePost is the HTTP handler gateways POST receipts to.
func (h *ReceiptHub) HandlePost(w http.ResponseWriter, r *http.Request) {
	var rc gateway.Receipt
	if err := json.NewDecoder(r.Body).Decode(&rc); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if rc.GatewayID == "" || rc.IP == "" {
		http.Error(w, "gateway_id and ip required", http.StatusBadRequest)
		return
	}
	h.publish(rc)
	w.WriteHeader(http.StatusNoContent)
}

// publish sends a receipt to every subscriber (buffered; slow consumers
// drop rather than block the gateways).
func (h *ReceiptHub) publish(rc gateway.Receipt) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subs {
		select {
		case ch <- rc:
		default:
		}
	}
}

// Subscribe returns a receipt channel and an unsubscribe func. Each
// experiment subscribes for the duration of its run.
func (h *ReceiptHub) Subscribe() (<-chan gateway.Receipt, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextID++
	id := h.nextID
	ch := make(chan gateway.Receipt, 1024)
	h.subs[id] = ch
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, id)
		h.mu.Unlock()
	}
}
