package domain_test

import (
	"testing"
	"time"

	"max/internal/domain"
)

func i64(v int64) *int64 { return &v }

func TestAdmit(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	limitedFuture := now.Add(15 * time.Minute)
	limitedPast := now.Add(-1 * time.Minute)

	cases := []struct {
		name    string
		view    domain.QuotaView
		prio    domain.Priority
		bgShare float64
		want    domain.Admission
	}{
		{
			name:    "unknown ceiling admits maintenance",
			view:    domain.QuotaView{UsedShort: 50000},
			prio:    domain.PrioMaintenance,
			bgShare: 0.7,
			want:    domain.AdmitNow,
		},
		{
			name:    "unknown ceiling admits interactive",
			view:    domain.QuotaView{UsedShort: 50000},
			prio:    domain.PrioInteractive,
			bgShare: 0.7,
			want:    domain.AdmitNow,
		},
		{
			name:    "LimitedUntil in future defers interactive",
			view:    domain.QuotaView{UsedShort: 100, EstShort: i64(1000), LimitedUntil: &limitedFuture},
			prio:    domain.PrioInteractive,
			bgShare: 0.7,
			want:    domain.DeferLimited,
		},
		{
			name:    "LimitedUntil in future defers task",
			view:    domain.QuotaView{UsedShort: 100, EstShort: i64(1000), LimitedUntil: &limitedFuture},
			prio:    domain.PrioTask,
			bgShare: 0.7,
			want:    domain.DeferLimited,
		},
		{
			name:    "LimitedUntil in past allows again",
			view:    domain.QuotaView{UsedShort: 100, EstShort: i64(1000), LimitedUntil: &limitedPast},
			prio:    domain.PrioTask,
			bgShare: 0.7,
			want:    domain.AdmitNow,
		},
		{
			name:    "50% used defers maintenance",
			view:    domain.QuotaView{UsedShort: 500, EstShort: i64(1000)},
			prio:    domain.PrioMaintenance,
			bgShare: 0.7,
			want:    domain.DeferShare,
		},
		{
			name:    "50% used still admits routine",
			view:    domain.QuotaView{UsedShort: 500, EstShort: i64(1000)},
			prio:    domain.PrioRoutine,
			bgShare: 0.7,
			want:    domain.AdmitNow,
		},
		{
			name:    "70% used with bgShare=0.7 defers task",
			view:    domain.QuotaView{UsedShort: 700, EstShort: i64(1000)},
			prio:    domain.PrioTask,
			bgShare: 0.7,
			want:    domain.DeferShare,
		},
		{
			name:    "70% used with bgShare=0.7 defers routine",
			view:    domain.QuotaView{UsedShort: 700, EstShort: i64(1000)},
			prio:    domain.PrioRoutine,
			bgShare: 0.7,
			want:    domain.DeferShare,
		},
		{
			name:    "70% used with bgShare=0.7 admits interactive",
			view:    domain.QuotaView{UsedShort: 700, EstShort: i64(1000)},
			prio:    domain.PrioInteractive,
			bgShare: 0.7,
			want:    domain.AdmitNow,
		},
		{
			name:    "100% used still admits interactive unless LimitedUntil set",
			view:    domain.QuotaView{UsedShort: 1000, EstShort: i64(1000)},
			prio:    domain.PrioInteractive,
			bgShare: 0.7,
			want:    domain.AdmitNow,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.Admit(tc.view, tc.prio, tc.bgShare, now)
			if got != tc.want {
				t.Fatalf("Admit = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLearnLimit(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

	learned := domain.LearnLimit(domain.QuotaView{UsedShort: 800}, nil, now)
	if learned.EstShort == nil || *learned.EstShort != 800 {
		t.Fatalf("EstShort = %v, want 800", learned.EstShort)
	}
	wantUntil := now.Add(domain.NextProbe(0))
	if learned.LimitedUntil == nil || !learned.LimitedUntil.Equal(wantUntil) {
		t.Fatalf("LimitedUntil = %v, want %v", learned.LimitedUntil, wantUntil)
	}

	explicitReset := now.Add(2 * time.Hour)
	learned2 := domain.LearnLimit(domain.QuotaView{UsedShort: 1200}, &explicitReset, now)
	if learned2.EstShort == nil || *learned2.EstShort != 1200 {
		t.Fatalf("EstShort = %v, want 1200", learned2.EstShort)
	}
	if learned2.LimitedUntil == nil || !learned2.LimitedUntil.Equal(explicitReset) {
		t.Fatalf("LimitedUntil = %v, want %v", learned2.LimitedUntil, explicitReset)
	}
}

func TestWindowStartMonotonic(t *testing.T) {
	// 2026-11-20 14:15 +07:00 == 07:15 UTC; 5h bucket is [07:00 UTC, 12:00 UTC).
	t1 := time.Date(2026, 11, 20, 14, 15, 0, 0, time.FixedZone("ICT", 7*3600))
	tSame := t1.Add(30 * time.Minute)
	w1 := domain.WindowStart(t1, 5)
	wSame := domain.WindowStart(tSame, 5)
	if w1.Location() != time.UTC {
		t.Fatalf("WindowStart location = %v, want UTC", w1.Location())
	}
	if !w1.Equal(wSame) {
		t.Fatalf("same 5h bucket produced different WindowStart: %v vs %v", w1, wSame)
	}

	tBack := t1.Add(-2 * time.Hour)
	wBack := domain.WindowStart(tBack, 5)
	if wBack.After(w1) {
		t.Fatalf("backward clock jump produced later WindowStart: %v > %v", wBack, w1)
	}
	if wBack.Unix() < 0 {
		t.Fatalf("WindowStart Unix = %d, must be non-negative", wBack.Unix())
	}

	preEpoch := time.Unix(-7200, 0)
	if got := domain.WindowStart(preEpoch, 5); got.Unix() < 0 {
		t.Fatalf("pre-epoch clock jump produced negative window start: %d", got.Unix())
	}
}

func TestNextProbe(t *testing.T) {
	wants := []time.Duration{
		15 * time.Minute,
		30 * time.Minute,
		60 * time.Minute,
		60 * time.Minute,
	}
	for i, want := range wants {
		if got := domain.NextProbe(i); got != want {
			t.Fatalf("NextProbe(%d) = %v, want %v", i, got, want)
		}
	}
}
