package tools_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"max/internal/app"
	"max/internal/app/tools"
	"max/internal/domain"
	"max/internal/testutil/testdb"
)

type captureMem struct {
	mu         sync.Mutex
	searchedAs []domain.ID
	saved      []string
}

func (m *captureMem) Healthy() bool { return true }
func (m *captureMem) Search(_ context.Context, agentID domain.ID, _ string, _ int) ([]app.MemoryHit, error) {
	m.mu.Lock()
	m.searchedAs = append(m.searchedAs, agentID)
	m.mu.Unlock()
	return []app.MemoryHit{{ID: "h1", AuthorAgentID: agentID, Content: "doanh thu quý 3 tăng 18%"}}, nil
}
func (m *captureMem) Remember(_ context.Context, _ domain.ID, content string, _ []string) error {
	m.mu.Lock()
	m.saved = append(m.saved, content)
	m.mu.Unlock()
	return nil
}

func TestAgentCannotSearchOthers(t *testing.T) {
	st := testdb.New(t)
	mem := &captureMem{}
	svc := tools.NewService(st, t.TempDir(), app.SystemClock{}, app.UUIDv7Gen{}).WithMemory(mem)

	caller := tools.Caller{
		RunID:          "run-caller",
		AgentID:        "ag-real-caller",
		ConversationID: "conv-1",
		Policy:         domain.AgentPolicy{Scope: domain.AgentScope{Slug: "researcher"}, Autonomy: domain.AutonomyAuto},
		Origin:         domain.OriginOwner,
	}
	res := svc.MemorySearchWithRaw(context.Background(), caller, "doanh thu", json.RawMessage(`{"query":"doanh thu","agentId":"ag-victim"}`))
	if res.IsError {
		t.Fatalf("MemorySearch error: %s", res.Text)
	}
	if len(mem.searchedAs) != 1 || mem.searchedAs[0] != "ag-real-caller" {
		t.Fatalf("searchedAs = %+v, want [ag-real-caller]", mem.searchedAs)
	}

	saveRes := svc.MemorySave(context.Background(), caller, "Token nhạy cảm Bearer topsecret123456")
	if saveRes.IsError {
		t.Fatalf("MemorySave error: %s", saveRes.Text)
	}
	if len(mem.saved) != 1 || strings.Contains(mem.saved[0], "topsecret123456") {
		t.Fatalf("MemorySave did not redact secret: %+v", mem.saved)
	}
}
