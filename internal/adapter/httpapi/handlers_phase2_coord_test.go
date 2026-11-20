package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"max/internal/adapter/httpapi/gen"
	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/coord"
	"max/internal/domain"
)

func TestCreateTaskOriginOwner(t *testing.T) {
	env := newPhase2APIEnv(t)
	rec := env.do(http.MethodPost, "/api/v1/tasks", `{"title":"Launch v1","description":"Prepare release"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("createTask code = %d: %s", rec.Code, rec.Body.String())
	}
	var tk gen.Task
	_ = json.Unmarshal(rec.Body.Bytes(), &tk)
	if tk.RootOrigin != string(domain.OriginOwner) {
		t.Fatalf("rootOrigin = %q, want owner", tk.RootOrigin)
	}
}

func TestTaskTreeEndpoint(t *testing.T) {
	ctx := context.Background()
	env := newPhase2APIEnv(t)
	rootID, err := env.coordSvc.Create(ctx, coord.NewTask{Title: "L0", Origin: domain.OriginOwner})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	parent := rootID
	for lvl := 1; lvl <= 5; lvl++ {
		p := parent
		cid, err := env.coordSvc.Create(ctx, coord.NewTask{Title: fmt.Sprintf("L%d", lvl), ParentID: &p})
		if err != nil {
			t.Fatalf("create L%d: %v", lvl, err)
		}
		parent = cid
	}

	rec := env.do(http.MethodGet, "/api/v1/tasks/"+rootID+"/tree", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("tree code = %d: %s", rec.Code, rec.Body.String())
	}
	var tree gen.TaskTreeNode
	_ = json.Unmarshal(rec.Body.Bytes(), &tree)
	depth := 0
	cur := tree
	for len(cur.Children) > 0 {
		depth++
		cur = cur.Children[0]
	}
	if depth != 5 {
		t.Fatalf("tree depth = %d, want 5", depth)
	}
}

func TestRoutineUpsertValidates(t *testing.T) {
	env := newPhase2APIEnv(t)
	rec := env.do(http.MethodPut, "/api/v1/routines/morning-brief", `{"agent":"chief-of-staff","schedule":"not a cron","body":"Brief me"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid cron code = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "schedule") {
		t.Fatalf("expected 400 error naming 'schedule', got %s", rec.Body.String())
	}
}

func TestRunRoutineNow(t *testing.T) {
	ctx := context.Background()
	env := newPhase2APIEnv(t)
	up := env.do(http.MethodPut, "/api/v1/routines/daily-sync", `{"agent":"chief-of-staff","schedule":"0 8 * * *","body":"Sync metrics"}`)
	if up.Code != http.StatusOK {
		t.Fatalf("upsert routine code = %d: %s", up.Code, up.Body.String())
	}

	run1 := env.do(http.MethodPost, "/api/v1/routines/daily-sync/run", "")
	if run1.Code != http.StatusAccepted {
		t.Fatalf("first runRoutineNow code = %d: %s", run1.Code, run1.Body.String())
	}
	var r1Resp gen.RunRoutineNowResponse
	_ = json.Unmarshal(run1.Body.Bytes(), &r1Resp)
	if r1Resp.TaskId == "" || r1Resp.WakeupId == "" {
		t.Fatalf("unexpected runRoutineNow resp: %+v", r1Resp)
	}

	chief, _, _ := env.st.Read().GetAgentBySlug(ctx, "chief-of-staff")
	claimed, err := env.actors.ClaimBatch(ctx, actor.Capacity{Free: map[domain.EngineName]int{domain.EngineAgy: 1}}, time.Now())
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimBatch: %v (len=%d)", err, len(claimed))
	}
	_ = env.st.WithTx(ctx, func(tx app.Tx) error {
		_, err := env.coordSvc.Checkout(ctx, tx, r1Resp.TaskId, chief.ID, claimed[0].RunID)
		return err
	})

	run2 := env.do(http.MethodPost, "/api/v1/routines/daily-sync/run", "")
	if run2.Code != http.StatusConflict {
		t.Fatalf("concurrent runRoutineNow code = %d, want 409 (%s)", run2.Code, run2.Body.String())
	}
	if !strings.Contains(run2.Body.String(), claimed[0].RunID) {
		t.Fatalf("expected 409 body to include active run ID %q, got %s", claimed[0].RunID, run2.Body.String())
	}
}
