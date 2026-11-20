package agentsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"max/internal/domain"
)

var (
	// ErrUngatedEngine indicates a sensitive role or owner-browser profile was assigned to a level > 1 engine without allow_ungated_engine.
	ErrUngatedEngine = errors.New("ungated engine for sensitive role")
	// ErrValidation indicates invalid user-supplied agent/team configuration fields.
	ErrValidation = errors.New("validation error")
)

// AgentPatch holds optional updates to apply to an AGENT.md file.
type AgentPatch struct {
	Name               *string
	Team               *string
	Manager            *string
	Engines            *[]string
	Models             *map[string]string
	Autonomy           *string
	Web                *bool
	Browser            *string
	Skills             *[]string
	QuotaTokensDay     *int64
	Heartbeat          *string
	Sensitive          *bool
	AllowUngatedEngine *bool
	Body               *string
}

// TeamPatch holds optional updates to apply to a TEAM.md file.
type TeamPatch struct {
	Name           *string
	Mandate        *TeamMandate
	MaxRunning     *int
	QuotaTokensDay *int64
	Body           *string
}

// NamedTemplate pairs a template directory slug with its parsed TemplateFile.
type NamedTemplate struct {
	Slug string
	File TemplateFile
}

// ListTemplates scans templates/*/TEMPLATE.md in the vault and returns valid parsed templates sorted by slug.
func (s *Service) ListTemplates(ctx context.Context) ([]NamedTemplate, error) {
	if s.vault == nil {
		return nil, nil
	}
	entries, err := s.vault.ListDir(ctx, "templates")
	if err != nil {
		return nil, nil
	}
	sort.Strings(entries)
	var out []NamedTemplate
	for _, slug := range entries {
		if slug == "" || slug[0] == '.' || slug[0] == '_' {
			continue
		}
		raw, err := s.vault.Read(ctx, fmt.Sprintf("templates/%s/TEMPLATE.md", slug))
		if err != nil {
			continue
		}
		tf, err := ParseTemplate(raw)
		if err != nil {
			continue
		}
		out = append(out, NamedTemplate{Slug: slug, File: tf})
	}
	return out, nil
}

