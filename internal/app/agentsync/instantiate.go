package agentsync

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var slugValidRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Instantiate renders templates/<template>/TEMPLATE.md into agents/<slug>/AGENT.md, commits, and calls SyncAll.
func (s *Service) Instantiate(ctx context.Context, template, slug, team string, params map[string]any) error {
	slug = strings.TrimSpace(slug)
	team = strings.TrimSpace(team)
	template = strings.TrimSpace(template)
	if !slugValidRe.MatchString(slug) {
		return fmt.Errorf("invalid agent slug %q", slug)
	}
	if team == "" {
		return fmt.Errorf("team is required")
	}

	tplRel := fmt.Sprintf("templates/%s/TEMPLATE.md", template)
	tplRaw, err := s.vault.Read(ctx, tplRel)
	if err != nil {
		return fmt.Errorf("read template %q: %w", tplRel, err)
	}
	tpl, err := ParseTemplate(tplRaw)
	if err != nil {
		return err
	}

	rel := fmt.Sprintf("agents/%s/AGENT.md", slug)
	if err := s.checkSlugAvailable(ctx, slug, rel); err != nil {
		return err
	}

	rendered, err := renderAgentFromTemplate(tpl, template, slug, team, params)
	if err != nil {
		return err
	}
	if err := s.vault.Write(ctx, rel, rendered); err != nil {
		return err
	}
	if _, err := s.vault.CommitPaths(ctx, []string{rel}, fmt.Sprintf("vault(agent): instantiate %s from %s", slug, template)); err != nil {
		return err
	}
	_, probs, err := s.SyncAll(ctx)
	if err != nil {
		return err
	}
	for _, p := range probs {
		if p.Slug == slug {
			return fmt.Errorf("sync %s: %s", slug, p.Message)
		}
	}
	return nil
}

// InstantiateBulk instantiates up to hundreds of agents from one template in a single git commit and a single SyncAll pass.
func (s *Service) InstantiateBulk(
	ctx context.Context,
	template, team, slugPrefix string,
	rows []map[string]any,
) (created []string, problems []Problem, err error) {
	template = strings.TrimSpace(template)
	team = strings.TrimSpace(team)
	slugPrefix = strings.TrimSpace(slugPrefix)
	if team == "" {
		return nil, nil, fmt.Errorf("team is required")
	}
	if slugPrefix == "" {
		slugPrefix = template
	}

	tplRel := fmt.Sprintf("templates/%s/TEMPLATE.md", template)
	tplRaw, err := s.vault.Read(ctx, tplRel)
	if err != nil {
		return nil, nil, fmt.Errorf("read template %q: %w", tplRel, err)
	}
	tpl, err := ParseTemplate(tplRaw)
	if err != nil {
		return nil, nil, err
	}

	seenSlugs := make(map[string]bool, len(rows))
	var commitPaths []string

	for idx, row := range rows {
		slug := fmt.Sprintf("%s-%d", slugPrefix, idx+1)
		if custom, ok := row["slug"].(string); ok && strings.TrimSpace(custom) != "" {
			slug = strings.TrimSpace(custom)
		}
		rel := fmt.Sprintf("agents/%s/AGENT.md", slug)

		if !slugValidRe.MatchString(slug) || seenSlugs[slug] {
			problems = append(problems, Problem{Slug: slug, Path: rel, Message: "invalid or duplicate slug in batch"})
			continue
		}
		if err := s.checkSlugAvailable(ctx, slug, rel); err != nil {
			problems = append(problems, Problem{Slug: slug, Path: rel, Message: err.Error()})
			continue
		}

		rendered, rErr := renderAgentFromTemplate(tpl, template, slug, team, row)
		if rErr != nil {
			problems = append(problems, Problem{Slug: slug, Path: rel, Message: rErr.Error()})
			continue
		}
		if wErr := s.vault.Write(ctx, rel, rendered); wErr != nil {
			problems = append(problems, Problem{Slug: slug, Path: rel, Message: wErr.Error()})
			continue
		}
		seenSlugs[slug] = true
		created = append(created, slug)
		commitPaths = append(commitPaths, rel)
	}

	if len(commitPaths) == 0 {
		return created, problems, nil
	}

	if _, err := s.vault.CommitPaths(ctx, commitPaths, fmt.Sprintf("vault(agents): bulk instantiate %d from %s", len(commitPaths), template)); err != nil {
		return nil, problems, err
	}
	_, syncProbs, err := s.SyncAll(ctx)
	problems = append(problems, syncProbs...)
	return created, problems, err
}

func (s *Service) checkSlugAvailable(ctx context.Context, slug, rel string) error {
	if _, err := s.vault.Read(ctx, rel); err == nil {
		return fmt.Errorf("agent slug %q already exists at %s", slug, rel)
	}
	if _, exists, err := s.store.Read().GetAgentBySlug(ctx, slug); err == nil && exists {
		return fmt.Errorf("agent slug %q already registered", slug)
	}
	return nil
}

func renderAgentFromTemplate(tpl TemplateFile, templateSlug, slug, team string, params map[string]any) ([]byte, error) {
	merged := make(map[string]any, len(tpl.ParamDefaults)+len(params))
	for k, v := range tpl.ParamDefaults {
		merged[k] = v
	}
	for k, v := range params {
		merged[k] = v
	}

	for _, req := range tpl.RequiredParams {
		val, ok := merged[req]
		if !ok || val == nil || strings.TrimSpace(fmt.Sprint(val)) == "" {
			return nil, fmt.Errorf("missing required parameter %q", req)
		}
	}

	displayName := fmt.Sprintf("%s (%s)", tpl.Name, slug)
	if customName, ok := merged["name"].(string); ok && strings.TrimSpace(customName) != "" {
		displayName = strings.TrimSpace(customName)
	}

	engines := make([]string, len(tpl.Engines))
	for i, e := range tpl.Engines {
		engines[i] = string(e)
	}
	models := make(map[string]string, len(tpl.Models))
	for k, v := range tpl.Models {
		models[string(k)] = v
	}

	fm := rawFrontmatter{
		Name:               displayName,
		Template:           templateSlug,
		Team:               team,
		Manager:            tpl.Manager,
		Engines:            engines,
		Models:             models,
		Autonomy:           string(tpl.Autonomy),
		Web:                tpl.Web,
		Browser:            string(tpl.Browser),
		Skills:             tpl.Skills,
		QuotaTokensDay:     tpl.QuotaTokensDay,
		Heartbeat:          tpl.Heartbeat,
		Params:             merged,
		Sensitive:          tpl.Sensitive,
		AllowUngatedEngine: tpl.AllowUngatedEngine,
	}
	var fmBuf bytes.Buffer
	enc := yaml.NewEncoder(&fmBuf)
	enc.SetIndent(2)
	if err := enc.Encode(fm); err != nil {
		return nil, fmt.Errorf("encode agent frontmatter: %w", err)
	}
	_ = enc.Close()

	body := tpl.Body
	for k, v := range merged {
		strVal := fmt.Sprint(v)
		body = strings.ReplaceAll(body, "{{"+k+"}}", strVal)
		body = strings.ReplaceAll(body, "{{ "+k+" }}", strVal)
	}
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}

	var out bytes.Buffer
	out.WriteString("---\n")
	out.Write(fmBuf.Bytes())
	out.WriteString("---\n")
	out.WriteString(body)
	return out.Bytes(), nil
}
