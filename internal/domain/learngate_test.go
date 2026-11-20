package domain_test

import (
	"strings"
	"testing"

	"max/internal/domain"
)

func TestGateFacts(t *testing.T) {
	msgs := map[domain.ID]domain.MsgMeta{
		"msg-user-1": {ID: "msg-user-1", Role: "user"},
		"msg-asst-1": {ID: "msg-asst-1", Role: "assistant"},
	}
	existing := map[string]bool{
		"pricing-v1": true,
	}

	proposals := []domain.FactProposal{
		{
			Slug:          "from-assistant",
			Type:          "preference",
			Statement:     "Chủ nhân thích màu xanh",
			Confidence:    4,
			EvidenceMsgID: "msg-asst-1",
		},
		{
			Slug:          "missing-msg",
			Type:          "company",
			Statement:     "Trụ sở tại Hà Nội",
			Confidence:    4,
			EvidenceMsgID: "msg-nonexistent",
		},
		{
			Slug:          "low-conf",
			Type:          "preference",
			Statement:     "Có lẽ thích họp sáng",
			Confidence:    1,
			EvidenceMsgID: "msg-user-1",
		},
		{
			Slug:          "pricing-v1",
			Type:          "company",
			Statement:     "Ghi đè trực tiếp không có supersedes",
			Confidence:    5,
			EvidenceMsgID: "msg-user-1",
		},
		{
			Slug:          "pricing-v2",
			Type:          "company",
			Statement:     "Giá gói Pro mới là 49 USD/tháng",
			Confidence:    5,
			EvidenceMsgID: "msg-user-1",
			Supersedes:    "pricing-v1",
		},
	}

	res := domain.GateFacts(proposals, msgs, existing)
	if len(res.Accepted) != 1 || res.Accepted[0].Slug != "pricing-v2" {
		t.Fatalf("Accepted = %+v, want only pricing-v2", res.Accepted)
	}
	if res.SupersedeUpdates["pricing-v1"] != "pricing-v2" {
		t.Fatalf("SupersedeUpdates = %+v, want pricing-v1 -> pricing-v2", res.SupersedeUpdates)
	}
	if len(res.Blocked) != 4 {
		t.Fatalf("Blocked len = %d (%+v), want 4", len(res.Blocked), res.Blocked)
	}
	if !strings.Contains(res.Blocked[0].Reason, "bằng chứng không phải lời chủ nhân") {
		t.Fatalf("Blocked[0] reason = %q, want owner evidence message error", res.Blocked[0].Reason)
	}
	if !strings.Contains(res.Blocked[3].Reason, "chỉ thêm") {
		t.Fatalf("Blocked[3] reason = %q, want append-only error", res.Blocked[3].Reason)
	}
}

func TestGateSkill(t *testing.T) {
	existing := map[string]bool{"browse-web": true}
	disabled := map[string]bool{"spam-blast": true}

	if ok, _ := domain.GateSkill(domain.SkillProposal{
		Slug:        "long-desc",
		Description: strings.Repeat("a", 151),
		Body:        "# Skill\nSteps...",
	}, existing, disabled); ok {
		t.Fatal("expected 151-char description to be blocked")
	}

	if ok, _ := domain.GateSkill(domain.SkillProposal{
		Slug:        "browse-web",
		Description: "Duplicate existing skill",
		Body:        "# Skill",
	}, existing, disabled); ok {
		t.Fatal("expected duplicate existing skill to be blocked")
	}

	if ok, _ := domain.GateSkill(domain.SkillProposal{
		Slug:        "spam-blast",
		Description: "Previously rejected skill",
		Body:        "# Skill",
	}, existing, disabled); ok {
		t.Fatal("expected disabled skill to be blocked")
	}

	if ok, reason := domain.GateSkill(domain.SkillProposal{
		Slug:        "invoice-parse",
		Description: "Trích xuất bảng kê hóa đơn đầu vào chuẩn Việt Nam",
		Body:        "# Invoice Parse\n1. Đọc file...",
	}, existing, disabled); !ok {
		t.Fatalf("expected valid skill to pass, got reason=%q", reason)
	}
}
