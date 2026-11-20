package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"max/internal/adapter/httpapi/gen"
	"max/internal/app"
	"max/internal/domain"
)

var errDiffConflict = errors.New("diff hunk context conflict")

func (s *Server) ListProposals(ctx context.Context, req gen.ListProposalsRequestObject) (gen.ListProposalsResponseObject, error) {
	status := ""
	if req.Params.Status != nil {
		status = strings.TrimSpace(*req.Params.Status)
	}
	limit := 50
	if req.Params.Limit != nil && *req.Params.Limit > 0 && *req.Params.Limit <= 100 {
		limit = *req.Params.Limit
	}
	rows, err := s.deps.Store.Read().ListProposals(ctx, status, limit)
	if err != nil {
		return nil, err
	}
	items := make([]gen.Proposal, 0, len(rows))
	for _, r := range rows {
		items = append(items, toGenProposal(r))
	}
	return gen.ListProposals200JSONResponse{Items: items}, nil
}

func (s *Server) DecideProposal(ctx context.Context, req gen.DecideProposalRequestObject) (gen.DecideProposalResponseObject, error) {
	if req.Body == nil {
		return gen.DecideProposal400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "missing body"}}, nil
	}
	prop, found, err := s.deps.Store.Read().GetProposal(ctx, req.ProposalId)
	if err != nil {
		return nil, err
	}
	if !found {
		return gen.DecideProposal404JSONResponse{Error: "proposal not found"}, nil
	}
	if prop.Status != "pending" {
		return gen.DecideProposal409JSONResponse{Error: "proposal already decided"}, nil
	}

	approve := req.Body.Approve
	switch prop.Kind {
	case "skill":
		if err := s.decideSkillProposal(ctx, prop, approve); err != nil {
			return gen.DecideProposal400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
		}
	case "instruction_edit":
		if approve {
			if err := s.applyInstructionEditProposal(ctx, prop); err != nil {
				if errors.Is(err, errDiffConflict) {
					return gen.DecideProposal409JSONResponse{Error: err.Error()}, nil
				}
				return gen.DecideProposal400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
			}
		}
	case "routine":
		if approve {
			if err := s.approveRoutineProposal(ctx, prop); err != nil {
				return gen.DecideProposal400JSONResponse{ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()}}, nil
			}
		}
	}

	newStatus := "rejected"
	if approve {
		newStatus = "approved"
	}
	nowMs := s.deps.Clock.Now().UnixMilli()
	if err := s.deps.Store.WithTx(ctx, func(tx app.Tx) error {
		if err := tx.UpdateProposalStatus(ctx, prop.ID, newStatus, nowMs); err != nil {
			return err
		}
		return tx.ResolveInboxByRef(ctx, "proposal", prop.ID, nowMs)
	}); err != nil {
		return nil, err
	}
	prop.Status = newStatus
	prop.DecidedAt = &nowMs
	_ = s.appendAudit(ctx, "proposal.decide", prop.ID, map[string]any{"kind": prop.Kind, "status": newStatus})
	return gen.DecideProposal200JSONResponse(toGenProposal(prop)), nil
}

func (s *Server) decideSkillProposal(ctx context.Context, prop app.ProposalRecord, approve bool) error {
	if s.deps.Vault == nil {
		return errors.New("vault unavailable")
	}
	var payload struct {
		Slug string `json:"slug"`
	}
	_ = json.Unmarshal([]byte(prop.PayloadJSON), &payload)
	slug := strings.TrimSpace(payload.Slug)
	if slug == "" && prop.TargetPath != nil {
		parts := strings.Split(strings.Trim(*prop.TargetPath, "/"), "/")
		if len(parts) >= 3 && parts[0] == "skills" && parts[1] == "_proposed" {
			slug = parts[2]
		}
	}
	if slug == "" {
		return errors.New("missing skill slug")
	}
	proposedFile := fmt.Sprintf("skills/_proposed/%s/SKILL.md", slug)
	proposedDir := fmt.Sprintf("skills/_proposed/%s", slug)

	if approve {
		raw, err := s.deps.Vault.Read(ctx, proposedFile)
		if err != nil {
			return fmt.Errorf("read proposed skill: %w", err)
		}
		activeFile := fmt.Sprintf("skills/%s/SKILL.md", slug)
		if err := s.deps.Vault.Write(ctx, activeFile, raw); err != nil {
			return err
		}
		if err := s.deps.Vault.Remove(ctx, proposedDir); err != nil {
			return err
		}
		if _, err := s.deps.Vault.CommitPaths(ctx, []string{activeFile, proposedFile}, fmt.Sprintf("vault(skill): approve %s", slug)); err != nil {
			return err
		}
		return s.deps.Vault.MirrorSkills(ctx)
	}

	_ = s.deps.Vault.Remove(ctx, proposedDir)
	_, _ = s.deps.Vault.CommitPaths(ctx, []string{proposedFile}, fmt.Sprintf("vault(skill): reject %s", slug))

	nowMs := s.deps.Clock.Now().UnixMilli()
	return s.deps.Store.WithTx(ctx, func(tx app.Tx) error {
		var disabled []string
		if raw, ok, _ := tx.GetKV(ctx, "skills.disabled"); ok && raw != "" {
			_ = json.Unmarshal([]byte(raw), &disabled)
		}
		if !slices.Contains(disabled, slug) {
			disabled = append(disabled, slug)
		}
		b, _ := json.Marshal(disabled)
		return tx.UpsertKV(ctx, "skills.disabled", string(b), nowMs)
	})
}

