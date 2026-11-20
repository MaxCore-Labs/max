package learn_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"max/internal/adapter/vaultgit"
	"max/internal/app"
	"max/internal/app/learn"
	"max/internal/domain"
	"max/internal/testutil/testdb"
)

type fakeLearnEngine struct {
	proposal domain.Proposal
	lastSpec app.RunSpec
}

func (e *fakeLearnEngine) Name() domain.EngineName { return domain.EngineAgy }
func (e *fakeLearnEngine) Caps() app.EngineCaps    { return app.EngineCaps{StructuredOut: true} }
func (e *fakeLearnEngine) Health(context.Context) app.EngineHealth {
	return app.EngineHealth{Installed: true, LoggedIn: true}
}
func (e *fakeLearnEngine) Run(_ context.Context, spec app.RunSpec, _ app.PermissionFunc) (<-chan app.EngineEvent, error) {
	e.lastSpec = spec
	ch := make(chan app.EngineEvent, 4)
	raw, _ := json.Marshal(e.proposal)
	ch <- app.EngineEvent{Type: "text_delta", Text: string(raw)}
	ch <- app.EngineEvent{Type: "final", InputTokens: 100, OutputTokens: 50}
	close(ch)
	return ch, nil
}

func setupLearnEnv(t *testing.T, eng app.Engine, nowFn func() time.Time) (app.Store, *vaultgit.Vault, *learn.Service) {
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
	clk := app.Clock(app.SystemClock{})
	if nowFn != nil {
		clk = clockFn(nowFn)
	}
	svc := learn.NewService(st, v, eng, clk, app.UUIDv7Gen{})
	return st, v, svc
}

type clockFn func() time.Time

func (f clockFn) Now() time.Time { return f() }

func seedThreadWithMsg(t *testing.T, st app.Store, threadID, msgID, content string, msgTime time.Time) {
	t.Helper()
	ctx := context.Background()
	ms := msgTime.UnixMilli()
	err := st.WithTx(ctx, func(tx app.Tx) error {
		if _, ok, _ := tx.GetThread(ctx, threadID); !ok {
			if err := tx.InsertThread(ctx, app.ThreadRecord{
				ID: threadID, AgentID: "a1", Title: "Chat", LastMsgAt: &ms, CreatedAt: ms, UpdatedAt: ms,
			}); err != nil {
				return err
			}
		}
		if err := tx.InsertMessage(ctx, app.MessageRecord{
			ID: msgID, ThreadID: threadID, Role: "user", Content: content, AttachmentsJSON: "[]", CreatedAt: ms,
		}); err != nil {
			return err
		}
		return tx.TouchThread(ctx, threadID, ms)
	})
	if err != nil {
		t.Fatalf("seedThreadWithMsg: %v", err)
	}
}

func TestDue(t *testing.T) {
	base := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	st, _, svc := setupLearnEnv(t, &fakeLearnEngine{}, func() time.Time { return base })
	ctx := context.Background()

	// 1. Thread quiet 14m -> not due yet; quiet 15m -> due.
	seedThreadWithMsg(t, st, "th-normal", "m-1", "Chủ nhân xác nhận chu kỳ chốt sổ kế toán công ty là ngày 25 hằng tháng.", base)
	due14, err := svc.Due(ctx, base.Add(14*time.Minute))
	if err != nil || len(due14) != 0 {
		t.Fatalf("Due at 14m = %v (err=%v), want empty", due14, err)
	}
	due15, err := svc.Due(ctx, base.Add(15*time.Minute))
	if err != nil || len(due15) != 1 || due15[0] != "th-normal" {
		t.Fatalf("Due at 15m = %v (err=%v), want [th-normal]", due15, err)
	}

	// 2. Explicit "ghi nhớ giúp" -> due after 30s.
	tExplicit := base.Add(20 * time.Minute)
	seedThreadWithMsg(t, st, "th-explicit", "m-exp", "Hãy ghi nhớ giúp: luôn viết báo cáo bằng tiếng Việt", tExplicit)
	due30s, err := svc.Due(ctx, tExplicit.Add(30*time.Second))
	if err != nil {
		t.Fatalf("Due 30s: %v", err)
	}
	foundExp := false
	for _, id := range due30s {
		if id == "th-explicit" {
			foundExp = true
		}
	}
	if !foundExp {
		t.Fatalf("expected th-explicit due after 30s, got %v", due30s)
	}

	// 3. Short turn < 40 chars without "ghi nhớ" -> skipped even after 20m.
	tShort := base.Add(40 * time.Minute)
	seedThreadWithMsg(t, st, "th-short", "m-short", "ok cảm ơn em nhé", tShort)
	dueShort, err := svc.Due(ctx, tShort.Add(20*time.Minute))
	if err != nil {
		t.Fatalf("Due short: %v", err)
	}
	for _, id := range dueShort {
		if id == "th-short" {
			t.Fatalf("expected short turn (< 40 chars) to be skipped, got %v", dueShort)
		}
	}
}

