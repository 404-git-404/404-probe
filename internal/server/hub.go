package server

import "sync"

type hub struct {
	mu      sync.Mutex
	clients map[chan []byte]struct{}
	limit   int
}

func newHub(limit int) *hub { return &hub{clients: make(map[chan []byte]struct{}), limit: limit} }

func (h *hub) subscribe() (chan []byte, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.clients) >= h.limit {
		return nil, false
	}
	ch := make(chan []byte, 1)
	h.clients[ch] = struct{}{}
	return ch, true
}

func (h *hub) unsubscribe(ch chan []byte) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
}

func (h *hub) publish(message []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		copyMessage := append([]byte(nil), message...)
		select {
		case ch <- copyMessage:
		default:
		}
	}
}
