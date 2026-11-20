package learn

import (
	"context"
	"encoding/json"
	"strings"
)

func (s *Service) listSlugs(ctx context.Context, dir, trimSuffix string) map[string]bool {
	entries, _ := s.vault.ListDir(ctx, dir)
	out := make(map[string]bool, len(entries))
	for _, e := range entries {
		if strings.HasPrefix(e, ".") {
			continue
		}
		if trimSuffix != "" {
			if !strings.HasSuffix(e, trimSuffix) {
				continue
			}
			e = strings.TrimSuffix(e, trimSuffix)
		}
		out[e] = true
	}
	return out
}

func (s *Service) loadDisabledSkills(ctx context.Context) map[string]bool {
	out := make(map[string]bool)
	raw, ok, err := s.store.Read().GetKV(ctx, disabledSkillsKVKey)
	if err != nil || !ok || raw == "" {
		return out
	}
	var list []string
	if json.Unmarshal([]byte(raw), &list) == nil {
		for _, slug := range list {
			out[slug] = true
		}
	}
	return out
}

func setFrontmatterField(doc, key, val string) string {
	trimmed := strings.TrimLeft(doc, "\r\n")
	if !strings.HasPrefix(trimmed, "---\n") {
		return "---\n" + key + ": " + val + "\n---\n\n" + doc
	}
	rest := trimmed[4:]
	endIdx := strings.Index(rest, "\n---\n")
	if endIdx < 0 {
		return doc
	}
	fm := rest[:endIdx]
	body := rest[endIdx+5:]
	lines := strings.Split(fm, "\n")
	replaced := false
	prefix := key + ":"
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), prefix) {
			lines[i] = key + ": " + val
			replaced = true
		}
	}
	if !replaced {
		lines = append(lines, key+": "+val)
	}
	return "---\n" + strings.Join(lines, "\n") + "\n---\n" + body
}

func removeIndexSlugLine(memDoc, slug string) string {
	needle1 := "(facts/" + slug + ".md)"
	needle2 := "[[memory/facts/" + slug
	var kept []string
	for _, line := range strings.Split(memDoc, "\n") {
		if strings.Contains(line, needle1) || strings.Contains(line, needle2) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func defaultStr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimSpace(v)
}
