package sqlite_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"max/internal/adapter/sqlite"
	"max/internal/app"
	"max/internal/domain"
	"max/internal/testutil/testdb"
)

func seedMemoryAgents(t *testing.T, st app.Store) {
	t.Helper()
	ctx := context.Background()
	err := st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertTeam(ctx, app.TeamRecord{ID: "team-mkt", Slug: "marketing", Name: "Marketing", MaxRunning: 2, Status: "active"}); err != nil {
			return err
		}
		if err := tx.InsertTeam(ctx, app.TeamRecord{ID: "team-ops", Slug: "ops", Name: "Ops", MaxRunning: 2, Status: "active"}); err != nil {
			return err
		}
		for _, ag := range []app.AgentRecord{
			{ID: "ag-1", Slug: "researcher-01", Name: "R1", TeamID: "team-mkt", EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto, Browser: domain.BrowserNone, ConfigRev: "r", Status: domain.AgentActive},
			{ID: "ag-2", Slug: "writer-01", Name: "W1", TeamID: "team-mkt", EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto, Browser: domain.BrowserNone, ConfigRev: "r", Status: domain.AgentActive},
			{ID: "ag-3", Slug: "ops-01", Name: "O1", TeamID: "team-ops", EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto, Browser: domain.BrowserNone, ConfigRev: "r", Status: domain.AgentActive},
		} {
			if err := tx.InsertAgent(ctx, ag); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seedMemoryAgents: %v", err)
	}
}

func TestVietnameseDiacriticsMatch(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t).(*sqlite.Store)
	seedMemoryAgents(t, st)
	mem := sqlite.NewMemory(st, app.SystemClock{}, app.UUIDv7Gen{})

	samples := []string{
		"Đã kiểm tra cổng thanh toán nội địa sáng nay.",
		"Hoàn tất đối soát công nợ tháng 11 với kế toán.",
		"Hướng dẫn khắc phục lỗi kết nối máy chủ đối tác.",
	}
	for _, s := range samples {
		if err := mem.Remember(ctx, "ag-1", s, nil); err != nil {
			t.Fatalf("Remember(%q): %v", s, err)
		}
	}

	cases := []struct {
		query string
		want  string
	}{
		{query: "cong thanh toan", want: "cổng thanh toán"},
		{query: "cổng", want: "cổng thanh toán"},
		{query: "doi soat cong no", want: "đối soát công nợ"},
		{query: "đối soát", want: "đối soát công nợ"},
		{query: "khac phuc loi", want: "khắc phục lỗi"},
		{query: "khắc phục lỗi", want: "khắc phục lỗi"},
	}
	for _, tc := range cases {
		hits, err := mem.Search(ctx, "ag-1", tc.query, 5)
		if err != nil {
			t.Fatalf("Search(%q): %v", tc.query, err)
		}
		if len(hits) == 0 || !strings.Contains(hits[0].Content, tc.want) {
			t.Fatalf("Search(%q) = %+v, want hit containing original accented %q", tc.query, hits, tc.want)
		}
	}

	// Verify hostile FTS5 syntax characters never error or escape scope.
	if _, err := mem.Search(ctx, "ag-1", `NEAR("cong":* - OR "unclosed`, 5); err != nil {
		t.Fatalf("hostile query errored: %v", err)
	}
}

func TestSharedTeamMemoryIncludesAuthorAndHidesPrivate(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t).(*sqlite.Store)
	seedMemoryAgents(t, st)
	mem := sqlite.NewMemory(st, app.SystemClock{}, app.UUIDv7Gen{})

	if err := mem.RememberEntry(ctx, sqlite.MemoryEntry{
		AuthorAgentID:  "ag-1",
		Scope:          "private",
		ConversationID: "conv-priv",
		Content:        "Bí mật riêng của researcher về chiến lược giá.",
	}); err != nil {
		t.Fatalf("RememberEntry private: %v", err)
	}
	if err := mem.RememberEntry(ctx, sqlite.MemoryEntry{
		AuthorAgentID:  "ag-1",
		Scope:          "team",
		ConversationID: "conv-team",
		Content:        "Tài liệu chiến lược giá dùng chung cho nhóm marketing.",
	}); err != nil {
		t.Fatalf("RememberEntry team: %v", err)
	}

	// Teammate ag-2 sees shared team memory with author attribution, NOT private memory.
	hits, err := mem.Search(ctx, "ag-2", "chien luoc gia", 5)
	if err != nil {
		t.Fatalf("Search teammate: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("teammate hits = %+v, want exactly 1 shared hit", hits)
	}
	if hits[0].AuthorAgentID != "ag-1" || !strings.Contains(hits[0].Content, "dùng chung") {
		t.Fatalf("unexpected shared hit: %+v", hits[0])
	}

	// Outsider ag-3 in team-ops sees neither.
	otherHits, err := mem.Search(ctx, "ag-3", "chien luoc gia", 5)
	if err != nil {
		t.Fatalf("Search outsider: %v", err)
	}
	if len(otherHits) != 0 {
		t.Fatalf("outsider hits = %+v, want 0", otherHits)
	}
}

func TestRetentionPrunesByRowIDBothTableAndFTS(t *testing.T) {
	ctx := context.Background()
	st := testdb.New(t).(*sqlite.Store)
	seedMemoryAgents(t, st)

	oldTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newTime := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	cur := oldTime
	mem := sqlite.NewMemory(st, clockFunc(func() time.Time { return cur }), app.UUIDv7Gen{})

	for range 4 {
		if err := mem.Remember(ctx, "ag-1", "Ký ức cũ hết hạn lưu trữ năm ngoái", nil); err != nil {
			t.Fatalf("Remember old: %v", err)
		}
	}
	cur = newTime
	for range 3 {
		if err := mem.Remember(ctx, "ag-1", "Ký ức mới vừa cập nhật hôm nay", nil); err != nil {
			t.Fatalf("Remember new: %v", err)
		}
	}

	pruned, err := mem.Prune(ctx, newTime.Add(-30*24*time.Hour), 2, 500)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	// 4 expired + 1 over per-agent cap (3 new -> cap 2) = 5 pruned.
	if pruned != 5 {
		t.Fatalf("pruned = %d, want 5", pruned)
	}

	tableCount, ftsCount, err := mem.CountsForTest(ctx)
	if err != nil {
		t.Fatalf("CountsForTest: %v", err)
	}
	if tableCount != 2 || ftsCount != 2 {
		t.Fatalf("counts = (table=%d, fts=%d), want (2, 2)", tableCount, ftsCount)
	}

	oldHits, err := mem.Search(ctx, "ag-1", "nam ngoai", 5)
	if err != nil || len(oldHits) != 0 {
		t.Fatalf("expected 0 hits for pruned FTS entries, got %+v (err=%v)", oldHits, err)
	}
}

type clockFunc func() time.Time

func (f clockFunc) Now() time.Time { return f() }
