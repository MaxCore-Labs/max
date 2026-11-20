//go:build agyreal

package agy_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"max/internal/adapter/engine/agy"
	"max/internal/app"
	"max/internal/domain"
)

func TestRealPing(t *testing.T) {
	bin := os.Getenv("MAX_REAL_AGY_BIN")
	home := os.Getenv("MAX_REAL_AGY_HOME")
	if bin == "" || home == "" {
		t.Skip("set MAX_REAL_AGY_BIN and MAX_REAL_AGY_HOME to run real agy integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	drv := agy.New(bin, home, agy.ModeACP, slog.Default())
	evCh, err := drv.Run(ctx, app.RunSpec{
		RunID:    "real-ping-1",
		Cwd:      t.TempDir(),
		Prompt:   "Call the max ping tool once and reply with its output.",
		RunToken: "real-ping-tok",
		Autonomy: domain.AutonomyAuto,
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	sawFinal := false
	for ev := range evCh {
		if ev.Type == "final" {
			sawFinal = true
		}
	}
	if !sawFinal {
		t.Fatal("expected final event from real agy run")
	}
}
