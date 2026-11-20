package main

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"max/internal/adapter/sqlite"
	"max/internal/testutil/invariants"
)

func TestLoadtestSmall(t *testing.T) {
	dataDir := t.TempDir()
	var outBuf, errBuf bytes.Buffer

	code := run([]string{
		"loadtest",
		"--data-dir", dataDir,
		"--agents", "200",
		"--wakeups", "2000",
		"--slots", "3",
		"--sleep-min-ms", "5",
		"--sleep-max-ms", "20",
	}, &outBuf, &errBuf)
	if code != 0 {
		t.Fatalf("max loadtest exit = %d\nstdout:\n%s\nstderr:\n%s", code, outBuf.String(), errBuf.String())
	}

	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(dataDir, "max.sqlite3"))
	if err != nil {
		t.Fatalf("open loadtest db: %v", err)
	}
	defer func() { _ = st.Close() }()

	vs := invariants.Check(ctx, st, invariants.WithQuiescent(true))
	if len(vs) > 0 {
		t.Fatalf("invariant violations = %+v, want 0", vs)
	}

	doneCount, totalCount, err := st.CountWakeupsDoneAndTotal(ctx)
	if err != nil {
		t.Fatalf("count wakeups: %v", err)
	}
	if totalCount != 2000 || doneCount != 2000 {
		t.Fatalf("wakeups done/total = %d/%d, want 2000/2000", doneCount, totalCount)
	}
}