func TestRunWritesOneCommit(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	eng := &fakeLearnEngine{
		proposal: domain.Proposal{
			Facts: []domain.FactProposal{
				{
					Slug:          "billing-day",
					Type:          "company",
					Statement:     "Ngày chốt sổ kế toán là 25 hằng tháng.",
					Confidence:    5,
					EvidenceMsgID: "msg-u1",
				},
				{
					Slug:          "bad-fact",
					Type:          "company",
					Statement:     "Bị chặn do thiếu bằng chứng",
					Confidence:    5,
					EvidenceMsgID: "msg-missing",
				},
			},
			Skills: []domain.SkillProposal{
				{
					Slug:        "monthly-close",
					Description: "Quy trình chốt sổ tháng 25 gọn nhẹ",
					Body:        "# Monthly Close\nKiểm đối chiếu công nợ.",
				},
			},
			InstructionEdits: []domain.EditProposal{
				{
					TargetPath: "agents/chief-of-staff/AGENT.md",
					Title:      "Thêm lưu ý chốt sổ ngày 25",
					Diff:       "--- a/agents/chief-of-staff/AGENT.md\n+++ b/agents/chief-of-staff/AGENT.md\n@@ -1 +1,2 @@\n+Nhắc chốt sổ ngày 25.",
				},
			},
		},
	}
	st, v, svc := setupLearnEnv(t, eng, func() time.Time { return now })
	ctx := context.Background()
	seedThreadWithMsg(t, st, "th-learn", "msg-u1", "Quy định mới của công ty: ngày chốt sổ kế toán là 25 hằng tháng nhé.", now)

	beforeLog := gitLogSubjects(t, v.Dir())
	sha, err := svc.Run(ctx, "th-learn")
	if err != nil || sha == "" {
		t.Fatalf("Run: sha=%q err=%v", sha, err)
	}

	afterLog := gitLogSubjects(t, v.Dir())
	if len(afterLog)-len(beforeLog) != 1 {
		t.Fatalf("expected exactly 1 new git commit, before=%d after=%d (%v)", len(beforeLog), len(afterLog), afterLog)
	}
	if !strings.HasPrefix(afterLog[0], "learn:") {
		t.Fatalf("commit subject = %q, want prefix 'learn:'", afterLog[0])
	}

	factRaw, err := v.Read(ctx, "memory/facts/billing-day.md")
	if err != nil || !strings.Contains(string(factRaw), "25 hằng tháng") {
		t.Fatalf("fact file missing or unexpected: %q (%v)", string(factRaw), err)
	}
	memIndex, err := v.Read(ctx, "memory/MEMORY.md")
	if err != nil || !strings.Contains(string(memIndex), "billing-day") {
		t.Fatalf("MEMORY.md missing billing-day entry: %q (%v)", string(memIndex), err)
	}
	skillRaw, err := v.Read(ctx, "skills/_proposed/monthly-close/SKILL.md")
	if err != nil || !strings.Contains(string(skillRaw), "Monthly Close") {
		t.Fatalf("proposed skill missing: %q (%v)", string(skillRaw), err)
	}
	logRaw, err := v.Read(ctx, "memory/learning-log.md")
	if err != nil || !strings.Contains(string(logRaw), "## Blocked") || !strings.Contains(string(logRaw), "bad-fact") {
		t.Fatalf("learning-log.md missing Blocked section: %q (%v)", string(logRaw), err)
	}
}

