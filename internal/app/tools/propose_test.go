package tools_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"max/internal/adapter/vaultgit"
	"max/internal/app"
	"max/internal/app/tools"
	"max/internal/domain"
	"max/internal/testutil/testdb"
)

func setupProposeEnv(t *testing.T) (app.Store, *vaultgit.Vault, *tools.Service) {
	t.Helper()
	ctx := context.Background()
	st := testdb.New(t)
	vaultDir := filepath.Join(t.TempDir(), "vault")
	v := vaultgit.New(vaultDir)
	if err := v.EnsureInstalled(ctx); err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}
	_ = st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertTeam(ctx, app.TeamRecord{ID: "t1", Slug: "core", Name: "Core", MaxRunning: 2, Status: "active"}); err != nil {
			return err
		}
		return tx.InsertAgent(ctx, app.AgentRecord{
			ID: "a1", Slug: "chief-of-staff", Name: "CoS", TeamID: "t1",
			EnginesJSON: `["agy"]`, EngineMask: 4, Autonomy: domain.AutonomyAuto,
			Browser: domain.BrowserNone, ConfigRev: "r1", Status: domain.AgentActive,
		})
	})
	svc := tools.NewService(st, vaultDir, app.SystemClock{}, app.UUIDv7Gen{}).WithVault(v)
	return st, v, svc
}

func TestRememberFactNeedsOwnerMessage(t *testing.T) {
	ctx := context.Background()
	st, v, svc := setupProposeEnv(t)

	// 1. Task conversation with no owner chat thread -> IsError = true.
	_ = st.WithTx(ctx, func(tx app.Tx) error {
		tkID := "tk-1"
		if err := tx.InsertTask(ctx, app.TaskRecord{
			ID: tkID, RootID: tkID, RootOrigin: domain.OriginRoutine, Title: "Bg", Description: "Bg",
			Status: "in_progress", CreatedAt: 1000, UpdatedAt: 1000,
		}); err != nil {
			return err
		}
		_, err := tx.EnsureTaskConversation(ctx, "a1", tkID, "conv-task")
		return err
	})
	bgCaller := tools.Caller{
		RunID: "r-bg", AgentID: "a1", ConversationID: "conv-task",
		Policy: domain.AgentPolicy{Scope: domain.AgentScope{Slug: "chief-of-staff"}, Autonomy: domain.AutonomyAuto},
		Origin: domain.OriginRoutine,
	}
	badRes := svc.RememberFact(ctx, bgCaller, "tax-id", "company", "Mã số thuế 0101234567", 5, "msg-fake", "")
	if !badRes.IsError {
		t.Fatalf("expected RememberFact without owner message to fail, got: %s", badRes.Text)
	}

	// 2. Owner chat thread with verified user message -> accepted & written to memory/facts/tax-id.md!
	_ = st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertThread(ctx, app.ThreadRecord{ID: "th-1", AgentID: "a1", Title: "Chat", CreatedAt: 1000, UpdatedAt: 1000}); err != nil {
			return err
		}
		if err := tx.InsertMessage(ctx, app.MessageRecord{
			ID: "msg-owner-1", ThreadID: "th-1", Role: "user", Content: "Mã số thuế công ty là 0101234567", AttachmentsJSON: "[]", CreatedAt: 1000,
		}); err != nil {
			return err
		}
		_, err := tx.EnsureThreadConversation(ctx, "a1", "th-1", "conv-chat")
		return err
	})
	chatCaller := tools.Caller{
		RunID: "r-chat", AgentID: "a1", ConversationID: "conv-chat",
		Policy: domain.AgentPolicy{Scope: domain.AgentScope{Slug: "chief-of-staff"}, Autonomy: domain.AutonomyAuto},
		Origin: domain.OriginOwner,
	}
	okRes := svc.RememberFact(ctx, chatCaller, "tax-id", "company", "Mã số thuế công ty là 0101234567", 5, "msg-owner-1", "")
	if okRes.IsError {
		t.Fatalf("RememberFact with valid owner message failed: %s", okRes.Text)
	}
	raw, err := v.Read(ctx, "memory/facts/tax-id.md")
	if err != nil || !strings.Contains(string(raw), "0101234567") {
		t.Fatalf("expected fact file written, got %q (%v)", string(raw), err)
	}
}

