package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"max/internal/adapter/httpapi"
	"max/internal/adapter/httpapi/gen"
	"max/internal/adapter/sqlite"
	"max/internal/adapter/vaultgit"
	"max/internal/app"
	"max/internal/app/actor"
	"max/internal/app/agentsync"
	"max/internal/app/connections"
	"max/internal/app/grants"
	"max/internal/app/learn"
	"max/internal/platform"
)

type phase3Env struct {
	st     *sqlite.Store
	vault  *vaultgit.Vault
	learn  *learn.Service
	conns  *connections.Service
	grants *grants.Service
	h      http.Handler
	cookie *http.Cookie
	now    time.Time
}

type p3Clock struct {
	now time.Time
}

func (c *p3Clock) Now() time.Time { return c.now }

func newPhase3Env(t *testing.T, publicURL string) (*phase3Env, *p3Clock) {
	t.Helper()
	ctx := context.Background()
	dataDir := t.TempDir()
	vaultDir := filepath.Join(t.TempDir(), "vault")
	st, err := sqlite.Open(ctx, filepath.Join(dataDir, "max.sqlite3"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	v := vaultgit.New(vaultDir)
	if err := v.EnsureInstalled(ctx); err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}
	clk := &p3Clock{now: time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)}
	ids := app.UUIDv7Gen{}
	syncSvc := agentsync.NewService(st, v, clk, ids)
	_, _, _ = syncSvc.SyncAll(ctx)

	connSvc, err := connections.NewService(st, dataDir, clk)
	if err != nil {
		t.Fatalf("connections.NewService: %v", err)
	}
	grantSvc := grants.NewService(st, clk, ids)
	learnSvc := learn.NewService(st, v, nil, clk, ids)
	actors := actor.NewService(st, clk, ids)

	if publicURL == "" {
		publicURL = "http://127.0.0.1:8787"
	}
	h, err := httpapi.New(httpapi.Deps{
		Store:       st,
		Vault:       v,
		AgentSync:   syncSvc,
		Actors:      actors,
		Learn:       learnSvc,
		Connections: connSvc,
		Grants:      grantSvc,
		Config: platform.Config{
			DataDir:       dataDir,
			VaultDir:      vaultDir,
			Listen:        "127.0.0.1:8787",
			PublicURL:     publicURL,
			AdminPassword: "phase3-owner-password",
		},
		Clock: clk,
		IDs:   ids,
	})
	if err != nil {
		t.Fatalf("httpapi.New: %v", err)
	}

	loginReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"password":"phase3-owner-password"}`))
	loginReq.Host = "127.0.0.1:8787"
	loginReq.Header.Set("Origin", publicURL)
	loginReq.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, loginReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d: %s", rec.Code, rec.Body.String())
	}

	return &phase3Env{
		st:     st,
		vault:  v,
		learn:  learnSvc,
		conns:  connSvc,
		grants: grantSvc,
		h:      h,
		cookie: rec.Result().Cookies()[0],
		now:    clk.now,
	}, clk
}

func (e *phase3Env) do(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = "127.0.0.1:8787"
	req.Header.Set("Origin", "http://127.0.0.1:8787")
	req.Header.Set("Idempotency-Key", "idem-"+method+"-"+path)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if e.cookie != nil {
		req.AddCookie(e.cookie)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func TestUndoLastLearning(t *testing.T) {
	ctx := context.Background()
	env, _ := newPhase3Env(t, "")

	_ = env.vault.Write(ctx, "memory/facts/temp-fact.md", []byte("---\nslug: temp-fact\n---\n\nTạm ghi.\n"))
	_, _ = env.vault.CommitPaths(ctx, []string{"memory/facts/temp-fact.md"}, "learn: thread t1 (1 facts, 0 skills)")

	rec := env.do(http.MethodPost, "/api/v1/knowledge/learn/undo", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("undo status = %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := env.vault.Read(ctx, "memory/facts/temp-fact.md"); err == nil {
		t.Fatal("expected temp-fact.md removed after undoLastLearning")
	}
}

func TestDecideProposalSkill(t *testing.T) {
	ctx := context.Background()
	env, clk := newPhase3Env(t, "")

	// 1. Approve proposed skill -> moves to skills/<slug>/SKILL.md and mirrors to .claude/skills/<slug>/SKILL.md.
	skillBody := "---\nname: export-vat\ndescription: Xuất bảng kê thuế GTGT\n---\n\n# Export VAT\nSteps.\n"
	_ = env.vault.Write(ctx, "skills/_proposed/export-vat/SKILL.md", []byte(skillBody))
	_, _ = env.vault.CommitPaths(ctx, []string{"skills/_proposed/export-vat/SKILL.md"}, "learn: proposed export-vat")

	tp1 := "skills/_proposed/export-vat/SKILL.md"
	_ = env.st.WithTx(ctx, func(tx app.Tx) error {
		return tx.InsertProposal(ctx, app.ProposalRecord{
			ID: "prop-sk-approve", Kind: "skill", TargetPath: &tp1, Title: "Skill export-vat",
			PayloadJSON: `{"slug":"export-vat","description":"Xuất bảng kê thuế GTGT","body":"# Export VAT"}`,
			Status:      "pending", CreatedAt: clk.now.UnixMilli(),
		})
	})

	recApp := env.do(http.MethodPost, "/api/v1/proposals/prop-sk-approve/decide", `{"approve":true}`)
	if recApp.Code != http.StatusOK {
		t.Fatalf("approve skill status = %d: %s", recApp.Code, recApp.Body.String())
	}
	if _, err := env.vault.Read(ctx, "skills/export-vat/SKILL.md"); err != nil {
		t.Fatalf("expected active skill in skills/export-vat/SKILL.md: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.vault.Dir(), ".claude", "skills", "export-vat", "SKILL.md")); err != nil {
		t.Fatalf("expected mirrored skill in .claude/skills/export-vat/SKILL.md: %v", err)
	}

	// 2. Reject proposed skill -> removes _proposed and records in disabled list.
	_ = env.vault.Write(ctx, "skills/_proposed/bad-skill/SKILL.md", []byte("---\nname: bad-skill\ndescription: Bad\n---\n\nNo.\n"))
	_, _ = env.vault.CommitPaths(ctx, []string{"skills/_proposed/bad-skill/SKILL.md"}, "learn: proposed bad-skill")
	tp2 := "skills/_proposed/bad-skill/SKILL.md"
	_ = env.st.WithTx(ctx, func(tx app.Tx) error {
		return tx.InsertProposal(ctx, app.ProposalRecord{
			ID: "prop-sk-reject", Kind: "skill", TargetPath: &tp2, Title: "Skill bad-skill",
			PayloadJSON: `{"slug":"bad-skill"}`, Status: "pending", CreatedAt: clk.now.UnixMilli(),
		})
	})

	recRej := env.do(http.MethodPost, "/api/v1/proposals/prop-sk-reject/decide", `{"approve":false}`)
	if recRej.Code != http.StatusOK {
		t.Fatalf("reject skill status = %d: %s", recRej.Code, recRej.Body.String())
	}
	if _, err := env.vault.Read(ctx, "skills/_proposed/bad-skill/SKILL.md"); err == nil {
		t.Fatal("expected rejected skill deleted from skills/_proposed/")
	}
	disabledRaw, ok, _ := env.st.Read().GetKV(ctx, "skills.disabled")
	if !ok || !strings.Contains(disabledRaw, "bad-skill") {
		t.Fatalf("skills.disabled = %q (ok=%v), want bad-skill recorded", disabledRaw, ok)
	}
}

func TestDecideProposalInstructionEdit(t *testing.T) {
	ctx := context.Background()
	env, clk := newPhase3Env(t, "")

	target := "company/COMPANY.md"
	orig := "# Company\nLine one\nLine two\n"
	_ = env.vault.Write(ctx, target, []byte(orig))
	_, _ = env.vault.CommitPaths(ctx, []string{target}, "vault: baseline company")

	diff := "--- a/company/COMPANY.md\n+++ b/company/COMPANY.md\n@@ -1,3 +1,4 @@\n # Company\n Line one\n Line two\n+Line three added\n"
	payload, _ := json.Marshal(map[string]string{"target_path": target, "diff": diff})

	_ = env.st.WithTx(ctx, func(tx app.Tx) error {
		return tx.InsertProposal(ctx, app.ProposalRecord{
			ID: "prop-diff-ok", Kind: "instruction_edit", TargetPath: &target, Title: "Add line three",
			PayloadJSON: string(payload), Status: "pending", CreatedAt: clk.now.UnixMilli(),
		})
	})
	recOK := env.do(http.MethodPost, "/api/v1/proposals/prop-diff-ok/decide", `{"approve":true}`)
	if recOK.Code != http.StatusOK {
		t.Fatalf("apply diff status = %d: %s", recOK.Code, recOK.Body.String())
	}
	updated, _ := env.vault.Read(ctx, target)
	if !strings.Contains(string(updated), "Line three added") {
		t.Fatalf("COMPANY.md missing applied diff:\n%s", string(updated))
	}

	// File diverges so diff context no longer matches -> 409 Conflict!
	_ = env.vault.Write(ctx, target, []byte("# Diverged completely\n"))
	_, _ = env.vault.CommitPaths(ctx, []string{target}, "vault: manual owner edit")
	_ = env.st.WithTx(ctx, func(tx app.Tx) error {
		return tx.InsertProposal(ctx, app.ProposalRecord{
			ID: "prop-diff-409", Kind: "instruction_edit", TargetPath: &target, Title: "Stale diff",
			PayloadJSON: string(payload), Status: "pending", CreatedAt: clk.now.UnixMilli(),
		})
	})
	rec409 := env.do(http.MethodPost, "/api/v1/proposals/prop-diff-409/decide", `{"approve":true}`)
	if rec409.Code != http.StatusConflict {
		t.Fatalf("stale diff status = %d, want 409: %s", rec409.Code, rec409.Body.String())
	}
}

func TestConnectionSecretsWriteOnly(t *testing.T) {
	env, _ := newPhase3Env(t, "")

	putBody := `{"kind":"http","enabled":true,"config":{"url":"https://api.example.com","bearer":"sk-ultra-secret-token-777"},"tools":[{"name":"ping","write":false}]}`
	putRec := env.do(http.MethodPut, "/api/v1/connections/crm", putBody)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT connection status = %d: %s", putRec.Code, putRec.Body.String())
	}
	if strings.Contains(putRec.Body.String(), "sk-ultra-secret-token-777") {
		t.Fatalf("PUT response leaked secret: %s", putRec.Body.String())
	}

	getRec := env.do(http.MethodGet, "/api/v1/connections", "")
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET connections status = %d: %s", getRec.Code, getRec.Body.String())
	}
	raw := getRec.Body.String()
	if strings.Contains(raw, "sk-ultra-secret-token-777") {
		t.Fatalf("GET connections leaked secret: %s", raw)
	}
	var list gen.ConnectionListResponse
	_ = json.Unmarshal(getRec.Body.Bytes(), &list)
	if len(list.Items) != 1 || !list.Items[0].Configured {
		t.Fatalf("connections list = %+v, want configured=true", list)
	}
}

func TestTotpFlow(t *testing.T) {
	env, clk := newPhase3Env(t, "")

	setupRec := env.do(http.MethodPost, "/api/v1/auth/totp/setup", "")
	if setupRec.Code != http.StatusOK {
		t.Fatalf("totp setup status = %d: %s", setupRec.Code, setupRec.Body.String())
	}
	var setupResp gen.TotpSetupResponse
	_ = json.Unmarshal(setupRec.Body.Bytes(), &setupResp)
	if setupResp.Secret == "" || !strings.HasPrefix(setupResp.OtpauthUrl, "otpauth://totp/") {
		t.Fatalf("unexpected setupResp: %+v", setupResp)
	}

	codeNow, err := httpapi.GenerateTOTPCode(setupResp.Secret, clk.now)
	if err != nil {
		t.Fatalf("GenerateTOTPCode: %v", err)
	}
	confRec := env.do(http.MethodPost, "/api/v1/auth/totp/confirm", `{"code":"`+codeNow+`"}`)
	if confRec.Code != http.StatusOK {
		t.Fatalf("totp confirm status = %d: %s", confRec.Code, confRec.Body.String())
	}

	doLogin := func(payload string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(payload))
		r.Host = "127.0.0.1:8787"
		r.Header.Set("Origin", "http://127.0.0.1:8787")
		r.Header.Set("Content-Type", "application/json")
		r.RemoteAddr = "127.0.0.10:55555"
		w := httptest.NewRecorder()
		env.h.ServeHTTP(w, r)
		return w
	}

	// Login without TOTP code now fails with 401.
	if r := doLogin(`{"password":"phase3-owner-password"}`); r.Code != http.StatusUnauthorized {
		t.Fatalf("login without totpCode = %d, want 401", r.Code)
	}
	// Login with valid TOTP code succeeds!
	if r := doLogin(`{"password":"phase3-owner-password","totpCode":"` + codeNow + `"}`); r.Code != http.StatusOK {
		t.Fatalf("login with valid totpCode = %d: %s", r.Code, r.Body.String())
	}

	// 5 wrong codes -> temporary lockout (429)!
	for i := 1; i <= 5; i++ {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"password":"phase3-owner-password","totpCode":"000000"}`))
		r.Host = "127.0.0.1:8787"
		r.Header.Set("Origin", "http://127.0.0.1:8787")
		r.Header.Set("Content-Type", "application/json")
		// Distinct client IP per attempt so IP login limiter doesn't fire before TOTP lockout!
		r.RemoteAddr = "127.0.0." + string(rune('2'+i)) + ":40000"
		w := httptest.NewRecorder()
		env.h.ServeHTTP(w, r)
	}
	locked := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"password":"phase3-owner-password","totpCode":"`+codeNow+`"}`))
	locked.Host = "127.0.0.1:8787"
	locked.Header.Set("Origin", "http://127.0.0.1:8787")
	locked.Header.Set("Content-Type", "application/json")
	locked.RemoteAddr = "127.0.0.99:40000"
	lockedRec := httptest.NewRecorder()
	env.h.ServeHTTP(lockedRec, locked)
	if lockedRec.Code != http.StatusTooManyRequests {
		t.Fatalf("after 5 wrong TOTP codes, status = %d, want 429: %s", lockedRec.Code, lockedRec.Body.String())
	}
}

func TestTotpRequiredWhenPublic(t *testing.T) {
	env, _ := newPhase3Env(t, "https://max.mycompany.com")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	req.Host = "max.mycompany.com"
	req.AddCookie(env.cookie)
	rec := httptest.NewRecorder()
	env.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("getStatus = %d: %s", rec.Code, rec.Body.String())
	}
	var st gen.StatusResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if st.Warnings == nil || len(*st.Warnings) == 0 {
		t.Fatalf("expected mandatory TOTP warning when PublicURL is public internet, got %+v", st)
	}
	joined := strings.ToLower(strings.Join(*st.Warnings, " "))
	if !strings.Contains(joined, "totp") {
		t.Fatalf("warnings %v missing TOTP requirement", *st.Warnings)
	}
}
