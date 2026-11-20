package httpapi

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"max/internal/adapter/httpapi/gen"
)

func (s *Server) ListFacts(ctx context.Context, _ gen.ListFactsRequestObject) (gen.ListFactsResponseObject, error) {
	if s.deps.Vault == nil {
		return gen.ListFacts200JSONResponse{Items: []gen.FactItem{}}, nil
	}
	entries, err := s.deps.Vault.ListDir(ctx, "memory/facts")
	if err != nil {
		return gen.ListFacts200JSONResponse{Items: []gen.FactItem{}}, nil
	}
	sort.Strings(entries)
	items := make([]gen.FactItem, 0, len(entries))
	for _, name := range entries {
		if !strings.HasSuffix(name, ".md") || strings.HasPrefix(name, ".") {
			continue
		}
		raw, rErr := s.deps.Vault.Read(ctx, "memory/facts/"+name)
		if rErr != nil {
			continue
		}
		fm, body := parseSimpleFrontmatter(string(raw))
		slug := strings.TrimSuffix(name, ".md")
		if v := fm["slug"]; v != "" {
			slug = v
		}
		conf, _ := strconv.Atoi(fm["confidence"])
		item := gen.FactItem{
			Slug:          slug,
			Type:          defaultOr(fm["type"], "company"),
			Statement:     strings.TrimSpace(body),
			Confidence:    conf,
			EvidenceMsgId: fm["evidence_msg_id"],
		}
		if sup := fm["supersedes"]; sup != "" {
			item.Supersedes = &sup
		}
		if supBy := fm["superseded_by"]; supBy != "" {
			item.SupersededBy = &supBy
		}
		items = append(items, item)
	}
	return gen.ListFacts200JSONResponse{Items: items}, nil
}

func (s *Server) UndoLastLearning(ctx context.Context, _ gen.UndoLastLearningRequestObject) (gen.UndoLastLearningResponseObject, error) {
	var err error
	if s.deps.Learn != nil {
		err = s.deps.Learn.UndoLast(ctx)
	} else if s.deps.Vault != nil {
		_, err = s.deps.Vault.RevertLastByPrefix(ctx, "learn:")
	} else {
		return gen.UndoLastLearning400JSONResponse{
			ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: "learning service unavailable"},
		}, nil
	}
	if err != nil {
		return gen.UndoLastLearning400JSONResponse{
			ErrorRespJSONResponse: gen.ErrorRespJSONResponse{Error: err.Error()},
		}, nil
	}
	_ = s.appendAudit(ctx, "learn.undo", "HEAD", map[string]bool{"reverted": true})
	return gen.UndoLastLearning200JSONResponse{Reverted: true}, nil
}

func parseSimpleFrontmatter(doc string) (map[string]string, string) {
	fm := make(map[string]string)
	t := strings.TrimLeft(doc, "\r\n")
	if !strings.HasPrefix(t, "---\n") {
		return fm, doc
	}
	rest := t[4:]
	idx := strings.Index(rest, "\n---\n")
	if idx < 0 {
		return fm, doc
	}
	for _, line := range strings.Split(rest[:idx], "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok {
			fm[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return fm, rest[idx+5:]
}

func defaultOr(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return strings.TrimSpace(v)
}
