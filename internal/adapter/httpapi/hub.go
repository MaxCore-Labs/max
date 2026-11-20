package httpapi

import (
	"sync"
	"time"

	"max/internal/app"
	"max/internal/domain"
	"max/internal/platform"
)

const (
	maxRunTextBytes = 256 * 1024
	defaultPingStep = 20 * time.Second
)

type runStream struct {
	buf      []byte
	watchers map[uint64]chan string
}

// Hub holds transient in-RAM SSE state: 256 KB tail-capped live run text and 1Hz status counter throttling.
type Hub struct {
	bus   *platform.Bus
	clock app.Clock

	mu         sync.Mutex
	nextWatch  uint64
	runs       map[domain.ID]*runStream
	lastStatus time.Time
	pendingSt  any
	timerSet   bool
	pingSource func() (<-chan time.Time, func())
}

// NewHub constructs an SSE Hub.
func NewHub(bus *platform.Bus, clock app.Clock) *Hub {
	if bus == nil {
		bus = platform.NewBus()
	}
	if clock == nil {
		clock = app.SystemClock{}
	}
	return &Hub{
		bus:   bus,
		clock: clock,
		runs:  make(map[domain.ID]*runStream),
		pingSource: func() (<-chan time.Time, func()) {
			tk := time.NewTicker(defaultPingStep)
			return tk.C, tk.Stop
		},
	}
}

// SetPingSource overrides the 20-second keepalive ticker (used by deterministic SSE tests).
func (h *Hub) SetPingSource(fn func() (<-chan time.Time, func())) {
	h.mu.Lock()
	h.pingSource = fn
	h.mu.Unlock()
}

func (h *Hub) newPingChan() (<-chan time.Time, func()) {
	h.mu.Lock()
	fn := h.pingSource
	h.mu.Unlock()
	return fn()
}

// AppendRunText appends streaming model text for runID, keeping at most the latest 256 KB in RAM.
func (h *Hub) AppendRunText(runID domain.ID, delta string) {
	if delta == "" {
		return
	}
	clean := platform.Redact(delta)

	h.mu.Lock()
	defer h.mu.Unlock()

	rs := h.runs[runID]
	if rs == nil {
		rs = &runStream{watchers: make(map[uint64]chan string)}
		h.runs[runID] = rs
	}
	rs.buf = append(rs.buf, clean...)
	if len(rs.buf) > maxRunTextBytes {
		rs.buf = append([]byte(nil), rs.buf[len(rs.buf)-maxRunTextBytes:]...)
	}

	for id, ch := range rs.watchers {
		select {
		case ch <- clean:
		default:
			close(ch)
			delete(rs.watchers, id)
		}
	}
}

// ClearRunText drops buffered live text and closes active streamRun watchers when a run finishes.
func (h *Hub) ClearRunText(runID domain.ID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	rs, ok := h.runs[runID]
	if !ok {
		return
	}
	for id, ch := range rs.watchers {
		close(ch)
		delete(rs.watchers, id)
	}
	delete(h.runs, runID)
}

// RunText returns the current <= 256 KB tail snapshot for runID plus a live delta channel and cancel func.
func (h *Hub) RunText(runID domain.ID) (snapshot string, deltas <-chan string, cancel func()) {
	h.mu.Lock()
	rs := h.runs[runID]
	if rs == nil {
		rs = &runStream{watchers: make(map[uint64]chan string)}
		h.runs[runID] = rs
	}
	snap := string(rs.buf)
	h.nextWatch++
	wid := h.nextWatch
	ch := make(chan string, 128)
	rs.watchers[wid] = ch
	h.mu.Unlock()

	cancelFn := func() {
		h.mu.Lock()
		if cur, ok := h.runs[runID]; ok {
			if c, exists := cur.watchers[wid]; exists {
				close(c)
				delete(cur.watchers, wid)
			}
		}
		h.mu.Unlock()
	}
	return snap, ch, cancelFn
}

// PublishStatus publishes aggregate status counters to topic "status" at most once per second.
func (h *Hub) PublishStatus(data any) {
	now := h.clock.Now()

	h.mu.Lock()
	elapsed := now.Sub(h.lastStatus)
	if elapsed >= time.Second {
		h.lastStatus = now
		h.pendingSt = nil
		h.mu.Unlock()
		h.bus.Publish(platform.Event{Topic: "status", Type: "status", Data: data})
		return
	}

	h.pendingSt = data
	if h.timerSet {
		h.mu.Unlock()
		return
	}
	h.timerSet = true
	wait := time.Second - elapsed
	h.mu.Unlock()

	time.AfterFunc(wait, func() {
		h.mu.Lock()
		latest := h.pendingSt
		h.pendingSt = nil
		h.timerSet = false
		h.lastStatus = h.clock.Now()
		h.mu.Unlock()
		if latest != nil {
			h.bus.Publish(platform.Event{Topic: "status", Type: "status", Data: latest})
		}
	})
}
