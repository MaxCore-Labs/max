package engine_test

import (
	"testing"

	"max/internal/adapter/engine"
	"max/internal/app"
)

func TestLevel(t *testing.T) {
	cases := []struct {
		name string
		caps app.EngineCaps
		want int
	}{
		{
			name: "pre-tool gate + allowlist -> level 1",
			caps: app.EngineCaps{PreToolGate: true, ToolAllowlist: true, Sandbox: true},
			want: 1,
		},
		{
			name: "sandbox only -> level 2",
			caps: app.EngineCaps{Sandbox: true, PerRunMCP: true},
			want: 2,
		},
		{
			name: "gate without allowlist and no sandbox -> level 3",
			caps: app.EngineCaps{PreToolGate: true, PerRunMCP: true},
			want: 3,
		},
		{
			name: "no gate and no sandbox -> level 3",
			caps: app.EngineCaps{PerRunMCP: true, Resume: true},
			want: 3,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := engine.Level(tc.caps); got != tc.want {
				t.Fatalf("Level(%+v) = %d, want %d", tc.caps, got, tc.want)
			}
		})
	}

	spec := engine.RunAs(engine.ProcSpec{Bin: "agy", Args: []string{"acp"}}, "max-agy")
	if spec.OSUser != "max-agy" {
		t.Fatalf("RunAs OSUser=%q, want max-agy", spec.OSUser)
	}
}
