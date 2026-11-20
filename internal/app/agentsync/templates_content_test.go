package agentsync_test

import (
	"bytes"
	"io/fs"
	"strings"
	"testing"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"max/internal/app/agentsync"
	"max/internal/domain"
	vaulttemplate "max/vault-template"
)

var expectedSpec71Templates = []string{
	"chief-of-staff",
	"researcher",
	"writer",
	"reviewer",
	"marketer",
	"sales-outreach",
	"support",
	"bookkeeper",
	"ops-admin",
	"monitor",
	"developer",
}

var expectedSeedTeams = []string{
	"core",
	"marketing",
	"sales",
	"support",
	"ops",
}

var expectedSeedSkills = []string{
	"browse-web",
	"ingest-source",
	"remember",
	"make-routine",
	"daily-brief",
}

func TestAllTemplatesParse(t *testing.T) {
	for _, slug := range expectedSpec71Templates {
		rel := "templates/" + slug + "/TEMPLATE.md"
		raw, err := fs.ReadFile(vaulttemplate.FS, rel)
		if err != nil {
			t.Fatalf("missing template %s: %v", rel, err)
		}
		tf, err := agentsync.ParseTemplate(raw)
		if err != nil {
			t.Fatalf("ParseTemplate(%s): %v", slug, err)
		}
		if len(tf.Engines) != 1 || tf.Engines[0] != domain.EngineAgy {
			t.Fatalf("template %s engines = %v, want [agy]", slug, tf.Engines)
		}
		lines := strings.Split(strings.TrimSuffix(tf.Body, "\n"), "\n")
		if len(lines) > 200 {
			t.Fatalf("template %s body lines = %d, want <= 200", slug, len(lines))
		}
	}

	for _, teamSlug := range expectedSeedTeams {
		rel := "teams/" + teamSlug + "/TEAM.md"
		raw, err := fs.ReadFile(vaulttemplate.FS, rel)
		if err != nil {
			t.Fatalf("missing team %s: %v", rel, err)
		}
		if _, err := agentsync.ParseTeam(raw); err != nil {
			t.Fatalf("ParseTeam(%s): %v", teamSlug, err)
		}
	}
}

func TestTemplateRoleRules(t *testing.T) {
	revRaw, err := fs.ReadFile(vaulttemplate.FS, "templates/reviewer/TEMPLATE.md")
	if err != nil {
		t.Fatalf("read reviewer: %v", err)
	}
	rev, err := agentsync.ParseTemplate(revRaw)
	if err != nil {
		t.Fatalf("parse reviewer: %v", err)
	}
	if !strings.Contains(strings.ToLower(rev.Body), "mặc định coi kết quả là sai") {
		t.Fatalf("reviewer body missing 'mặc định coi kết quả là sai': %s", rev.Body)
	}

	cosRaw, err := fs.ReadFile(vaulttemplate.FS, "templates/chief-of-staff/TEMPLATE.md")
	if err != nil {
		t.Fatalf("read chief-of-staff: %v", err)
	}
	cos, err := agentsync.ParseTemplate(cosRaw)
	if err != nil {
		t.Fatalf("parse chief-of-staff: %v", err)
	}
	if !strings.Contains(strings.ToLower(cos.Body), "giao việc dài cho agent khác") {
		t.Fatalf("chief-of-staff body missing delegation rule: %s", cos.Body)
	}

	for _, slug := range expectedSpec71Templates {
		raw, _ := fs.ReadFile(vaulttemplate.FS, "templates/"+slug+"/TEMPLATE.md")
		tf, err := agentsync.ParseTemplate(raw)
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(tf.Body)
		readsInbound := slug == "support" || strings.Contains(lower, "nội dung người ngoài") || strings.Contains(lower, "hộp thư đến")
		if readsInbound && !tf.Sensitive {
			t.Fatalf("template %s handles external/inbound content but Sensitive=false", slug)
		}
	}
}

func TestSkillDescriptions(t *testing.T) {
	type skillFrontmatter struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}

	for _, slug := range expectedSeedSkills {
		rel := "skills/" + slug + "/SKILL.md"
		raw, err := fs.ReadFile(vaulttemplate.FS, rel)
		if err != nil {
			t.Fatalf("missing skill %s: %v", rel, err)
		}
		norm := strings.ReplaceAll(string(raw), "\r\n", "\n")
		if !strings.HasPrefix(norm, "---\n") {
			t.Fatalf("skill %s missing frontmatter", slug)
		}
		rest := norm[len("---\n"):]
		end := strings.Index(rest, "\n---\n")
		if end < 0 {
			t.Fatalf("skill %s missing closing frontmatter fence", slug)
		}
		var fm skillFrontmatter
		if err := yaml.NewDecoder(bytes.NewReader([]byte(rest[:end]))).Decode(&fm); err != nil {
			t.Fatalf("decode skill %s frontmatter: %v", slug, err)
		}
		desc := strings.TrimSpace(fm.Description)
		if desc == "" {
			t.Fatalf("skill %s has empty description", slug)
		}
		if utf8.RuneCountInString(desc) > 150 || len(desc) > 150 {
			t.Fatalf("skill %s description exceeds 150 chars (%d bytes): %q", slug, len(desc), desc)
		}
		if strings.HasPrefix(strings.ToLower(desc), "activates when") {
			t.Fatalf("skill %s description must not start with 'Activates when': %q", slug, desc)
		}
	}
}