func TestRunIsReadOnlyForModel(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	eng := &fakeLearnEngine{}
	st, _, svc := setupLearnEnv(t, eng, func() time.Time { return now })
	seedThreadWithMsg(t, st, "th-ro", "msg-ro", "Chủ nhân dặn kiểm tra quyền chạy chỉ đọc của lượt học tự động.", now)

	if _, err := svc.Run(context.Background(), "th-ro"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if eng.lastSpec.Autonomy != domain.AutonomySuggest {
		t.Fatalf("RunSpec.Autonomy = %q, want suggest", eng.lastSpec.Autonomy)
	}
}

func TestUndoLast(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	eng := &fakeLearnEngine{
		proposal: domain.Proposal{
			Facts: []domain.FactProposal{{
				Slug:          "undo-me",
				Type:          "preference",
				Statement:     "Tạm thời ghi nhận để thử hoàn tác.",
				Confidence:    4,
				EvidenceMsgID: "msg-undo",
			}},
		},
	}
	st, v, svc := setupLearnEnv(t, eng, func() time.Time { return now })
	ctx := context.Background()
	origMem, _ := v.Read(ctx, "memory/MEMORY.md")

	seedThreadWithMsg(t, st, "th-undo", "msg-undo", "Chủ nhân nói tạm thời ghi nhận để thử hoàn tác một commit học.", now)
	if _, err := svc.Run(ctx, "th-undo"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := v.Read(ctx, "memory/facts/undo-me.md"); err != nil {
		t.Fatalf("expected fact before UndoLast: %v", err)
	}

	if err := svc.UndoLast(ctx); err != nil {
		t.Fatalf("UndoLast: %v", err)
	}
	if _, err := v.Read(ctx, "memory/facts/undo-me.md"); err == nil {
		t.Fatal("expected fact file undo-me.md to disappear after UndoLast")
	}
	afterMem, _ := v.Read(ctx, "memory/MEMORY.md")
	if string(afterMem) != string(origMem) {
		t.Fatalf("MEMORY.md after undo = %q, want original %q", string(afterMem), string(origMem))
	}
}

func TestInputBudget(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	eng := &fakeLearnEngine{}
	st, _, svc := setupLearnEnv(t, eng, func() time.Time { return now })
	ctx := context.Background()

	// 6 messages x 10,000 chars = 60,000 chars (> 48,000 budget -> only first 4 fit).
	for i := 1; i <= 6; i++ {
		msgID := fmt.Sprintf("msg-b-%02d", i)
		chunk := fmt.Sprintf("Tin nhắn thứ %d: %s", i, strings.Repeat("x", 10000))
		seedThreadWithMsg(t, st, "th-budget", msgID, chunk, now.Add(time.Duration(i)*time.Second))
	}

	if _, err := svc.Run(ctx, "th-budget"); err != nil {
		t.Fatalf("Run first pass: %v", err)
	}
	th, ok, _ := st.Read().GetThread(ctx, "th-budget")
	if !ok || th.LearnedThroughMsg == nil {
		t.Fatalf("thread after first pass = %+v", th)
	}
	if *th.LearnedThroughMsg == "msg-b-06" {
		t.Fatal("expected learned_through_msg NOT to jump to msg-b-06 when > 48,000 chars")
	}
	if *th.LearnedThroughMsg != "msg-b-04" {
		t.Fatalf("learned_through_msg = %q, want msg-b-04", *th.LearnedThroughMsg)
	}

	// Second pass processes the remaining 2 messages and advances to msg-b-06.
	if _, err := svc.Run(ctx, "th-budget"); err != nil {
		t.Fatalf("Run second pass: %v", err)
	}
	th2, _, _ := st.Read().GetThread(ctx, "th-budget")
	if th2.LearnedThroughMsg == nil || *th2.LearnedThroughMsg != "msg-b-06" {
		t.Fatalf("learned_through_msg after second pass = %v, want msg-b-06", th2.LearnedThroughMsg)
	}
}

func gitLogSubjects(t *testing.T, dir string) []string {
	t.Helper()
	cmd := exec.Command("git", "log", "--format=%s")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}
