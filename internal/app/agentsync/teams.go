package agentsync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"max/internal/app"
)

// TeamMandate describes a team's outcome objective and measurement criteria (Spec 7.2).
type TeamMandate struct {
	Outcome  string   `yaml:"outcome" json:"outcome"`
	Measures []string `yaml:"measures" json:"measures"`
}

// TeamFile holds parsed TEAM.md frontmatter and body.
type TeamFile struct {
	Name           string
	Mandate        TeamMandate
	MaxRunning     int
	QuotaTokensDay *int64
	Body           string
}

type rawTeamFrontmatter struct {
	Name           string      `yaml:"name"`
	Mandate        TeamMandate `yaml:"mandate"`
	MaxRunning     *int        `yaml:"max_running"`
	QuotaTokensDay *int64      `yaml:"quota_tokens_day"`
}

// ParseTeam parses and validates raw TEAM.md bytes.
func ParseTeam(raw []byte) (TeamFile, error) {
	yamlText, body, err := splitFrontmatter(raw)
	if err != nil {
		return TeamFile{}, err
	}
	var rf rawTeamFrontmatter
	dec := yaml.NewDecoder(bytes.NewReader([]byte(yamlText)))
	dec.KnownFields(true)
	if err := dec.Decode(&rf); err != nil {
		return TeamFile{}, fmt.Errorf("invalid yaml in TEAM.md: %w", err)
	}
	if strings.TrimSpace(rf.Name) == "" {
		return TeamFile{}, fmt.Errorf("missing required field 'name' in TEAM.md")
	}
	maxRun := 2
	if rf.MaxRunning != nil {
		if *rf.MaxRunning < 1 || *rf.MaxRunning > 64 {
			return TeamFile{}, fmt.Errorf("invalid max_running=%d in TEAM.md (must be in [1, 64])", *rf.MaxRunning)
		}
		maxRun = *rf.MaxRunning
	}
	if rf.Mandate.Measures == nil {
		rf.Mandate.Measures = []string{}
	}
	return TeamFile{
		Name:           strings.TrimSpace(rf.Name),
		Mandate:        rf.Mandate,
		MaxRunning:     maxRun,
		QuotaTokensDay: rf.QuotaTokensDay,
		Body:           body,
	}, nil
}

func (s *Service) syncTeams(ctx context.Context) ([]Problem, error) {
	entries, err := s.vault.ListDir(ctx, "teams")
	if err != nil {
		return nil, fmt.Errorf("list vault teams: %w", err)
	}
	sort.Strings(entries)

	type parsedTeam struct {
		slug string
		file TeamFile
	}
	var valid []parsedTeam
	var problems []Problem

	for _, slug := range entries {
		if slug == "" || slug[0] == '.' || slug[0] == '_' {
			continue
		}
		rel := fmt.Sprintf("teams/%s/TEAM.md", slug)
		raw, readErr := s.vault.Read(ctx, rel)
		if readErr != nil {
			continue
		}
		tf, parseErr := ParseTeam(raw)
		if parseErr != nil {
			problems = append(problems, Problem{Slug: slug, Path: rel, Message: parseErr.Error()})
			continue
		}
		valid = append(valid, parsedTeam{slug: slug, file: tf})
	}
	if len(valid) == 0 {
		return problems, nil
	}

	err = s.store.WithTx(ctx, func(tx app.Tx) error {
		for _, item := range valid {
			mandateBytes, _ := json.Marshal(item.file.Mandate)
			if err := tx.UpsertSyncedTeam(ctx, app.TeamRecord{
				ID:             s.ids.New(),
				Slug:           item.slug,
				Name:           item.file.Name,
				MandateJSON:    string(mandateBytes),
				MaxRunning:     item.file.MaxRunning,
				QuotaTokensDay: item.file.QuotaTokensDay,
				Status:         "active",
			}); err != nil {
				return err
			}
		}
		return nil
	})
	return problems, err
}
