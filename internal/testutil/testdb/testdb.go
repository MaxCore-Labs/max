package testdb

import (
	"context"
	"path/filepath"
	"testing"

	"max/internal/adapter/sqlite"
	"max/internal/app"
)

// New opens a fresh migrated SQLite WAL database in t.TempDir() and closes it via t.Cleanup.
func New(t *testing.T) app.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.sqlite3")
	st, err := sqlite.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("testdb.New: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})
	return st
}
