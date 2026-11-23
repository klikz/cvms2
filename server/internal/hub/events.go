package hub

import (
	"encoding/json"
	"sync"
	"time"
)

// Events is a tiny pub/sub used to push live updates to browsers.
type Events struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}

	debounceMu sync.Mutex
	pending    map[string]*time.Timer
}

// NewEvents creates a broker.
func NewEvents() *Events {
	return &Events{subs: map[chan []byte]struct{}{}, pending: map[string]*time.Timer{}}
}

// Subscribe returns a channel of JSON events and an unsubscribe func.
func (e *Events) Subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 64)
	e.mu.Lock()
	e.subs[ch] = struct{}{}
	e.mu.Unlock()
	return ch, func() {
		e.mu.Lock()
		if _, ok := e.subs[ch]; ok {
			delete(e.subs, ch)
			close(ch)
		}
		e.mu.Unlock()
	}
}

// Publish sends an event to all subscribers (dropping for slow ones).
func (e *Events) Publish(typ string, data any) {
	b, err := json.Marshal(map[string]any{"type": typ, "data": data})
	if err != nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for ch := range e.subs {
		select {
		case ch <- b:
		default:
		}
	}
}

// PublishDebounced coalesces bursts of the same event type.
func (e *Events) PublishDebounced(typ string) {
	e.debounceMu.Lock()
	defer e.debounceMu.Unlock()
	if _, ok := e.pending[typ]; ok {
		return
	}
	e.pending[typ] = time.AfterFunc(time.Second, func() {
		e.debounceMu.Lock()
		delete(e.pending, typ)
		e.debounceMu.Unlock()
		e.Publish(typ, nil)
	})
}
