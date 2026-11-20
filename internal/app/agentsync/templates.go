package agentsync

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"max/internal/domain"
)

// TemplateFile holds defaults of AgentFile plus required parameter names declared in TEMPLATE.md.
type TemplateFile struct {
	Name               string
	Manager            string
	Engines            []domain.EngineName
	Models             map[domain.EngineName]string
	Autonomy           domain.Autonomy
	Web                bool
	Browser            domain.BrowserProfile
	Skills             []string
	QuotaTokensDay     *int64
	Heartbeat          string
	Sensitive          bool
	AllowUngatedEngine bool
	RequiredParams     []string
	ParamDefaults      map[string]any
	Body               string
}

type rawTemplateFrontmatter struct {
	Name               string            `yaml:"name"`
	Manager            string            `yaml:"manager"`
	Engines            []string          `yaml:"engines"`
	Models             map[string]string `yaml:"models"`
	Autonomy           string            `yaml:"autonomy"`
	Web                bool              `yaml:"web"`
	Browser            string            `yaml:"browser"`
	Skills             []string          `yaml:"skills"`
	QuotaTokensDay     *int64            `yaml:"quota_tokens_day"`
	Heartbeat          string            `yaml:"heartbeat"`
	Sensitive          bool              `yaml:"sensitive"`
	AllowUngatedEngine bool              `yaml:"allow_ungated_engine"`
	RequiredParams     []string          `yaml:"required_params"`
	Params             any               `yaml:"params"`
}

// ParseTemplate parses and validates raw TEMPLATE.md bytes.
func ParseTemplate(raw []byte) (TemplateFile, error) {
	yamlText, body, err := splitFrontmatter(raw)
	if err != nil {
		return TemplateFile{}, err
	}
	var rf rawTemplateFrontmatter
	dec := yaml.NewDecoder(bytes.NewReader([]byte(yamlText)))
	dec.KnownFields(true)
	if err := dec.Decode(&rf); err != nil {
		return TemplateFile{}, fmt.Errorf("invalid yaml in TEMPLATE.md: %w", err)
	}
	if strings.TrimSpace(rf.Name) == "" {
		return TemplateFile{}, fmt.Errorf("missing required field 'name' in TEMPLATE.md")
	}

	engines := make([]domain.EngineName, 0, max(1, len(rf.Engines)))
	if len(rf.Engines) == 0 {
		engines = append(engines, domain.EngineAgy)
	} else {
		for _, e := range rf.Engines {
			en := domain.EngineName(strings.TrimSpace(e))
			if domain.EngineBit(en) == 0 {
				return TemplateFile{}, fmt.Errorf("invalid engines entry %q in TEMPLATE.md", e)
			}
			engines = append(engines, en)
		}
	}

	autonomy := domain.AutonomyAuto
	if strings.TrimSpace(rf.Autonomy) != "" {
		autonomy = domain.Autonomy(strings.TrimSpace(rf.Autonomy))
		if autonomy != domain.AutonomyAuto && autonomy != domain.AutonomySuggest {
			return TemplateFile{}, fmt.Errorf("invalid autonomy %q in TEMPLATE.md", rf.Autonomy)
		}
	}

	browser := domain.BrowserNone
	if strings.TrimSpace(rf.Browser) != "" {
		browser = domain.BrowserProfile(strings.TrimSpace(rf.Browser))
		if browser != domain.BrowserNone && browser != domain.BrowserAnon && browser != domain.BrowserOwner {
			return TemplateFile{}, fmt.Errorf("invalid browser profile %q in TEMPLATE.md", rf.Browser)
		}
	}

	models := make(map[domain.EngineName]string, len(rf.Models))
	for k, v := range rf.Models {
		en := domain.EngineName(strings.TrimSpace(k))
		if domain.EngineBit(en) == 0 {
			return TemplateFile{}, fmt.Errorf("invalid engine key %q in TEMPLATE.md models", k)
		}
		models[en] = strings.TrimSpace(v)
	}

	reqParams := make([]string, 0, len(rf.RequiredParams))
	for _, p := range rf.RequiredParams {
		if trimmed := strings.TrimSpace(p); trimmed != "" && !slices.Contains(reqParams, trimmed) {
			reqParams = append(reqParams, trimmed)
		}
	}
	defaults := make(map[string]any)
	switch pv := rf.Params.(type) {
	case []any:
		for _, item := range pv {
			switch v := item.(type) {
			case string:
				if s := strings.TrimSpace(v); s != "" && !slices.Contains(reqParams, s) {
					reqParams = append(reqParams, s)
				}
			case map[string]any:
				name, _ := v["name"].(string)
				name = strings.TrimSpace(name)
				if name == "" {
					continue
				}
				if req, ok := v["required"].(bool); !ok || req {
					if !slices.Contains(reqParams, name) {
						reqParams = append(reqParams, name)
					}
				}
				if def, hasDef := v["default"]; hasDef {
					defaults[name] = def
				}
			}
		}
	case map[string]any:
		for k, v := range pv {
			defaults[k] = v
		}
	}

	return TemplateFile{
		Name:               strings.TrimSpace(rf.Name),
		Manager:            strings.TrimSpace(rf.Manager),
		Engines:            engines,
		Models:             models,
		Autonomy:           autonomy,
		Web:                rf.Web,
		Browser:            browser,
		Skills:             rf.Skills,
		QuotaTokensDay:     rf.QuotaTokensDay,
		Heartbeat:          strings.TrimSpace(rf.Heartbeat),
		Sensitive:          rf.Sensitive,
		AllowUngatedEngine: rf.AllowUngatedEngine,
		RequiredParams:     reqParams,
		ParamDefaults:      defaults,
		Body:               body,
	}, nil
}
