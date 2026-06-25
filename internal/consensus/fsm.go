package consensus

import "sync"

type FSM struct {
	mu        sync.Mutex
	blocklist map[string]bool
}

func NewFSM() *FSM {
	return &FSM{blocklist: make(map[string]bool)}
}

func (f *FSM) Apply(data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// ponytail: simple serialized command format "add:1.2.3.4" / "del:1.2.3.4"
	key := string(data)
	if len(key) > 4 && key[:4] == "add:" {
		f.blocklist[key[4:]] = true
	} else if len(key) > 4 && key[:4] == "del:" {
		delete(f.blocklist, key[4:])
	}
}

func (f *FSM) IsBlocked(ip string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.blocklist[ip]
}

func (f *FSM) Snapshot() map[string]bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make(map[string]bool, len(f.blocklist))
	for k, v := range f.blocklist {
		cp[k] = v
	}
	return cp
}

func (f *FSM) Restore(snap map[string]bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blocklist = snap
}