func TestProposalsAreDisabled(t *testing.T) {
	ctx := context.Background()
	st, v, svc := setupProposeEnv(t)

	caller := tools.Caller{
		RunID: "r-prop", AgentID: "a1", ConversationID: "conv-p",
		Policy: domain.AgentPolicy{Scope: domain.AgentScope{Slug: "chief-of-staff"}, Autonomy: domain.AutonomyAuto},
		Origin: domain.OriginOwner,
	}

	rRes := svc.RoutinePropose(ctx, caller, "morning-digest", "Điểm tin sáng", "0 8 * * *", "Tổng hợp tin tức ngành.")
	if rRes.IsError {
		t.Fatalf("RoutinePropose error: %s", rRes.Text)
	}
	if _, err := os.Stat(filepath.Join(v.Dir(), "routines", "morning-digest.md")); !os.IsNotExist(err) {
		t.Fatal("RoutinePropose must NOT create live file in routines/ before owner approval")
	}

	aRes := svc.AgentPropose(ctx, caller, "seo-specialist", "writer", "core", "Cần thêm chuyên viên bài viết SEO")
	if aRes.IsError {
		t.Fatalf("AgentPropose error: %s", aRes.Text)
	}
	if _, found, _ := st.Read().GetAgentBySlug(ctx, "seo-specialist"); found {
		t.Fatal("AgentPropose must NOT instantiate agent before owner approval")
	}

	props, err := st.Read().ListProposals(ctx, "pending", 20)
	if err != nil || len(props) != 2 {
		t.Fatalf("pending proposals = %+v (err=%v), want 2", props, err)
	}
	inbox, _ := st.Read().ListInboxItems(ctx, 20)
	if len(inbox) < 2 {
		t.Fatalf("expected >= 2 Inbox items for proposals, got %d", len(inbox))
	}
}

func TestInstructionProposeStoresDiff(t *testing.T) {
	ctx := context.Background()
	st, _, svc := setupProposeEnv(t)

	caller := tools.Caller{
		RunID: "r-diff", AgentID: "a1", ConversationID: "conv-d",
		Policy: domain.AgentPolicy{Scope: domain.AgentScope{Slug: "chief-of-staff"}, Autonomy: domain.AutonomyAuto},
		Origin: domain.OriginOwner,
	}
	diffText := "--- a/agents/chief-of-staff/AGENT.md\n+++ b/agents/chief-of-staff/AGENT.md\n@@ -1 +1,2 @@\n+Luôn đính kèm link nguồn.\n"
	res := svc.InstructionPropose(ctx, caller, "agents/chief-of-staff/AGENT.md", "Bổ sung quy tắc trích nguồn", diffText)
	if res.IsError {
		t.Fatalf("InstructionPropose error: %s", res.Text)
	}

	props, err := st.Read().ListProposals(ctx, "pending", 10)
	if err != nil || len(props) != 1 {
		t.Fatalf("ListProposals = %+v (err=%v)", props, err)
	}
	if props[0].Kind != "instruction_edit" || props[0].TargetPath == nil || *props[0].TargetPath != "agents/chief-of-staff/AGENT.md" {
		t.Fatalf("unexpected proposal metadata: %+v", props[0])
	}
	var payload map[string]string
	_ = json.Unmarshal([]byte(props[0].PayloadJSON), &payload)
	if !strings.Contains(payload["diff"], "Luôn đính kèm link nguồn.") {
		t.Fatalf("stored payload missing diff: %s", props[0].PayloadJSON)
	}
}
