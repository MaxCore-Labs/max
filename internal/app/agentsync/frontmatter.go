package agentsync

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"max/internal/domain"
)

const maxAgentBodyLines = 200

// AgentFile holds the parsed YAML frontmatter and Markdown role body of an AGENT.md file.
type AgentFile struct {
	Name               string
	Template           string
	Team               string
	Manager            string
	Engines            []domain.EngineName
	Models             map[domain.EngineName]string
	Autonomy           domain.Autonomy
	Web                bool
	Browser            domain.BrowserProfile
	Skills             []string
	QuotaTokensDay     *int64
	Heartbeat          string
	Params             map[string]any
	Sensitive          bool
	AllowUngatedEngine bool
	Body               string
	Warnings           []string
}

type rawFrontmatter struct {
	Name               string            `yaml:"name"`
	Template           string            `yaml:"template"`
	Team               string            `yaml:"team"`
	Manager            string            `yaml:"manager"`
	Engines            []string          `yaml:"engines"`
	Models             map[string]string `yaml:"models"`
	Autonomy           string            `yaml:"autonomy"`
	Web                bool              `yaml:"web"`
	Browser            string            `yaml:"browser"`
	Skills             []string          `yaml:"skills"`
	QuotaTokensDay     *int64            `yaml:"quota_tokens_day"`
	Heartbeat          string            `yaml:"heartbeat"`
	Params             map[string]any    `yaml:"params"`
	Sensitive          bool              `yaml:"sensitive"`
	AllowUngatedEngine bool              `yaml:"allow_ungated_engine"`
}

func splitFrontmatter(raw []byte) (yamlText, body string, err error) {
	if !utf8.Valid(raw) {
		return "", "", fmt.Errorf("invalid utf-8 encoding")
	}
	norm := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.HasPrefix(norm, "---\n") {
		return "", "", fmt.Errorf("missing opening --- frontmatter fence")
	}
	rest := norm[len("---\n"):]
	endIdx := strings.Index(rest, "\n---\n")
	fenceLen := len("\n---\n")
	if endIdx < 0 {
		if strings.HasSuffix(rest, "\n---") {
			endIdx = len(rest) - len("\n---")
			fenceLen = len("\n---")
		} else {
			return "", "", fmt.Errorf("missing closing --- frontmatter fence")
		}
	}
	return rest[:endIdx], rest[endIdx+fenceLen:], nil
}

// Parse validates UTF-8, extracts YAML frontmatter + Markdown body from raw AGENT.md bytes, and checks schema rules.
func Parse(raw []byte) (AgentFile, error) {
	yamlText, body, err := splitFrontmatter(raw)
	if err != nil {
		return AgentFile{}, err
	}

	var rf rawFrontmatter
	dec := yaml.NewDecoder(bytes.NewReader([]byte(yamlText)))
	dec.KnownFields(true)
	if err := dec.Decode(&rf); err != nil {
		return AgentFile{}, fmt.Errorf("invalid yaml in frontmatter: %w", err)
	}

	if strings.TrimSpace(rf.Name) == "" {
		return AgentFile{}, fmt.Errorf("missing required field 'name'")
	}
	if strings.TrimSpace(rf.Team) == "" {
		return AgentFile{}, fmt.Errorf("missing required field 'team'")
	}

	engines := make([]domain.EngineName, 0, max(1, len(rf.Engines)))
	if len(rf.Engines) == 0 {
		engines = append(engines, domain.EngineAgy)
	} else {
		for _, e := range rf.Engines {
			en := domain.EngineName(strings.TrimSpace(e))
			if domain.EngineBit(en) == 0 {
				return AgentFile{}, fmt.Errorf("invalid engines entry %q (allowed: agy, claude, codex)", e)
			}
			engines = append(engines, en)
		}
	}

	autonomy := domain.AutonomyAuto
	if strings.TrimSpace(rf.Autonomy) != "" {
		autonomy = domain.Autonomy(strings.TrimSpace(rf.Autonomy))
		if autonomy != domain.AutonomyAuto && autonomy != domain.AutonomySuggest {
			return AgentFile{}, fmt.Errorf("invalid autonomy %q (allowed: auto, suggest)", rf.Autonomy)
		}
	}

	browser := domain.BrowserNone
	if strings.TrimSpace(rf.Browser) != "" {
		browser = domain.BrowserProfile(strings.TrimSpace(rf.Browser))
		if browser != domain.BrowserNone && browser != domain.BrowserAnon && browser != domain.BrowserOwner {
			return AgentFile{}, fmt.Errorf("invalid browser profile %q (allowed: none, anon, owner)", rf.Browser)
		}
	}

	models := make(map[domain.EngineName]string, len(rf.Models))
	for k, v := range rf.Models {
		en := domain.EngineName(strings.TrimSpace(k))
		if domain.EngineBit(en) == 0 {
			return AgentFile{}, fmt.Errorf("invalid engine key %q in models", k)
		}
		models[en] = strings.TrimSpace(v)
	}

	var warnings []string
	trimmedBody := strings.TrimSuffix(body, "\n")
	if trimmedBody != "" && len(strings.Split(trimmedBody, "\n")) > maxAgentBodyLines {
		warnings = append(warnings, fmt.Sprintf("body exceeds %d lines cap (will be truncated in system prompt)", maxAgentBodyLines))
	}

	return AgentFile{
		Name:               strings.TrimSpace(rf.Name),
		Template:           strings.TrimSpace(rf.Template),
		Team:               strings.TrimSpace(rf.Team),
		Manager:            strings.TrimSpace(rf.Manager),
		Engines:            engines,
		Models:             models,
		Autonomy:           autonomy,
		Web:                rf.Web,
		Browser:            browser,
		Skills:             rf.Skills,
		QuotaTokensDay:     rf.QuotaTokensDay,
		Heartbeat:          strings.TrimSpace(rf.Heartbeat),
		Params:             rf.Params,
		Sensitive:          rf.Sensitive,
		AllowUngatedEngine: rf.AllowUngatedEngine,
		Body:               body,
		Warnings:           warnings,
	}, nil
}

func engineIsolationLevel(e domain.EngineName) int {
	switch e {
	case domain.EngineClaude:
		return 1
	case domain.EngineCodex:
		return 2
	case domain.EngineAgy:
		return 3
	default:
		return 3
	}
}
