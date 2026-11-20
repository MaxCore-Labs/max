package tools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"max/internal/app"
	"max/internal/app/tools"
	"max/internal/domain"
	"max/internal/testutil/testdb"
)

func setupToolsFixture(t *testing.T) (st app.Store, vaultDir string, caller tools.Caller, svc *tools.Service) {
	t.Helper()
	ctx := context.Background()
	st = testdb.New(t)
	vaultDir = t.TempDir()
	agentID := "agent-chief"
	convID := "conv-1"

	if err := st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertTeam(ctx, app.TeamRecord{ID: "t1", Slug: "core", Name: "Core", MaxRunning: 2, Status: "active"}); err != nil {
			return err
		}
		if err := tx.InsertAgent(ctx, app.AgentRecord{
			ID: agentID, Slug: "chief", Name: "Chief", TeamID: "t1",
			EnginesJSON: `["agy"]`, EngineMask: 4, ModelsJSON: `{}`,
			Autonomy: domain.AutonomyAuto, Browser: domain.BrowserNone,
			SkillsJSON: `[]`, ParamsJSON: `{}`, ConfigRev: "r1", Status: domain.AgentActive,
		}); err != nil {
			return err
		}
		return tx.InsertConversation(ctx, app.ConversationRecord{
			ID: convID, AgentID: agentID, Sessions: `{}`,
		})
	}); err != nil {
		t.Fatalf("seed tools fixture: %v", err)
	}

	caller = tools.Caller{
		RunID:          "run-1",
		AgentID:        agentID,
		ConversationID: convID,
		Policy: domain.AgentPolicy{
			Scope:    domain.AgentScope{Slug: "chief", TeamSlug: "core"},
			Autonomy: domain.AutonomyAuto,
		},
		Origin: domain.OriginOwner,
	}
	svc = tools.NewService(st, vaultDir, app.SystemClock{}, app.UUIDv7Gen{})
	return st, vaultDir, caller, svc
}

func TestPing(t *testing.T) {
	_, _, caller, svc := setupToolsFixture(t)
	res := svc.Ping(context.Background(), caller)
	if res.IsError || !strings.Contains(res.Text, "chief") {
		t.Fatalf("Ping = %+v, want non-error containing 'chief'", res)
	}
}

func TestNoteUpdateCap(t *testing.T) {
	_, vaultDir, caller, svc := setupToolsFixture(t)
	notesPath := filepath.Join(vaultDir, "agents", "chief", "notes", "NOTES.md")

	lines80 := make([]string, 80)
	for i := range lines80 {
		lines80[i] = fmt.Sprintf("note %d", i+1)
	}
	resOK := svc.NoteUpdate(context.Background(), caller, strings.Join(lines80, "\n"))
	if resOK.IsError {
		t.Fatalf("80-line NoteUpdate failed: %+v", resOK)
	}
	saved, err := os.ReadFile(notesPath)
	if err != nil || !strings.Contains(string(saved), "note 80") {
		t.Fatalf("read NOTES.md: err=%v content=%q", err, string(saved))
	}

	lines81 := append(append([]string(nil), lines80...), "note 81")
	resBad := svc.NoteUpdate(context.Background(), caller, strings.Join(lines81, "\n"))
	if !resBad.IsError {
		t.Fatalf("expected IsError=true for 81 lines, got %+v", resBad)
	}
	savedAfter, err := os.ReadFile(notesPath)
	if err != nil || strings.Contains(string(savedAfter), "note 81") {
		t.Fatalf("NOTES.md should remain unchanged after 81-line error, got %q", string(savedAfter))
	}
}

func TestAskOwnerParks(t *testing.T) {
	ctx := context.Background()
	st, _, caller, svc := setupToolsFixture(t)

	res := svc.AskOwner(ctx, caller, "Duyệt ngân sách Q4?", []string{"Đồng ý", "Hoãn"})
	if res.IsError {
		t.Fatalf("AskOwner failed: %+v", res)
	}
	if !strings.Contains(strings.ToLower(res.Text), "kết thúc lượt") {
		t.Fatalf("AskOwner response missing 'kết thúc lượt': %q", res.Text)
	}

	inbox, err := st.Read().ListInboxItems(ctx, 10)
	if err != nil || len(inbox) != 1 || inbox[0].Kind != "question" {
		t.Fatalf("inbox = %+v (err=%v), want 1 question item", inbox, err)
	}

	conv, _, _ := st.Read().GetConversation(ctx, caller.ConversationID)
	if conv.WaitingFor == nil {
		t.Fatal("expected conversations.waiting_for to be populated")
	}
	var wf map[string]any
	if err := json.Unmarshal([]byte(*conv.WaitingFor), &wf); err != nil || wf["kind"] != "owner" {
		t.Fatalf("waiting_for = %q, want kind='owner'", *conv.WaitingFor)
	}
}

func TestAskOwnerSingleQuestion(t *testing.T) {
	ctx := context.Background()
	_, _, caller, svc := setupToolsFixture(t)

	first := svc.AskOwner(ctx, caller, "Câu hỏi 1?", nil)
	if first.IsError {
		t.Fatalf("first AskOwner failed: %+v", first)
	}
	second := svc.AskOwner(ctx, caller, "Câu hỏi 2?", nil)
	if !second.IsError || !strings.Contains(strings.ToLower(second.Text), "chỉ một câu hỏi mỗi lượt") {
		t.Fatalf("second AskOwner in same run = %+v, want IsError with 'chỉ một câu hỏi mỗi lượt'", second)
	}
}

func TestToolResultCap(t *testing.T) {
	_, vaultDir, caller, svc := setupToolsFixture(t)
	huge := strings.Repeat("A", 20000)

	capped := svc.CapOutput(caller, "task_list", huge)
	if capped.IsError {
		t.Fatalf("CapOutput IsError: %+v", capped)
	}
	if len(capped.Text) > 8000 {
		t.Fatalf("len(capped.Text) = %d, want <= 8000", len(capped.Text))
	}
	if !strings.Contains(capped.Text, "agents/chief/artifacts/_tool/") {
		t.Fatalf("expected spill file path under agents/chief/artifacts/_tool/ in:\n%s", capped.Text)
	}

	entries, err := os.ReadDir(filepath.Join(vaultDir, "agents", "chief", "artifacts", "_tool"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("spill directory entries = %d (err=%v), want 1", len(entries), err)
	}
	_ = time.Now()
}
