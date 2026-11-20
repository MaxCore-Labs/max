package connections_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"max/internal/app"
	"max/internal/app/connections"
	"max/internal/testutil/testdb"
)

func TestConnectionEncryptedAtRest(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t)
	dataDir := t.TempDir()
	svc, err := connections.NewService(st, dataDir, app.SystemClock{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	keyPath := filepath.Join(dataDir, ".key")
	fi, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat .key: %v", err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf(".key perm = %o, want 0600", fi.Mode().Perm())
	}

	secretPlain := []byte(`{"url":"https://mcp.example.com","api_key":"sk-live-987654321"}`)
	err = svc.Upsert(ctx, connections.UpsertInput{
		Name:   "crm",
		Kind:   "http",
		Config: secretPlain,
		Tools: []connections.ConnTool{
			{Name: "create_lead", Write: true, TargetField: "email"},
			{Name: "get_lead", Write: false},
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	rec, ok, err := st.Read().GetConnection(ctx, "crm")
	if err != nil || !ok {
		t.Fatalf("GetConnection: ok=%v err=%v", ok, err)
	}
	if bytes.Contains(rec.ConfigCiphertext, []byte("sk-live-987654321")) {
		t.Fatal("plaintext secret stored unencrypted in connections.config")
	}

	dec, err := svc.DecryptConfig(ctx, "crm")
	if err != nil || !bytes.Equal(dec, secretPlain) {
		t.Fatalf("DecryptConfig = %q (%v), want original plaintext", string(dec), err)
	}
}
