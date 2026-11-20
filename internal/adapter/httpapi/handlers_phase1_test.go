package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"max/internal/adapter/httpapi"
	"max/internal/adapter/httpapi/gen"
	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/approvals"
	"max/internal/app/chat"
	"max/internal/app/limits"
	"max/internal/app/tools"
	"max/internal/domain"
	"max/internal/platform"
	"max/internal/testutil/fakeengine"
	"max/internal/testutil/testdb"
)

type phase1APIEnv struct {
	st        app.Store
	h         http.Handler
	cookie    *http.Cookie
	actors    *actor.Service
	approvals *approvals.Service
	toolsSvc  *tools.Service
	slots     *limits.Slots
	quota     *limits.Quota
	execCalls *atomic.Int32
}

func newPhase1APIEnv(t *testing.T) phase1APIEnv {
	t.Helper()
	ctx := context.Background()
	st := testdb.New(t)
	clk := app.SystemClock{}
	ids := app.UUIDv7Gen{}
	actorSvc := actor.NewService(st, clk, ids)
	var execCalls atomic.Int32
	apprSvc := approvals.NewService(st, actorSvc, func(_ context.Context, _ tools.Caller, _ string, _ json.RawMessage) tools.Result {
		execCalls.Add(1)
		return tools.Result{Text: "executed"}
	}, clk, ids)
	toolsSvc := tools.NewService(st, t.TempDir(), clk, ids)
	chatSvc := chat.NewService(st, actorSvc, clk, ids, nil)
	slots := limits.NewSlots(map[domain.EngineName]int{domain.EngineAgy: 3}, nil)
	quota := limits.NewQuota(st, clk, limits.QuotaConfig{ShortHours: 5, BackgroundShare: 0.7, TZ: "UTC"})

	if err := st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertTeam(ctx, app.TeamRecord{ID: "t1", Slug: "core", Name: "Core", MaxRunning: 3, Status: "active"}); err != nil {
			return err
		}
		if err := tx.InsertAgent(ctx, app.AgentRecord{
			ID: "ag-1", Slug: "chief", Name: "Chief", TeamID: "t1",
			EnginesJSON: `["agy"]`, EngineMask: 4, ModelsJSON: `{}`,
			Autonomy: domain.AutonomyAuto, Browser: domain.BrowserNone,
			SkillsJSON: `[]`, ParamsJSON: `{}`, ConfigRev: "r1", Status: domain.AgentActive,
		}); err != nil {
			return err
		}
		return tx.InsertConversation(ctx, app.ConversationRecord{ID: "conv-1", AgentID: "ag-1", Sessions: `{}`})
	}); err != nil {
		t.Fatalf("seed phase1 env: %v", err)
	}

	h, err := httpapi.New(httpapi.Deps{
		Store:     st,
		Actors:    actorSvc,
		Approvals: apprSvc,
		Chat:      chatSvc,
		Slots:     slots,
		Quota:     quota,
		Engines: map[domain.EngineName]app.Engine{
			domain.EngineAgy: fakeengine.New(domain.EngineAgy),
		},
		Config: platform.Config{
			Listen:        "127.0.0.1:8787",
			PublicURL:     "http://127.0.0.1:8787",
			AdminPassword: testAdminPassword,
		},
		Clock: clk,
		IDs:   ids,
	})
	if err != nil {
		t.Fatalf("httpapi.New: %v", err)
	}
	loginRec := doLogin(h, "127.0.0.1:8787", testAdminPassword)
	return phase1APIEnv{
		st:        st,
		h:         h,
		cookie:    loginRec.Result().Cookies()[0],
		actors:    actorSvc,
		approvals: apprSvc,
		toolsSvc:  toolsSvc,
		slots:     slots,
		quota:     quota,
		execCalls: &execCalls,
	}
}

