package domain

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	maxSkillDescChars = 150
	minFactConfidence = 2
)

var validLearnSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Proposal holds candidate facts, skills, instruction diffs, and open questions emitted by a read-only learning pass.
type Proposal struct {
	Facts            []FactProposal  `json:"facts"`
	Skills           []SkillProposal `json:"skills"`
	InstructionEdits []EditProposal  `json:"instruction_edits"`
	OpenQuestions    []string        `json:"open_questions"`
}

// FactProposal proposes a durable owner/company fact backed by an explicit owner message ID.
type FactProposal struct {
	Slug          string `json:"slug"`
	Type          string `json:"type"`
	Statement     string `json:"statement"`
	Confidence    int    `json:"confidence"`
	EvidenceMsgID ID     `json:"evidence_msg_id"`
	Supersedes    string `json:"supersedes,omitempty"`
}

// SkillProposal proposes a reusable skill under skills/_proposed/<slug>/SKILL.md.
type SkillProposal struct {
	Slug        string `json:"slug"`
	Description string `json:"description"`
	Body        string `json:"body"`
}

// EditProposal proposes a unified diff against AGENT.md, TEAM.md, or COMPANY.md for owner review.
type EditProposal struct {
	TargetPath string `json:"target_path"`
	Title      string `json:"title"`
	Diff       string `json:"diff"`
}

// MsgMeta supplies verified message role metadata to GateFacts.
type MsgMeta struct {
	ID   ID
	Role string
}

// Blocked records a rejected learning item and its Vietnamese reason for learning-log.md.
type Blocked struct {
	Item   string
	Reason string
}

// GateResult summarizes accepted facts, blocked facts, and old-slug -> new-slug supersede updates.
type GateResult struct {
	Accepted         []FactProposal
	Blocked          []Blocked
	SupersedeUpdates map[string]string
}

// GateFacts validates candidate facts purely against verified owner message evidence and append-only rules (Spec 8.9.5).
func GateFacts(p []FactProposal, msgs map[ID]MsgMeta, existing map[string]bool) GateResult {
	res := GateResult{
		SupersedeUpdates: make(map[string]string),
	}
	seenBatch := make(map[string]bool)

	for _, f := range p {
		slug := strings.TrimSpace(f.Slug)
		stmt := strings.TrimSpace(f.Statement)
		itemName := slug
		if itemName == "" {
			itemName = "(unnamed-fact)"
		}

		if !validLearnSlugRe.MatchString(slug) {
			res.Blocked = append(res.Blocked, Blocked{Item: itemName, Reason: "slug không hợp lệ"})
			continue
		}
		if stmt == "" {
			res.Blocked = append(res.Blocked, Blocked{Item: slug, Reason: "nội dung sự thật rỗng"})
			continue
		}
		if f.Confidence < minFactConfidence {
			res.Blocked = append(res.Blocked, Blocked{Item: slug, Reason: "độ tin cậy quá thấp (confidence < 2)"})
			continue
		}
		meta, found := msgs[f.EvidenceMsgID]
		if !found {
			res.Blocked = append(res.Blocked, Blocked{Item: slug, Reason: "bằng chứng không tồn tại trong cuộc trò chuyện"})
			continue
		}
		if meta.Role != "user" {
			res.Blocked = append(res.Blocked, Blocked{Item: slug, Reason: "bằng chứng không phải lời chủ nhân"})
			continue
		}
		sup := strings.TrimSpace(f.Supersedes)
		if (existing[slug] || seenBatch[slug]) && sup == "" {
			res.Blocked = append(res.Blocked, Blocked{Item: slug, Reason: "chỉ thêm mới; thay thế phải khai supersedes trên slug mới"})
			continue
		}
		if existing[slug] || seenBatch[slug] {
			res.Blocked = append(res.Blocked, Blocked{Item: slug, Reason: "slug đã tồn tại (chỉ thêm file mới khi supersedes)"})
			continue
		}
		if sup != "" && !existing[sup] {
			res.Blocked = append(res.Blocked, Blocked{Item: slug, Reason: "fact bị thay thế không tồn tại"})
			continue
		}

		f.Slug = slug
		f.Statement = stmt
		f.Supersedes = sup
		seenBatch[slug] = true
		res.Accepted = append(res.Accepted, f)
		if sup != "" {
			res.SupersedeUpdates[sup] = slug
		}
	}
	return res
}

// GateSkill validates a proposed skill against the 150-char description cap, existing skills, and disabled list.
func GateSkill(s SkillProposal, existing, disabled map[string]bool) (ok bool, reason string) {
	slug := strings.TrimSpace(s.Slug)
	if !validLearnSlugRe.MatchString(slug) {
		return false, "tên skill không hợp lệ"
	}
	desc := strings.TrimSpace(s.Description)
	if desc == "" {
		return false, "mô tả skill rỗng"
	}
	if utf8.RuneCountInString(desc) > maxSkillDescChars {
		return false, "mô tả skill vượt quá 150 ký tự"
	}
	if existing[slug] {
		return false, "tên skill trùng với skill đang hoạt động"
	}
	if disabled[slug] {
		return false, "tên skill nằm trong danh sách đã tắt"
	}
	if strings.TrimSpace(s.Body) == "" {
		return false, "nội dung hướng dẫn skill rỗng"
	}
	return true, ""
}
