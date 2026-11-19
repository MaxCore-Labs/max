package main

import "testing"

func TestIsQuotaLimitSignalNoFalsePositiveOn429Substring(t *testing.T) {
	if isQuotaLimitSignal("unexpected stopReason=\"max_tokens\" totalTokens=42915 session=abc429def", "", "max_tokens") {
		t.Fatal("bare 429 inside token count or session ID must not match quota signal")
	}
	if !isQuotaLimitSignal("rpc session/prompt (-32000): RESOURCE_EXHAUSTED: quota exceeded", "", "") {
		t.Fatal("expected RESOURCE_EXHAUSTED to match")
	}
	if !isQuotaLimitSignal("", "Error: HTTP 429 Too Many Requests", "end_turn") {
		t.Fatal("expected stderr HTTP 429 to match")
	}
}

func TestSelectPermissionOptionStrict(t *testing.T) {
	opts := []permOption{
		{OptionID: "allow_always", Kind: "allow_always"},
		{OptionID: "allow_once", Kind: "allow_once"},
		{OptionID: "reject_once", Kind: "reject_once"},
	}
	if got, ok := selectPermissionOption("read", opts); !ok || got != "allow_once" {
		t.Fatalf("read expected allow_once, got %q (%v)", got, ok)
	}
	if got, ok := selectPermissionOption("edit", opts); !ok || got != "allow_once" {
		t.Fatalf("edit expected allow_once, got %q (%v)", got, ok)
	}
	if got, ok := selectPermissionOption("execute", opts); !ok || got != "reject_once" {
		t.Fatalf("execute expected reject_once, got %q (%v)", got, ok)
	}
	if _, ok := selectPermissionOption("read", []permOption{{OptionID: "allow_always", Kind: "allow_always"}}); ok {
		t.Fatal("must never fall back to allow_always when allow_once is absent")
	}
}
