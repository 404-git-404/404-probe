package server

import "sync"

type hub struct {
	mu          sync.Mutex
	clients     map[chan []byte]struct{}
	limit       int
	queuedBytes map[chan []byte]int
}

func newHub(limit int) *hub {
	return &hub{clients: make(map[chan []byte]struct{}), queuedBytes: make(map[chan []byte]int), limit: limit}
}

func (h *hub) subscribe() (chan []byte, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.clients) >= h.limit {
		return nil, false
	}
	ch := make(chan []byte, 256)
	h.clients[ch] = struct{}{}
	return ch, true
}

func (h *hub) unsubscribe(ch chan []byte) {
	h.mu.Lock()
	delete(h.clients, ch)
	delete(h.queuedBytes, ch)
	h.mu.Unlock()
}

func (h *hub) publish(message []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		// Bound memory as well as event count. Closing a slow stream forces the
		// existing reconnect/full-read path instead of silently losing config or
		// lifecycle changes among runtime samples.
		if h.queuedBytes[ch]+len(message) > 1<<20 {
			close(ch)
			delete(h.clients, ch)
			delete(h.queuedBytes, ch)
			continue
		}
		copyMessage := append([]byte(nil), message...)
		select {
		case ch <- copyMessage:
			h.queuedBytes[ch] += len(copyMessage)
		default:
			close(ch)
			delete(h.clients, ch)
			delete(h.queuedBytes, ch)
		}
	}
}

func (h *hub) consumed(ch chan []byte, size int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[ch]; ok {
		h.queuedBytes[ch] -= size
		if h.queuedBytes[ch] < 0 {
			h.queuedBytes[ch] = 0
		}
	}
}
