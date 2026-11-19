package main

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestSPAAndStaticRouting(t *testing.T) {
	handler, distFS, err := newHandlerWithTimings(250*time.Millisecond, 150*time.Millisecond)
	if err != nil {
		t.Fatalf("newHandlerWithTimings: %v", err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	if err := runSelfTest(srv.URL, distFS); err != nil {
		t.Fatalf("runSelfTest failed: %v", err)
	}
}
