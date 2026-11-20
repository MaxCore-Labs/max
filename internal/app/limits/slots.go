package limits

import (
	"fmt"
	"maps"
	"sync"

	"max/internal/domain"
)

const minFreeMemoryBytes uint64 = 1024 * 1024 * 1024 // 1 GiB

// Slots tracks per-engine concurrent CLI process limits, interactive reservation, and low-RAM guard.
type Slots struct {
	mu      sync.Mutex
	caps    map[domain.EngineName]int
	used    map[domain.EngineName]int
	enabled map[domain.EngineName]bool
	freeMem func() uint64
}

// NewSlots constructs a Slots limiter. If freeMem is nil, the low-memory guard assumes >= 1 GiB free.
func NewSlots(caps map[domain.EngineName]int, freeMem func() uint64) *Slots {
	cp := make(map[domain.EngineName]int, len(caps))
	en := make(map[domain.EngineName]bool, len(caps))
	for k, v := range caps {
		cp[k] = v
		if v > 0 {
			en[k] = true
		}
	}
	return &Slots{
		caps:    cp,
		used:    make(map[domain.EngineName]int, len(caps)),
		enabled: en,
		freeMem: freeMem,
	}
}

// Free returns remaining process slots per engine (0 for all engines if free RAM < 1 GiB or engine disabled).
func (s *Slots) Free() map[domain.EngineName]int {
	lowRAM := s.freeMem != nil && s.freeMem() < minFreeMemoryBytes

	s.mu.Lock()
	defer s.mu.Unlock()

	out := make(map[domain.EngineName]int, len(s.caps))
	for eng, total := range s.caps {
		if lowRAM || !s.enabled[eng] {
			out[eng] = 0
			continue
		}
		rem := total - s.used[eng]
		if rem < 0 {
			rem = 0
		}
		out[eng] = rem
	}
	return out
}

// Acquire claims one slot on engine e up to its full capacity (equivalent to PrioInteractive).
func (s *Slots) Acquire(e domain.EngineName) bool {
	return s.AcquirePriority(e, domain.PrioInteractive)
}

// AcquirePriority claims one slot on engine e, reserving 1 slot for PrioInteractive whenever total >= 2.
func (s *Slots) AcquirePriority(e domain.EngineName, p domain.Priority) bool {
	if s.freeMem != nil && s.freeMem() < minFreeMemoryBytes {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.enabled[e] {
		return false
	}
	total := s.caps[e]
	if total <= 0 {
		return false
	}
	limit := total
	if total >= 2 && p < domain.PrioInteractive {
		limit = total - 1
	}
	if s.used[e] >= limit {
		return false
	}
	s.used[e]++
	return true
}

// Release frees one held slot on engine e.
func (s *Slots) Release(e domain.EngineName) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.used[e] > 0 {
		s.used[e]--
	}
}

// Snapshot returns current (capacity, used, enabled) maps for /api/v1/status and /api/v1/settings.
func (s *Slots) Snapshot() (caps, used map[domain.EngineName]int, enabled map[domain.EngineName]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.caps), maps.Clone(s.used), maps.Clone(s.enabled)
}

// UpdateEngine atomically updates capacity (1-8) and enabled state of engines, requiring >= 1 enabled engine.
func (s *Slots) UpdateEngine(caps map[domain.EngineName]int, enabled map[domain.EngineName]bool) error {
	for eng, c := range caps {
		if c < 1 || c > 8 {
			return fmt.Errorf("invalid slots %d for engine %s (must be 1-8)", c, eng)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	nextEnabled := maps.Clone(s.enabled)
	for eng, on := range enabled {
		nextEnabled[eng] = on
	}
	activeCount := 0
	for _, on := range nextEnabled {
		if on {
			activeCount++
		}
	}
	if activeCount == 0 {
		return fmt.Errorf("at least one engine must remain enabled")
	}
	for eng, c := range caps {
		s.caps[eng] = c
	}
	s.enabled = nextEnabled
	return nil
}