// UpdateAgentFile applies patch to agents/<slug>/AGENT.md, enforces schema and sensitive-role gates, commits, and calls SyncAll.
func (s *Service) UpdateAgentFile(ctx context.Context, slug string, patch AgentPatch) (unlocked bool, err error) {
	af, err := s.loadCurrentAgentFile(ctx, slug)
	if err != nil {
		return false, err
	}
	prevAllow := af.AllowUngatedEngine

	if patch.Name != nil {
		if strings.TrimSpace(*patch.Name) == "" {
			return false, fmt.Errorf("%w: name must not be empty", ErrValidation)
		}
		af.Name = strings.TrimSpace(*patch.Name)
	}
	if patch.Team != nil {
		if strings.TrimSpace(*patch.Team) == "" {
			return false, fmt.Errorf("%w: team must not be empty", ErrValidation)
		}
		af.Team = strings.TrimSpace(*patch.Team)
	}
	if patch.Manager != nil {
		af.Manager = strings.TrimSpace(*patch.Manager)
	}
	if patch.Engines != nil {
		if len(*patch.Engines) == 0 {
			return false, fmt.Errorf("%w: engines must not be empty", ErrValidation)
		}
		engs := make([]domain.EngineName, 0, len(*patch.Engines))
		for _, rawE := range *patch.Engines {
			en := domain.EngineName(strings.TrimSpace(rawE))
			if domain.EngineBit(en) == 0 {
				return false, fmt.Errorf("%w: unknown engine %q", ErrValidation, rawE)
			}
			engs = append(engs, en)
		}
		af.Engines = engs
	}
	if patch.Models != nil {
		mods := make(map[domain.EngineName]string, len(*patch.Models))
		for k, v := range *patch.Models {
			en := domain.EngineName(strings.TrimSpace(k))
			if domain.EngineBit(en) == 0 {
				return false, fmt.Errorf("%w: unknown engine %q in models", ErrValidation, k)
			}
			mods[en] = strings.TrimSpace(v)
		}
		af.Models = mods
	}
	if patch.Autonomy != nil {
		au := domain.Autonomy(strings.TrimSpace(*patch.Autonomy))
		if au != domain.AutonomyAuto && au != domain.AutonomySuggest {
			return false, fmt.Errorf("%w: invalid autonomy %q", ErrValidation, *patch.Autonomy)
		}
		af.Autonomy = au
	}
	if patch.Web != nil {
		af.Web = *patch.Web
	}
	if patch.Browser != nil {
		br := domain.BrowserProfile(strings.TrimSpace(*patch.Browser))
		if br != domain.BrowserNone && br != domain.BrowserAnon && br != domain.BrowserOwner {
			return false, fmt.Errorf("%w: invalid browser %q", ErrValidation, *patch.Browser)
		}
		af.Browser = br
	}
	if patch.Skills != nil {
		af.Skills = *patch.Skills
	}
	if patch.QuotaTokensDay != nil {
		af.QuotaTokensDay = patch.QuotaTokensDay
	}
	if patch.Heartbeat != nil {
		af.Heartbeat = strings.TrimSpace(*patch.Heartbeat)
	}
	if patch.Sensitive != nil {
		af.Sensitive = *patch.Sensitive
	}
	if patch.AllowUngatedEngine != nil {
		af.AllowUngatedEngine = *patch.AllowUngatedEngine
	}
	if patch.Body != nil {
		af.Body = *patch.Body
	}

	if gateErr := checkSensitiveGate(af); gateErr != nil {
		return false, fmt.Errorf("%w: %v", ErrUngatedEngine, gateErr)
	}

	encoded, err := encodeAgentFile(af)
	if err != nil {
		return false, err
	}
	rel := fmt.Sprintf("agents/%s/AGENT.md", slug)
	if err := s.vault.Write(ctx, rel, encoded); err != nil {
		return false, err
	}
	if _, err := s.vault.CommitPaths(ctx, []string{rel}, fmt.Sprintf("vault(agent): update %s", slug)); err != nil {
		return false, err
	}
	if _, _, err := s.SyncAll(ctx); err != nil {
		return false, err
	}
	return !prevAllow && af.AllowUngatedEngine, nil
}

// UpdateTeamFile applies patch to teams/<slug>/TEAM.md, commits, and calls SyncAll.
func (s *Service) UpdateTeamFile(ctx context.Context, slug string, patch TeamPatch) error {
	rel := fmt.Sprintf("teams/%s/TEAM.md", slug)
	var tf TeamFile
	if raw, err := s.vault.Read(ctx, rel); err == nil {
		if parsed, perr := ParseTeam(raw); perr == nil {
			tf = parsed
		}
	}
	if tf.Name == "" {
		if tm, ok, _ := s.store.Read().GetTeamBySlug(ctx, slug); ok {
			tf.Name = tm.Name
			tf.MaxRunning = tm.MaxRunning
			tf.QuotaTokensDay = tm.QuotaTokensDay
			_ = json.Unmarshal([]byte(tm.MandateJSON), &tf.Mandate)
		} else {
			tf.Name = slug
			tf.MaxRunning = 2
		}
	}
	if patch.Name != nil {
		if strings.TrimSpace(*patch.Name) == "" {
			return fmt.Errorf("%w: team name must not be empty", ErrValidation)
		}
		tf.Name = strings.TrimSpace(*patch.Name)
	}
	if patch.Mandate != nil {
		tf.Mandate = *patch.Mandate
	}
	if patch.MaxRunning != nil {
		if *patch.MaxRunning < 1 || *patch.MaxRunning > 64 {
			return fmt.Errorf("%w: maxRunning must be in [1, 64]", ErrValidation)
		}
		tf.MaxRunning = *patch.MaxRunning
	}
	if patch.QuotaTokensDay != nil {
		tf.QuotaTokensDay = patch.QuotaTokensDay
	}
	if patch.Body != nil {
		tf.Body = *patch.Body
	}
	if tf.Mandate.Measures == nil {
		tf.Mandate.Measures = []string{}
	}
	maxRun := tf.MaxRunning
	rf := rawTeamFrontmatter{
		Name:           tf.Name,
		Mandate:        tf.Mandate,
		MaxRunning:     &maxRun,
		QuotaTokensDay: tf.QuotaTokensDay,
	}
	var fmBuf bytes.Buffer
	enc := yaml.NewEncoder(&fmBuf)
	enc.SetIndent(2)
	if err := enc.Encode(rf); err != nil {
		return err
	}
	_ = enc.Close()
	body := tf.Body
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	out := append([]byte("---\n"), fmBuf.Bytes()...)
	out = append(out, []byte("---\n"+body)...)
	if err := s.vault.Write(ctx, rel, out); err != nil {
		return err
	}
	if _, err := s.vault.CommitPaths(ctx, []string{rel}, fmt.Sprintf("vault(team): update %s", slug)); err != nil {
		return err
	}
	_, _, err := s.SyncAll(ctx)
	return err
}

