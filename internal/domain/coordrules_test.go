package domain

import (
	"errors"
	"testing"
)

func TestCheckDelegate(t *testing.T) {
	cases := []struct {
		name string
		chk  CoordCheck
		want error
	}{
		{
			name: "same team within caps allowed",
			chk: CoordCheck{
				Depth:      4,
				Hops:       19,
				Run:        RunCounters{Delegates: 9},
				SenderTeam: "marketing",
				TargetTeam: "marketing",
			},
			want: nil,
		},
		{
			name: "depth 5 rejected because child would be depth 6",
			chk: CoordCheck{
				Depth:      5,
				Hops:       0,
				SenderTeam: "marketing",
				TargetTeam: "marketing",
			},
			want: ErrDepth,
		},
		{
			name: "hops 20 rejected",
			chk: CoordCheck{
				Depth:      1,
				Hops:       20,
				SenderTeam: "marketing",
				TargetTeam: "marketing",
			},
			want: ErrHops,
		},
		{
			name: "10 delegates already in run rejected",
			chk: CoordCheck{
				Depth:      1,
				Hops:       2,
				Run:        RunCounters{Delegates: 10},
				SenderTeam: "marketing",
				TargetTeam: "marketing",
			},
			want: ErrRunCap,
		},
		{
			name: "cross-team non-manager non-chief rejected",
			chk: CoordCheck{
				Depth:      1,
				Hops:       2,
				SenderTeam: "marketing",
				TargetTeam: "sales",
			},
			want: ErrNotAllowedTarget,
		},
		{
			name: "cross-team direct manager allowed",
			chk: CoordCheck{
				Depth:           1,
				Hops:            2,
				SenderTeam:      "marketing",
				TargetTeam:      "core",
				TargetIsManager: true,
			},
			want: nil,
		},
		{
			name: "chief-of-staff cross-team allowed",
			chk: CoordCheck{
				Depth:         1,
				Hops:          2,
				SenderTeam:    "core",
				TargetTeam:    "sales",
				SenderIsChief: true,
			},
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckDelegate(tc.chk)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("got err %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCheckMessage(t *testing.T) {
	cases := []struct {
		name string
		chk  CoordCheck
		want error
	}{
		{
			name: "under limits allowed",
			chk: CoordCheck{
				Hops:             19,
				Run:              RunCounters{Messages: 9},
				PairMsgsLastHour: 5,
			},
			want: nil,
		},
		{
			name: "hops 20 rejected",
			chk: CoordCheck{
				Hops: 20,
			},
			want: ErrHops,
		},
		{
			name: "10 messages in run rejected",
			chk: CoordCheck{
				Hops: 1,
				Run:  RunCounters{Messages: 10},
			},
			want: ErrRunCap,
		},
		{
			name: "pair already sent 6 messages in last hour rejected",
			chk: CoordCheck{
				Hops:             1,
				PairMsgsLastHour: 6,
			},
			want: ErrPairRate,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckMessage(tc.chk)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("got err %v, want %v", err, tc.want)
			}
		})
	}
}

func TestIndirectLoop(t *testing.T) {
	// Simulate A -> B -> C -> A indirect coordination loop where per-run and per-pair caps are never exceeded,
	// verifying that causal hops stops the chain on the 21st step.
	hops := 0
	for step := 1; step <= 25; step++ {
		var err error
		if step%3 == 0 {
			err = CheckMessage(CoordCheck{
				Hops:             hops,
				Run:              RunCounters{Messages: 0},
				PairMsgsLastHour: (step - 1) / 3,
			})
		} else {
			err = CheckDelegate(CoordCheck{
				Depth:         1,
				Hops:          hops,
				Run:           RunCounters{Delegates: 0},
				SenderTeam:    "core",
				TargetTeam:    "core",
				SenderIsChief: true,
			})
		}

		if step <= 20 {
			if err != nil {
				t.Fatalf("step %d (hops=%d) unexpectedly failed: %v", step, hops, err)
			}
			hops++
			continue
		}

		if !errors.Is(err, ErrHops) {
			t.Fatalf("step %d (hops=%d): got err %v, want ErrHops", step, hops, err)
		}
		return
	}
}
