package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"max/internal/domain"
	"max/internal/platform"
)

// MemorySearch searches episodic memories strictly scoped to c.AgentID (Spec 8.5.2 & 8.9.4).
func (s *Service) MemorySearch(ctx context.Context, c Caller, query string) Result {
	if ok, res := s.checkPolicy(c, "memory_search", domain.ToolClassR, ""); !ok {
		return res
	}
	if s.memory == nil || !s.memory.Healthy() {
		return Result{Text: "dịch vụ trí nhớ tạm không dùng được; hãy tiếp tục bằng ghi chú và vault", IsError: true}
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return Result{Text: "memory_search requires non-empty query", IsError: true}
	}
	hits, err := s.memory.Search(ctx, c.AgentID, q, 5)
	if err != nil {
		return Result{Text: fmt.Sprintf("memory_search tạm không dùng được: %v", err), IsError: true}
	}
	if len(hits) == 0 {
		return Result{Text: "Không tìm thấy ký ức phù hợp."}
	}
	var sb strings.Builder
	for i, h := range hits {
		if i > 0 {
			sb.WriteByte('\n')
		}
		if h.AuthorAgentID != "" && h.AuthorAgentID != c.AgentID {
			_, _ = fmt.Fprintf(&sb, "%d. [author:%s] %s", i+1, h.AuthorAgentID, h.Content)
		} else {
			_, _ = fmt.Fprintf(&sb, "%d. %s", i+1, h.Content)
		}
	}
	return s.CapOutput(c, "memory_search", sb.String())
}

// MemorySearchWithRaw ignores any model-supplied agentId in raw JSON and delegates strictly with c.AgentID.
func (s *Service) MemorySearchWithRaw(ctx context.Context, c Caller, query string, _ json.RawMessage) Result {
	return s.MemorySearch(ctx, c, query)
}

// MemorySave saves one redacted episodic memory entry under c.AgentID (class Wd).
func (s *Service) MemorySave(ctx context.Context, c Caller, content string) Result {
	if ok, res := s.checkPolicy(c, "memory_save", domain.ToolClassWd, ""); !ok {
		return res
	}
	clean := strings.TrimSpace(platform.Redact(content))
	if clean == "" {
		return Result{Text: "memory_save requires non-empty content", IsError: true}
	}
	if s.memory == nil || !s.memory.Healthy() {
		return Result{Text: "dịch vụ trí nhớ tạm không dùng được", IsError: true}
	}
	if err := s.memory.Remember(ctx, c.AgentID, clean, nil); err != nil {
		return Result{Text: fmt.Sprintf("memory_save failed: %v", err), IsError: true}
	}
	return Result{Text: "Đã lưu ký ức sự kiện."}
}