func (s *Service) loadCurrentAgentFile(ctx context.Context, slug string) (AgentFile, error) {
	rel := fmt.Sprintf("agents/%s/AGENT.md", slug)
	if s.vault != nil {
		if raw, err := s.vault.Read(ctx, rel); err == nil {
			return Parse(raw)
		}
	}
	ag, ok, err := s.store.Read().GetAgentBySlug(ctx, slug)
	if err != nil {
		return AgentFile{}, err
	}
	if !ok {
		return AgentFile{}, fmt.Errorf("agent %q not found", slug)
	}
	tmSlug := "core"
	if tm, tok, _ := s.store.Read().GetTeam(ctx, ag.TeamID); tok {
		tmSlug = tm.Slug
	}
	var engStrs []string
	_ = json.Unmarshal([]byte(ag.EnginesJSON), &engStrs)
	engs := make([]domain.EngineName, 0, max(1, len(engStrs)))
	for _, e := range engStrs {
		engs = append(engs, domain.EngineName(e))
	}
	if len(engs) == 0 {
		engs = []domain.EngineName{domain.EngineAgy}
	}
	var rawMods map[string]string
	_ = json.Unmarshal([]byte(ag.ModelsJSON), &rawMods)
	mods := make(map[domain.EngineName]string, len(rawMods))
	for k, v := range rawMods {
		mods[domain.EngineName(k)] = v
	}
	var skills []string
	_ = json.Unmarshal([]byte(ag.SkillsJSON), &skills)
	var params map[string]any
	_ = json.Unmarshal([]byte(ag.ParamsJSON), &params)
	var tmpl string
	if ag.TemplateSlug != nil {
		tmpl = *ag.TemplateSlug
	}
	return AgentFile{
		Name:               ag.Name,
		Template:           tmpl,
		Team:               tmSlug,
		Engines:            engs,
		Models:             mods,
		Autonomy:           ag.Autonomy,
		Web:                ag.Web,
		Browser:            ag.Browser,
		Skills:             skills,
		QuotaTokensDay:     ag.QuotaTokensDay,
		Params:             params,
		AllowUngatedEngine: ag.AllowUngatedEngine,
		Body:               ag.Name + "\n",
	}, nil
}

func encodeAgentFile(af AgentFile) ([]byte, error) {
	engs := make([]string, len(af.Engines))
	for i, e := range af.Engines {
		engs[i] = string(e)
	}
	mods := make(map[string]string, len(af.Models))
	for k, v := range af.Models {
		mods[string(k)] = v
	}
	rf := rawFrontmatter{
		Name:               af.Name,
		Template:           af.Template,
		Team:               af.Team,
		Manager:            af.Manager,
		Engines:            engs,
		Models:             mods,
		Autonomy:           string(af.Autonomy),
		Web:                af.Web,
		Browser:            string(af.Browser),
		Skills:             af.Skills,
		QuotaTokensDay:     af.QuotaTokensDay,
		Heartbeat:          af.Heartbeat,
		Params:             af.Params,
		Sensitive:          af.Sensitive,
		AllowUngatedEngine: af.AllowUngatedEngine,
	}
	var fmBuf bytes.Buffer
	enc := yaml.NewEncoder(&fmBuf)
	enc.SetIndent(2)
	if err := enc.Encode(rf); err != nil {
		return nil, err
	}
	_ = enc.Close()
	body := af.Body
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
