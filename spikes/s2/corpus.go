package main

import (
	"fmt"
	"strings"
	"unicode"
)

const canaryText = "Ký ức mẫu bất biến của agent-00001: khóa đối chiếu CANARYALPHA9981 cho kiểm thử khởi động lại"
const canaryToken = "CANARYALPHA9981"

var topicQueryWords = []string{
	"triển khai cổng",
	"doanh thu quý",
	"rò rỉ kết nối",
	"đối soát hóa đơn",
	"lịch chạy định kỳ",
}

var (
	bankModules = []string{
		"scheduler", "outbox", "vaultgit", "scopeaudit", "mcpproxy",
		"ssehub", "sqlitewal", "quotagate", "approvals", "heartbeat",
		"curator", "sandboxes", "worktree", "router", "redactor",
		"ledger", "snapshots", "ingestor", "dispatcher", "verifier",
		"archiver",
	}
	bankActions = []string{
		"kiểm định chữ ký gói tin", "đồng bộ chỉ mục phân tán", "phục hồi trạng thái phiên",
		"cô lập vùng nhớ tạm thời", "chuẩn hóa biểu mẫu đầu vào", "giới hạn băng thông chiều lên",
		"niêm phong nhật ký giao dịch", "chuyển tiếp thông điệp nội bộ", "thu hồi giấy phép hết hạn",
		"đánh dấu bản ghi bất thường", "phân loại cảnh báo khẩn cấp", "tái lập kết nối dự phòng",
		"kiểm toán phạm vi tập tin", "nén gọn lịch sử hội thoại", "xác thực khóa phiên ngắn hạn",
		"khôi phục hàng đợi bị ngắt", "định tuyến luồng sự kiện", "giám sát mức tiêu thụ bộ nhớ",
		"đối chiếu biên lai kho vận", "lọc bỏ dữ liệu nhạy cảm", "cập nhật ngưỡng tự động",
	}
	bankContexts = []string{
		"cụm máy chủ phía bắc", "nhánh thử nghiệm tự động", "ca trực vận hành đêm",
		"đợt rà soát an ninh", "kịch bản tải đột biến", "quy trình duyệt hai lớp",
		"nhóm cộng tác liên phòng", "pha di trú cơ sở dữ liệu", "chế độ tiết kiệm tài nguyên",
		"mạng riêng cô lập hoàn toàn", "chu kỳ bảo trì cuối tuần", "bản dựng phát hành sớm",
		"ổ lưu trữ tốc độ cao", "kênh truyền mã hóa đầu cuối", "cổng kiểm soát biên giới",
		"phiên đối chiếu định mức", "vùng cách ly tiến trình con", "trạm thu thập số liệu",
		"kho lưu trữ chứng từ", "hàng rào chống lặp lệnh", "bộ đệm đọc song song",
	}
)

type synthMemory struct {
	AgentID     string
	TeamID      string
	Scope       string
	MemID       string
	ConvID      string
	Content     string
	UniqueToken string
}

// buildSynthMemory generates deterministic, low-overlap memories (< 0.25 Jaccard across
// all 20 rounds of the same agent) so agentmemory's 0.7 Jaccard superseding never triggers.
func buildSynthMemory(idx, numAgents int) synthMemory {
	aIdx := idx % numAgents
	round := idx / numAgents
	agentID := fmt.Sprintf("agent-%05d", aIdx+1)
	teamID := fmt.Sprintf("team-%04d", (aIdx/5)+1)
	scope := "private"
	if round%5 == 4 {
		scope = "team"
	}
	memID := fmt.Sprintf("mem-%07d", idx+1)
	convID := fmt.Sprintf("conv-%05d-%d", aIdx+1, (round%3)+1)
	uTok := fmt.Sprintf("utoka%05dr%03d", aIdx+1, round+1)

	if idx == 0 {
		return synthMemory{
			AgentID:     agentID,
			TeamID:      teamID,
			Scope:       "private",
			MemID:       memID,
			ConvID:      convID,
			Content:     canaryText + " " + uTok,
			UniqueToken: uTok,
		}
	}

	topic := topicQueryWords[(aIdx+round)%len(topicQueryWords)]
	mod := bankModules[(aIdx*3+round)%len(bankModules)]
	act := bankActions[(aIdx*5+round)%len(bankActions)]
	ctxPhrase := bankContexts[(aIdx*7+round)%len(bankContexts)]
	sig := fmt.Sprintf("sig%05x", (idx+1)*2654435761&0xfffff)

	content := fmt.Sprintf("%s %s %s %s %s %s", uTok, topic, mod, act, ctxPhrase, sig)
	return synthMemory{
		AgentID:     agentID,
		TeamID:      teamID,
		Scope:       scope,
		MemID:       memID,
		ConvID:      convID,
		Content:     content,
		UniqueToken: uTok,
	}
}

// foldVNForFTS maps đ/Đ -> d/D so SQLite FTS5 unicode61 with remove_diacritics 2
// matches both accented and unaccented Vietnamese queries consistently.
func foldVNForFTS(s string) string {
	return strings.NewReplacer("đ", "d", "Đ", "D").Replace(s)
}

// sanitizeFTSQuery splits user/agent natural text into safe quoted alphanumeric tokens.
func sanitizeFTSQuery(raw string, matchAll bool) string {
	folded := foldVNForFTS(raw)
	var toks []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			toks = append(toks, `"`+cur.String()+`"`)
			cur.Reset()
		}
	}
	for _, r := range folded {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	if len(toks) == 0 {
		return `""`
	}
	joiner := " OR "
	if matchAll {
		joiner = " AND "
	}
	return strings.Join(toks, joiner)
}
