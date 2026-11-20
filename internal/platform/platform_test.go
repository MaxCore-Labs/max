package platform

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"max/internal/domain"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(func(string) string { return "" })
	if err != nil {
		t.Fatalf("Load defaults failed: %v", err)
	}
	if cfg.Listen != "127.0.0.1:8787" {
		t.Fatalf("Listen = %q, want 127.0.0.1:8787", cfg.Listen)
	}
	if cfg.PublicURL != "http://127.0.0.1:8787" {
		t.Fatalf("PublicURL = %q, want http://127.0.0.1:8787", cfg.PublicURL)
	}
	if cfg.DataDir != "./data" || cfg.VaultDir != "./vault" {
		t.Fatalf("dirs = %q / %q", cfg.DataDir, cfg.VaultDir)
	}
	if cfg.TZ != "Asia/Ho_Chi_Minh" {
		t.Fatalf("TZ = %q, want Asia/Ho_Chi_Minh", cfg.TZ)
	}
	if cfg.Slots[domain.EngineAgy] != 3 || cfg.Slots[domain.EngineClaude] != 3 || cfg.Slots[domain.EngineCodex] != 3 {
		t.Fatalf("unexpected default Slots: %+v", cfg.Slots)
	}
	if cfg.QuotaBackgroundShare != 0.7 {
		t.Fatalf("QuotaBackgroundShare = %v, want 0.7", cfg.QuotaBackgroundShare)
	}
	if cfg.QuotaShortHours != 5 {
		t.Fatalf("QuotaShortHours = %d, want 5", cfg.QuotaShortHours)
	}
	if cfg.QuotaShortTokens != nil || cfg.QuotaWeekTokens != nil {
		t.Fatalf("expected nil quota ceilings in learning mode, got %v / %v", cfg.QuotaShortTokens, cfg.QuotaWeekTokens)
	}
}

func TestLoadRejectsBadSlots(t *testing.T) {
	for _, bad := range []string{"0", "9", "-1", "abc"} {
		_, err := Load(func(k string) string {
			if k == "MAX_SLOTS_AGY" {
				return bad
			}
			return ""
		})
		if err == nil || !strings.Contains(err.Error(), "MAX_SLOTS_AGY") {
			t.Fatalf("MAX_SLOTS_AGY=%q: expected error mentioning MAX_SLOTS_AGY, got %v", bad, err)
		}
	}
	for _, okVal := range []string{"1", "8"} {
		cfg, err := Load(func(k string) string {
			if k == "MAX_SLOTS_AGY" {
				return okVal
			}
			return ""
		})
		if err != nil {
			t.Fatalf("MAX_SLOTS_AGY=%q unexpected error: %v", okVal, err)
		}
		if okVal == "8" && cfg.Slots[domain.EngineAgy] != 8 {
			t.Fatalf("expected Slots[agy]=8, got %d", cfg.Slots[domain.EngineAgy])
		}
	}
}

func TestRedact(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		forbidden  string
		mustRetain string
	}{
		{
			name:       "anthropic_api_key",
			in:         "prefix sk-ant-api03-abc123XYZ987654321 suffix",
			forbidden:  "abc123XYZ987654321",
			mustRetain: "prefix ",
		},
		{
			name:       "telegram_bot_token",
			in:         "bot token 123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw end",
			forbidden:  "AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw",
			mustRetain: "bot token ",
		},
		{
			name:       "bearer_token",
			in:         "Authorization: Bearer xyzSecretToken999 ok",
			forbidden:  "xyzSecretToken999",
			mustRetain: "Authorization:",
		},
		{
			name:       "basic_auth_url",
			in:         "clone https://gituser:superSecretPass@host.example.com/x/repo.git done",
			forbidden:  "superSecretPass",
			mustRetain: "host.example.com/x/repo.git done",
		},
		{
			name:       "pem_private_key",
			in:         "key start -----BEGIN " + "PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC\n-----END " + "PRIVATE KEY----- key end",
			forbidden:  "MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC",
			mustRetain: "key end",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Redact(tc.in)
			if strings.Contains(got, tc.forbidden) {
				t.Fatalf("Redact(%q) leaked secret %q: got %q", tc.in, tc.forbidden, got)
			}
			if !strings.Contains(got, tc.mustRetain) {
				t.Fatalf("Redact(%q) lost non-secret context %q: got %q", tc.in, tc.mustRetain, got)
			}
		})
	}
}

func TestNewLoggerRedactsAttrsAndMessage(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, "info")
	log.Info("failed with Bearer myTopSecret",
		slog.String("key", "sk-ant-secretKeyValue12345"),
		slog.Any("err", errors.New("https://admin:pw12345@db.local/app")),
		slog.Group("nested", slog.String("tg", "98765432:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw")),
	)
	out := buf.String()
	for _, secret := range []string{"myTopSecret", "secretKeyValue12345", "pw12345", "AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw"} {
		if strings.Contains(out, secret) {
			t.Fatalf("logger leaked %q in %s", secret, out)
		}
	}
}