func (s *Server) applyInstructionEditProposal(ctx context.Context, prop app.ProposalRecord) error {
	if s.deps.Vault == nil {
		return errors.New("vault unavailable")
	}
	var payload struct {
		TargetPath string `json:"target_path"`
		Diff       string `json:"diff"`
	}
	_ = json.Unmarshal([]byte(prop.PayloadJSON), &payload)
	target := strings.TrimSpace(payload.TargetPath)
	if target == "" && prop.TargetPath != nil {
		target = strings.TrimSpace(*prop.TargetPath)
	}
	cleanTarget, ok := domain.CleanRel(target)
	if !ok {
		return errors.New("invalid target path")
	}
	origBytes, err := s.deps.Vault.Read(ctx, cleanTarget)
	if err != nil {
		return fmt.Errorf("%w: target file missing", errDiffConflict)
	}
	patched, err := applyUnifiedDiff(string(origBytes), payload.Diff)
	if err != nil {
		return err
	}
	if err := s.deps.Vault.Write(ctx, cleanTarget, []byte(patched)); err != nil {
		return err
	}
	if _, err := s.deps.Vault.CommitPaths(ctx, []string{cleanTarget}, fmt.Sprintf("vault(instructions): apply proposal %s", prop.ID)); err != nil {
		return err
	}
	if s.deps.AgentSync != nil {
		_, _, _ = s.deps.AgentSync.SyncAll(ctx)
	}
	return nil
}

func (s *Server) approveRoutineProposal(ctx context.Context, prop app.ProposalRecord) error {
	if s.deps.Vault == nil {
		return nil
	}
	var p map[string]string
	_ = json.Unmarshal([]byte(prop.PayloadJSON), &p)
	slug := strings.TrimSpace(p["slug"])
	if slug == "" {
		return errors.New("missing routine slug")
	}
	rel := fmt.Sprintf("routines/%s.md", slug)
	doc := fmt.Sprintf("---\nagent: chief-of-staff\nschedule: %q\nenabled: true\n---\n\n%s\n", p["schedule"], p["body"])
	if err := s.deps.Vault.Write(ctx, rel, []byte(doc)); err != nil {
		return err
	}
	_, err := s.deps.Vault.CommitPaths(ctx, []string{rel}, fmt.Sprintf("vault(routine): approve %s", slug))
	return err
}

func applyUnifiedDiff(orig, diff string) (string, error) {
	origTrim := strings.ReplaceAll(orig, "\r\n", "\n")
	trailingNL := strings.HasSuffix(origTrim, "\n")
	srcLines := strings.Split(strings.TrimSuffix(origTrim, "\n"), "\n")
	if origTrim == "" {
		srcLines = nil
	}

	diffLines := strings.Split(strings.ReplaceAll(diff, "\r\n", "\n"), "\n")
	var out []string
	srcIdx := 0
	sawHunk := false

	for i := 0; i < len(diffLines); {
		line := diffLines[i]
		if !strings.HasPrefix(line, "@@") {
			i++
			continue
		}
		sawHunk = true
		oldStart, ok := parseHunkOldStart(line)
		if !ok {
			return "", fmt.Errorf("%w: invalid hunk header %q", errDiffConflict, line)
		}
		targetIdx := max(0, oldStart-1)
		if targetIdx < srcIdx || targetIdx > len(srcLines) {
			return "", fmt.Errorf("%w: hunk position %d out of bounds", errDiffConflict, oldStart)
		}
		out = append(out, srcLines[srcIdx:targetIdx]...)
		srcIdx = targetIdx
		i++

		for i < len(diffLines) && !strings.HasPrefix(diffLines[i], "@@") {
			hl := diffLines[i]
			i++
			if hl == "" && i == len(diffLines) {
				break
			}
			if strings.HasPrefix(hl, `\`) {
				continue
			}
			switch {
			case strings.HasPrefix(hl, " "):
				want := hl[1:]
				if srcIdx >= len(srcLines) || srcLines[srcIdx] != want {
					return "", fmt.Errorf("%w: context mismatch at line %d", errDiffConflict, srcIdx+1)
				}
				out = append(out, want)
				srcIdx++
			case strings.HasPrefix(hl, "-"):
				want := hl[1:]
				if srcIdx >= len(srcLines) || srcLines[srcIdx] != want {
					return "", fmt.Errorf("%w: removed line mismatch at line %d", errDiffConflict, srcIdx+1)
				}
				srcIdx++
			case strings.HasPrefix(hl, "+"):
				out = append(out, hl[1:])
			default:
				return "", fmt.Errorf("%w: malformed diff line %q", errDiffConflict, hl)
			}
		}
	}
	if !sawHunk {
		return "", fmt.Errorf("%w: diff contains no hunks", errDiffConflict)
	}
	out = append(out, srcLines[srcIdx:]...)
	res := strings.Join(out, "\n")
	if trailingNL || len(out) > 0 {
		res += "\n"
	}
	return res, nil
}

func parseHunkOldStart(hdr string) (int, bool) {
	// Format: @@ -oldStart[,oldCount] +newStart[,newCount] @@
	idx := strings.Index(hdr, "-")
	if idx < 0 {
		return 0, false
	}
	rest := hdr[idx+1:]
	end := strings.IndexAny(rest, ", ")
	if end < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(rest[:end])
	return n, err == nil && n >= 0
}

func toGenProposal(r app.ProposalRecord) gen.Proposal {
	return gen.Proposal{
		Id:         r.ID,
		Kind:       r.Kind,
		AgentId:    r.AgentID,
		TargetPath: r.TargetPath,
		Title:      r.Title,
		Payload:    r.PayloadJSON,
		Status:     r.Status,
		DecidedAt:  r.DecidedAt,
		CreatedAt:  r.CreatedAt,
	}
}
