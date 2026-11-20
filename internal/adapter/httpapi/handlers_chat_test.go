package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"max/internal/adapter/httpapi"
	"max/internal/adapter/httpapi/gen"
	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/chat"
	"max/internal/domain"
	"max/internal/platform"
	"max/internal/testutil/testdb"
)

func newChatAPI(t *testing.T) (http.Handler, *http.Cookie, string) {
	t.Helper()
	ctx := context.Background()
	st := testdb.New(t)
	actorSvc := actor.NewService(st, app.SystemClock{}, app.UUIDv7Gen{})
	chatSvc := chat.NewService(st, actorSvc, app.SystemClock{}, app.UUIDv7Gen{}, nil)

	if err := st.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.InsertTeam(ctx, app.TeamRecord{ID: "t1", Slug: "core", Name: "Core", MaxRunning: 2, Status: "active"}); err != nil {
			return err
		}
		return tx.InsertAgent(ctx, app.AgentRecord{
			ID: "ag-chief", Slug: "chief-of-staff", Name: "Chief", TeamID: "t1",
			EnginesJSON: `["agy"]`, EngineMask: 4, ModelsJSON: `{}`,
			Autonomy: domain.AutonomyAuto, Browser: domain.BrowserNone,
			SkillsJSON: `[]`, ParamsJSON: `{}`, ConfigRev: "r1", Status: domain.AgentActive,
		})
	}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	thID, err := chatSvc.CreateThread(ctx, "ag-chief", "Owner Thread")
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	h, err := httpapi.New(httpapi.Deps{
		Store: st,
		Chat:  chatSvc,
		Config: platform.Config{
			Listen:        "127.0.0.1:8787",
			PublicURL:     "http://127.0.0.1:8787",
			AdminPassword: testAdminPassword,
		},
		Clock: app.SystemClock{},
		IDs:   app.UUIDv7Gen{},
	})
	if err != nil {
		t.Fatalf("httpapi.New: %v", err)
	}
	loginRec := doLogin(h, "127.0.0.1:8787", testAdminPassword)
	return h, loginRec.Result().Cookies()[0], thID
}

func TestSendMessage202(t *testing.T) {
	h, cookie, thID := newChatAPI(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/threads/"+thID+"/messages", bytes.NewBufferString(`{"content":"Bắt đầu làm việc"}`))
	req.Host = "127.0.0.1:8787"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "send-key-1")
	req.AddCookie(cookie)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	var resp gen.SendMessageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.MessageId == "" || resp.WakeupId == "" {
		t.Fatalf("expected non-empty messageId & wakeupId: %+v", resp)
	}
}

func TestListMessagesPagination(t *testing.T) {
	h, cookie, thID := newChatAPI(t)

	for i := range 5 {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/threads/"+thID+"/messages",
			bytes.NewBufferString(fmt.Sprintf(`{"content":"msg-%d"}`, i+1)))
		req.Host = "127.0.0.1:8787"
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("send %d status = %d", i, rec.Code)
		}
	}

	// Reject limit > 100 -> 400.
	reqOver := httptest.NewRequest(http.MethodGet, "/api/v1/threads/"+thID+"/messages?limit=101", nil)
	reqOver.Host = "127.0.0.1:8787"
	reqOver.AddCookie(cookie)
	recOver := httptest.NewRecorder()
	h.ServeHTTP(recOver, reqOver)
	if recOver.Code != http.StatusBadRequest {
		t.Fatalf("limit=101 status = %d, want 400", recOver.Code)
	}

	// Page 1 (limit=2).
	reqP1 := httptest.NewRequest(http.MethodGet, "/api/v1/threads/"+thID+"/messages?limit=2", nil)
	reqP1.Host = "127.0.0.1:8787"
	reqP1.AddCookie(cookie)
	recP1 := httptest.NewRecorder()
	h.ServeHTTP(recP1, reqP1)
	if recP1.Code != http.StatusOK {
		t.Fatalf("page 1 status = %d: %s", recP1.Code, recP1.Body.String())
	}
	var p1 gen.MessageListResponse
	_ = json.Unmarshal(recP1.Body.Bytes(), &p1)
	if len(p1.Items) != 2 || p1.NextCursor == nil || *p1.NextCursor == "" {
		t.Fatalf("page 1 = %+v, want 2 items and nextCursor", p1)
	}

	// Page 2 using before cursor (limit=2).
	reqP2 := httptest.NewRequest(http.MethodGet, "/api/v1/threads/"+thID+"/messages?limit=2&before="+*p1.NextCursor, nil)
	reqP2.Host = "127.0.0.1:8787"
	reqP2.AddCookie(cookie)
	recP2 := httptest.NewRecorder()
	h.ServeHTTP(recP2, reqP2)
	if recP2.Code != http.StatusOK {
		t.Fatalf("page 2 status = %d", recP2.Code)
	}
	var p2 gen.MessageListResponse
	_ = json.Unmarshal(recP2.Body.Bytes(), &p2)
	if len(p2.Items) != 2 {
		t.Fatalf("page 2 len = %d, want 2", len(p2.Items))
	}
	if p2.Items[0].Id == p1.Items[0].Id || p2.Items[0].Id == p1.Items[1].Id {
		t.Fatalf("page 2 overlapped page 1: p1=%+v p2=%+v", p1.Items, p2.Items)
	}
}
