package platform

import "sync"

// Event is an in-process topic-scoped notification delivered over SSE.
type Event struct {
	Topic string
	Type  string
	Data  any
}

type subscriber struct {
	topics map[string]bool
	ch     chan Event
	closed bool
}

// Bus is a non-blocking topic pub/sub bus that drops slow subscribers instead of stalling publishers.
type Bus struct {
	mu     sync.Mutex
	nextID uint64
	subs   map[uint64]*subscriber
}

// NewBus constructs an Event Bus.
func NewBus() *Bus {
	return &Bus{
		subs: make(map[uint64]*subscriber),
	}
}

// Publish delivers e to all subscribers of e.Topic without blocking; overflowing subscribers are closed and dropped.
func (b *Bus) Publish(e Event) {
	if e.Topic == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	for id, sub := range b.subs {
		if sub.closed || !sub.topics[e.Topic] {
			continue
		}
		select {
		case sub.ch <- e:
		default:
			sub.closed = true
			close(sub.ch)
			delete(b.subs, id)
		}
	}
}

// Subscribe registers a buffered channel for topics and returns (events, cancel).
func (b *Bus) Subscribe(topics []string, buf int) (<-chan Event, func()) {
	if buf <= 0 {
		buf = 256
	}
	tset := make(map[string]bool, len(topics))
	for _, t := range topics {
		if t != "" {
			tset[t] = true
		}
	}
	sub := &subscriber{
		topics: tset,
		ch:     make(chan Event, buf),
	}

	b.mu.Lock()
	b.nextID++
	id := b.nextID
	b.subs[id] = sub
	b.mu.Unlock()

	cancel := func() {
		b.mu.Lock()
		if cur, ok := b.subs[id]; ok && !cur.closed {
			cur.closed = true
			close(cur.ch)
			delete(b.subs, id)
		}
		b.mu.Unlock()
	}
	return sub.ch, cancel
}
