package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestEvalCommandFakeEngine(t *testing.T) {
	scenariosDir := filepath.Join("..", "..", "evals", "scenarios")

	// Single-scenario mode (--only 1 --runs 2) verifies end-to-end vault+SQLite execution and scoring.
	var stdout, stderr bytes.Buffer
	code := run([]string{"eval", "--engine", "fake", "--only", "1", "--runs", "2", "--scenarios-dir", scenariosDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("max eval --only 1 exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "01-stop-on-approval") {
		t.Fatalf("expected scenario output in stdout:\n%s", stdout.String())
	}

	// Full suite with --runs 1 (< 10 required for critical scenarios 1, 2, 8) must exit 1.
	stdout.Reset()
	stderr.Reset()
	failCode := run([]string{"eval", "--engine", "fake", "--runs", "1", "--scenarios-dir", scenariosDir}, &stdout, &stderr)
	if failCode != 1 {
		t.Fatalf("expected exit code 1 when critical scenarios have < 10 runs, got %d", failCode)
	}
}
