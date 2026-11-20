package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func jaccardOverWords(a, b string) float64 {
	setA := make(map[string]bool)
	for w := range strings.FieldsSeq(strings.ToLower(a)) {
		if utf8.RuneCountInString(w) > 2 {
			setA[w] = true
		}
	}
	setB := make(map[string]bool)
	inter := 0
	for w := range strings.FieldsSeq(strings.ToLower(b)) {
		if utf8.RuneCountInString(w) > 2 {
			if !setB[w] {
				setB[w] = true
				if setA[w] {
					inter++
				}
			}
		}
	}
	union := len(setA) + len(setB) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func TestSynthCorpusBelowJaccardSupersedeThreshold(t *testing.T) {
	// Verify all 20 rounds of agent-00002 have pairwise Jaccard < 0.40 (below nearMatch & 0.7 supersede).
	const numAgents = 100
	mems := make([]synthMemory, 0, 20)
	for r := 0; r < 20; r++ {
		mems = append(mems, buildSynthMemory(1+r*numAgents, numAgents))
	}
	for i := 0; i < len(mems); i++ {
		for j := i + 1; j < len(mems); j++ {
			sim := jaccardOverWords(mems[i].Content, mems[j].Content)
			if sim >= 0.40 {
				t.Fatalf("rounds %d and %d too similar (jaccard=%.3f >= 0.40):\n  %s\n  %s",
					i, j, sim, mems[i].Content, mems[j].Content)
			}
		}
	}
}

func TestAgentTokenInjective(t *testing.T) {
	a := agentToken("agent-1")
	b := agentToken("agent_1")
	c := agentToken("Agent-1")
	if a == b || a == c || b == c {
		t.Fatalf("expected distinct hex tokens, got %q %q %q", a, b, c)
	}
}

func TestVietnameseDiacriticsTeamScopeAndRetention(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	wdb, rdb, err := openFTSStore(ctx, dbPath)
	if err != nil {
		t.Fatalf("openFTSStore: %v", err)
	}
	defer func() {
		_ = rdb.Close()
		_ = wdb.Close()
	}()

	seed := []synthMemory{
		{MemID: "m1", AgentID: "agent-a", TeamID: "team-1", Scope: "private", ConvID: "c1", Content: "mở cổng duyệt quyền trên máy chủ"},
		{MemID: "m2", AgentID: "agent-a", TeamID: "team-1", Scope: "private", ConvID: "c1", Content: "đối soát hóa đơn và khắc phục lỗi mạng"},
		{MemID: "m3", AgentID: "agent-b", TeamID: "team-1", Scope: "team", ConvID: "c2", Content: "báo cáo chung của nhóm về quy trình triển khai"},
		{MemID: "m4", AgentID: "agent-b", TeamID: "team-1", Scope: "private", ConvID: "c2", Content: "ghi chú mật riêng của agent-b về triển khai"},
	}
	for idx, m := range seed {
		tx, err := wdb.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := insertEpisodicTx(ctx, tx, m, int64(1000+idx*100)); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	// 1. Vietnamese double-diacritic ('cổng') & 'đ'/'d' ('đối', 'khắc', 'lỗi') match both accented and unaccented
	for _, q := range []string{"cổng", "cong", "công", "đối soát", "doi soat", "khac phuc loi", "khắc phục lỗi"} {
		hits, err := searchEpisodic(ctx, rdb, "agent-a", "team-1", false, q, true, 5)
		if err != nil {
			t.Fatalf("query %q error: %v", q, err)
		}
		if len(hits) == 0 {
			t.Fatalf("expected Vietnamese query %q to match, got 0 hits", q)
		}
	}

	// 2. Shared team scope returns teammate's 'team' memory with author attribution, but NEVER teammate's 'private' memory
	sharedHits, err := searchEpisodic(ctx, rdb, "agent-a", "team-1", true, "triển khai", true, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(sharedHits) != 1 || sharedHits[0].ID != "m3" || sharedHits[0].AuthorAgentID != "agent-b" {
		t.Fatalf("unexpected shared team hits: %+v", sharedHits)
	}

	// 3. Retention pruning deletes from both episodic_memories and episodic_fts atomically
	deleted, err := pruneEpisodicMemories(ctx, wdb, 1050, 10)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 expired row pruned (m1 at t=1000), got %d", deleted)
	}
	afterPrune, err := searchEpisodic(ctx, rdb, "agent-a", "team-1", false, "cổng duyệt", true, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterPrune) != 0 {
		t.Fatalf("expected pruned memory m1 gone from FTS, got %+v", afterPrune)
	}
}
