package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionSubcommand(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := run([]string{"version"}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr=%q)", code, errBuf.String())
	}
	if !strings.HasPrefix(strings.TrimSpace(out.String()), "max ") {
		t.Fatalf("unexpected version output: %q", out.String())
	}
}
