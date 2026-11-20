package domain

import "time"

// QuotaView summarizes current usage and learned subscription ceilings for one engine (Spec 8.8.2).
type QuotaView struct {
	UsedShort, UsedWeek int64
	EstShort, EstWeek   *int64
	LimitedUntil        *time.Time
}

// Admission describes whether an engine can accept a wakeup of a given priority now.
type Admission int

const (
	AdmitNow Admission = iota
	DeferShare
	DeferLimited
)

func (a Admission) String() string {
	switch a {
	case AdmitNow:
		return "admit"
	case DeferShare:
		return "defer_share"
	case DeferLimited:
		return "defer_limited"
	default:
		return "unknown"
	}
}

// Admit decides whether priority p may start on an engine with QuotaView v (Spec 8.8.2).
func Admit(v QuotaView, p Priority, bgShare float64, now time.Time) Admission {
	if v.LimitedUntil != nil && now.Before(*v.LimitedUntil) {
		return DeferLimited
	}
	if p >= PrioInteractive {
		return AdmitNow
	}
	if bgShare <= 0 || bgShare > 1 {
		bgShare = 0.7
	}
	if v.EstShort != nil && *v.EstShort > 0 {
		ratio := float64(v.UsedShort) / float64(*v.EstShort)
		if p <= PrioMaintenance && ratio >= 0.50 {
			return DeferShare
		}
		if ratio >= bgShare {
			return DeferShare
		}
	}
	if v.EstWeek != nil && *v.EstWeek > 0 {
		weekRatio := float64(v.UsedWeek) / float64(*v.EstWeek)
		if p <= PrioMaintenance && weekRatio >= 0.50 {
			return DeferShare
		}
		if weekRatio >= bgShare {
			return DeferShare
		}
	}
	return AdmitNow
}

// LearnLimit updates a QuotaView after the CLI reports a provider rate/plan limit.
func LearnLimit(v QuotaView, resetAt *time.Time, now time.Time) QuotaView {
	out := v
	if v.UsedShort > 0 || v.EstShort == nil {
		est := v.UsedShort
		out.EstShort = &est
	}
	if resetAt != nil && resetAt.After(now) {
		r := *resetAt
		out.LimitedUntil = &r
	} else {
		until := now.Add(NextProbe(0))
		out.LimitedUntil = &until
	}
	return out
}

// WindowStart floors now down to a non-negative UTC multiple of hours.
func WindowStart(now time.Time, hours int) time.Time {
	if hours <= 0 {
		hours = 5
	}
	sec := now.UTC().Unix()
	if sec <= 0 {
		return time.Unix(0, 0).UTC()
	}
	winSec := int64(hours) * 3600
	floored := sec - (sec % winSec)
	return time.Unix(floored, 0).UTC()
}

// NextProbe returns the backoff duration before probing a rate-limited engine (15, 30, 60, 60... min).
func NextProbe(attempt int) time.Duration {
	switch {
	case attempt <= 0:
		return 15 * time.Minute
	case attempt == 1:
		return 30 * time.Minute
	default:
		return 60 * time.Minute
	}
}
