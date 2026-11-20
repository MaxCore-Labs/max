package domain

import (
	"testing"
	"time"
)

func TestDueSlots(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}

	// Monday 2026-11-16 08:00:30 EST
	mon0759 := time.Date(2026, 11, 16, 7, 59, 0, 0, ny)
	mon0800 := time.Date(2026, 11, 16, 8, 0, 0, 0, ny)
	mon080030 := time.Date(2026, 11, 16, 8, 0, 30, 0, ny)
	mon0801 := time.Date(2026, 11, 16, 8, 1, 0, 0, ny)
	thu1030 := time.Date(2026, 11, 19, 10, 30, 0, 0, ny) // 3 days downtime (Tue, Wed, Thu 08:00 missed)
	thu0800 := time.Date(2026, 11, 19, 8, 0, 0, 0, ny)

	// 1. 0 8 * * 1-5 at 08:00:30 Monday -> 1 slot (08:00:00)
	slots, err := DueSlots("0 8 * * 1-5", ny, mon0759, mon080030, "run_once")
	if err != nil || len(slots) != 1 || !slots[0].Equal(mon0800) {
		t.Fatalf("Monday 08:00:30: got %v (err=%v), want [%v]", slots, err, mon0800)
	}

	// 2. Called again at 08:01 after firing 08:00 -> empty
	slots, err = DueSlots("0 8 * * 1-5", ny, mon080030, mon0801, "run_once")
	if err != nil || len(slots) != 0 {
		t.Fatalf("Monday 08:01 repeat: got %v (err=%v), want empty", slots, err)
	}

	// 3. 3-day downtime with catch_up: run_once -> exactly 1 slot (most recent: Thu 08:00)
	slots, err = DueSlots("0 8 * * 1-5", ny, mon080030, thu1030, "run_once")
	if err != nil || len(slots) != 1 || !slots[0].Equal(thu0800) {
		t.Fatalf("3-day downtime run_once: got %v (err=%v), want [%v]", slots, err, thu0800)
	}

	// 4. 3-day downtime with catch_up: skip -> empty
	slots, err = DueSlots("0 8 * * 1-5", ny, mon080030, thu1030, "skip")
	if err != nil || len(slots) != 0 {
		t.Fatalf("3-day downtime skip: got %v (err=%v), want empty", slots, err)
	}

	// 5. Clock jumped backward 1 hour -> zero slots re-fired
	slots, err = DueSlots("0 8 * * 1-5", ny, mon080030, mon080030.Add(-time.Hour), "run_once")
	if err != nil || len(slots) != 0 {
		t.Fatalf("backward clock jump: got %v (err=%v), want empty", slots, err)
	}

	// 6. DST fall-back (2026-11-01 in America/New_York): 01:30 EDT -> 01:30 EST must not double-fire daily job
	dstBefore := time.Date(2026, 11, 1, 0, 59, 0, 0, ny)
	dstFirst := time.Date(2026, 11, 1, 3, 0, 10, 0, ny)
	slots, err = DueSlots("0 3 * * *", ny, dstBefore, dstFirst, "run_once")
	if err != nil || len(slots) != 1 {
		t.Fatalf("DST post-transition: got %v (err=%v), want 1 slot", slots, err)
	}
	slots2, err := DueSlots("0 3 * * *", ny, dstFirst, dstFirst.Add(30*time.Minute), "run_once")
	if err != nil || len(slots2) != 0 {
		t.Fatalf("DST repeat check: got %v (err=%v), want 0 slots", slots2, err)
	}
}