func (e phase1APIEnv) do(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	var rBody *bytes.Buffer
	if body != "" {
		rBody = bytes.NewBufferString(body)
	} else {
		rBody = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, rBody)
	req.Host = "127.0.0.1:8787"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.AddCookie(e.cookie)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func TestDecideApprovalRequiresIdempotencyKey(t *testing.T) {
	env := newPhase1APIEnv(t)
	apID, _, err := env.approvals.Ask(context.Background(), approvals.Request{
		Caller:   tools.Caller{RunID: "r1", AgentID: "ag-1", ConversationID: "conv-1"},
		ToolName: "gmail.send",
		Input:    json.RawMessage(`{"to":"a@b.com"}`),
		Reason:   "external",
		ByMax:    true,
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	rec := env.do(http.MethodPost, "/api/v1/approvals/"+apID+"/decide", `{"allow":true}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing Idempotency-Key status = %d, want 400", rec.Code)
	}
}

func TestDecideApprovalTwiceSameKey(t *testing.T) {
	env := newPhase1APIEnv(t)
	apID, _, err := env.approvals.Ask(context.Background(), approvals.Request{
		Caller:   tools.Caller{RunID: "r1", AgentID: "ag-1", ConversationID: "conv-1"},
		ToolName: "gmail.send",
		Input:    json.RawMessage(`{"to":"a@b.com"}`),
		Reason:   "external",
		ByMax:    true,
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}

	hdr := map[string]string{"Idempotency-Key": "dec-key-99"}
	r1 := env.do(http.MethodPost, "/api/v1/approvals/"+apID+"/decide", `{"allow":true}`, hdr)
	r2 := env.do(http.MethodPost, "/api/v1/approvals/"+apID+"/decide", `{"allow":true}`, hdr)
	if r1.Code != http.StatusOK || r2.Code != http.StatusOK {
		t.Fatalf("statuses = (%d, %d), want (200, 200)", r1.Code, r2.Code)
	}
	if env.execCalls.Load() != 1 {
		t.Fatalf("execCalls = %d, want 1", env.execCalls.Load())
	}
}

func TestAnswerOwnerQuestionWakes(t *testing.T) {
	ctx := context.Background()
	env := newPhase1APIEnv(t)

	caller := tools.Caller{
		RunID:          "run-q1",
		AgentID:        "ag-1",
		ConversationID: "conv-1",
		Policy: domain.AgentPolicy{
			Scope:    domain.AgentScope{Slug: "chief", TeamSlug: "core"},
			Autonomy: domain.AutonomyAuto,
		},
		Origin: domain.OriginOwner,
	}
	res := env.toolsSvc.AskOwner(ctx, caller, "Chọn phương án nào?", []string{"A", "B"})
	if res.IsError {
		t.Fatalf("AskOwner: %+v", res)
	}
	items, _ := env.st.Read().ListInboxItems(ctx, 10)
	if len(items) == 0 {
		t.Fatal("expected question inbox item")
	}

	rec := env.do(http.MethodPost, "/api/v1/inbox/"+items[0].ID+"/answer", `{"answer":"Phương án A"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("answer status = %d, body = %s", rec.Code, rec.Body.String())
	}

	conv, _, _ := env.st.Read().GetConversation(ctx, "conv-1")
	if conv.WaitingFor != nil {
		t.Fatalf("expected waiting_for cleared, got %q", *conv.WaitingFor)
	}
	queued, _ := env.st.Read().ListQueuedWakeups(ctx, "ag-1")
	if len(queued) != 1 || queued[0].Kind != domain.WakeOwnerReply {
		t.Fatalf("expected 1 owner_reply wakeup, got %+v", queued)
	}
}

func TestCancelRunRequeueOrDrop(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"requeue", "drop"} {
		t.Run(mode, func(t *testing.T) {
			env := newPhase1APIEnv(t)
			conv := "conv-1"
			if err := env.st.WithTx(ctx, func(tx app.Tx) error {
				_, _, err := env.actors.Wake(ctx, tx, actor.WakeInput{
					AgentID: "ag-1", Kind: domain.WakeTaskAssigned, Priority: domain.PrioTask, ConversationID: &conv,
				})
				return err
			}); err != nil {
				t.Fatalf("Wake: %v", err)
			}
			claimed, err := env.actors.ClaimBatch(ctx, actor.Capacity{Free: map[domain.EngineName]int{domain.EngineAgy: 1}}, app.SystemClock{}.Now())
			if err != nil || len(claimed) != 1 {
				t.Fatalf("ClaimBatch: %v", err)
			}

			rec := env.do(http.MethodPost, "/api/v1/runs/"+claimed[0].RunID+"/cancel", `{"mode":"`+mode+`"}`, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("cancel status = %d: %s", rec.Code, rec.Body.String())
			}

			wRec, _, _ := env.st.Read().GetWakeup(ctx, "ag-1", claimed[0].Wakeups[0].ID)
			wantStatus := "queued"
			if mode == "drop" {
				wantStatus = "dropped"
			}
			if wRec.Status != wantStatus {
				t.Fatalf("wakeup status = %q, want %q", wRec.Status, wantStatus)
			}
		})
	}
}

func TestSettingsEngines(t *testing.T) {
	env := newPhase1APIEnv(t)

	// Invalid slot count 9 -> 400.
	bad9 := env.do(http.MethodPut, "/api/v1/settings", `{"engines":[{"name":"agy","enabled":true,"slots":9,"defaultModel":"default"}]}`, nil)
	if bad9.Code != http.StatusBadRequest {
		t.Fatalf("slots=9 status = %d, want 400", bad9.Code)
	}

	// Disabling only enabled engine -> 400.
	badOff := env.do(http.MethodPut, "/api/v1/settings", `{"engines":[{"name":"agy","enabled":false,"slots":3,"defaultModel":"default"}]}`, nil)
	if badOff.Code != http.StatusBadRequest {
		t.Fatalf("disabling sole engine status = %d, want 400", badOff.Code)
	}

	// Valid slots=4 -> 200, saved in kv, Slots reflects 4 immediately.
	ok4 := env.do(http.MethodPut, "/api/v1/settings", `{"defaultEngine":"agy","engines":[{"name":"agy","enabled":true,"slots":4,"defaultModel":"default"}]}`, nil)
	if ok4.Code != http.StatusOK {
		t.Fatalf("slots=4 status = %d: %s", ok4.Code, ok4.Body.String())
	}
	if env.slots.Free()[domain.EngineAgy] != 4 {
		t.Fatalf("Slots.Free()[agy] = %d, want 4 immediately", env.slots.Free()[domain.EngineAgy])
	}
	kvVal, hasKV, _ := env.st.Read().GetKV(context.Background(), "settings.engines")
	if !hasKV || !strings.Contains(kvVal, `"slots":4`) {
		t.Fatalf("kv settings.engines = (%q, %v), want slots=4 persisted", kvVal, hasKV)
	}
}

func TestStatusShape(t *testing.T) {
	env := newPhase1APIEnv(t)
	rec := env.do(http.MethodGet, "/api/v1/status", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var st gen.StatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("unmarshal StatusResponse: %v", err)
	}
	if len(st.Engines) == 0 || st.Engines[0].Name != "agy" || st.Engines[0].Slots != 3 {
		t.Fatalf("unexpected engines in status: %+v", st.Engines)
	}
	if st.Queue.ByPriority == nil || st.Deps == nil {
		t.Fatalf("missing queue.byPriority or deps in status: %+v", st)
	}
}

func TestNoSecretsInResponses(t *testing.T) {
	env := newPhase1APIEnv(t)
	routes := []string{
		"/api/v1/auth/me",
		"/api/v1/threads",
		"/api/v1/runs",
		"/api/v1/approvals",
		"/api/v1/inbox",
		"/api/v1/usage",
		"/api/v1/quota",
		"/api/v1/status",
		"/api/v1/settings",
	}
	for _, p := range routes {
		rec := env.do(http.MethodGet, p, "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d", p, rec.Code)
		}
		if strings.Contains(rec.Body.String(), testAdminPassword) {
			t.Fatalf("GET %s leaked AdminPassword!", p)
		}
	}
}

func TestListsPaginated(t *testing.T) {
	env := newPhase1APIEnv(t)
	listRoutes := []string{
		"/api/v1/threads?limit=101",
		"/api/v1/runs?limit=101",
		"/api/v1/approvals?limit=101",
		"/api/v1/inbox?limit=101",
		"/api/v1/usage?limit=101",
	}
	for _, p := range listRoutes {
		rec := env.do(http.MethodGet, p, "", nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("GET %s status = %d, want 400 for limit > 100", p, rec.Code)
		}
	}
}

func TestAuditLogOnOwnerMutations(t *testing.T) {
	ctx := context.Background()
	env := newPhase1APIEnv(t)

	// 1. decideApproval
	apID, _, _ := env.approvals.Ask(ctx, approvals.Request{
		Caller:   tools.Caller{RunID: "r-aud", AgentID: "ag-1", ConversationID: "conv-1"},
		ToolName: "webhook.post",
		Input:    json.RawMessage(`{"auth":"Bearer secretInAudit"}`),
		Reason:   "ext",
		ByMax:    true,
	})
	r1 := env.do(http.MethodPost, "/api/v1/approvals/"+apID+"/decide", `{"allow":false}`, map[string]string{"Idempotency-Key": "aud-1"})
	if r1.Code != http.StatusOK {
		t.Fatalf("decideApproval status = %d", r1.Code)
	}

	// 2. updateSettings
	r2 := env.do(http.MethodPut, "/api/v1/settings", `{"engines":[{"name":"agy","enabled":true,"slots":2,"defaultModel":"default"}]}`, nil)
	if r2.Code != http.StatusOK {
		t.Fatalf("updateSettings status = %d", r2.Code)
	}

	// 3. cancelRun
	conv := "conv-1"
	_ = env.st.WithTx(ctx, func(tx app.Tx) error {
		_, _, err := env.actors.Wake(ctx, tx, actor.WakeInput{
			AgentID: "ag-1", Kind: domain.WakeTaskAssigned, Priority: domain.PrioTask, ConversationID: &conv,
		})
		return err
	})
	claimed, _ := env.actors.ClaimBatch(ctx, actor.Capacity{Free: map[domain.EngineName]int{domain.EngineAgy: 1}}, app.SystemClock{}.Now())
	r3 := env.do(http.MethodPost, "/api/v1/runs/"+claimed[0].RunID+"/cancel", `{"mode":"requeue"}`, nil)
	if r3.Code != http.StatusOK {
		t.Fatalf("cancelRun status = %d", r3.Code)
	}

	logs, err := env.st.Read().ListAuditLogs(ctx, 20)
	if err != nil || len(logs) != 3 {
		t.Fatalf("audit_log rows = %d (err=%v, logs=%+v), want 3", len(logs), err, logs)
	}
	wantActions := []string{"approval.decide", "settings.update", "run.cancel"}
	for i, want := range wantActions {
		if logs[i].Action != want {
			t.Fatalf("logs[%d].Action = %q, want %q", i, logs[i].Action, want)
		}
		if strings.Contains(logs[i].DetailJSON, "secretInAudit") {
			t.Fatalf("audit_log leaked unredacted secret: %s", logs[i].DetailJSON)
		}
	}
}
