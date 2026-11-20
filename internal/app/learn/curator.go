package learn

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"max/internal/app"
)

const curatorLastHeadKVKey = "curator.last_head"

var wikiLinkRe = regexp.MustCompile(`\[\[([^\]|#]+)(?:[|#][^\]]*)?\]\]`)

// Report summarizes one deterministic nightly Curator pass (no LLM invoked).
type Report struct {
	Skipped          bool
	IndexedFacts     int
	RetiredFromIndex int
	BrokenLinks      []string
	OrphanNotes      []string
	Warnings         []string
	CommitSHA        string
}

type parsedFact struct {
	slug         string
	supersededBy string
	statement    string
}

// Curate rebuilds MEMORY.md from memory/facts/*.md (retiring superseded index lines without deleting fact files),
// warns on files > 150 lines, and lints wiki/ links and orphans when the vault HEAD has changed.
func (s *Service) Curate(ctx context.Context, now time.Time) (Report, error) {
	head, err := s.vault.HeadSHA(ctx)
	if err != nil {
		return Report{}, err
	}
	if lastHead, ok, _ := s.store.Read().GetKV(ctx, curatorLastHeadKVKey); ok && lastHead != "" && lastHead == head {
		return Report{Skipped: true}, nil
	}

	var rep Report
	facts, err := s.readAllFacts(ctx)
	if err != nil {
		return Report{}, err
	}

	memRaw, _ := s.vault.Read(ctx, "memory/MEMORY.md")
	origMem := string(memRaw)
	updatedMem, added, retired := reconcileMemoryIndex(origMem, facts)
	rep.IndexedFacts = added
	rep.RetiredFromIndex = retired

	if updatedMem != origMem {
		if err := s.vault.Write(ctx, "memory/MEMORY.md", []byte(updatedMem)); err != nil {
			return Report{}, err
		}
		sha, err := s.vault.CommitPaths(ctx, []string{"memory/MEMORY.md"}, fmt.Sprintf("curate: rebuild memory index (+%d/-%d)", added, retired))
		if err != nil {
			return Report{}, err
		}
		rep.CommitSHA = sha
		if sha != "" {
			head = sha
		}
	}

	// Warn (never truncate) when COMPANY.md or MEMORY.md exceeds 150 lines.
	for _, checkPath := range []string{"company/COMPANY.md", "memory/MEMORY.md"} {
		if raw, rErr := s.vault.Read(ctx, checkPath); rErr == nil {
			nLines := countDocLines(string(raw))
			if nLines > 150 {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s vượt trần 150 dòng (%d dòng)", checkPath, nLines))
			}
		}
	}

	broken, orphans := s.lintWiki(ctx)
	rep.BrokenLinks = broken
	rep.OrphanNotes = orphans

	nowMs := now.UnixMilli()
	_ = s.store.WithTx(ctx, func(tx app.Tx) error {
		return tx.UpsertKV(ctx, curatorLastHeadKVKey, head, nowMs)
	})
	return rep, nil
}

func (s *Service) readAllFacts(ctx context.Context) ([]parsedFact, error) {
	entries, err := s.vault.ListDir(ctx, "memory/facts")
	if err != nil {
		return nil, err
	}
	sort.Strings(entries)
	var out []parsedFact
	for _, name := range entries {
		if !strings.HasSuffix(name, ".md") || strings.HasPrefix(name, ".") {
			continue
		}
		raw, rErr := s.vault.Read(ctx, "memory/facts/"+name)
		if rErr != nil {
			continue
		}
		slug := strings.TrimSuffix(name, ".md")
		fm, body := splitFrontmatter(string(raw))
		if v := fm["slug"]; v != "" {
			slug = v
		}
		stmt := firstNonEmptyLine(body)
		out = append(out, parsedFact{
			slug:         slug,
			supersededBy: fm["superseded_by"],
			statement:    stmt,
		})
	}
	return out, nil
}

func reconcileMemoryIndex(memDoc string, facts []parsedFact) (updated string, added, retired int) {
	superseded := make(map[string]bool)
	for _, f := range facts {
		if f.supersededBy != "" {
			superseded[f.slug] = true
		}
	}

	trimmed := strings.TrimRight(memDoc, "\r\n")
	if trimmed == "" {
		trimmed = "# Memory Index"
	}
	var kept []string
	for _, line := range strings.Split(trimmed, "\n") {
		drop := false
		for slug := range superseded {
			if strings.Contains(line, "(facts/"+slug+".md)") || strings.Contains(line, "[[memory/facts/"+slug) {
				drop = true
				retired++
				break
			}
		}
		if !drop {
			kept = append(kept, line)
		}
	}
	cur := strings.Join(kept, "\n")
	for _, f := range facts {
		if f.supersededBy != "" {
			continue
		}
		if strings.Contains(cur, "(facts/"+f.slug+".md)") || strings.Contains(cur, "[[memory/facts/"+f.slug) {
			continue
		}
		cur += fmt.Sprintf("\n- [%s](facts/%s.md): %s", f.slug, f.slug, f.statement)
		added++
	}
	return cur + "\n", added, retired
}

func (s *Service) lintWiki(ctx context.Context) (broken, orphans []string) {
	entries, err := s.vault.ListDir(ctx, "wiki")
	if err != nil {
		return nil, nil
	}
	notes := make(map[string]string)
	for _, name := range entries {
		if !strings.HasSuffix(name, ".md") || strings.HasPrefix(name, ".") {
			continue
		}
		rel := "wiki/" + name
		if raw, rErr := s.vault.Read(ctx, rel); rErr == nil {
			notes[rel] = string(raw)
		}
	}
	referenced := make(map[string]bool)
	for src, content := range notes {
		for _, m := range wikiLinkRe.FindAllStringSubmatch(content, -1) {
			target := strings.TrimSpace(m[1])
			target = strings.TrimSuffix(target, ".md")
			norm := target
			if !strings.Contains(norm, "/") {
				norm = "wiki/" + norm
			}
			normRel := norm + ".md"
			if _, exists := notes[normRel]; exists {
				if normRel != src {
					referenced[normRel] = true
				}
				continue
			}
			if _, rErr := s.vault.Read(ctx, normRel); rErr == nil {
				continue
			}
			broken = append(broken, fmt.Sprintf("%s -> %s", src, target))
		}
	}
	for rel := range notes {
		if rel == "wiki/index.md" || rel == "wiki/README.md" {
			continue
		}
		if !referenced[rel] {
			orphans = append(orphans, rel)
		}
	}
	sort.Strings(broken)
	sort.Strings(orphans)
	return broken, orphans
}

func splitFrontmatter(doc string) (map[string]string, string) {
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

func firstNonEmptyLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			return t
		}
	}
	return ""
}

func countDocLines(s string) int {
	t := strings.TrimSuffix(s, "\n")
	if t == "" {
		return 0
	}
	return len(strings.Split(t, "\n"))
}
