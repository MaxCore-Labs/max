package run

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"max/internal/app"
	"max/internal/app/approvals"
	"max/internal/app/tools"
	"max/internal/domain"
)

// PermissionDeps bundles dependencies injected into NewPermissionFunc.
type PermissionDeps struct {
	Store     app.Store
	Runs      *Service
	Approvals *approvals.Service
	VaultDir  string
	Clock     app.Clock
}

// NewPermissionFunc wires run-token verification, approved-once consumption, pure domain policy evaluation,
// in-scope touch tracking, and stop-and-resume approval creation (Spec 8.6).
func NewPermissionFunc(deps PermissionDeps) app.PermissionFunc {
	if deps.Clock == nil {
		deps.Clock = app.SystemClock{}
	}
	return func(ctx context.Context, ask app.ToolAsk) domain.Decision {
		if deps.Runs == nil {
			return domain.Decision{Verdict: domain.Deny, Reason: "run service unavailable"}
		}
		tokID, ok := deps.Runs.ResolveRunToken(ask.RunToken)
		if !ok {
			return domain.Decision{Verdict: domain.Deny, Reason: "expired or unknown run token"}
		}

		ag, found, err := deps.Store.Read().GetAgent(ctx, tokID.AgentID)
		if err != nil || !found {
			return domain.Decision{Verdict: domain.Deny, Reason: "agent not found"}
		}

		inputHash := approvals.HashInput(ask.Input)
		if deps.Approvals != nil {
			var consumed bool
			_ = deps.Store.WithTx(ctx, func(tx app.Tx) error {
				c, cerr := deps.Approvals.ConsumeOnce(ctx, tx, tokID.AgentID, ask.ToolName, inputHash)
				consumed = c
				return cerr
			})
			if consumed {
				rel, relOK := normalizeVaultRel(deps.VaultDir, ask.Path, ask.Input)
				if relOK && rel != "" && domain.ClassifyBuiltin(ask.ToolName) == domain.ToolClassWd {
					deps.Runs.RecordTouch(tokID.RunID, rel)
				}
				return domain.Decision{Verdict: domain.Allow}
			}
		}

		var rel, searchRel string
		if !strings.HasPrefix(ask.ToolName, "browser_") {
			var relOK, searchOK bool
			rel, relOK = normalizeVaultRel(deps.VaultDir, ask.Path, ask.Input)
			if !relOK {
				return domain.Decision{Verdict: domain.Deny, Reason: "path outside vault or invalid relative path"}
			}
			searchRel, searchOK = normalizeVaultRel(deps.VaultDir, ask.SearchRoot, nil)
			if !searchOK {
				return domain.Decision{Verdict: domain.Deny, Reason: "search root outside vault"}
			}
		}

		origin := domain.OriginOwner
		if tokID.TaskID != nil && *tokID.TaskID != "" {
			if tk, tok, _ := deps.Store.Read().GetTask(ctx, *tokID.TaskID); tok && tk.RootOrigin != "" {
				origin = tk.RootOrigin
			}
		}

		pol := domain.AgentPolicy{
			Scope:    domain.AgentScope{Slug: ag.Slug},
			Autonomy: ag.Autonomy,
			Web:      ag.Web,
			Browser:  ag.Browser,
			Shell:    ag.Shell,
		}
		tc := domain.ToolCall{
			Name:       ask.ToolName,
			Rel:        rel,
			SearchRoot: searchRel,
			InputHash:  inputHash,
			Input:      ask.Input,
		}

		dec := domain.Decide(pol, tc, origin, nil, deps.Clock.Now())
		switch dec.Verdict {
		case domain.Allow:
			cls := domain.ClassifyBuiltin(ask.ToolName)
			if cls == domain.ToolClassWd && rel != "" {
				deps.Runs.RecordTouch(tokID.RunID, rel)
			}
			return dec
		case domain.Ask:
			if deps.Approvals == nil {
				return domain.Decision{Verdict: domain.Deny, Reason: "approvals service unavailable"}
			}
			_, _, err := deps.Approvals.Ask(ctx, approvals.Request{
				Caller: tools.Caller{
					RunID:          tokID.RunID,
					AgentID:        tokID.AgentID,
					ConversationID: tokID.ConversationID,
					TaskID:         tokID.TaskID,
				},
				ToolName: ask.ToolName,
				Input:    ask.Input,
				Reason:   dec.Reason,
				ByMax:    false,
			})
			if err != nil {
				return domain.Decision{Verdict: domain.Deny, Reason: "failed to create approval request"}
			}
			return dec
		default:
			return dec
		}
	}
}

func normalizeVaultRel(vaultDir, rawPath string, rawInput json.RawMessage) (string, bool) {
	candidate := strings.TrimSpace(rawPath)
	if candidate == "" && len(rawInput) > 0 {
		var m map[string]any
		if json.Unmarshal(rawInput, &m) == nil {
			for _, k := range []string{"path", "file_path", "TargetFile", "Path"} {
				if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
					candidate = strings.TrimSpace(v)
					break
				}
			}
		}
	}
	if candidate == "" {
		return "", true
	}

	normCand := filepath.ToSlash(filepath.Clean(candidate))
	normVault := strings.TrimSuffix(filepath.ToSlash(filepath.Clean(vaultDir)), "/")

	if normVault != "" && normVault != "." {
		if normCand == normVault {
			return "", true
		}
		if strings.HasPrefix(normCand, normVault+"/") {
			normCand = strings.TrimPrefix(normCand, normVault+"/")
		} else if filepath.IsAbs(candidate) || strings.HasPrefix(normCand, "/") {
			return "", false
		}
	}

	cleaned, ok := domain.CleanRel(normCand)
	if !ok {
		return "", false
	}
	return cleaned, true
}
